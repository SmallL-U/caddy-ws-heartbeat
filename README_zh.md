# Caddy WebSocket 心跳模块

[English](./README.md) | [中文](./README_zh.md)

`caddy-ws-heartbeat` 模块是一个 Caddy HTTP 处理器，用于将 HTTP 连接升级为 WebSocket 连接，并发送定期的心跳 ping 以保持连接活跃。

## 功能特点

- 将 HTTP 连接升级为 WebSocket 连接
- 向 WebSocket 客户端发送定期心跳 ping
- 在客户端和后端 WebSocket 服务器之间代理 WebSocket 消息
- 支持子协议协商

## 安装

要使用此模块，您需要构建包含 `caddy-ws-heartbeat` 模块的 Caddy。请按照以下步骤操作：

1. 使用模块构建 Caddy：
    ```sh
    xcaddy build --with github.com/smalll-u/caddy-ws-heartbeat
    ```

## 配置

您可以在 Caddyfile 中配置 `caddy-ws-heartbeat` 模块。以下是示例配置：

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

### 参数

- `interval`：心跳 ping 的间隔时间（默认：`15s`）
- `backend`：后端 WebSocket 服务器主机和允许的路径

## 使用多个后端地址

如果您需要使用多个后端地址，可以通过在 Caddyfile 中定义多个路由来实现。每个路由应包含一个 `ws_heartbeat` 指令。以下是示例配置：

```caddyfile
route /backend1 {
    ws_heartbeat {
        # backend1 的配置
    }
}

route /backend2 {
    ws_heartbeat {
        # backend2 的配置
    }
}

# 根据需要添加更多路由
```

这种设置允许您处理多个后端地址，每个地址都有自己的 `ws_heartbeat` 配置。

## 使用方法

1. 使用您的 Caddyfile 配置启动 Caddy 服务器：
    ```sh
    caddy run --config /path/to/Caddyfile
    ```

2. 使用 WebSocket 客户端连接到 WebSocket 端点。

## 示例

以下是可作为后端使用的简单 WebSocket 服务器示例：

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
