package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"recodex-go/internal/relay"
	"recodex-go/internal/serverlog"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "relay listen address")
	flag.Parse()

	hub := relay.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"name":"rcc-relay"}`))
	})
	mux.HandleFunc("/relay/", hub.HandleWebSocket)

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Recodex Relay 已启动: http://%s", *addr)
	serverlog.RelayStartup(*addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve relay: %v", err)
	}
}
