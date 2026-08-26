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

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	application, err := app.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)

	for {
		runCtx, cancel := context.WithCancel(rootCtx)
		done := make(chan error, 1)
		go func(current *app.App) { done <- current.RunContext(runCtx) }(application)

	running:
		for {
			select {
			case <-rootCtx.Done():
				cancel()
				return <-done
			case err := <-done:
				cancel()
				return err
			case <-reload:
				candidateConfig, loadErr := config.Load(configPath)
				if loadErr != nil {
					log.Printf("config reload rejected; keeping current application: %v", loadErr)
					continue
				}
				candidate, initErr := app.New(candidateConfig)
				if initErr != nil {
					log.Printf("config reload initialization rejected; keeping current application: %v", initErr)
					continue
				}
				cancel()
				if runErr := <-done; runErr != nil {
					return runErr
				}
				application = candidate
				log.Printf("config reload applied from %s", configPath)
				break running
			}
		}
	}
}
