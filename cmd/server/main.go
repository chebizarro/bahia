// Package main is the entrypoint for the Bahia deployment registry server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/config"
)

func main() {
	configPath := flag.String("config", "", "path to config YAML file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		log.Fatalf("application error: %v", err)
	}
}

type serverApplication interface {
	RunContext(context.Context) error
}

type serverSignalSource struct {
	root   context.Context
	reload <-chan os.Signal
	stop   func()
}

type serverDependencies struct {
	loadConfig     func(string) (*config.Config, error)
	newApplication func(*config.Config) (serverApplication, error)
	newSignals     func() serverSignalSource
	logf           func(string, ...any)
}

func run(configPath string) error {
	return runWithDependencies(configPath, serverDependencies{
		loadConfig: config.Load,
		newApplication: func(cfg *config.Config) (serverApplication, error) {
			return app.New(cfg)
		},
		newSignals: productionSignalSource,
		logf:       log.Printf,
	})
}

func productionSignalSource() serverSignalSource {
	rootCtx, stopRoot := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	return serverSignalSource{
		root:   rootCtx,
		reload: reload,
		stop: func() {
			signal.Stop(reload)
			stopRoot()
		},
	}
}

func runWithDependencies(configPath string, deps serverDependencies) error {
	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	application, err := deps.newApplication(cfg)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}

	signals := deps.newSignals()
	if signals.stop != nil {
		defer signals.stop()
	}
	logf := deps.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	for {
		runCtx, cancel := context.WithCancel(signals.root)
		done := make(chan error, 1)
		go func(current serverApplication) { done <- current.RunContext(runCtx) }(application)

	running:
		for {
			select {
			case <-signals.root.Done():
				cancel()
				return <-done
			case err := <-done:
				cancel()
				return err
			case <-signals.reload:
				candidateConfig, loadErr := deps.loadConfig(configPath)
				if loadErr != nil {
					logf("config reload rejected; keeping current application: %v", loadErr)
					continue
				}
				candidate, initErr := deps.newApplication(candidateConfig)
				if initErr != nil {
					logf("config reload initialization rejected; keeping current application: %v", initErr)
					continue
				}
				cancel()
				if runErr := <-done; runErr != nil {
					return runErr
				}
				application = candidate
				logf("config reload applied from %s", configPath)
				break running
			}
		}
	}
}
