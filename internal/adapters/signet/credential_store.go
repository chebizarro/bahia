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

// signetCredentialIDPattern is Signet's credential id: "cred_" and the hex
// BLAKE2b digest of (owner, type, label) (signet_secret_id_generate).
var signetCredentialIDPattern = regexp.MustCompile(`^cred_[0-9a-f]{64}$`)

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
	// ClosedRetryBudget is nostr.closed_retry_budget; 0 keeps the default.
	ClosedRetryBudget int
	// OutboundAdmission gates every gift wrap and NIP-42 AUTH; nil uses the
	// process-wide controller.
	OutboundAdmission *nostrout.Admission
}

// CredentialStore implements secretstore.SecretStore over Signet's
// gift-wrapped ContextVM management plane (credential/inspect,
// credential/deliver, credential/list). One long-lived subscription receives
// every reply addressed to the requester; replies are matched to callers by
// JSON-RPC id. See docs/architecture/secret-store.md.
type CredentialStore struct {
	signetPK nostr.PubKey
	clientSK nostr.SecretKey
	clientPK nostr.PubKey
	owner    string
	timeout  time.Duration
	pool     *nostrpool.RelayPool
	logger   *zap.Logger
	cancel   context.CancelFunc
	done     chan struct{}

	mu       sync.Mutex
	pending  map[string]chan signetJSONRPCResponse
	stopped  error
	lastAt   time.Time
	lastErr  error
	everDone bool
}

var _ secretstore.SecretStore = (*CredentialStore)(nil)

// OpenCredentialStore validates cfg and opens the reply subscription. It does
// not wait for Signet: the daemon starts while Signet is down, and every
// operation fails closed with secretstore.ErrUnavailable until it answers.
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
	clientPK := cfg.ClientKey.Public()
	if clientPK == cfg.SignetPubkey {
		return nil, fmt.Errorf("signet secret store: client key must not be Signet's own key")
	}
	opts := []nostrpool.RelayPoolOption{
		nostrpool.WithAuthSigner(keyer.NewPlainKeySigner(cfg.ClientKey)),
		nostrpool.WithOutboundAdmission(nostrout.Or(cfg.OutboundAdmission)),
	}
	if cfg.ClosedRetryBudget > 0 {
		opts = append(opts, nostrpool.WithRetryableClosedBudget(cfg.ClosedRetryBudget))
	}
	named := logger.Named("signet-secret-store")
	pool := nostrpool.NewRelayPool(cfg.Relays, named, opts...)
	subCtx, cancel := context.WithCancel(ctx)
	// One subscription for the store's lifetime. Replies are backdated by up
	// to two days, so the since bound must reach that far back; on a reissue
	// after a dropped connection the resume cursor keeps the same reach, and
	// the pool's dedupe absorbs replays.
	sub, err := pool.SubscribeWithOptions(subCtx, []nostr.Filter{{
		Kinds: []nostr.Kind{signetKindGiftWrap},
		Tags:  nostr.TagMap{"p": []string{clientPK.Hex()}},
		Since: nostr.Timestamp(time.Now().Add(-signetReplyWindow).Unix()),
	}}, nostrpool.SubscribeOptions{AwaitUnavailableRelays: true, ResumeOverlap: signetReplyWindow})
	if err != nil {
		cancel()
		pool.Close()
		return nil, fmt.Errorf("signet secret store: subscribe to replies: %w", err)
	}
	s := &CredentialStore{
		signetPK: cfg.SignetPubkey,
		clientSK: cfg.ClientKey,
		clientPK: clientPK,
		owner:    cfg.Owner,
		timeout:  cfg.RequestTimeout,
		pool:     pool,
		logger:   named,
		cancel:   cancel,
		done:     make(chan struct{}),
		pending:  make(map[string]chan signetJSONRPCResponse),
	}
	go s.readReplies(subCtx, sub)
	return s, nil
}

// Close ends the reply subscription and the relay pool.
func (s *CredentialStore) Close() {
	s.cancel()
	<-s.done
	s.pool.Close()
}

// LastOutcome reports the last Signet round trip: when it ended and, if
// Signet was unreachable or refused this requester, why. An answer about the
// credential itself (not found, revoked, expired, another version) is a
// healthy round trip. ok is false before the first round trip.
func (s *CredentialStore) LastOutcome() (at time.Time, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Inspect returns payload-free metadata for a credential the owner holds.
func (s *CredentialStore) Inspect(ctx context.Context, credentialID string) (secretstore.Metadata, error) {
	if !signetCredentialIDPattern.MatchString(credentialID) {
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet credential ids are cred_ and 64 hex characters", secretstore.ErrInvalidRef)
	}
	var raw signetCredentialMetadata
	if err := s.call(ctx, "credential/inspect", map[string]any{"credential_id": credentialID}, &raw); err != nil {
		return secretstore.Metadata{}, err
	}
	if raw.CredentialID != credentialID {
		return secretstore.Metadata{}, fmt.Errorf("%w: Signet inspected credential %q, not %q", secretstore.ErrUnavailable, raw.CredentialID, credentialID)
	}
	if raw.AgentID != s.owner {
		// Signet delivers only the owner's credentials to this requester;
		// any other owner is unresolvable here.
		return secretstore.Metadata{}, fmt.Errorf("%w: credential is not owned by %q", secretstore.ErrDenied, s.owner)
	}
	return raw.metadata()
}

// Resolve fetches the pinned version under a one-use Signet delivery lease.
// credential/deliver always releases the active version and does not report
// which one, so the delivery is bracketed by two metadata reads: rotation
// increments the version atomically and never reuses one, so equal versions
// before and after prove the delivered value is that version.
func (s *CredentialStore) Resolve(ctx context.Context, ref domain.SecretCredentialRef) (*secretstore.Value, error) {
	if err := secretstore.ValidateRef(ref); err != nil {
		return nil, err
	}
	if err := s.requireActiveVersion(ctx, ref); err != nil {
		return nil, err
	}
	var delivered signetDelivery
	if err := s.call(ctx, "credential/deliver", map[string]any{"agent_id": s.owner, "credential_id": ref.CredentialID}, &delivered); err != nil {
		return nil, err
	}
	payload, leaseExpiresAt, err := delivered.open(ref.CredentialID, s.owner)
	if err != nil {
		return nil, err
	}
	value := secretstore.NewValue(payload, leaseExpiresAt)
	if err := s.requireActiveVersion(ctx, ref); err != nil {
		value.Zeroize()
		return nil, err
	}
	return value, nil
}

func (s *CredentialStore) requireActiveVersion(ctx context.Context, ref domain.SecretCredentialRef) error {
	meta, err := s.Inspect(ctx, ref.CredentialID)
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

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return fmt.Errorf("signet secret store: request id: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	replies := make(chan signetJSONRPCResponse, 1)
	s.mu.Lock()
	if s.stopped != nil {
		stopped := s.stopped
		s.mu.Unlock()
		return fmt.Errorf("%w: %v", secretstore.ErrUnavailable, stopped)
	}
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
		results, err := s.pool.PublishWithResults(ctx, gift)
		published <- publishOutcome(results, err)
	}()
	for {
		select {
		case reply := <-replies:
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

// publishOutcome is nil when at least one relay accepted the request. An
// outbound admission refusal (budget, circuit breaker, kill switch) arrives
// as publishErr with no per-relay results; the request is not retried here.
func publishOutcome(results []nostrpool.PublishResult, publishErr error) error {
	reasons := make([]string, 0, len(results)+1)
	for _, result := range results {
		if result.Succeeded() {
			return nil
		}
		reason := result.Reason
		if result.Error != nil {
			reason = result.Error.Error()
		}
		reasons = append(reasons, result.RelayURL+": "+reason)
	}
	if publishErr != nil {
		reasons = append(reasons, publishErr.Error())
	}
	return fmt.Errorf("%w: no relay accepted the Signet request (%s)", secretstore.ErrUnavailable, strings.Join(reasons, "; "))
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

// readReplies unwraps every gift wrap addressed to the requester and hands a
// reply to the caller waiting on its JSON-RPC id. Gift wraps that do not open
// as a Signet-sealed reply, and replies nobody waits for (history, or a call
// that already timed out), are dropped.
func (s *CredentialStore) readReplies(ctx context.Context, sub *nostrpool.MergedSubscription) {
	defer close(s.done)
	defer sub.Close()
	defer func() {
		s.mu.Lock()
		s.stopped = errors.New("Signet reply subscription ended")
		s.mu.Unlock()
	}()
	closed := sub.Closed
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				if gaveUp := sub.GaveUp(); gaveUp != nil {
					s.logger.Error("Signet reply subscription ended", zap.Error(gaveUp))
				}
				return
			}
			reply, err := s.openReply(ev)
			if err != nil {
				s.logger.Debug("ignoring gift wrap that is not a Signet reply", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
				continue
			}
			s.mu.Lock()
			waiter := s.pending[reply.ID]
			s.mu.Unlock()
			if waiter != nil {
				select {
				case waiter <- reply:
				default:
				}
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

func (s *CredentialStore) openReply(gift *nostr.Event) (signetJSONRPCResponse, error) {
	var none signetJSONRPCResponse
	if gift.Kind != signetKindGiftWrap {
		return none, fmt.Errorf("kind %d is not a gift wrap", gift.Kind)
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
	var rumor nostr.Event
	if err := json.Unmarshal([]byte(rumorJSON), &rumor); err != nil {
		return none, fmt.Errorf("decode rumor: %w", err)
	}
	// NIP-59: the rumor's author is the seal's signer.
	if rumor.PubKey != s.signetPK {
		return none, fmt.Errorf("rumor author is not Signet")
	}
	var reply signetJSONRPCResponse
	if err := json.Unmarshal([]byte(rumor.Content), &reply); err != nil {
		return none, fmt.Errorf("decode JSON-RPC reply: %w", err)
	}
	if reply.ID == "" {
		return none, fmt.Errorf("reply has no id")
	}
	return reply, nil
}

// decodeSignetReply maps a JSON-RPC error onto the secretstore sentinels by
// Signet's stable reason code (error.data); the message never carries
// secret material.
func decodeSignetReply(method string, reply signetJSONRPCResponse, out any) error {
	if reply.Error != nil {
		code := reply.Error.Data
		var sentinel error
		switch code {
		case "not_found", "credential_not_found":
			sentinel = secretstore.ErrNotFound
		case "revoked", "credential_revoked":
			sentinel = secretstore.ErrRevoked
		case "expired", "credential_expired":
			sentinel = secretstore.ErrExpired
		case "not_owner", "no_capability", "deny_listed", "type_denied", "lease_invalid":
			sentinel = secretstore.ErrDenied
		default:
			sentinel = secretstore.ErrUnavailable
		}
		if code == "" {
			code = fmt.Sprintf("code %d", reply.Error.Code)
		}
		return fmt.Errorf("%w: Signet %s refused (%s): %s", sentinel, method, code, reply.Error.Message)
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
// so the base64 text Bahia owns can be overwritten after decoding.
type signetDelivery struct {
	CredentialID   string          `json:"credential_id"`
	AgentID        string          `json:"agent_id"`
	PayloadB64     json.RawMessage `json:"payload_b64"`
	LeaseExpiresAt int64           `json:"lease_expires_at"`
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
