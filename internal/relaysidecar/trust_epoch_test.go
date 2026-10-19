package relaysidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSidecarTrustEpochReplacesReadWriteAdmissionAndRecordsAck(t *testing.T) {
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	cfg := sidecarTestConfig(t)
	cfg.ServiceID = "deployment-a"
	cfg.OperatorRootPubkey = fixture.RootPubkey
	cfg.PublicKey = fixture.OldPubkey
	ackKey := nostr.MustSecretKeyFromHex("4444444444444444444444444444444444444444444444444444444444444444")
	cfg.Sidecar.AckPrivateKeyFile = filepath.Join(t.TempDir(), "ack.key")
	require.NoError(t, os.WriteFile(cfg.Sidecar.AckPrivateKeyFile, []byte("4444444444444444444444444444444444444444444444444444444444444444"), 0o600))
	cfg.Sidecar.AdministratorPubkeys = []string{fixture.OldPubkey}
	seed, err := openEventStore(t.Context(), cfg.Sidecar.DataDir, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, seed.Save(t.Context(), fixture.Epoch))
	require.NoError(t, seed.Close())
	resolved, err := PreflightTrustEpoch(t.Context(), cfg, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, fixture.NewPubkey, resolved)
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop())
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Close()) }()
	require.False(t, server.readAuth.isAllowedReader(fixture.OldPubkey), "admin allowlist cannot restore old key")
	require.True(t, server.readAuth.isAllowedReader(fixture.NewPubkey))
	old := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "old"}
	require.NoError(t, old.Sign(nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")))
	reject, reason := server.admission.acceptEvent(t.Context(), old)
	require.True(t, reject)
	require.Contains(t, reason, "hostile service event")
	pubkey := ackKey.Public()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey},
		Tags: nostr.TagMap{"d": []string{"trust-epoch-ack:deployment-a:sidecar"}}}
	seen := false
	for ack := range server.store.Query(t.Context(), filter, 0) {
		body, parseErr := trustepoch.Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
		require.NoError(t, parseErr)
		resolution := trustepoch.Resolution{ServiceID: "deployment-a", Pubkey: fixture.NewPubkey,
			Epoch: body.Epoch, Record: &body, EventID: fixture.Epoch.ID.Hex()}
		require.NoError(t, trustepoch.VerifyAcknowledgmentAuthority(ack, resolution, "sidecar", pubkey.Hex(), false))
		seen = true
	}
	require.True(t, seen)
	badHead := fixture.Epoch
	badHead.Content = "{}"
	reject, reason = server.admission.acceptEvent(t.Context(), badHead)
	require.True(t, reject)
	require.Contains(t, reason, "unverifiable")
	require.False(t, server.readAuth.isAllowedReader(fixture.NewPubkey), "malformed root head poisons service reads")
}

func TestSidecarRemoteAckSignerPinsIdentityAndPreservesEnvelope(t *testing.T) {
	cfg, _, ackKey := activatedAckFixture(t)
	opens := 0
	var session *closingAckSigner
	opener := func(context.Context, config.NostrConfig) (nostr.Keyer, error) {
		opens++
		keyer, err := nostrutil.NewLocalKeyer(ackKey.Hex())
		session = &closingAckSigner{Keyer: keyer}
		return session, err
	}
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop(),
		func(o *serverOptions) { o.ackSignerOpener = opener })
	require.NoError(t, err)
	t.Cleanup(func() { require.True(t, session.closed.Load(), "ack signer session must close with the sidecar") })
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.Equal(t, "ok", server.ack.health())
	require.Equal(t, 1, opens)
	acks := sidecarAcks(t, server, ackKey.Public())
	require.Len(t, acks, 1)
	require.Equal(t, nostr.Kind(kinds.CASControlState), acks[0].Kind)
	require.Equal(t, nostr.Tags{
		{"d", "trust-epoch-ack:deployment-a:sidecar"},
		{"t", "identity-recovery-receipt"},
		{"domain", "identity"},
		{"schema", "bahia.trust-epoch-ack.v1"},
		{"legacy_kind", "32053"},
		{"epoch", "1"},
		{"deleted", "false"},
	}, acks[0].Tags)
	require.Equal(t, "{\"service_id\":\"deployment-a\",\"consumer_id\":\"sidecar\",\"epoch\":1,\"incident_id\":\"0199f4a6-3700-7000-8000-000000000001\",\"enforcing_pubkey\":\"3c72addb4fdf09af94f0c94d7fe92a386a7e70cf8a1d85916386bb2535c7b1b1\",\"epoch_event_id\":\"f261d4501f94089977c61817f9b47eb688ee4c02efdcb8dd102fb2ea8b06f4af\"}", acks[0].Content)
	require.NoError(t, server.ack.acknowledge(t.Context()))
	require.Len(t, sidecarAcks(t, server, ackKey.Public()), 1, "same epoch is idempotent")
}

type closingAckSigner struct {
	nostr.Keyer
	closed atomic.Bool
}

func (s *closingAckSigner) Close() error { s.closed.Store(true); return nil }

func TestSidecarAckSignerMismatchFailsClosed(t *testing.T) {
	cfg, _, ackKey := activatedAckFixture(t)
	wrong := nostr.MustSecretKeyFromHex("5555555555555555555555555555555555555555555555555555555555555555")
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop(),
		func(o *serverOptions) {
			o.ackSignerOpener = func(context.Context, config.NostrConfig) (nostr.Keyer, error) {
				return nostrutil.NewLocalKeyer(wrong.Hex())
			}
		})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.Empty(t, sidecarAcks(t, server, ackKey.Public()))
	require.Empty(t, sidecarAcks(t, server, wrong.Public()))
	ready := httptest.NewRecorder()
	server.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Contains(t, ready.Body.String(), `"ack_signer":"unavailable"`)
	require.Contains(t, ready.Body.String(), `"status":"warn"`)
	require.NotContains(t, ready.Body.String(), "secret bunker URL")
}

type wrongAuthorAckSigner struct {
	nostr.Keyer
	wrong nostr.SecretKey
}

func (s wrongAuthorAckSigner) SignEvent(_ context.Context, event *nostr.Event) error {
	return event.Sign(s.wrong)
}

func TestSidecarAckSignerReturnedAuthorMismatchDoesNotPublish(t *testing.T) {
	cfg, _, ackKey := activatedAckFixture(t)
	wrong := nostr.MustSecretKeyFromHex("5555555555555555" + "5555555555555555" + "5555555555555555" + "5555555555555555")
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop(),
		func(o *serverOptions) {
			o.ackSignerOpener = func(context.Context, config.NostrConfig) (nostr.Keyer, error) {
				keyer, err := nostrutil.NewLocalKeyer(ackKey.Hex())
				return wrongAuthorAckSigner{Keyer: keyer, wrong: wrong}, err
			}
		})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.Empty(t, sidecarAcks(t, server, ackKey.Public()))
	require.Empty(t, sidecarAcks(t, server, wrong.Public()))
	require.Equal(t, "unavailable", server.ack.health())
}

func TestSidecarAckSignerUnavailableRetriesOnEpochWake(t *testing.T) {
	cfg, epoch, ackKey := activatedAckFixture(t)
	opens := 0
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop(),
		func(o *serverOptions) {
			o.ackSignerOpener = func(context.Context, config.NostrConfig) (nostr.Keyer, error) {
				opens++
				if opens == 1 {
					return nil, errors.New("secret bunker URL must not be surfaced")
				}
				return nostrutil.NewLocalKeyer(ackKey.Hex())
			}
		})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.Equal(t, "unavailable", server.ack.health())
	published := make(chan struct{}, 1)
	previous := server.relay.OnEventSaved
	server.relay.OnEventSaved = func(ctx context.Context, ev nostr.Event) {
		previous(ctx, ev)
		if ev.Tags.GetD() == "trust-epoch-ack:deployment-a:sidecar" {
			published <- struct{}{}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	go func() { server.ack.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	server.relay.OnEventSaved(t.Context(), epoch)
	select {
	case <-published:
	case <-t.Context().Done():
		t.Fatal("ack publication did not follow epoch wake")
	}
	cancel()
	<-done
	require.Equal(t, "ok", server.ack.health())
	require.Len(t, sidecarAcks(t, server, ackKey.Public()), 1)
}

func activatedAckFixture(t *testing.T) (config.NostrConfig, nostr.Event, nostr.SecretKey) {
	t.Helper()
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	cfg := sidecarTestConfig(t)
	cfg.ServiceID, cfg.OperatorRootPubkey, cfg.PublicKey = "deployment-a", fixture.RootPubkey, fixture.OldPubkey
	ackKey := nostr.MustSecretKeyFromHex("4444444444444444444444444444444444444444444444444444444444444444")
	cfg.Sidecar.AckSigner = config.SidecarAckSignerConfig{PublicKey: ackKey.Public().Hex(), NostrSignerConfig: config.NostrSignerConfig{
		Method:              config.NostrSignerNIP46,
		BunkerURI:           "bunker://0000000000000000000000000000000000000000000000000000000000000003?relay=wss%3A%2F%2Frelay.example",
		ClientSecretKeyFile: "/run/secrets/sidecar-ack-client",
	}}
	seed, err := openEventStore(t.Context(), cfg.Sidecar.DataDir, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, seed.Save(t.Context(), fixture.Epoch))
	require.NoError(t, seed.Close())
	return cfg, fixture.Epoch, ackKey
}

func sidecarAcks(t *testing.T, server *Server, pubkey nostr.PubKey) []nostr.Event {
	t.Helper()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"d": {"trust-epoch-ack:deployment-a:sidecar"}}}
	var acks []nostr.Event
	for event := range server.store.Query(t.Context(), filter, 0) {
		acks = append(acks, event)
	}
	return acks
}

func TestSidecarOnlyExemptsExactVerifiedInventoryReplay(t *testing.T) {
	oldKey := nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")
	newKey := nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")
	old := nostr.Event{Kind: 1, CreatedAt: 100, Content: "inventoried"}
	require.NoError(t, old.Sign(oldKey))
	p := newPolicy(newKey.Public())
	p.trust = &sidecarTrustEpoch{verifier: &trustepoch.Verifier{LegacyPubkey: oldKey.Public().Hex()},
		resolution: trustepoch.Resolution{Pubkey: newKey.Public().Hex(),
			Record:    &trustepoch.Epoch{OldPubkey: oldKey.Public().Hex(), Activated: true, ActivationBarrier: 150},
			Inventory: map[string]struct{}{old.ID.Hex(): {}}}, logger: zap.NewNop()}
	p.now = func() nostr.Timestamp { return nostr.Timestamp(10 * 365 * 24 * 60 * 60) }
	reject, _ := p.acceptEvent(t.Context(), old)
	require.False(t, reject, "exact inventory replay bypasses ordinary event-age admission")
	unlisted := nostr.Event{Kind: 1, CreatedAt: 100, Content: "unlisted"}
	require.NoError(t, unlisted.Sign(oldKey))
	reject, _ = p.acceptEvent(t.Context(), unlisted)
	require.True(t, reject)
}
