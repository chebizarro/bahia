// Package main is the entrypoint for the Bahia Khatru relay sidecar.
package main

import (
	"context"
	"flag"
	"fmt"
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

	if err := run(*configPath); err != nil {
		log.Fatalf("relay sidecar error: %v", err)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger, err := newLogger(cfg.Log)
	if err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)

	var cancel context.CancelFunc
	var done <-chan error
	start := func() error {
		if !cfg.Nostr.Sidecar.Enabled {
			logger.Info("relay sidecar disabled; waiting for config reload")
			cancel = nil
			done = nil
			return nil
		}
		server, newErr := relaysidecar.New(cfg.Nostr, logger)
		if newErr != nil {
			return newErr
		}
		var runCtx context.Context
		runCtx, cancel = context.WithCancel(rootCtx)
		runDone := make(chan error, 1)
		done = runDone
		go func() { runDone <- server.Run(runCtx) }()
		return nil
	}
	stopCurrent := func() error {
		if cancel == nil {
			return nil
		}
		cancel()
		return <-done
	}
	if err := start(); err != nil {
		return fmt.Errorf("initialize relay sidecar: %w", err)
	}

	for {
		select {
		case <-rootCtx.Done():
			return stopCurrent()
		case runErr := <-done:
			return runErr
		case <-reload:
			candidate, loadErr := config.Load(configPath)
			if loadErr != nil {
				logger.Warn("config reload rejected; keeping current sidecar", zap.Error(loadErr))
				continue
			}
			if stopErr := stopCurrent(); stopErr != nil {
				return stopErr
			}
			cfg = candidate
			if startErr := start(); startErr != nil {
				return fmt.Errorf("apply config reload: %w", startErr)
			}
			logger.Info("config reload applied", zap.String("path", configPath))
		}
	}
}

func newLogger(cfg config.LogConfig) (*zap.Logger, error) {
	if cfg.Format == "console" {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}
