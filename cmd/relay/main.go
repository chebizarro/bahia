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

type sidecarRuntime interface {
	Run(context.Context) error
	Close() error
}

type sidecarFactory func(config.NostrConfig, *zap.Logger) (sidecarRuntime, error)

type activeRuntime struct {
	runtime sidecarRuntime
	cancel  context.CancelFunc
	done    <-chan error
}

type runtimeSupervisor struct {
	rootCtx context.Context
	logger  *zap.Logger
	factory sidecarFactory
	active  *activeRuntime
}

func (s *runtimeSupervisor) prepare(cfg *config.Config) (sidecarRuntime, error) {
	if !cfg.Nostr.Sidecar.Enabled {
		return nil, nil
	}
	return s.factory(cfg.Nostr, s.logger)
}

func (s *runtimeSupervisor) start(runtime sidecarRuntime) {
	if runtime == nil {
		s.active = nil
		s.logger.Info("relay sidecar disabled; waiting for config reload")
		return
	}
	runCtx, cancel := context.WithCancel(s.rootCtx)
	done := make(chan error, 1)
	s.active = &activeRuntime{runtime: runtime, cancel: cancel, done: done}
	go func() { done <- runtime.Run(runCtx) }()
}

func (s *runtimeSupervisor) stop() error {
	if s.active == nil {
		return nil
	}
	active := s.active
	active.cancel()
	err := <-active.done
	s.active = nil
	return err
}

func (s *runtimeSupervisor) replace(cfg *config.Config) error {
	replacement, err := s.prepare(cfg)
	if err != nil {
		return err
	}
	if err := s.stop(); err != nil {
		if replacement != nil {
			_ = replacement.Close()
		}
		return err
	}
	s.start(replacement)
	return nil
}

func (s *runtimeSupervisor) done() <-chan error {
	if s.active == nil {
		return nil
	}
	return s.active.done
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

	supervisor := &runtimeSupervisor{
		rootCtx: rootCtx,
		logger:  logger,
		factory: func(cfg config.NostrConfig, logger *zap.Logger) (sidecarRuntime, error) {
			return relaysidecar.New(cfg, logger)
		},
	}
	initial, err := supervisor.prepare(cfg)
	if err != nil {
		return fmt.Errorf("initialize relay sidecar: %w", err)
	}
	supervisor.start(initial)

	for {
		select {
		case <-rootCtx.Done():
			return supervisor.stop()
		case runErr := <-supervisor.done():
			return runErr
		case <-reload:
			candidate, loadErr := config.Load(configPath)
			if loadErr != nil {
				logger.Warn("config reload rejected; keeping current sidecar", zap.Error(loadErr))
				continue
			}
			if replaceErr := supervisor.replace(candidate); replaceErr != nil {
				logger.Warn("config reload initialization failed; keeping current sidecar", zap.Error(replaceErr))
				continue
			}
			cfg = candidate
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
