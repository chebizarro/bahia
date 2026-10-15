package servicesigner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/promenade/frost"
	"github.com/btcsuite/btcd/btcec/v2"
)

// Bifrost uses kind 20004 for encrypted peer traffic. The relay adapter must
// keep one author/p-tag/kind-scoped REQ open, deliver EVENTs continuously,
// surface CLOSED/AUTH as errors, and return from Publish only after OK true.
const FrostrMessageKind nostr.Kind = 20004

type FrostrRelayFrame struct {
	Event nostr.Event
	Err   error
}

type FrostrEventRelay interface {
	Publish(context.Context, nostr.Event) error
	Incoming() <-chan FrostrRelayFrame
}

type frostrWire struct {
	Version      int      `json:"v"`
	Phase        string   `json:"phase"`
	Session      string   `json:"session"`
	PeerID       int      `json:"peer_id"`
	Message      string   `json:"message,omitempty"`
	Participants [2]int   `json:"participants"`
	Commitments  []string `json:"commitments,omitempty"`
	Peer         string   `json:"peer,omitempty"`
	Value        string   `json:"value,omitempty"`
}

type frostrWireResult struct {
	message frostrWire
	err     error
}

type frostrPending struct {
	phase string
	ch    chan frostrWireResult
}

// NostrFrostrTransport is the kind-20004 encrypted-round transport skeleton.
// The injected relay adapter owns live subscriptions, EOSE/CLOSED/AUTH, replay
// cursors and OK verification; this layer owns session/identity correlation.
type NostrFrostrTransport struct {
	local    nostr.Keyer
	localPub nostr.PubKey
	peers    map[int]nostr.PubKey
	relay    FrostrEventRelay
	timeout  time.Duration
	mu       sync.Mutex
	pending  map[string]frostrPending
	done     chan struct{}
	once     sync.Once
}

func NewNostrFrostrTransport(ctx context.Context, local nostr.Keyer, peers map[int]nostr.PubKey, relay FrostrEventRelay, timeout time.Duration) (*NostrFrostrTransport, error) {
	if local == nil || relay == nil || relay.Incoming() == nil || len(peers) != 2 || timeout <= 0 {
		return nil, errors.New("FROSTR Nostr transport requires identity, peers, relay and timeout")
	}
	pub, err := local.GetPublicKey(ctx)
	if err != nil || pub == nostr.ZeroPK {
		return nil, errors.New("FROSTR Nostr transport identity unavailable")
	}
	t := &NostrFrostrTransport{local: local, localPub: pub, peers: peers, relay: relay, timeout: timeout, pending: make(map[string]frostrPending), done: make(chan struct{})}
	go t.receive()
	return t, nil
}

func (t *NostrFrostrTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

func (t *NostrFrostrTransport) receive() {
	for {
		select {
		case <-t.done:
			return
		case frame, ok := <-t.relay.Incoming():
			if !ok {
				t.failPending(errors.New("FROSTR relay subscription closed"))
				return
			}
			if frame.Err != nil {
				t.failPending(errors.New("FROSTR relay subscription failed"))
				return
			}
			t.accept(frame.Event)
		}
	}
}

func (t *NostrFrostrTransport) failPending(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pending := range t.pending {
		select {
		case pending.ch <- frostrWireResult{err: err}:
		default:
		}
	}
}

func hasPTag(tags nostr.Tags, pub nostr.PubKey) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == pub.Hex() {
			return true
		}
	}
	return false
}

func (t *NostrFrostrTransport) accept(ev nostr.Event) {
	if ev.Kind != FrostrMessageKind || !ev.CheckID() || !ev.VerifySignature() || !hasPTag(ev.Tags, t.localPub) {
		return
	}
	var peerID int
	for id, pub := range t.peers {
		if pub == ev.PubKey {
			peerID = id
			break
		}
	}
	if peerID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()
	clear, err := t.local.Decrypt(ctx, ev.Content, ev.PubKey)
	if err != nil || len(clear) > 8192 {
		return
	}
	var wire frostrWire
	if json.Unmarshal([]byte(clear), &wire) != nil || wire.Version != 1 || wire.PeerID != peerID {
		return
	}
	t.mu.Lock()
	pending, ok := t.pending[wire.Session]
	t.mu.Unlock()
	if !ok || wire.Phase != pending.phase+"/response" {
		return
	}
	select {
	case pending.ch <- frostrWireResult{message: wire}:
	default:
	}
}

func (t *NostrFrostrTransport) exchange(ctx context.Context, peerID int, phase string, wire frostrWire) (frostrWire, error) {
	peer, ok := t.peers[peerID]
	if !ok {
		return frostrWire{}, errors.New("FROSTR unknown peer")
	}
	if wire.Session == "" {
		return frostrWire{}, errors.New("FROSTR empty session")
	}
	wire.Version, wire.Phase, wire.PeerID = 1, phase, peerID
	encoded, err := json.Marshal(wire)
	if err != nil {
		return frostrWire{}, errors.New("FROSTR request encoding failed")
	}
	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	ch := make(chan frostrWireResult, 1)
	t.mu.Lock()
	if _, exists := t.pending[wire.Session]; exists {
		t.mu.Unlock()
		return frostrWire{}, errors.New("FROSTR session already active")
	}
	t.pending[wire.Session] = frostrPending{phase: phase, ch: ch} // subscribe-before-publish; incoming stream is already live
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.pending, wire.Session); t.mu.Unlock() }()
	ciphertext, err := t.local.Encrypt(callCtx, string(encoded), peer)
	if err != nil {
		return frostrWire{}, errors.New("FROSTR request encryption failed")
	}
	ev := nostr.Event{Kind: FrostrMessageKind, CreatedAt: nostr.Now(), Content: ciphertext, Tags: nostr.Tags{{"p", peer.Hex()}}}
	if err := t.local.SignEvent(callCtx, &ev); err != nil {
		return frostrWire{}, errors.New("FROSTR transport event signing failed")
	}
	if err := t.relay.Publish(callCtx, ev); err != nil {
		return frostrWire{}, errors.New("FROSTR round event was not accepted by a relay")
	}
	select {
	case <-callCtx.Done():
		return frostrWire{}, callCtx.Err()
	case <-t.done:
		return frostrWire{}, errors.New("FROSTR transport closed")
	case reply := <-ch:
		if reply.err != nil {
			return frostrWire{}, reply.err
		}
		if reply.message.Phase != phase+"/response" || reply.message.Participants != wire.Participants || reply.message.Message != wire.Message || reply.message.Peer != wire.Peer {
			return frostrWire{}, errors.New("FROSTR round reply mismatched request")
		}
		return reply.message, nil
	}
}

func (t *NostrFrostrTransport) Commit(ctx context.Context, req FrostrCommitRequest) (frost.Commitment, error) {
	wire, err := t.exchange(ctx, req.PeerID, "sign/commit", frostrWire{Session: hex.EncodeToString(req.Session[:]), Message: hex.EncodeToString(req.Message[:]), Participants: req.Participants})
	if err != nil {
		return frost.Commitment{}, err
	}
	var value frost.Commitment
	if len(wire.Value) != 136 || value.DecodeHex(wire.Value) != nil || value.SignerID != req.PeerID {
		return frost.Commitment{}, errors.New("FROSTR commitment reply invalid")
	}
	return value, nil
}

func (t *NostrFrostrTransport) Sign(ctx context.Context, req FrostrSignRequest) (frost.PartialSignature, error) {
	values := make([]string, len(req.Commitments))
	for i, c := range req.Commitments {
		values[i] = c.Hex()
	}
	wire, err := t.exchange(ctx, req.PeerID, "sign/partial", frostrWire{Session: hex.EncodeToString(req.Session[:]), Message: hex.EncodeToString(req.Message[:]), Participants: req.Participants, Commitments: values})
	if err != nil {
		return frost.PartialSignature{}, err
	}
	var value frost.PartialSignature
	if len(wire.Value) != 68 || value.DecodeHex(wire.Value) != nil || value.SignerIdentifier != req.PeerID {
		return frost.PartialSignature{}, errors.New("FROSTR partial signature reply invalid")
	}
	return value, nil
}

func (t *NostrFrostrTransport) ECDH(ctx context.Context, req FrostrECDHRequest) (*btcec.JacobianPoint, error) {
	wire, err := t.exchange(ctx, req.PeerID, "ecdh/share", frostrWire{Session: hex.EncodeToString(req.Session[:]), Peer: req.Peer.Hex(), Participants: req.Participants})
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(wire.Value)
	if err != nil || len(b) != 33 {
		return nil, errors.New("FROSTR ECDH reply invalid")
	}
	pub, err := btcec.ParsePubKey(b)
	if err != nil {
		return nil, errors.New("FROSTR ECDH point invalid")
	}
	var point btcec.JacobianPoint
	pub.AsJacobian(&point)
	return &point, nil
}

var _ FrostrRoundTransport = (*NostrFrostrTransport)(nil)
