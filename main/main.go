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
	defer func(conn *websocket.Conn) {
		_ = conn.Close()
	}(conn)

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
	http.HandleFunc("/ws", wsHandler)
	fmt.Println("WebSocket server started on :9000")
	log.Fatal(http.ListenAndServe(":9000", nil))
}
