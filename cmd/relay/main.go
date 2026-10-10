// Package main is the entrypoint for the Bahia Khatru relay sidecar.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"github.com/openagentsinc/bahia/internal/servicesigner"
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

type sidecarFactory func(context.Context, config.NostrConfig, nostr.Signer, *zap.Logger) (sidecarRuntime, error)

// signerOpener opens the service signer nostr.signer configures; cancelling
// ctx aborts a pending open but not the opened session.
type signerOpener func(context.Context, config.NostrConfig) (*serviceSigner, error)

// serviceSigner is an open service identity and the config it was opened
// from. Exactly one owner closes it.
type serviceSigner struct {
	cfg   config.NostrConfig
	keyer nostr.Keyer
	close func()
}

func (s *serviceSigner) Close() {
	if s != nil {
		s.close()
	}
}

// openServiceSigner opens the configured service signer; its session lasts
// until Close, so a SIGTERM cannot end it while the runtime still signs. The
// sidecar always runs as the service identity (NIP-11 pubkey, write and read
// admission, config acknowledgements), so an unconfigured signer is an
// error. NIP-46 requests pass the process-wide outbound admission controller.
func openServiceSigner(ctx context.Context, cfg config.NostrConfig) (*serviceSigner, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	abortOpen := context.AfterFunc(ctx, cancel)
	keyer, err := servicesigner.Open(lifetime, cfg, servicesigner.Options{})
	if !abortOpen() && err == nil {
		err = ctx.Err()
		closeKeyer(keyer)
	}
	if err != nil {
		cancel()
		if errors.Is(err, servicesigner.ErrNotConfigured) {
			return nil, fmt.Errorf("relay sidecar requires the service identity: configure nostr.signer (or nostr.private_key for the local signer): %w", err)
		}
		return nil, err
	}
	return &serviceSigner{cfg: cfg, keyer: keyer, close: func() {
		cancel()
		closeKeyer(keyer)
	}}, nil
}

func closeKeyer(keyer nostr.Keyer) {
	if closer, ok := keyer.(io.Closer); ok {
		_ = closer.Close()
	}
}

// sameServiceSigner reports whether a and b configure the same service
// signer, so a reload can keep the open session.
// TODO(bahia-cd0wr.3.7): replace with servicesigner.SameSigner
func sameServiceSigner(a, b config.NostrConfig) bool {
	return a.ServiceSignerMethod() == b.ServiceSignerMethod() &&
		strings.TrimSpace(a.PrivateKey) == strings.TrimSpace(b.PrivateKey) &&
		strings.EqualFold(a.PublicKey, b.PublicKey) &&
		a.Signer == b.Signer
}

type activeRuntime struct {
	runtime sidecarRuntime
	cancel  context.CancelFunc
	done    <-chan error
}

// runtimeSupervisor runs one sidecar runtime at a time and owns the service
// signer it signs with. All methods run on the reload loop's goroutine.
type runtimeSupervisor struct {
	rootCtx    context.Context
	logger     *zap.Logger
	factory    sidecarFactory
	openSigner signerOpener
	active     *activeRuntime
	signer     *serviceSigner
}

// prepare builds the runtime for cfg without disturbing the active one. It
// reuses the open signer when cfg leaves the service signer unchanged and
// opens a new one otherwise; a new signer is closed here if the runtime
// fails, so the active signer is untouched by a failed reload.
func (s *runtimeSupervisor) prepare(cfg *config.Config) (sidecarRuntime, *serviceSigner, error) {
	if !cfg.Nostr.Sidecar.Enabled {
		return nil, nil, nil
	}
	signer := s.signer
	if signer == nil || !sameServiceSigner(signer.cfg, cfg.Nostr) {
		opened, err := s.openSigner(s.rootCtx, cfg.Nostr)
		if err != nil {
			return nil, nil, err
		}
		signer = opened
	}
	runtime, err := s.factory(s.rootCtx, cfg.Nostr, signer.keyer, s.logger)
	if err != nil {
		s.discard(signer)
		return nil, nil, err
	}
	return runtime, signer, nil
}

// discard closes signer unless it is the one the supervisor owns.
func (s *runtimeSupervisor) discard(signer *serviceSigner) {
	if signer != s.signer {
		signer.Close()
	}
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

// replace swaps in the runtime for cfg. The previous signer is closed only
// after the runtime that signed with it has stopped and the swap succeeded.
func (s *runtimeSupervisor) replace(cfg *config.Config) error {
	replacement, signer, err := s.prepare(cfg)
	if err != nil {
		return err
	}
	if err := s.stop(); err != nil {
		if replacement != nil {
			_ = replacement.Close()
		}
		s.discard(signer)
		return err
	}
	if signer != s.signer {
		s.signer.Close()
		s.signer = signer
	}
	s.start(replacement)
	return nil
}

// shutdown stops the runtime, then closes the signer it signed with.
func (s *runtimeSupervisor) shutdown() error {
	err := s.stop()
	s.signer.Close()
	s.signer = nil
	return err
}

// exited records that the active runtime returned on its own.
func (s *runtimeSupervisor) exited() {
	s.active.cancel()
	s.active = nil
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
		rootCtx:    rootCtx,
		logger:     logger,
		factory:    newSidecar,
		openSigner: openServiceSigner,
	}
	defer func() { _ = supervisor.shutdown() }()
	if err := supervisor.replace(cfg); err != nil {
		return fmt.Errorf("initialize relay sidecar: %w", err)
	}

	for {
		select {
		case <-rootCtx.Done():
			return supervisor.shutdown()
		case runErr := <-supervisor.done():
			supervisor.exited()
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
			logger.Info("config reload applied", zap.String("path", configPath))
		}
	}
}

func newSidecar(ctx context.Context, cfg config.NostrConfig, signer nostr.Signer, logger *zap.Logger) (sidecarRuntime, error) {
	return relaysidecar.New(ctx, cfg, signer, logger)
}

func newLogger(cfg config.LogConfig) (*zap.Logger, error) {
	if cfg.Format == "console" {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}
