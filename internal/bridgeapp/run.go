package bridgeapp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"recodex-go/internal/api"
	"recodex-go/internal/config"
	"recodex-go/internal/serverlog"
)

func Run(args []string) error {
	flags := flag.NewFlagSet("recodex", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "path to bridge YAML config")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	bridge, err := api.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("create bridge server: %w", err)
	}
	defer bridge.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.RunRelayClient(ctx); err != nil {
		return fmt.Errorf("start relay client: %w", err)
	}

	addr := cfg.Server.Address()
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           bridge.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()

	log.Printf("Recodex Bridge 已启动: http://%s", addr)
	serverlog.BridgeStartup(addr, bridge.PairingToken())
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	bridge.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	serveErr := <-serverErrors
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}
