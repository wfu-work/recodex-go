package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"recodex-go/internal/api"
	"recodex-go/internal/config"
	"recodex-go/internal/relay"
	"recodex-go/internal/serverlog"
)

func main() {
	configPath := flag.String("config", "bridge.yaml", "path to bridge YAML config")
	relayAddr := flag.String("relay-addr", "127.0.0.1:8787", "relay listen address")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	bridge, err := api.NewServer(cfg)
	if err != nil {
		log.Fatalf("create bridge server: %v", err)
	}

	bridgeAddr := cfg.Server.Address()
	bridgeServer := &http.Server{
		Addr:              bridgeAddr,
		Handler:           bridge.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	relayMux := http.NewServeMux()
	relayHub := relay.NewHub()
	relayMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"name":"rcc-relay"}`))
	})
	relayMux.HandleFunc("/relay/", relayHub.HandleWebSocket)
	relayServer := &http.Server{
		Addr:              *relayAddr,
		Handler:           relayMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("Recodex Bridge 已启动: http://%s", bridgeAddr)
		serverlog.BridgeStartup(bridgeAddr, bridge.PairingToken())
		if err := bridgeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	go func() {
		log.Printf("Recodex Relay 已启动: http://%s", *relayAddr)
		serverlog.RelayStartup(*relayAddr)
		if err := relayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	log.Fatalf("serve: %v", <-errCh)
}
