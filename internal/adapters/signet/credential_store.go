package signet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// signetReplyWindow is how far back Signet's management replies can be
// dated. Signet gift-wraps every ContextVM reply with NIP-59 timestamps
// randomized up to two days into the past (nostrc nips/nip17 GW_TIME_WINDOW);
// signetctl subscribes with the same two days plus an hour. A narrower since
// lets the relay filter out live replies.
const signetReplyWindow = 49 * time.Hour

// maxReplyContent bounds a gift wrap's content before any cryptography: a
// NIP-44 payload of the largest credential Signet delivers (1 MiB), sealed
// twice, stays well below it.
const maxReplyContent = 4 << 20

const (
	defaultReconnectMin = time.Second
	defaultReconnectMax = time.Minute
)

// signetCredentialIDPattern is Signet's credential id: "cred_" and the hex
// BLAKE2b digest of (owner, type, label) (signet_secret_id_generate).
var signetCredentialIDPattern = regexp.MustCompile(`^cred_[0-9a-f]{64}$`)

// errReplySubscriptionDown marks requests refused while the reply
// subscription has not caught up.
var errReplySubscriptionDown = errors.New("signet reply subscription is not caught up")

// CredentialStoreConfig configures the Signet-backed secret store.
type CredentialStoreConfig struct {
	// SignetPubkey is Signet's management identity (its bunker pubkey).
	SignetPubkey nostr.PubKey
	// Relays carry the gift-wrapped management requests and replies.
	Relays []string
	// ClientKey is the dedicated requester identity Signet authorizes for
	// credential delivery. It is never the service key.
	ClientKey nostr.SecretKey
	// Owner is the Signet agent id that owns the credentials (the vault).
	Owner string
	// RequestTimeout bounds each management round trip.
	RequestTimeout time.Duration
	// ReconnectMin and ReconnectMax bound the exponential backoff before the
	// reply subscription is reopened after it ended; zero uses 1s and 1m.
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	// ClosedRetryBudget is nostr.closed_retry_budget; 0 keeps the default.
	ClosedRetryBudget int
	// OutboundAdmission gates every gift wrap and NIP-42 AUTH; nil uses the
	// process-wide controller.
	OutboundAdmission *nostrout.Admission
}

// CredentialStore implements secretstore.SecretStore over Signet's
// gift-wrapped ContextVM management plane (credential/inspect,
// credential/deliver, credential/list). One supervised subscription receives
// every reply addressed to the requester; replies are matched to callers by
// JSON-RPC id. See docs/architecture/secret-store.md.
type CredentialStore struct {
	signetPK     nostr.PubKey
	clientSK     nostr.SecretKey
	clientPK     nostr.PubKey
	owner        string
	timeout      time.Duration
	reconnectMin time.Duration
	reconnectMax time.Duration
	pool         *nostrpool.RelayPool
	admission    *nostrout.Admission
	logger       *zap.Logger
	cancel       context.CancelFunc
	done         chan struct{}

	mu sync.Mutex
	// A declared outbound operation permits one publication at a time, but
	// replies to already-published requests can remain in flight.
	batchPublishMu sync.Mutex
	pending        map[string]chan signetJSONRPCResponse
	ready          chan struct{} // closed while the reply subscription is caught up
	subErr         error         // why the subscription is not caught up
	everDown       bool          // a subscription ended at least once
	lastAt         time.Time
	lastErr        error
	everDone       bool
	// labels holds the label of every credential an Inspect admitted. A
	// credential id is a digest of (owner, type, label), so an id's label
	// never changes and a cached label proves its tenant without a round
	// trip.
	labels map[string]string
	// reportsVersion: the last delivery reported its version (nostrc-pvj7b),
	// so a credential whose label is cached resolves in one round trip.
	reportsVersion bool
}

// maxRoundTripsPerResolve is the worst case of one Resolve: a one-round-trip
// delivery that comes back without a version (Signet stopped reporting
// versions) and is retried as inspect, deliver, inspect.
const maxRoundTripsPerResolve = 4

// Signet has no multi-credential delivery method. This bounds the number of
// independent requests sharing the store's supervised reply subscription.
const maxConcurrentResolves = 4

// maxCachedLabels bounds the label cache; it is cleared when full.
const maxCachedLabels = 4096

var _ secretstore.SecretStore = (*CredentialStore)(nil)

// OpenCredentialStore validates cfg and starts the reply subscription. It
// does not wait for Signet: the daemon starts while Signet is down, and every
// operation fails closed with secretstore.ErrUnavailable until the
// subscription has caught up and Signet answers.
func OpenCredentialStore(ctx context.Context, cfg CredentialStoreConfig, logger *zap.Logger) (*CredentialStore, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.SignetPubkey == nostr.ZeroPK {
		return nil, fmt.Errorf("signet secret store: Signet pubkey is required")
	}
	if cfg.ClientKey == [32]byte{} {
		return nil, fmt.Errorf("signet secret store: client key is required")
	}
	if len(cfg.Relays) == 0 {
		return nil, fmt.Errorf("signet secret store: at least one relay is required")
	}
	if strings.TrimSpace(cfg.Owner) == "" {
		return nil, fmt.Errorf("signet secret store: credential owner agent id is required")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, fmt.Errorf("signet secret store: request timeout must be positive")
	}
	if cfg.ReconnectMin < 0 || cfg.ReconnectMax < 0 {
		return nil, fmt.Errorf("signet secret store: reconnect backoff must not be negative")
	}
	clientPK := cfg.ClientKey.Public()
	if clientPK == cfg.SignetPubkey {
		return nil, fmt.Errorf("signet secret store: client key must not be Signet's own key")
	}
	admission := nostrout.Or(cfg.OutboundAdmission)
	opts := []nostrpool.RelayPoolOption{
		nostrpool.WithAuthSigner(keyer.NewPlainKeySigner(cfg.ClientKey)),
		nostrpool.WithOutboundAdmission(admission),
	}
	if cfg.ClosedRetryBudget > 0 {
		opts = append(opts, nostrpool.WithRetryableClosedBudget(cfg.ClosedRetryBudget))
	}
	named := logger.Named("signet-secret-store")
	// The pool authenticates as the requester, never as the service, and
	// only addresses the explicitly configured Signet relays (configuration
	// refuses public ones). It publishes no service-signed control-plane
	// event, so it carries no control-plane relay guard.
	s := &CredentialStore{
		signetPK:     cfg.SignetPubkey,
		clientSK:     cfg.ClientKey,
		clientPK:     clientPK,
		owner:        cfg.Owner,
		timeout:      cfg.RequestTimeout,
		reconnectMin: cfg.ReconnectMin,
		reconnectMax: cfg.ReconnectMax,
		pool:         nostrpool.NewRelayPool(cfg.Relays, named, opts...),
		admission:    admission,
		logger:       named,
		done:         make(chan struct{}),
		pending:      make(map[string]chan signetJSONRPCResponse),
		labels:       make(map[string]string),
		ready:        make(chan struct{}),
		subErr:       errors.New("waiting for the first EOSE"),
	}
	if s.reconnectMin == 0 {
		s.reconnectMin = defaultReconnectMin
	}
	if s.reconnectMax == 0 {
		s.reconnectMax = defaultReconnectMax
	}
	if s.reconnectMax < s.reconnectMin {
		s.reconnectMax = s.reconnectMin
	}
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.superviseReplies(subCtx)
	return s, nil
}

// Close ends the reply subscription and the relay pool.
func (s *CredentialStore) Close() {
	s.cancel()
	<-s.done
	s.pool.Close()
}

// LastOutcome reports the store's health: when the last Signet round trip
// ended and, if the reply subscription is down or Signet was unreachable or
// refused this requester, why. An answer about the credential itself (not
// found, revoked, expired, another version, another tenant) is a healthy
// round trip. ok is false before the first round trip.
func (s *CredentialStore) LastOutcome() (at time.Time, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil && s.everDown {
		return s.lastAt, true, fmt.Errorf("%w: %w (%v); reconnecting", secretstore.ErrUnavailable, errReplySubscriptionDown, s.subErr)
	}
	return s.lastAt, s.everDone, s.lastErr
}

// Health lists the owner's credential metadata: an authenticated,
// payload-free round trip that proves the requester is authorized and
// Signet's persistent store is open.
func (s *CredentialStore) Health(ctx context.Context) error {
	var listed struct {
		Count int `json:"count"`
	}
	return s.call(ctx, "credential/list", map[string]any{"agent_id": s.owner}, &listed)
}

// Inspect returns payload-free metadata for a credential of scope that the
// owner holds.
func (s *CredentialStore) Inspect(ctx context.Context, credentialID string, scope secretstore.Scope) (secretstore.Metadata, error) {
	if !signetCredentialIDPattern.MatchString(credentialID) {
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet credential ids are cred_ and 64 hex characters", secretstore.ErrInvalidRef)
	}
	if !scope.Valid() {
		return secretstore.Metadata{}, fmt.Errorf("%w: a credential scope needs an org and a service, or a system purpose", secretstore.ErrDenied)
	}
	var raw signetCredentialMetadata
	if err := s.call(ctx, "credential/inspect", map[string]any{"credential_id": credentialID}, &raw); err != nil {
		return secretstore.Metadata{}, err
	}
	if raw.CredentialID != credentialID {
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet inspected another credential", secretstore.ErrUnavailable)
	}
	if raw.AgentID != s.owner {
		// Signet delivers only the owner's credentials to this requester;
		// any other owner is unresolvable here.
		return secretstore.Metadata{}, fmt.Errorf("%w: credential is not owned by %q", secretstore.ErrDenied, s.owner)
	}
	if !scope.Admits(raw.Label) {
		// One owner holds every credential: the label is the tenant (or
		// system) binding. The other label is never echoed.
		return secretstore.Metadata{}, fmt.Errorf("%w: credential is not labelled %s", secretstore.ErrDenied, scope.Want())
	}
	meta, err := raw.metadata()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	s.mu.Lock()
	if len(s.labels) >= maxCachedLabels {
		clear(s.labels)
	}
	s.labels[credentialID] = raw.Label
	s.mu.Unlock()
	return meta, nil
}

// Resolve fetches the pinned version of a credential of scope under a
// one-use Signet delivery lease.
//
// A credential whose label an earlier Inspect admitted, from a Signet that
// reports the delivered version (nostrc-pvj7b), resolves in one round trip:
// credential/deliver with expected_version, the tenant proven by the cached
// label and the pin by the reported version. Otherwise the delivery is
// preceded by a metadata read (owner, tenant label, status, version); a
// Signet that reports the version settles the pin in its reply, and one that
// does not is bracketed by a second metadata read: rotation increments the
// version atomically and never reuses one, so equal versions before and
// after prove the delivered value is that version.
func (s *CredentialStore) Resolve(ctx context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error) {
	if err := secretstore.ValidateRef(ref); err != nil {
		return nil, err
	}
	if s.oneRoundTrip(ref.CredentialID, scope) {
		value, version, err := s.deliver(ctx, ref)
		if err != nil {
			return nil, err
		}
		if version != nil {
			if *version != ref.Version {
				value.Zeroize()
				return nil, fmt.Errorf("%w: Signet delivered version %d, reference pins %d", secretstore.ErrVersionMismatch, *version, ref.Version)
			}
			return value, nil
		}
		// Signet stopped reporting versions: the delivery proves nothing
		// about the pin without the metadata bracket.
		value.Zeroize()
		s.setReportsVersion(false)
	}
	if err := s.requireActiveVersion(ctx, ref, scope); err != nil {
		return nil, err
	}
	value, version, err := s.deliver(ctx, ref)
	if err != nil {
		return nil, err
	}
	s.setReportsVersion(version != nil)
	if version != nil {
		if *version != ref.Version {
			value.Zeroize()
			return nil, fmt.Errorf("%w: Signet delivered version %d, reference pins %d", secretstore.ErrVersionMismatch, *version, ref.Version)
		}
		return value, nil
	}
	if err := s.requireActiveVersion(ctx, ref, scope); err != nil {
		value.Zeroize()
		return nil, err
	}
	return value, nil
}

// ResolveAll resolves reqs through one shared reply subscription and declared
// outbound operation. Independent Signet deliveries overlap, but admission
// still paces each gift wrap on the bulk lane. It is all or nothing.
func (s *CredentialStore) ResolveAll(ctx context.Context, reqs []secretstore.Request) ([]*secretstore.Value, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	// At most maxRoundTripsPerResolve round trips per request.
	op, err := s.admission.BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: maxRoundTripsPerResolve * len(reqs), MaxWait: s.timeout})
	if err != nil {
		return nil, fmt.Errorf("%w: outbound admission refused %d secret resolves: %w", secretstore.ErrUnavailable, len(reqs), err)
	}
	defer op.Close()
	ctx = nostrout.WithOperation(ctx, op)
	values := make([]*secretstore.Value, len(reqs))
	errs := make([]error, len(reqs))
	work := make(chan int)
	var workers sync.WaitGroup
	for range min(len(reqs), maxConcurrentResolves) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range work {
				values[i], errs[i] = s.Resolve(ctx, reqs[i].Ref, reqs[i].Scope)
			}
		}()
	}
	for i := range reqs {
		work <- i
	}
	close(work)
	workers.Wait()
	for i, err := range errs {
		if err != nil {
			secretstore.ZeroizeAll(values)
			return nil, &secretstore.BatchError{Index: i, Err: err}
		}
	}
	return values, nil
}

func (s *CredentialStore) oneRoundTrip(credentialID string, scope secretstore.Scope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	label, ok := s.labels[credentialID]
	return ok && s.reportsVersion && scope.Admits(label)
}

func (s *CredentialStore) setReportsVersion(reports bool) {
	s.mu.Lock()
	s.reportsVersion = reports
	s.mu.Unlock()
}

// deliver asks for ref's value; version is the delivered version when
// Signet reports it.
func (s *CredentialStore) deliver(ctx context.Context, ref domain.SecretCredentialRef) (*secretstore.Value, *int, error) {
	var delivered signetDelivery
	if err := s.call(ctx, "credential/deliver", map[string]any{"agent_id": s.owner, "credential_id": ref.CredentialID, "expected_version": ref.Version}, &delivered); err != nil {
		return nil, nil, err
	}
	payload, leaseExpiresAt, err := delivered.open(ref.CredentialID, s.owner)
	if err != nil {
		return nil, nil, err
	}
	return secretstore.NewValue(payload, leaseExpiresAt), delivered.Version, nil
}

func (s *CredentialStore) requireActiveVersion(ctx context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) error {
	meta, err := s.Inspect(ctx, ref.CredentialID, scope)
	if err != nil {
		return err
	}
	switch meta.Status {
	case secretstore.StatusActive:
	case secretstore.StatusRevoked:
		return secretstore.ErrRevoked
	case secretstore.StatusExpired:
		return secretstore.ErrExpired
	}
	if meta.Version != ref.Version {
		return fmt.Errorf("%w: active version is %d, reference pins %d", secretstore.ErrVersionMismatch, meta.Version, ref.Version)
	}
	return nil
}

// call sends one gift-wrapped JSON-RPC request and waits for its reply.
func (s *CredentialStore) call(ctx context.Context, method string, params map[string]any, out any) (err error) {
	defer func() { s.record(err) }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// A request is only sent once the reply subscription has caught up, so
	// its reply cannot fall in a gap.
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		// The subscription's reason can name relays; it is in LastOutcome
		// and the log, not in this caller-visible error.
		return fmt.Errorf("%w: %w", secretstore.ErrUnavailable, errReplySubscriptionDown)
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return fmt.Errorf("signet secret store: request id: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	replies := make(chan signetJSONRPCResponse, 1)
	s.mu.Lock()
	s.pending[id] = replies
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	gift, err := s.wrapRequest(id, method, params)
	if err != nil {
		return err
	}
	// The reply can arrive before every relay has answered OK (one relay may
	// be slow), so publication runs beside the wait; it only ends the call
	// early when no relay accepted the request.
	published := make(chan error, 1)
	go func() {
		if nostrout.OperationFromContext(ctx) != nil {
			s.batchPublishMu.Lock()
			defer s.batchPublishMu.Unlock()
		}
		results, err := s.pool.PublishWithResults(ctx, gift)
		published <- s.publishOutcome(method, results, err)
	}()
	for {
		select {
		case reply := <-replies:
			if published != nil && nostrout.OperationFromContext(ctx) != nil {
				// An operation admits one publication at a time: the
				// next request of the batch waits for this one's OKs.
				select {
				case <-published:
				case <-ctx.Done():
				}
			}
			return decodeSignetReply(method, reply, out)
		case perr := <-published:
			if perr != nil {
				return perr
			}
			published = nil
		case <-ctx.Done():
			return fmt.Errorf("%w: Signet did not answer %s within %s", secretstore.ErrUnavailable, method, s.timeout)
		}
	}
}

func (s *CredentialStore) record(err error) {
	if err != nil && !errors.Is(err, secretstore.ErrUnavailable) && !errors.Is(err, secretstore.ErrDenied) {
		err = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAt, s.lastErr, s.everDone = time.Now(), err, true
}

// publishOutcome is nil when at least one relay accepted the request. Each
// relay's refusal is logged; the error, which reaches intent status and
// reveal responses, names no relay. An outbound admission refusal (budget,
// circuit breaker, kill switch) arrives as publishErr with no per-relay
// results and is local, so its reason is kept; the request is not retried.
func (s *CredentialStore) publishOutcome(method string, results []nostrpool.PublishResult, publishErr error) error {
	for _, result := range results {
		if result.Succeeded() {
			return nil
		}
	}
	for _, result := range results {
		reason := result.Reason
		if result.Error != nil {
			reason = result.Error.Error()
		}
		s.logger.Warn("Signet relay refused a secret store request", zap.String("method", method), zap.String("relay", result.RelayURL), zap.String("reason", reason))
	}
	if publishErr != nil && len(results) == 0 {
		return fmt.Errorf("%w: outbound admission refused the Signet request: %w", secretstore.ErrUnavailable, publishErr)
	}
	return fmt.Errorf("%w: no relay accepted the Signet request (%d refused; see the daemon log)", secretstore.ErrUnavailable, len(results))
}

// wrapRequest builds rumor (kind 14) → seal (kind 13, signed by the
// requester) → gift wrap (kind 1059, ephemeral key), all to Signet.
func (s *CredentialStore) wrapRequest(id, method string, params map[string]any) (nostr.Event, error) {
	body, err := json.Marshal(signetJSONRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encode Signet request: %w", err)
	}
	now := nostr.Now()
	rumor := nostr.Event{Kind: nostr.KindDirectMessage, Content: string(body), CreatedAt: now, PubKey: s.clientPK, Tags: nostr.Tags{{"p", s.signetPK.Hex()}}}
	rumor.ID = rumor.GetID()
	sealKey, err := nip44.GenerateConversationKey(s.signetPK, s.clientSK)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("derive Signet seal key: %w", err)
	}
	sealContent, err := nip44.Encrypt(rumor.String(), sealKey)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encrypt Signet rumor: %w", err)
	}
	seal := nostr.Event{Kind: nostr.KindSeal, Content: sealContent, CreatedAt: now, Tags: nostr.Tags{}}
	if err := seal.Sign(s.clientSK); err != nil {
		return nostr.Event{}, fmt.Errorf("sign Signet seal: %w", err)
	}
	ephemeral := nostr.Generate()
	giftKey, err := nip44.GenerateConversationKey(s.signetPK, ephemeral)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("derive Signet gift-wrap key: %w", err)
	}
	giftContent, err := nip44.Encrypt(seal.String(), giftKey)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encrypt Signet seal: %w", err)
	}
	gift := nostr.Event{Kind: signetKindGiftWrap, Content: giftContent, CreatedAt: now, Tags: nostr.Tags{{"p", s.signetPK.Hex()}}}
	if err := gift.Sign(ephemeral); err != nil {
		return nostr.Event{}, fmt.Errorf("sign Signet gift wrap: %w", err)
	}
	return gift, nil
}

// superviseReplies keeps one reply subscription open for the store's
// lifetime and reopens it with exponential backoff whenever it ends (every
// relay refused it for good, or left the pool).
func (s *CredentialStore) superviseReplies(ctx context.Context) {
	defer close(s.done)
	backoff := s.reconnectMin
	for {
		caughtUp, err := s.readReplies(ctx)
		if ctx.Err() != nil {
			return
		}
		s.setDown(err)
		s.logger.Warn("Signet reply subscription ended; reconnecting", zap.Error(err), zap.Duration("backoff", backoff))
		if caughtUp {
			backoff = s.reconnectMin
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, s.reconnectMax)
	}
}

func (s *CredentialStore) setUp() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil {
		s.subErr = nil
		close(s.ready)
	}
}

func (s *CredentialStore) setDown(reason error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr == nil {
		s.ready = make(chan struct{})
	}
	s.subErr = reason
	s.everDown = true
}

// readReplies runs one reply subscription until it ends. The store is ready
// once a relay has sent EOSE: from then on a reply to a new request is
// delivered live. Every gift wrap is checked before any cryptography;
// replies nobody waits for (history, or a call that already timed out) are
// dropped. caughtUp reports whether this subscription ever reached EOSE.
func (s *CredentialStore) readReplies(ctx context.Context) (caughtUp bool, err error) {
	// Replies are backdated by up to two days, so the since bound must reach
	// that far back; on a reissue after a dropped connection the resume
	// cursor keeps the same reach, and the pool's dedupe absorbs replays.
	sub, err := s.pool.SubscribeWithOptions(ctx, []nostr.Filter{signetReplyFilter(s.clientPK, nostr.Timestamp(time.Now().Add(-signetReplyWindow).Unix()))}, nostrpool.SubscribeOptions{AwaitUnavailableRelays: true, ResumeOverlap: signetReplyWindow, ValidateEvent: s.plausibleReply})
	if err != nil {
		return false, fmt.Errorf("subscribe to Signet replies: %w", err)
	}
	defer sub.Close()
	eose, closed := sub.RelayEOSE, sub.Closed
	for {
		select {
		case <-ctx.Done():
			return caughtUp, ctx.Err()
		case _, ok := <-eose:
			if !ok {
				eose = nil
				continue
			}
			if !caughtUp {
				caughtUp = true
				s.setUp()
			}
		case ev, ok := <-sub.Events:
			if !ok {
				if gaveUp := sub.GaveUp(); gaveUp != nil {
					return caughtUp, gaveUp
				}
				return caughtUp, errors.New("reply subscription closed")
			}
			reply, err := s.openReply(ev)
			if err != nil {
				s.logger.Debug("ignoring gift wrap that is not a Signet reply", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
				continue
			}
			s.mu.Lock()
			waiter := s.pending[reply.ID]
			s.mu.Unlock()
			if waiter == nil {
				clear(reply.Result)
				continue
			}
			select {
			case waiter <- reply:
			default:
				clear(reply.Result)
			}
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			s.logger.Warn("Signet reply relay closed the subscription", zap.String("relay", c.RelayURL), zap.String("reason", c.Reason), zap.Bool("terminal", c.Terminal))
		}
	}
}

// plausibleReply runs before any ECDH: a gift wrap addressed to the
// requester, of bounded size, whose id and ephemeral signature verify.
func (s *CredentialStore) plausibleReply(ev *nostr.Event) bool {
	if ev.Kind != signetKindGiftWrap || len(ev.Content) == 0 || len(ev.Content) > maxReplyContent {
		return false
	}
	addressed := false
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == s.clientPK.Hex() {
			addressed = true
		}
	}
	return addressed && ev.CheckID() && ev.VerifySignature()
}

func (s *CredentialStore) openReply(gift *nostr.Event) (signetJSONRPCResponse, error) {
	var none signetJSONRPCResponse
	if !s.plausibleReply(gift) {
		return none, fmt.Errorf("not a gift wrap addressed to the requester")
	}
	giftKey, err := nip44.GenerateConversationKey(gift.PubKey, s.clientSK)
	if err != nil {
		return none, err
	}
	sealJSON, err := nip44.Decrypt(gift.Content, giftKey)
	if err != nil {
		return none, fmt.Errorf("open gift wrap: %w", err)
	}
	var seal nostr.Event
	if err := json.Unmarshal([]byte(sealJSON), &seal); err != nil {
		return none, fmt.Errorf("decode seal: %w", err)
	}
	if seal.Kind != nostr.KindSeal || seal.PubKey != s.signetPK || !seal.CheckID() || !seal.VerifySignature() {
		return none, fmt.Errorf("seal is not signed by Signet")
	}
	sealKey, err := nip44.GenerateConversationKey(s.signetPK, s.clientSK)
	if err != nil {
		return none, err
	}
	rumorJSON, err := nip44.Decrypt(seal.Content, sealKey)
	if err != nil {
		return none, fmt.Errorf("open seal: %w", err)
	}
	// The decrypted texts are Go strings and cannot be overwritten; the
	// byte copies made to decode them can.
	rumorBytes := []byte(rumorJSON)
	defer clear(rumorBytes)
	var rumor nostr.Event
	if err := json.Unmarshal(rumorBytes, &rumor); err != nil {
		return none, fmt.Errorf("decode rumor: %w", err)
	}
	// NIP-59: the rumor's author is the seal's signer; NIP-17: a kind-14
	// message addressed to the requester.
	if rumor.PubKey != s.signetPK {
		return none, fmt.Errorf("rumor author is not Signet")
	}
	if rumor.Kind != nostr.KindDirectMessage {
		return none, fmt.Errorf("rumor kind %d is not a direct message", rumor.Kind)
	}
	if tag := rumor.Tags.Find("p"); len(tag) < 2 || tag[1] != s.clientPK.Hex() {
		return none, fmt.Errorf("rumor is not addressed to the requester")
	}
	contentBytes := []byte(rumor.Content)
	defer clear(contentBytes)
	var reply signetJSONRPCResponse
	if err := json.Unmarshal(contentBytes, &reply); err != nil {
		return none, fmt.Errorf("decode JSON-RPC reply: %w", err)
	}
	if reply.ID == "" {
		clear(reply.Result)
		return none, fmt.Errorf("reply has no id")
	}
	return reply, nil
}

// decodeSignetReply maps a JSON-RPC error onto the secretstore sentinels by
// Signet's stable reason code (error.data). Only the method and the code
// reach callers, never Signet's free-text message. The raw result is
// overwritten once decoded.
func decodeSignetReply(method string, reply signetJSONRPCResponse, out any) error {
	defer clear(reply.Result)
	if reply.Error != nil {
		code := reply.Error.Data
		var sentinel error
		switch {
		case code == "not_found" || code == "credential_not_found":
			sentinel = secretstore.ErrNotFound
		case code == "revoked" || code == "credential_revoked":
			sentinel = secretstore.ErrRevoked
		case code == "expired" || code == "credential_expired":
			sentinel = secretstore.ErrExpired
		case code == "version_mismatch":
			sentinel = secretstore.ErrVersionMismatch
		case code == "not_owner" || code == "no_capability" || code == "deny_listed" || code == "type_denied" || code == "lease_invalid" || code == "unauthorized":
			sentinel = secretstore.ErrDenied
		case code == "" && reply.Error.Code == -32002:
			// The requester is not authorized to use the management plane.
			sentinel = secretstore.ErrDenied
		default:
			sentinel = secretstore.ErrUnavailable
		}
		if code == "" {
			code = fmt.Sprintf("code %d", reply.Error.Code)
		}
		return fmt.Errorf("%w: Signet %s refused (%s)", sentinel, method, code)
	}
	if out == nil {
		return nil
	}
	if len(reply.Result) == 0 || string(reply.Result) == "null" {
		return fmt.Errorf("%w: Signet %s returned no result", secretstore.ErrUnavailable, method)
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		return fmt.Errorf("%w: decode Signet %s result: %v", secretstore.ErrUnavailable, method, err)
	}
	return nil
}

// signetCredentialMetadata is Signet's payload-free metadata
// (signet_mgmt_add_secret_metadata).
type signetCredentialMetadata struct {
	CredentialID string `json:"credential_id"`
	AgentID      string `json:"agent_id"`
	SecretType   string `json:"secret_type"`
	Label        string `json:"label"`
	CreatedAt    int64  `json:"created_at"`
	RotatedAt    *int64 `json:"rotated_at"`
	ExpiresAt    *int64 `json:"expires_at"`
	RevokedAt    *int64 `json:"revoked_at"`
	Version      int    `json:"version"`
	Status       string `json:"status"`
}

func (m signetCredentialMetadata) metadata() (secretstore.Metadata, error) {
	status := secretstore.Status(m.Status)
	switch status {
	case secretstore.StatusActive, secretstore.StatusExpired, secretstore.StatusRevoked:
	default:
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet reported unknown credential status %q", secretstore.ErrUnavailable, m.Status)
	}
	if m.Version < 1 {
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet reported credential version %d", secretstore.ErrUnavailable, m.Version)
	}
	unix := func(v *int64) time.Time {
		if v == nil || *v == 0 {
			return time.Time{}
		}
		return time.Unix(*v, 0).UTC()
	}
	return secretstore.Metadata{
		CredentialID: m.CredentialID,
		Owner:        m.AgentID,
		Type:         m.SecretType,
		Label:        m.Label,
		Version:      m.Version,
		Status:       status,
		CreatedAt:    unix(&m.CreatedAt),
		RotatedAt:    unix(m.RotatedAt),
		ExpiresAt:    unix(m.ExpiresAt),
		RevokedAt:    unix(m.RevokedAt),
	}, nil
}

// signetDelivery is the credential/deliver result. PayloadB64 stays raw JSON
// so the base64 text Bahia owns can be overwritten after decoding. Version is
// present when Signet reports the delivered version.
type signetDelivery struct {
	CredentialID   string          `json:"credential_id"`
	AgentID        string          `json:"agent_id"`
	PayloadB64     json.RawMessage `json:"payload_b64"`
	LeaseExpiresAt int64           `json:"lease_expires_at"`
	Version        *int            `json:"version"`
}

func (d *signetDelivery) open(credentialID, owner string) ([]byte, time.Time, error) {
	defer clear(d.PayloadB64)
	if d.CredentialID != credentialID || d.AgentID != owner {
		return nil, time.Time{}, fmt.Errorf("%w: Signet delivered a different credential", secretstore.ErrUnavailable)
	}
	if d.LeaseExpiresAt <= 0 {
		return nil, time.Time{}, fmt.Errorf("%w: Signet delivery carried no lease", secretstore.ErrUnavailable)
	}
	encoded := bytes.TrimSpace(d.PayloadB64)
	if len(encoded) < 2 || encoded[0] != '"' || encoded[len(encoded)-1] != '"' {
		return nil, time.Time{}, fmt.Errorf("%w: Signet delivery carried no payload", secretstore.ErrUnavailable)
	}
	encoded = encoded[1 : len(encoded)-1]
	payload := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(payload, encoded)
	if err != nil || n == 0 {
		clear(payload)
		return nil, time.Time{}, fmt.Errorf("%w: Signet delivery payload is not base64", secretstore.ErrUnavailable)
	}
	return payload[:n], time.Unix(d.LeaseExpiresAt, 0).UTC(), nil
}
