package signet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// WriterLease is a local attempt gate. Signet independently validates the
// authenticated NIP-46 client identity and epoch atomically on every request.
type WriterLease struct {
	Epoch       uint64
	OwnerPubkey nostr.PubKey
	ExpiresAt   time.Time
}

// WriterLeaseSource supplies the currently renewed Signet writer lease.
type WriterLeaseSource func(context.Context) (WriterLease, error)

type epochSignerSession struct {
	publicKey func(context.Context) (nostr.PubKey, error)
	rpc       func(context.Context, string, []string) (string, error)
	alive     func() bool
}

// EpochSigner implements Nostr event signing through Signet's fenced NIP-46
// sign_event request. It is not connected to Bahia's service-key runtime until
// the non-event raw-key cryptography has a compatible migration contract.
type EpochSigner struct {
	expected     nostr.PubKey
	clientPubkey nostr.PubKey
	lease        WriterLeaseSource
	session      func() (epochSignerSession, error)
	now          func() time.Time
	mu           sync.Mutex
	highestEpoch uint64
}

// NewEpochSigner requires the existing Bahia service pubkey independently of
// Signet and a persistent dedicated NIP-46 client key. It never uses mock or
// legacy one-parameter signing when a lease is unavailable.
func NewEpochSigner(client *Client, expectedServicePubkey string, lease WriterLeaseSource) (*EpochSigner, error) {
	if client == nil || client.bunkerURI == "" || client.allowMock || !client.clientKeyExplicit {
		return nil, errors.New("Signet epoch signer requires a real bunker and explicit dedicated NIP-46 client key")
	}
	clientSecret, err := nostrutil.SecretKeyFromHex(client.clientSecretKey)
	if err != nil {
		return nil, fmt.Errorf("decode dedicated NIP-46 client key: %w", err)
	}
	return newEpochSigner(expectedServicePubkey, clientSecret.Public(), lease, client.epochSession)
}

func newEpochSigner(expectedHex string, clientPubkey nostr.PubKey, lease WriterLeaseSource, session func() (epochSignerSession, error)) (*EpochSigner, error) {
	expected, err := nostr.PubKeyFromHex(expectedHex)
	if err != nil || expected == nostr.ZeroPK {
		return nil, errors.New("valid expected existing Bahia service pubkey is required")
	}
	if clientPubkey == nostr.ZeroPK || clientPubkey == expected {
		return nil, errors.New("dedicated NIP-46 client pubkey must differ from Bahia service pubkey")
	}
	if lease == nil || session == nil {
		return nil, errors.New("Signet writer lease and session are required")
	}
	return &EpochSigner{expected: expected, clientPubkey: clientPubkey, lease: lease, session: session, now: time.Now}, nil
}

func (c *Client) epochSession() (epochSignerSession, error) {
	c.mu.Lock()
	bunker, generation := c.bunker, c.connectionGeneration
	connected := c.connected
	c.mu.Unlock()
	if !connected || bunker == nil {
		return epochSignerSession{}, ErrNotConnected
	}
	return epochSignerSession{
		publicKey: bunker.GetPublicKey,
		rpc:       bunker.RPC,
		alive: func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.connected && c.bunker == bunker && c.connectionGeneration == generation
		},
	}, nil
}

func (s *EpochSigner) checkedSession(ctx context.Context) (epochSignerSession, error) {
	session, err := s.session()
	if err != nil {
		return epochSignerSession{}, err
	}
	if session.publicKey == nil || session.rpc == nil || session.alive == nil || !session.alive() {
		return epochSignerSession{}, ErrNotConnected
	}
	actual, err := session.publicKey(ctx)
	if err != nil {
		return epochSignerSession{}, fmt.Errorf("read Signet signer pubkey: %w", err)
	}
	if actual != s.expected {
		return epochSignerSession{}, fmt.Errorf("Signet signer pubkey mismatch: expected %s, got %s", s.expected.Hex(), actual.Hex())
	}
	if !session.alive() {
		return epochSignerSession{}, ErrNotConnected
	}
	return session, nil
}

func (s *EpochSigner) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	if s == nil {
		return nostr.ZeroPK, ErrNotConnected
	}
	session, err := s.checkedSession(ctx)
	if err != nil {
		return nostr.ZeroPK, err
	}
	lease, err := s.lease(ctx)
	if err != nil {
		return nostr.ZeroPK, fmt.Errorf("read Signet writer lease: %w", err)
	}
	if err := s.checkLease(lease); err != nil {
		return nostr.ZeroPK, err
	}
	if !session.alive() {
		return nostr.ZeroPK, ErrNotConnected
	}
	return s.expected, nil
}

func (s *EpochSigner) SignEvent(ctx context.Context, event *nostr.Event) error {
	if s == nil {
		return ErrNotConnected
	}
	if event == nil {
		return ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.PubKey != nostr.ZeroPK && event.PubKey != s.expected {
		return errors.New("event author differs from pinned Bahia service pubkey")
	}
	if event.ID != (nostr.ID{}) || event.Sig != ([64]byte{}) {
		return errors.New("event must be unsigned before Signet signing")
	}
	request := *event
	request.Tags = cloneTags(event.Tags)
	session, err := s.checkedSession(ctx)
	if err != nil {
		return err
	}
	lease, err := s.lease(ctx)
	if err != nil {
		return fmt.Errorf("read Signet writer lease: %w", err)
	}
	if err := s.checkLease(lease); err != nil {
		return err
	}
	if !session.alive() {
		return ErrNotConnected
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode event for Signet: %w", err)
	}
	response, err := session.rpc(ctx, "sign_event", []string{string(requestJSON), strconv.FormatUint(lease.Epoch, 10)})
	if err != nil {
		return fmt.Errorf("Signet epoch sign_event: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !session.alive() {
		return ErrNotConnected
	}
	var signed nostr.Event
	if err := json.Unmarshal([]byte(response), &signed); err != nil {
		return fmt.Errorf("decode Signet signed event: %w", err)
	}
	if signed.PubKey != s.expected || signed.Kind != request.Kind || signed.CreatedAt != request.CreatedAt || signed.Content != request.Content || !sameTags(signed.Tags, request.Tags) {
		return errors.New("Signet changed event identity or immutable request fields")
	}
	if !signed.CheckID() || !signed.VerifySignature() {
		return errors.New("Signet returned invalid event id or signature")
	}
	currentLease, err := s.lease(ctx)
	if err != nil {
		return fmt.Errorf("recheck Signet writer lease: %w", err)
	}
	if currentLease.Epoch != lease.Epoch || currentLease.OwnerPubkey != lease.OwnerPubkey {
		return errors.New("Signet writer lease changed during signing")
	}
	if err := s.checkLease(currentLease); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease.Epoch < s.highestEpoch || !s.now().Before(currentLease.ExpiresAt) {
		return errors.New("Signet writer lease became stale before signed event acceptance")
	}
	if !session.alive() {
		return ErrNotConnected
	}
	*event = signed
	return nil
}

func (s *EpochSigner) checkLease(lease WriterLease) error {
	if lease.Epoch == 0 || lease.OwnerPubkey != s.clientPubkey || lease.ExpiresAt.IsZero() || !s.now().Before(lease.ExpiresAt) {
		return errors.New("missing, expired, or wrong-owner Signet writer lease")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease.Epoch < s.highestEpoch {
		return fmt.Errorf("stale Signet writer epoch %d", lease.Epoch)
	}
	s.highestEpoch = lease.Epoch
	return nil
}

func cloneTags(tags nostr.Tags) nostr.Tags {
	if tags == nil {
		return nostr.Tags{}
	}
	cloned := make(nostr.Tags, len(tags))
	for i, tag := range tags {
		cloned[i] = append(nostr.Tag(nil), tag...)
	}
	return cloned
}

func sameTags(a, b nostr.Tags) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
