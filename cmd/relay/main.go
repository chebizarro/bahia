// Package main is the entrypoint for the Bahia Khatru relay sidecar.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"go.uber.org/zap"
)

func main() {
	configPath := flag.String("config", "", "path to config YAML file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if !cfg.Nostr.Sidecar.Enabled {
		log.Fatalf("nostr.sidecar.enabled must be true to run the relay sidecar")
	}

	logger, err := newLogger(cfg.Log)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	server, err := relaysidecar.New(cfg.Nostr, logger)
	if err != nil {
		log.Fatalf("failed to initialize relay sidecar: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx); err != nil {
		log.Fatalf("relay sidecar error: %v", err)
	}
}

func newLogger(cfg config.LogConfig) (*zap.Logger, error) {
	if cfg.Format == "console" {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}
