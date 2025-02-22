package wsheartbeat

import (
	"fmt"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WSHeartbeat holds configuration and state for the websocket heartbeat module.
type WSHeartbeat struct {
	// Interval between heartbeat pings as a string (e.g., "15s").
	Interval string `json:"interval,omitempty"`
	// intervalDuration is the parsed duration of Interval.
	intervalDuration time.Duration

	// BackendHost is the host of the backend websocket server.
	BackendHost string `json:"backend_host,omitempty"`
	// BackendPaths is a list of allowed backend paths for websocket upgrade.
	BackendPaths []string `json:"backend_paths,omitempty"`

	// mu protects the connections map.
	mu sync.Mutex
	// connections tracks active client websocket connections.
	connections map[*websocket.Conn]struct{}

	// logger is used for logging module events.
	logger *zap.Logger
}

func init() {
	// Register WSHeartbeat as a Caddy module.
	caddy.RegisterModule(&WSHeartbeat{})
	// Register the directive "ws_heartbeat" for the HTTP Caddyfile.
	httpcaddyfile.RegisterHandlerDirective("ws_heartbeat", parseCaddyfile)
}

// CaddyModule returns the Caddy module information.
func (m *WSHeartbeat) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.ws_heartbeat",
		New: func() caddy.Module {
			return new(WSHeartbeat)
		},
	}
}

// Provision sets up the module, parsing durations and ensuring required configuration is provided.
func (m *WSHeartbeat) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger(m)
	// Set default interval if not provided.
	if m.Interval == "" {
		m.Interval = "15s"
	}
	// Parse the interval duration.
	dur, err := time.ParseDuration(m.Interval)
	if err != nil || dur <= 0 {
		return fmt.Errorf("invalid interval: %s", m.Interval)
	}
	m.intervalDuration = dur
	// Ensure backend host is specified.
	if m.BackendHost == "" {
		return fmt.Errorf("backend host (first value) must be specified")
	}
	// Ensure at least one backend path is provided.
	if len(m.BackendPaths) == 0 {
		return fmt.Errorf("backend paths (second value and onwards) must have at least one entry")
	}
	// Initialize the connections map.
	m.connections = make(map[*websocket.Conn]struct{})
	m.logger.Debug("WSHeartbeat provisioned",
		zap.String("interval", m.Interval),
		zap.String("backend_host", m.BackendHost),
		zap.Strings("backend_paths", m.BackendPaths),
	)
	return nil
}

// ServeHTTP handles incoming HTTP requests and upgrades them to websocket connections if appropriate.
func (m *WSHeartbeat) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// If the request is not a websocket upgrade, pass it on to the next handler.
	if !websocket.IsWebSocketUpgrade(r) {
		return next.ServeHTTP(w, r)
	}

	// Check if the request URL path is allowed based on BackendPaths.
	allowed := false
	for _, p := range m.BackendPaths {
		if p == r.URL.Path {
			allowed = true
			break
		}
	}
	if !allowed {
		return next.ServeHTTP(w, r)
	}

	// Get and process the Sec-WebSocket-Protocol header from the client.
	rawClientProtocols := r.Header.Get("Sec-WebSocket-Protocol")
	var offeredByClient []string
	if rawClientProtocols != "" {
		parts := strings.Split(rawClientProtocols, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				offeredByClient = append(offeredByClient, p)
			}
		}
	}

	// Construct the backend websocket URL.
	backendURL := "ws://" + m.BackendHost + r.URL.String()
	// Clone the client's headers and remove websocket-specific headers.
	reqHeader := r.Header.Clone()
	reqHeader.Del("Sec-WebSocket-Version")
	reqHeader.Del("Sec-WebSocket-Key")
	reqHeader.Del("Sec-WebSocket-Extensions")
	reqHeader.Del("Sec-WebSocket-Protocol")
	reqHeader.Del("Connection")
	reqHeader.Del("Upgrade")

	// Use a websocket dialer to connect to the backend, passing the offered subprotocols.
	dialer := websocket.Dialer{
		Subprotocols: offeredByClient,
	}
	backendConn, _, err := dialer.Dial(backendURL, reqHeader)
	if err != nil {
		m.logger.Error("dial backend error", zap.Error(err))
		return err
	}

	// Get the subprotocol chosen by the backend.
	chosenByBackend := backendConn.Subprotocol()

	// Upgrade the client connection.
	upgrader := websocket.Upgrader{
		// Allow connections from any origin.
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	// If the backend selected a subprotocol, include it in the upgrade.
	if chosenByBackend != "" {
		upgrader.Subprotocols = []string{chosenByBackend}
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = backendConn.Close()
		return err
	}

	// Ensure the subprotocol selected by the client matches the backend's.
	chosenByClient := clientConn.Subprotocol()
	if chosenByBackend != chosenByClient {
		_ = clientConn.Close()
		_ = backendConn.Close()
		return fmt.Errorf("subprotocol mismatch: backend=%q, client=%q", chosenByBackend, chosenByClient)
	}

	// Add the client connection to the active connections map.
	m.mu.Lock()
	m.connections[clientConn] = struct{}{}
	m.mu.Unlock()

	// Start a goroutine to send periodic pings to the client.
	go m.handlePing(clientConn)

	// Set up error channels and proxy messages between client and backend.
	errCh := make(chan error, 2)
	go m.proxyWebSocket(clientConn, backendConn, errCh)
	go m.proxyWebSocket(backendConn, clientConn, errCh)

	// Wait for any error in the proxying.
	err = <-errCh
	// Close both connections on error.
	_ = clientConn.Close()
	_ = backendConn.Close()

	// Remove the client connection from the active connections map.
	m.mu.Lock()
	delete(m.connections, clientConn)
	m.mu.Unlock()

	return err
}

// proxyWebSocket copies messages between two websocket connections.
func (m *WSHeartbeat) proxyWebSocket(src, dst *websocket.Conn, errCh chan error) {
	for {
		// Read message from the source connection.
		msgType, msg, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		// Write the message to the destination connection.
		err = dst.WriteMessage(msgType, msg)
		if err != nil {
			errCh <- err
			return
		}
	}
}

// handlePing sends periodic ping messages to a websocket connection to keep it alive.
func (m *WSHeartbeat) handlePing(conn *websocket.Conn) {
	// Create a ticker for the ping interval.
	pingTicker := time.NewTicker(m.intervalDuration)
	defer pingTicker.Stop()

	// Set a pong handler to log when a pong is received.
	conn.SetPongHandler(func(appData string) error {
		m.logger.Debug("Received pong from client")
		return nil
	})

	// Send a ping on each tick.
	for {
		select {
		case <-pingTicker.C:
			// Write a ping message with a deadline.
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			if err != nil {
				m.logger.Warn("Failed to send ping, closing connection", zap.Error(err))
				return
			} else {
				m.logger.Debug("Sent ping to client")
			}
		}
	}
}

// UnmarshalCaddyfile parses Caddyfile tokens into the WSHeartbeat configuration.
func (m *WSHeartbeat) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	// Process each token block.
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "interval":
				// Parse the interval value.
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Interval = d.Val()
			case "backend":
				// Parse the backend host and paths.
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.BackendHost = d.Val()
				for d.NextArg() {
					m.BackendPaths = append(m.BackendPaths, d.Val())
				}
			default:
				return d.ArgErr()
			}
		}
	}
	return nil
}

// parseCaddyfile is a helper function to parse the Caddyfile configuration for WSHeartbeat.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m WSHeartbeat
	err := m.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Ensure WSHeartbeat implements the required interfaces.
var (
	_ caddyfile.Unmarshaler       = (*WSHeartbeat)(nil)
	_ caddy.Provisioner           = (*WSHeartbeat)(nil)
	_ caddyhttp.MiddlewareHandler = (*WSHeartbeat)(nil)
)
