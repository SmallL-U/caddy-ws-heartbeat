# Caddy WebSocket Heartbeat Module

[English](./README.md) | [中文](./README_zh.md)

The `caddy-ws-heartbeat` module is a Caddy HTTP handler that upgrades HTTP connections to WebSocket connections and sends periodic heartbeat pings to keep the connections alive.

## Features

- Upgrades HTTP connections to WebSocket connections
- Sends periodic heartbeat pings to WebSocket clients
- Proxies WebSocket messages between clients and a backend WebSocket server
- Supports subprotocol negotiation

## Installation

To use this module, you need to build Caddy with the `caddy-ws-heartbeat` module included:

```sh
xcaddy build --with github.com/smalll-u/caddy-ws-heartbeat
```

## Configuration

Configure the `caddy-ws-heartbeat` module in your Caddyfile:

```Caddyfile
{
    order ws_heartbeat before reverse_proxy
}

:8080 {
    route {
        ws_heartbeat {
            interval 15s
            backend backend.example.com /ws /chat
        }
        reverse_proxy backend.example.com
    }
}
```

### Parameters

- `interval`: The interval between heartbeat pings (default: `15s`)
- `backend`: The backend WebSocket server host and allowed paths

## Using Multiple Backend Addresses

For multiple backend addresses, define multiple routes in your Caddyfile:

```caddyfile
route /backend1 {
    ws_heartbeat {
        # Configuration for backend1
    }
}

route /backend2 {
    ws_heartbeat {
        # Configuration for backend2
    }
}

# Add more routes as needed
```

This setup allows you to handle multiple backend addresses, each with its own `ws_heartbeat` configuration.

## Usage

1. Start the Caddy server:
    ```sh
    caddy run --config /path/to/Caddyfile
    ```

2. Connect to the WebSocket endpoint using a WebSocket client

## Example

A simple backend WebSocket server example:

```go
package main

import (
    "fmt"
    "github.com/gorilla/websocket"
    "log"
    "net/http"
)

var upgrader = websocket.Upgrader{}

func wsHandler(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Println("WebSocket Upgrade error:", err)
        return
    }
    defer conn.Close()

    for {
        _, msg, err := conn.ReadMessage()
        if err != nil {
            fmt.Println("Client disconnected")
            return
        }
        fmt.Println("Received message:", string(msg))
    }
}

func main() {
    http.HandleFunc("/ws/", wsHandler)
    fmt.Println("WebSocket server started on :9000")
    log.Fatal(http.ListenAndServe(":9000", nil))
}
```
