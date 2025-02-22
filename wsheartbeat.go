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
	"sync"
	"time"
)

type WSHeartbeat struct {
	// Heartbeat interval configuration; can be specified via the WS_PING_INTERVAL environment variable or in the Caddyfile.
	Interval         string `json:"interval,omitempty"`
	intervalDuration time.Duration

	// Backend configuration: the first value is host:port, subsequent values are allowed forwarding paths (can be extended arbitrarily).
	BackendHost  string   `json:"backend_host,omitempty"`
	BackendPaths []string `json:"backend_paths,omitempty"`

	mu          sync.Mutex
	connections map[*websocket.Conn]struct{}

	logger *zap.Logger
}

func init() {
	caddy.RegisterModule(&WSHeartbeat{})
	httpcaddyfile.RegisterHandlerDirective("ws_heartbeat", parseCaddyfile)
}

func (m *WSHeartbeat) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.ws_heartbeat",
		New: func() caddy.Module {
			return new(WSHeartbeat)
		},
	}
}

// Provision is called when loading the configuration.
func (m *WSHeartbeat) Provision(ctx caddy.Context) error {
	// Obtain Caddy's native logger.
	m.logger = ctx.Logger(m)

	// Configure the heartbeat interval.
	if m.Interval == "" {
		m.Interval = "15s"
	}
	dur, err := time.ParseDuration(m.Interval)
	if err != nil || dur <= 0 {
		return fmt.Errorf("invalid interval: %s", m.Interval)
	}
	m.intervalDuration = dur

	// Check if backend configuration is complete.
	if m.BackendHost == "" {
		return fmt.Errorf("backend host (first value) must be specified")
	}
	if len(m.BackendPaths) == 0 {
		return fmt.Errorf("backend paths (second value and onwards) must have at least one entry")
	}

	m.connections = make(map[*websocket.Conn]struct{})

	m.logger.Debug("WSHeartbeat provisioned",
		zap.String("interval", m.Interval),
		zap.String("backend_host", m.BackendHost),
		zap.Strings("backend_paths", m.BackendPaths),
	)
	return nil
}

// ServeHTTP handles incoming HTTP requests.
func (m *WSHeartbeat) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Non-WebSocket upgrade requests are passed to the next handler (e.g., reverse_proxy).
	if !websocket.IsWebSocketUpgrade(r) {
		return next.ServeHTTP(w, r)
	}

	// Check if the request path is in the allowed BackendPaths.
	// If not, pass the request to the next handler without WebSocket proxying.
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

	// Upgrade the client connection.
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.Error("Failed to upgrade client connection", zap.Error(err))
		return err
	}

	m.mu.Lock()
	m.connections[clientConn] = struct{}{}
	m.mu.Unlock()

	m.logger.Debug("Client WebSocket connection established", zap.String("remote_addr", r.RemoteAddr))

	// Construct backend URL.
	backendURL := "ws://" + m.BackendHost + r.URL.String()

	// Clone client header
	reqHeader := r.Header.Clone()

	// Remove WebSocket headers from the request.
	reqHeader.Del("Sec-WebSocket-Version")
	reqHeader.Del("Sec-WebSocket-Key")
	reqHeader.Del("Sec-WebSocket-Extensions")
	reqHeader.Del("Sec-WebSocket-Protocol")
	reqHeader.Del("Connection")

	// Dial the backend WebSocket service.
	backendConn, _, err := websocket.DefaultDialer.Dial(backendURL, reqHeader)
	if err != nil {
		m.logger.Debug("Failed to dial backend WebSocket server", zap.Error(err))
		_ = clientConn.Close()
		return err
	}
	m.logger.Debug("Backend WebSocket connection established", zap.String("backend_url", backendURL))

	// Start a heartbeat goroutine for the client connection.
	go m.handlePing(clientConn)

	// Start bidirectional forwarding: messages are forwarded between client and backend.
	errCh := make(chan error, 2)
	go m.proxyWebSocket(clientConn, backendConn, errCh)
	go m.proxyWebSocket(backendConn, clientConn, errCh)

	// Wait until one side encounters an error or the connection is closed.
	err = <-errCh
	_ = clientConn.Close()
	_ = backendConn.Close()
	m.mu.Lock()
	delete(m.connections, clientConn)
	m.mu.Unlock()
	m.logger.Debug("WebSocket connections closed", zap.Error(err))
	return err
}

// proxyWebSocket implements one-way message forwarding.
func (m *WSHeartbeat) proxyWebSocket(src, dst *websocket.Conn, errCh chan error) {
	for {
		msgType, msg, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		err = dst.WriteMessage(msgType, msg)
		if err != nil {
			errCh <- err
			return
		}
	}
}

// handlePing maintains the ping/pong heartbeat mechanism (for client connection).
func (m *WSHeartbeat) handlePing(conn *websocket.Conn) {
	pingTicker := time.NewTicker(m.intervalDuration)
	defer pingTicker.Stop()

	// Set pong callback.
	conn.SetPongHandler(func(appData string) error {
		m.logger.Debug("Received pong from client")
		return nil
	})

	for {
		select {
		case <-pingTicker.C:
			// Send a ping message.
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

// UnmarshalCaddyfile parses the Caddyfile configuration.
func (m *WSHeartbeat) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		// Parse instructions within the block.
		for d.NextBlock(0) {
			switch d.Val() {
			case "interval":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Interval = d.Val()
			case "backend":
				// The backend directive supports multiple values: the first is host:port, and subsequent ones are allowed forwarding paths.
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

// parseCaddyfile parses the Caddyfile configuration.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m WSHeartbeat
	err := m.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

var (
	_ caddyfile.Unmarshaler       = (*WSHeartbeat)(nil)
	_ caddy.Provisioner           = (*WSHeartbeat)(nil)
	_ caddyhttp.MiddlewareHandler = (*WSHeartbeat)(nil)
)
