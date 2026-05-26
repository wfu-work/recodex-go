package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"recodex-go/internal/api"
	"recodex-go/internal/config"
	"recodex-go/internal/serverlog"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to bridge YAML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	bridge, err := api.NewServer(cfg)
	if err != nil {
		log.Fatalf("create bridge server: %v", err)
	}
	if err := bridge.RunRelayClient(context.Background()); err != nil {
		log.Fatalf("start relay client: %v", err)
	}

	addr := cfg.Server.Address()
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           bridge.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Recodex Bridge 已启动: http://%s", addr)
	serverlog.BridgeStartup(addr, bridge.PairingToken())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
