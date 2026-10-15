package servicesigner

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/promenade/frost"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

func frostrFixture(t testing.TB) (config.NostrConfig, []frost.KeyShard, nostr.SecretKey) {
	t.Helper()
	priv, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x23}, 32))
	shares, group, _ := frost.TrustedKeyDeal(&priv.Key, 2, 3)
	group.ToAffine()
	var service nostr.PubKey
	copy(service[:], group.X.Bytes()[:])
	var secret nostr.SecretKey
	secretBytes := priv.Key.Bytes()
	copy(secret[:], secretBytes[:])
	path := filepath.Join(t.TempDir(), "daemon.share")
	require.NoError(t, os.WriteFile(path, []byte(shares[0].Hex()+"\n"), 0o600))
	cfg := config.NostrConfig{PublicKey: service.Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerFrostr, Frostr: config.NostrSignerFrostrConfig{
		ShareFile:        path,
		TransportKeyFile: filepath.Join(t.TempDir(), "node.key"),
		Relays:           []string{"wss://relay.example"},
		Peers: []config.NostrSignerFrostrPeer{
			{ID: 2, Pubkey: testClient.Public().Hex(), PublicShare: shares[1].PublicKeyShard.Hex()},
			{ID: 3, Pubkey: testService.Public().Hex(), PublicShare: shares[2].PublicKeyShard.Hex()},
		},
	}}}
	return cfg, shares, secret
}

type testFrostrFence struct{}

func (testFrostrFence) Acquire(ctx context.Context, _ nostr.PubKey) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}

type rejectingFrostrFence struct{}

func (rejectingFrostrFence) Acquire(context.Context, nostr.PubKey) (func(), error) {
	return nil, errors.New("secret-do-not-log")
}

func TestFrostrStandardBIP340AndThresholdECDH(t *testing.T) {
	cfg, shares, _ := frostrFixture(t)
	transport := NewInProcessFrostrTransport(shares[1], shares[2])
	signer, err := Open(t.Context(), cfg, Options{FrostrTransport: transport, FrostrFence: testFrostrFence{}})
	require.NoError(t, err)
	pub, err := signer.GetPublicKey(t.Context())
	require.NoError(t, err)
	require.Equal(t, cfg.PublicKey, pub.Hex())

	ev := nostr.Event{Kind: 30900, CreatedAt: 123456, Tags: nostr.Tags{{"d", "fixture"}}, Content: `{"ok":true}`}
	require.NoError(t, signer.SignEvent(t.Context(), &ev))
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	require.Equal(t, pub, ev.PubKey)
	// Verify with an independent standard BIP-340 implementation too.
	signature, err := schnorr.ParseSignature(ev.Sig[:])
	require.NoError(t, err)
	groupPub, err := schnorr.ParsePubKey(pub[:])
	require.NoError(t, err)
	require.True(t, signature.Verify(ev.ID[:], groupPub))

	threshold := signer.(*frostrKeyer)
	peer := testClient.Public()
	for _, pair := range [][2]int{{1, 2}, {1, 3}} {
		// Force one peer unavailable to exercise both 2-of-3 subsets.
		peerTransport := NewInProcessFrostrTransport(shares[pair[1]-1])
		threshold.transport = peerTransport
		got, err := threshold.ConversationKey(t.Context(), peer)
		require.NoError(t, err)
		recovered, err := frost.AggregateSecretKeyShards([]frost.KeyShard{shares[pair[0]-1], shares[pair[1]-1]})
		require.NoError(t, err)
		var testOnlySecret nostr.SecretKey
		secretBytes := recovered.Bytes()
		copy(testOnlySecret[:], secretBytes[:])
		want, err := nip44.GenerateConversationKey(peer, testOnlySecret)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	threshold.transport = transport
	cipher, err := threshold.EncryptBytes(t.Context(), []byte{0, 0xff, 0x80, 1}, peer)
	require.NoError(t, err)
	opened, err := threshold.DecryptBytes(t.Context(), cipher, peer)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0xff, 0x80, 1}, opened)
}

func TestFrostrFailClosed(t *testing.T) {
	cfg, shares, _ := frostrFixture(t)
	_, err := Open(t.Context(), cfg, Options{})
	require.ErrorContains(t, err, "round transport is not configured")
	transport := NewInProcessFrostrTransport(shares[1])
	_, err = Open(t.Context(), cfg, Options{FrostrTransport: transport})
	require.ErrorContains(t, err, "single-writer fence is not configured")
	require.NoError(t, os.Chmod(cfg.Signer.Frostr.ShareFile, 0o644))
	_, err = Open(t.Context(), cfg, Options{FrostrTransport: transport, FrostrFence: testFrostrFence{}})
	require.ErrorContains(t, err, "owner-only")
	require.NoError(t, os.Chmod(cfg.Signer.Frostr.ShareFile, 0o600))
	// A syntactically valid mounted share with the wrong scalar must fail at
	// Open, and the upstream validator's secret-bearing error must be hidden.
	tampered := shares[0]
	tampered.Secret = shares[1].Secret
	require.NoError(t, os.WriteFile(cfg.Signer.Frostr.ShareFile, []byte(tampered.Hex()), 0o600))
	_, err = Open(t.Context(), cfg, Options{FrostrTransport: transport, FrostrFence: testFrostrFence{}})
	require.ErrorContains(t, err, "mounted share is invalid")
	require.NotContains(t, err.Error(), shares[1].Secret.String())
	require.NoError(t, os.WriteFile(cfg.Signer.Frostr.ShareFile, []byte(shares[0].Hex()), 0o600))
	cfg.Signer.Frostr.Peers[0].PublicShare = strings.Repeat("a", 80)
	_, err = Open(t.Context(), cfg, Options{FrostrTransport: transport, FrostrFence: testFrostrFence{}})
	require.ErrorContains(t, err, "public share is invalid")

	cfg, shares, _ = frostrFixture(t)
	signer, err := Open(t.Context(), cfg, Options{FrostrTransport: NewInProcessFrostrTransport(), FrostrFence: testFrostrFence{}})
	require.NoError(t, err)
	ev := nostr.Event{Kind: 30900, Content: "not signed"}
	before := ev
	err = signer.SignEvent(t.Context(), &ev)
	require.ErrorContains(t, err, "unavailable")
	require.Equal(t, before, ev)
	_ = shares
}

type secretErrorTransport struct{ FrostrRoundTransport }

func (secretErrorTransport) Commit(context.Context, FrostrCommitRequest) (frost.Commitment, error) {
	return frost.Commitment{}, errors.New("secret-do-not-log")
}

func TestFrostrTransportErrorsDoNotRevealSecrets(t *testing.T) {
	cfg, _, _ := frostrFixture(t)
	signer, err := Open(t.Context(), cfg, Options{FrostrTransport: secretErrorTransport{}, FrostrFence: testFrostrFence{}})
	require.NoError(t, err)
	err = signer.SignEvent(t.Context(), &nostr.Event{Kind: 1})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-do-not-log")
}

func TestFrostrFenceGuardsSigningAndECDH(t *testing.T) {
	cfg, shares, _ := frostrFixture(t)
	signer, err := Open(t.Context(), cfg, Options{FrostrTransport: NewInProcessFrostrTransport(shares[1]), FrostrFence: rejectingFrostrFence{}})
	require.NoError(t, err)
	err = signer.SignEvent(t.Context(), &nostr.Event{Kind: 1})
	require.ErrorContains(t, err, "fence unavailable")
	require.NotContains(t, err.Error(), "secret-do-not-log")
	_, err = signer.(*frostrKeyer).ConversationKey(t.Context(), testClient.Public())
	require.ErrorContains(t, err, "fence unavailable")
	require.NotContains(t, err.Error(), "secret-do-not-log")
}

func BenchmarkFrostrSignInProcess(b *testing.B) {
	cfg, shares, _ := frostrFixture(b)
	signer, err := Open(context.Background(), cfg, Options{FrostrTransport: NewInProcessFrostrTransport(shares[1], shares[2]), FrostrFence: testFrostrFence{}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := nostr.Event{Kind: 30900, CreatedAt: nostr.Timestamp(i + 1), Content: hex.EncodeToString([]byte{byte(i)})}
		if err := signer.SignEvent(context.Background(), &ev); err != nil {
			b.Fatal(err)
		}
	}
}
