package relaysidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/codec/betterbinary"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

const retentionSweepInterval = 15 * time.Minute

// configAckSigner signs config-consumer acknowledgements as the service.
type configAckSigner struct{ signer nostr.Signer }

func (s configAckSigner) Sign(ctx context.Context, event *nostr.Event) error {
	return s.signer.SignEvent(ctx, event)
}

type relayConfigPublisher struct{ relay *khatru.Relay }

func (p relayConfigPublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if _, err := p.relay.AddEvent(ctx, event); err != nil {
		return 0, err
	}
	return 1, nil
}

// Server wraps the Khatru relay used by Bahia's local sidecar topology.
type Server struct {
	cfg         config.RelaySidecarConfig
	relay       *khatru.Relay
	store       *eventStore
	retention   retentionPolicy
	swept       sweepCounters
	policy      *adminPolicy
	admission   *policy // event admission policy (wraps adminPolicy + intent authors)
	trustEpoch  *sidecarTrustEpoch
	readAuth    *readAuthPolicy
	httpServer  *http.Server
	logger      *zap.Logger
	consumer    *ConfigConsumer
	fanout      *liveFanout
	configDirty *configDirtySet
	mirror      *mirrorForwarder
	// configAfter schedules a re-read of a desired coordinate at its NIP-40
	// expiration; tests replace it.
	configAfter func(time.Duration, func())
	wg          sync.WaitGroup
}

// sweepCounters are the events retention sweeps deleted, by cause.
type sweepCounters struct {
	expired, request, regular atomic.Uint64
}

// ServerOption configures an optional sidecar integration.
type ServerOption func(*serverOptions)

type serverOptions struct {
	mirrorTransport MirrorTransport
}

// WithMirrorTransport supplies the NIP-aware external relay pool when
// mirror_external is enabled. A missing transport fails sidecar startup.
func WithMirrorTransport(transport MirrorTransport) ServerOption {
	return func(options *serverOptions) { options.mirrorTransport = transport }
}

// New creates a Khatru sidecar relay backed by durable storage. signer is the
// service identity (servicesigner.Open in cmd/relay): its pubkey is the NIP-11
// pubkey and is always admitted to write and read. The caller owns the signer and closes it after the server
// stops.
func New(ctx context.Context, nostrCfg config.NostrConfig, signer nostr.Signer, logger *zap.Logger, opts ...ServerOption) (*Server, error) {
	var options serverOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if signer == nil {
		return nil, errors.New("relay sidecar requires the service signer (nostr.signer or nostr.private_key)")
	}
	servicePubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("read relay sidecar service pubkey: %w", err)
	}
	if servicePubkey == nostr.ZeroPK {
		return nil, errors.New("relay sidecar service signer reported no pubkey")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if nostrCfg.Sidecar.MaxQueryLimit <= 0 {
		nostrCfg.Sidecar.MaxQueryLimit = 2000
	}
	if nostrCfg.Sidecar.PublicURL == "" {
		nostrCfg.Sidecar.PublicURL = "ws://localhost:3334"
	}
	if nostrCfg.Sidecar.SubscriberQueueSize <= 0 {
		nostrCfg.Sidecar.SubscriberQueueSize = defaultSubscriberQueueSize
	}
	if nostrCfg.Sidecar.NegentropyMaxEvents <= 0 {
		nostrCfg.Sidecar.NegentropyMaxEvents = config.DefaultRelaySidecarNegentropyMaxEvents
	}
	retention := newRetentionPolicy(nostrCfg.Sidecar)
	store, err := openEventStore(context.Background(), nostrCfg.Sidecar.DataDir, logger)
	if err != nil {
		return nil, err
	}
	trust, err := openSidecarTrustEpoch(ctx, nostrCfg, store, logger)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("resolve sidecar trust epoch: %w", err)
	}
	owned := false
	defer func() {
		if !owned {
			if trust != nil {
				_ = trust.state.Close()
			}
			_ = store.Close()
		}
	}()
	if trust != nil && trust.resolution.Pubkey != servicePubkey.Hex() {
		return nil, fmt.Errorf("relay sidecar signer does not match resolved trust epoch")
	}

	pol := newPolicy(servicePubkey)
	pol.trust = trust
	admin, err := openAdminPolicy(nostrCfg.Sidecar)
	if err != nil {
		return nil, err
	}
	pol.admin = admin
	readAuth := newReadAuthPolicy(nostrCfg.Sidecar, pol, logger)

	var mirror *mirrorForwarder
	if nostrCfg.Sidecar.MirrorExternal {
		mirror, err = newMirrorForwarder(ctx, nostrCfg, signer, store, pol, logger, options.mirrorTransport)
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("configure relay sidecar mirror: %w", err)
		}
	}
	relay := khatru.NewRelay()
	// Khatru requires *log.Logger; route its diagnostics through the existing zap sink.
	relay.Log = zap.NewStdLog(logger.Named("bahia-relay-sidecar"))
	relay.ServiceURL = nostrCfg.Sidecar.PublicURL
	state := admin.snapshot()
	relay.Info.Name = state.Metadata.Name
	relay.Info.Description = state.Metadata.Description
	relay.Info.Icon = state.Metadata.Icon
	relay.Info.PostingPolicy = "Accepts every valid signed Nostr event kind. Event authorization belongs to protocol consumers, not relay kind allowlists. Regular and ephemeral events created more than one year ago are refused; replaceable and addressable events and deletion requests are accepted at any age. If mirror_external is enabled, this relay asynchronously forwards stored accepted events only to configured Bahia-controlled external service relays; public relays are never mirror targets."
	// 9: kind-5 deletions are applied and kept as tombstones (store.go).
	// 40: expired events are refused, hidden and swept. 45: COUNT from the
	// indexes. 77: negentropy.
	relay.Info.SupportedNIPs = []any{1, 9, 11, 17, 40, 42, 44, 45, 51, 59, 65, 70, 77}
	relay.Info.Limitation = &nip11.RelayLimitationDocument{
		MaxMessageLength: int(relay.MaxMessageSize),
		MaxLimit:         nostrCfg.Sidecar.MaxQueryLimit,
		DefaultLimit:     nostrCfg.Sidecar.MaxQueryLimit, // a REQ without limit gets the cap
		MaxContentLength: betterbinary.MaxContentSize,
		// No CreatedAtLowerLimit: the one-year cap is per kind (see
		// nostrutil.AgeCapped), and the field would claim it for every kind.
		CreatedAtUpperLimit: int64(maxEventFutureSkew / time.Second),
	}
	relay.Info.Retention = retention.nip11()
	relay.OverwriteRelayInformation = func(_ context.Context, _ *http.Request, info nip11.RelayInformationDocument) nip11.RelayInformationDocument {
		limitation := *info.Limitation
		limitation.RestrictedWrites = admin.restrictsWrites()
		limitation.AuthRequired = readAuth.mode == config.ReadAuthModeEnforce
		info.Limitation = &limitation
		return info
	}
	relay.Negentropy = true
	relay.Info.PubKey = &servicePubkey

	// Khatru broadcasts to matching subscribers synchronously, before it sends
	// the publisher's OK. The fanout disables that path and delivers through
	// per-connection bounded queues instead. The publisher is acknowledged
	// promptly, and a slow subscriber is CLOSED rather than silently skipped.
	fanout := newLiveFanout(logger, nostrCfg.Sidecar.SubscriberQueueSize)
	fanout.install(relay)

	relay.OnEvent = pol.acceptEvent
	// Khatru runs a REQ filter's stored query before it registers the live
	// listener. beginRequest starts buffering live matches before the query, so
	// an event saved in between is still delivered (see pendingListener).
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if reject, msg := pol.acceptFilter(ctx, filter); reject {
			return reject, msg
		}
		if reject, msg := readAuth.checkReadAuth(ctx, filter); reject {
			return reject, msg
		}
		if khatru.IsNegentropySession(ctx) {
			return negentropyTooLarge(ctx, store, filter, nostrCfg.Sidecar.NegentropyMaxEvents)
		}
		fanout.beginRequest(ctx, filter)
		return false, ""
	}
	relay.OnCount = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if reject, msg := pol.acceptFilter(ctx, filter); reject {
			return reject, msg
		}
		return readAuth.checkReadAuth(ctx, filter)
	}
	relay.StoreEvent = store.Save
	relay.ReplaceEvent = store.Replace
	// DeleteEvent stays nil: the store applies kind-5 requests itself when it
	// saves them (NIP-09 `e` and `a` semantics), and a nil hook makes
	// khatru's weaker handler a no-op. NIP-11 advertises 9 explicitly.
	relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		limit := nostrCfg.Sidecar.MaxQueryLimit
		if khatru.IsNegentropySession(ctx) {
			// NIP-77 reconciles the whole set; OnRequest already refused a
			// filter matching more than this.
			limit = nostrCfg.Sidecar.NegentropyMaxEvents
		}
		return fanout.trackStored(ctx, store.Query(ctx, filter, limit))
	}
	relay.Count = store.Count
	var consumer *ConfigConsumer
	// Fleet operators authorized by the daemon's TrustSet are also allowed to
	// author desired config. Explicit sidecar authors remain supported.
	configAuthors := append(append([]string(nil), nostrCfg.Sidecar.ConfigTrustedPubkeys...), nostrCfg.AuthorizedPubkeys...)
	if len(configAuthors) > 0 && nostrCfg.Sidecar.ConfigProjectionPath != "" {
		consumer, err = NewConfigConsumer(ConfigConsumerConfig{
			ServiceID: nostrCfg.Sidecar.ServiceID, Scope: nostrCfg.Sidecar.Scope,
			ProjectionPath: nostrCfg.Sidecar.ConfigProjectionPath,
			TrustedAuthors: configAuthors,
			Signer:         configAckSigner{signer: signer}, Publisher: relayConfigPublisher{relay: relay},
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
			if mirror != nil {
				mirror.transport.Close()
			}
			_ = store.Close()
			return nil, err
		}
		pol.configAuthors = make(map[string]bool, len(configAuthors))
		for _, author := range configAuthors {
			pol.configAuthors[author] = true
		}
	}
	var configDirty *configDirtySet
	if consumer != nil {
		configDirty = newConfigDirtySet()
	}
	// OnEventSaved also runs for internal AddEvent publishes, such as config
	// status, where khatru never broadcasts. Dispatching here covers both paths.
	relay.OnEventSaved = func(_ context.Context, event nostr.Event) {
		logger.Debug("sidecar event accepted", zap.String("event_id", event.ID.Hex()), zap.Uint16("kind", uint16(event.Kind)))
		fanout.dispatch(event)
		if mirror != nil {
			mirror.Notify()
		}
		if configDirty != nil && (event.Kind == configListKind || event.Kind == configPolicyKind) {
			configDirty.mark(replaceableKey(event))
		}
		if configDirty != nil && event.Kind == nostr.KindDeletion {
			// The store has already applied the request (NIP-09). Re-read every
			// desired coordinate; one it deleted reads back absent and is
			// withdrawn, the rest are unchanged no-ops.
			for key := range consumer.desiredEvents() {
				configDirty.mark(key)
			}
		}
	}
	relay.OnEphemeralEvent = func(_ context.Context, event nostr.Event) {
		fanout.dispatch(event)
	}
	if err := acknowledgeSidecarEpoch(ctx, trust, nostrCfg.Sidecar.AckPrivateKeyFile, store, relay); err != nil {
		return nil, err
	}

	owned = true
	return &Server{
		cfg:         nostrCfg.Sidecar,
		relay:       relay,
		store:       store,
		retention:   retention,
		policy:      admin,
		admission:   pol,
		trustEpoch:  trust,
		readAuth:    readAuth,
		logger:      logger,
		consumer:    consumer,
		fanout:      fanout,
		configDirty: configDirty,
		mirror:      mirror,
		configAfter: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+metricsPath, s.serveMetrics)
	mux.HandleFunc("GET /ready", s.serveReady)
	if publicPath == "/" {
		mux.Handle("/", dispatch)
		return mux
	}

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

func (s *Server) serveReady(w http.ResponseWriter, _ *http.Request) {
	response := struct {
		Status string              `json:"status"`
		Mirror []MirrorRelayStatus `json:"mirror,omitempty"`
	}{Status: "pass"}
	if err := s.store.ping(); err != nil {
		http.Error(w, "sidecar event store unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.mirror != nil {
		status, err := s.mirror.Status()
		if err != nil {
			http.Error(w, "sidecar mirror progress unavailable", http.StatusServiceUnavailable)
			return
		}
		response.Mirror = status
		for _, relay := range status {
			if relay.Backlog > 0 || relay.Failure != "" {
				response.Status = "warn"
				break
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
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

// SetIntentAuthors replaces the set of pubkeys that may publish intent events
// through the sidecar. This is the in-process equivalent of the NIP-86
// setintentauthors method; the daemon calls it directly when the sidecar runs
// in the same process, and via NIP-86 when it runs as a separate binary.
func (s *Server) SetIntentAuthors(pubkeys []string) {
	s.admission.SetIntentAuthors(pubkeys)
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
	if s.mirror != nil {
		s.mirror.transport.Close()
	}
	if s.trustEpoch != nil {
		_ = s.trustEpoch.state.Close()
	}
	return s.store.Close()
}

// Run starts the sidecar HTTP server and shuts it down when ctx is cancelled.
func (s *Server) Run(ctx context.Context) (runErr error) {
	defer func() {
		if err := s.Close(); err != nil && runErr == nil {
			runErr = fmt.Errorf("close relay sidecar store: %w", err)
		}
	}()
	addr := s.cfg.ListenAddr
	if addr == "" {
		addr = "0.0.0.0:3334"
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	if s.mirror != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.mirror.Run(workerCtx)
		}()
	}
	s.startConfigWorker(workerCtx)
	if s.consumer != nil {
		s.consumer.Start(workerCtx)
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
		runErr = fmt.Errorf("relay sidecar server: %w", err)
	case <-ctx.Done():
	}
	stopSweep()
	<-sweepDone

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("relay sidecar shutdown: %w", err)
	}
	stopWorkers()
	s.wg.Wait()
	if s.consumer != nil {
		s.consumer.wait()
	}
	if runErr != nil {
		return runErr
	}
	s.logger.Info("relay sidecar stopped")
	return nil
}

// negentropyTooLarge refuses a NIP-77 NEG-OPEN whose filter matches more
// events than one session reconciles, instead of reconciling a truncated set.
func negentropyTooLarge(ctx context.Context, store *eventStore, filter nostr.Filter, maxEvents int) (bool, string) {
	if filter.Limit > 0 && filter.Limit <= maxEvents {
		return false, ""
	}
	count, err := store.Count(ctx, filter)
	if err != nil {
		return true, "error: could not size the negentropy set"
	}
	if int64(count) > int64(maxEvents) {
		return true, fmt.Sprintf("blocked: filter matches %d events, more than the %d this relay reconciles in one NIP-77 session; narrow it with since/until", count, maxEvents)
	}
	return false, ""
}

func (s *Server) sweepRetention(ctx context.Context, now time.Time) (sweepResult, error) {
	result, err := s.store.SweepRetention(ctx, now, s.retention)
	s.swept.expired.Add(uint64(result.Expired))
	s.swept.request.Add(uint64(result.Request))
	s.swept.regular.Add(uint64(result.Regular))
	return result, err
}

func (s *Server) runRetentionSweeps(ctx context.Context) {
	sweep := func() {
		result, err := s.sweepRetention(ctx, time.Now())
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("relay sidecar retention sweep failed", zap.Error(err))
			}
			return
		}
		if result.total() > 0 {
			s.logger.Info("relay sidecar retention sweep completed",
				zap.Int64("expired_events", result.Expired),
				zap.Int64("request_events", result.Request),
				zap.Int64("regular_events", result.Regular))
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
