package servicesigner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/promenade/frost"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

type fakeFrostrRelay struct {
	incoming   chan FrostrRelayFrame
	local      nostr.PubKey
	peers      map[int]nostrutil.LocalKeyer
	remote     *InProcessFrostrTransport
	mu         sync.Mutex
	count      int
	virtualRTT time.Duration
}

func (r *fakeFrostrRelay) Incoming() <-chan FrostrRelayFrame { return r.incoming }

func (r *fakeFrostrRelay) Publish(ctx context.Context, ev nostr.Event) error {
	if !ev.CheckID() || !ev.VerifySignature() || ev.Kind != FrostrMessageKind {
		return errors.New("invalid outgoing event")
	}
	var recipient nostrutil.LocalKeyer
	var peerID int
	for id, keyer := range r.peers {
		pub, _ := keyer.GetPublicKey(ctx)
		if hasPTag(ev.Tags, pub) {
			recipient, peerID = keyer, id
			break
		}
	}
	if peerID == 0 {
		return errors.New("no peer")
	}
	clear, err := recipient.Decrypt(ctx, ev.Content, ev.PubKey)
	if err != nil {
		return err
	}
	var request frostrWire
	if err := json.Unmarshal([]byte(clear), &request); err != nil {
		return err
	}
	response := request
	response.Phase = request.Phase + "/response"
	sessionBytes, err := hex.DecodeString(request.Session)
	if err != nil || len(sessionBytes) != 32 {
		return errors.New("bad session")
	}
	var session [32]byte
	copy(session[:], sessionBytes)
	messageBytes, err := hex.DecodeString(request.Message)
	if request.Message != "" && (err != nil || len(messageBytes) != 32) {
		return errors.New("bad message")
	}
	var message [32]byte
	copy(message[:], messageBytes)
	switch request.Phase {
	case "sign/commit":
		value, err := r.remote.Commit(ctx, FrostrCommitRequest{PeerID: peerID, Session: session, Message: message, Participants: request.Participants})
		if err != nil {
			return err
		}
		response.Value = value.Hex()
	case "sign/partial":
		commits := make([]frost.Commitment, len(request.Commitments))
		for i, encoded := range request.Commitments {
			if commits[i].DecodeHex(encoded) != nil {
				return errors.New("bad commitment")
			}
		}
		value, err := r.remote.Sign(ctx, FrostrSignRequest{FrostrCommitRequest: FrostrCommitRequest{PeerID: peerID, Session: session, Message: message, Participants: request.Participants}, Commitments: commits})
		if err != nil {
			return err
		}
		response.Value = value.Hex()
	case "ecdh/share":
		pub, err := nostr.PubKeyFromHex(request.Peer)
		if err != nil {
			return err
		}
		value, err := r.remote.ECDH(ctx, FrostrECDHRequest{PeerID: peerID, Session: session, Peer: pub, Participants: request.Participants})
		if err != nil {
			return err
		}
		value.ToAffine()
		response.Value = hex.EncodeToString(btcec.NewPublicKey(&value.X, &value.Y).SerializeCompressed())
	default:
		return errors.New("unknown phase")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	cipher, err := recipient.Encrypt(ctx, string(encoded), r.local)
	if err != nil {
		return err
	}
	reply := nostr.Event{Kind: FrostrMessageKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", r.local.Hex()}}, Content: cipher}
	if err := recipient.SignEvent(ctx, &reply); err != nil {
		return err
	}
	r.mu.Lock()
	r.count++
	r.virtualRTT += 25 * time.Millisecond
	r.mu.Unlock()
	select {
	case r.incoming <- FrostrRelayFrame{Event: reply}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestFrostrNostrRoundSeamWithFakeRelay(t *testing.T) {
	cfg, shares, _ := frostrFixture(t)
	localSecret := nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")
	require.NoError(t, os.WriteFile(cfg.Signer.Frostr.TransportKeyFile, []byte(localSecret.Hex()), 0o600))
	peer2, err := nostrutil.NewLocalKeyer(testClient.Hex())
	require.NoError(t, err)
	peer3, err := nostrutil.NewLocalKeyer(testService.Hex())
	require.NoError(t, err)
	relay := &fakeFrostrRelay{incoming: make(chan FrostrRelayFrame, 8), local: localSecret.Public(), peers: map[int]nostrutil.LocalKeyer{2: peer2, 3: peer3}, remote: NewInProcessFrostrTransport(shares[1], shares[2])}
	signer, err := Open(t.Context(), cfg, Options{FrostrRelay: relay, FrostrFence: testFrostrFence{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = signer.(*frostrKeyer).Close() })
	ev := nostr.Event{Kind: 30900, CreatedAt: 9, Content: "roundtrip"}
	require.NoError(t, signer.SignEvent(t.Context(), &ev))
	require.True(t, ev.VerifySignature())
	require.Equal(t, 2, relay.count, "commit and partial are two publish/response exchanges")
	_, err = signer.(*frostrKeyer).ConversationKey(t.Context(), testClient.Public())
	require.NoError(t, err)
	require.Equal(t, 3, relay.count)
	require.Equal(t, 75*time.Millisecond, relay.virtualRTT)
}
