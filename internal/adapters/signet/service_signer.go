package signet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

type serviceSignerSession struct {
	publicKey func(context.Context) (nostr.PubKey, error)
	rpc       func(context.Context, string, []string) (string, error)
	alive     func() bool
}

// ServiceSigner signs and NIP-44-encrypts as Bahia's existing service identity
// through standard NIP-46 requests to Signet. The fence lives in Signet: the
// fenced service identity accepts requests only from the authenticated NIP-46
// client pubkey a provisioner assigned with agent/writer-acquire. Bahia's side
// of that contract is a dedicated, explicit client key that differs from the
// service key, and a bunker whose reported pubkey is the pinned service key.
type ServiceSigner struct {
	expected nostr.PubKey
	session  func() (serviceSignerSession, error)
}

// NewServiceSigner requires the existing Bahia service pubkey independently of
// Signet and a persistent dedicated NIP-46 client key. It never uses mock mode.
func NewServiceSigner(client *Client, expectedServicePubkey string) (*ServiceSigner, error) {
	if client == nil || client.bunkerURI == "" || client.allowMock || !client.clientKeyExplicit {
		return nil, errors.New("Signet service signer requires a real bunker and explicit dedicated NIP-46 client key")
	}
	clientSecret, err := nostrutil.SecretKeyFromHex(client.clientSecretKey)
	if err != nil {
		return nil, fmt.Errorf("decode dedicated NIP-46 client key: %w", err)
	}
	return newServiceSigner(expectedServicePubkey, clientSecret.Public(), client.serviceSession)
}

func newServiceSigner(expectedHex string, clientPubkey nostr.PubKey, session func() (serviceSignerSession, error)) (*ServiceSigner, error) {
	expected, err := nostr.PubKeyFromHex(expectedHex)
	if err != nil || expected == nostr.ZeroPK {
		return nil, errors.New("valid expected existing Bahia service pubkey is required")
	}
	if clientPubkey == nostr.ZeroPK || clientPubkey == expected {
		return nil, errors.New("dedicated NIP-46 client pubkey must differ from Bahia service pubkey")
	}
	if session == nil {
		return nil, errors.New("Signet session is required")
	}
	return &ServiceSigner{expected: expected, session: session}, nil
}

func (c *Client) serviceSession() (serviceSignerSession, error) {
	c.mu.Lock()
	bunker, generation := c.bunker, c.connectionGeneration
	connected := c.connected
	c.mu.Unlock()
	if !connected || bunker == nil {
		return serviceSignerSession{}, ErrNotConnected
	}
	return serviceSignerSession{
		publicKey: bunker.GetPublicKey,
		rpc:       bunker.RPC,
		alive: func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.connected && c.bunker == bunker && c.connectionGeneration == generation
		},
	}, nil
}

func (s *ServiceSigner) checkedSession(ctx context.Context) (serviceSignerSession, error) {
	session, err := s.session()
	if err != nil {
		return serviceSignerSession{}, err
	}
	if session.publicKey == nil || session.rpc == nil || session.alive == nil || !session.alive() {
		return serviceSignerSession{}, ErrNotConnected
	}
	actual, err := session.publicKey(ctx)
	if err != nil {
		return serviceSignerSession{}, fmt.Errorf("read Signet signer pubkey: %w", err)
	}
	if actual != s.expected {
		return serviceSignerSession{}, fmt.Errorf("Signet signer pubkey mismatch: expected %s, got %s", s.expected.Hex(), actual.Hex())
	}
	if !session.alive() {
		return serviceSignerSession{}, ErrNotConnected
	}
	return session, nil
}

func (s *ServiceSigner) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	if s == nil {
		return nostr.ZeroPK, ErrNotConnected
	}
	if _, err := s.checkedSession(ctx); err != nil {
		return nostr.ZeroPK, err
	}
	return s.expected, nil
}

func (s *ServiceSigner) SignEvent(ctx context.Context, event *nostr.Event) error {
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
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode event for Signet: %w", err)
	}
	response, err := session.rpc(ctx, "sign_event", []string{string(requestJSON)})
	if err != nil {
		return fmt.Errorf("Signet sign_event: %w", err)
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
	*event = signed
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
