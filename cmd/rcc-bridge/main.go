package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"recodex-go/internal/api"
	"recodex-go/internal/config"
)

func main() {
	configPath := flag.String("config", "bridge.yaml", "path to bridge YAML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	server, err := api.NewServer(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	addr := cfg.Server.Address()
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("rcc-bridge listening on http://%s", addr)
	log.Printf("pairing token: %s", server.PairingToken())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
