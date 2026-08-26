package relaysidecar

import (
	"context"
	"fmt"
	"iter"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

const retentionSweepInterval = 15 * time.Minute

// Server wraps the Khatru relay used by Bahia's local sidecar topology.
type relayConfigSigner struct{ secret nostr.SecretKey }

func (s relayConfigSigner) Sign(_ context.Context, event *nostr.Event) error {
	return event.Sign(s.secret)
}

type relayConfigPublisher struct{ relay *khatru.Relay }

func (p relayConfigPublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if _, err := p.relay.AddEvent(ctx, event); err != nil {
		return 0, err
	}
	return 1, nil
}

type Server struct {
	cfg        config.RelaySidecarConfig
	relay      *khatru.Relay
	store      *sqliteStore
	policy     *adminPolicy
	httpServer *http.Server
	logger     *zap.Logger
	consumer   *ConfigConsumer
}

// New creates a Khatru sidecar relay backed by durable storage.
func New(nostrCfg config.NostrConfig, logger *zap.Logger) (*Server, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if nostrCfg.Sidecar.MaxQueryLimit <= 0 {
		nostrCfg.Sidecar.MaxQueryLimit = 2000
	}
	if nostrCfg.Sidecar.PublicURL == "" {
		nostrCfg.Sidecar.PublicURL = "ws://localhost:3334"
	}

	pol, err := newPolicy(nostrCfg)
	if err != nil {
		return nil, err
	}
	admin, err := openAdminPolicy(nostrCfg.Sidecar)
	if err != nil {
		return nil, err
	}
	pol.admin = admin
	store, err := newSQLiteStore(nostrCfg.Sidecar.DataDir)
	if err != nil {
		return nil, err
	}
	relay := khatru.NewRelay()
	relay.Log = log.New(os.Stderr, "[bahia-relay-sidecar] ", log.LstdFlags)
	relay.ServiceURL = nostrCfg.Sidecar.PublicURL
	state := admin.snapshot()
	relay.Info.Name = state.Metadata.Name
	relay.Info.Description = state.Metadata.Description
	relay.Info.Icon = state.Metadata.Icon
	relay.Info.PostingPolicy = "Accepts every valid signed Nostr event kind. Event authorization belongs to protocol consumers, not relay kind allowlists. If mirror_external is enabled, this relay is the upstream boundary and Bahia will not also connect directly to mirrored public upstream relays."
	relay.Info.SupportedNIPs = []any{1, 11, 17, 40, 42, 44, 51, 59, 65, 70}

	if servicePubkey, ok, err := deriveFiatjafPubkey(nostrCfg.PrivateKey); err != nil {
		return nil, err
	} else if ok {
		pk := servicePubkey
		relay.Info.PubKey = &pk
	}

	relay.OnEvent = pol.acceptEvent
	relay.OnRequest = pol.acceptFilter
	relay.OnCount = pol.acceptFilter
	relay.StoreEvent = store.Save
	relay.ReplaceEvent = store.Replace
	relay.DeleteEvent = store.Delete
	relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		return store.Query(ctx, filter, nostrCfg.Sidecar.MaxQueryLimit)
	}
	relay.Count = func(ctx context.Context, filter nostr.Filter) (uint32, error) {
		return store.Count(ctx, filter), nil
	}
	// Khatru broadcasts to matching subscribers synchronously before sending
	// the publisher's OK. Persist first, acknowledge promptly, and move fanout
	// off the publisher path so slow subscribers cannot stall unrelated writes.
	relay.PreventBroadcast = func(_ *khatru.WebSocket, _ nostr.Filter, _ nostr.Event) bool {
		return true
	}
	var consumer *ConfigConsumer
	if len(nostrCfg.Sidecar.ConfigTrustedPubkeys) > 0 {
		secret, ok, err := parseFiatjafSecret(nostrCfg.PrivateKey)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		if !ok {
			_ = store.Close()
			return nil, fmt.Errorf("nostr.private_key is required when relay-sidecar config trusted authors are configured")
		}
		consumer, err = NewConfigConsumer(ConfigConsumerConfig{
			ServiceID: nostrCfg.Sidecar.ServiceID, Scope: nostrCfg.Sidecar.Scope,
			ProjectionPath: nostrCfg.Sidecar.ConfigProjectionPath,
			TrustedAuthors: nostrCfg.Sidecar.ConfigTrustedPubkeys,
			Signer:         relayConfigSigner{secret: secret}, Publisher: relayConfigPublisher{relay: relay},
			Apply: func(projection ConfigProjection) error {
				if err := admin.applyConfigProjection(projection); err != nil {
					return err
				}
				metadata := admin.snapshot().Metadata
				relay.Info.Name = metadata.Name
				relay.Info.Description = metadata.Description
				relay.Info.Icon = metadata.Icon
				return nil
			},
		})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	relay.OnEventSaved = func(ctx context.Context, event nostr.Event) {
		logger.Debug("sidecar event accepted", zap.String("event_id", event.ID.Hex()), zap.Uint16("kind", uint16(event.Kind)))
		go relay.ForceBroadcastEvent(event)
		if consumer != nil && (event.Kind == configListKind || event.Kind == configPolicyKind) {
			go func() {
				if err := consumer.Handle(context.Background(), event); err != nil {
					logger.Warn("relay-sidecar desired config rejected", zap.String("event_id", event.ID.Hex()), zap.Error(err))
				}
			}()
		}
	}
	relay.OnEphemeralEvent = func(_ context.Context, event nostr.Event) {
		go relay.ForceBroadcastEvent(event)
	}

	return &Server{
		cfg:      nostrCfg.Sidecar,
		relay:    relay,
		store:    store,
		policy:   admin,
		logger:   logger,
		consumer: consumer,
	}, nil
}

// Handler returns the relay HTTP handler. It supports NIP-11 over HTTP and
// Nostr WebSocket traffic on the configured public URL/path.
func (s *Server) Handler() http.Handler {
	publicPath := sidecarPublicPath(s.cfg.PublicURL)
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.handleNIP86(w, r)
			return
		}
		s.relay.ServeHTTP(w, r)
	})
	if publicPath == "/" {
		return dispatch
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		rewritten := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = publicPath
		rewritten.URL = &urlCopy
		dispatch.ServeHTTP(w, rewritten)
	})
	mux.Handle(publicPath, dispatch)
	mux.Handle(publicPath+"/", dispatch)
	return mux
}

func sidecarPublicPath(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "/"
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" || path == "/" {
		return "/"
	}
	return "/" + strings.Trim(path, "/")
}

// Relay returns the underlying Khatru relay for focused package tests.
func (s *Server) Relay() *khatru.Relay {
	return s.relay
}

func (s *Server) applyMetadata(metadata relayMetadata) {
	s.relay.Info.Name = metadata.Name
	s.relay.Info.Description = metadata.Description
	s.relay.Info.Icon = metadata.Icon
}

// Close releases resources of a replacement runtime that was initialized but not started.
func (s *Server) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// Run starts the sidecar HTTP server and shuts it down when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	addr := s.cfg.ListenAddr
	if addr == "" {
		addr = "0.0.0.0:3334"
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("relay sidecar starting", zap.String("addr", addr), zap.String("public_url", s.cfg.PublicURL))
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sweepCtx, stopSweep := context.WithCancel(ctx)
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		s.runRetentionSweeps(sweepCtx)
	}()

	select {
	case err := <-errCh:
		stopSweep()
		<-sweepDone
		return fmt.Errorf("relay sidecar server: %w", err)
	case <-ctx.Done():
		stopSweep()
		<-sweepDone
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("relay sidecar shutdown: %w", err)
	}
	if err := s.store.Close(); err != nil {
		return fmt.Errorf("close relay sidecar store: %w", err)
	}
	s.logger.Info("relay sidecar stopped")
	return nil
}

func (s *Server) runRetentionSweeps(ctx context.Context) {
	sweep := func() {
		deleted, err := s.store.SweepRetention(ctx, time.Now(), s.cfg.EventRetention, s.cfg.RequestRetention)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("relay sidecar retention sweep failed", zap.Error(err))
			}
			return
		}
		if deleted > 0 {
			s.logger.Info("relay sidecar retention sweep completed", zap.Int64("deleted_events", deleted))
		}
	}

	sweep()
	ticker := time.NewTicker(retentionSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
