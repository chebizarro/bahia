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

// Server wraps the Khatru relay used by Bahia's local sidecar topology.
type Server struct {
	cfg        config.RelaySidecarConfig
	relay      *khatru.Relay
	store      *memoryStore
	httpServer *http.Server
	logger     *zap.Logger
}

// New creates a Khatru sidecar relay with in-memory, rebuildable storage.
func New(nostrCfg config.NostrConfig, logger *zap.Logger) (*Server, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if nostrCfg.Sidecar.MaxQueryLimit <= 0 {
		nostrCfg.Sidecar.MaxQueryLimit = 500
	}
	if nostrCfg.Sidecar.PublicURL == "" {
		nostrCfg.Sidecar.PublicURL = "ws://localhost:3334"
	}

	pol, err := newPolicy(nostrCfg)
	if err != nil {
		return nil, err
	}
	store := newMemoryStore()
	relay := khatru.NewRelay()
	relay.Log = log.New(os.Stderr, "[bahia-relay-sidecar] ", log.LstdFlags)
	relay.ServiceURL = nostrCfg.Sidecar.PublicURL
	relay.Info.Name = "Bahia Relay Sidecar"
	relay.Info.Description = "Local Khatru relay sidecar for Bahia browser bootstrap and Nostr events."
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
	relay.OnEventSaved = func(ctx context.Context, event nostr.Event) {
		logger.Debug("sidecar event accepted", zap.String("event_id", event.ID.Hex()), zap.Uint16("kind", uint16(event.Kind)))
	}

	return &Server{
		cfg:    nostrCfg.Sidecar,
		relay:  relay,
		store:  store,
		logger: logger,
	}, nil
}

// Handler returns the relay HTTP handler. It supports NIP-11 over HTTP and
// Nostr WebSocket traffic on the configured public URL/path.
func (s *Server) Handler() http.Handler {
	publicPath := sidecarPublicPath(s.cfg.PublicURL)
	if publicPath == "/" {
		return s.relay
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
		s.relay.ServeHTTP(w, rewritten)
	})
	mux.Handle(publicPath, s.relay)
	mux.Handle(publicPath+"/", s.relay)
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

	select {
	case err := <-errCh:
		return fmt.Errorf("relay sidecar server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("relay sidecar shutdown: %w", err)
	}
	s.logger.Info("relay sidecar stopped")
	return nil
}
