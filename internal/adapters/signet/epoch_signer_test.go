package signet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type epochSignerFixture struct {
	signer   *EpochSigner
	service  nostr.SecretKey
	client   nostr.SecretKey
	lease    WriterLease
	alive    bool
	requests [][]string
	response func(*nostr.Event) (string, error)
}

func newEpochSignerFixture(t *testing.T) *epochSignerFixture {
	t.Helper()
	service, err := nostr.SecretKeyFromHex(strings.Repeat("1", 64))
	require.NoError(t, err)
	client, err := nostr.SecretKeyFromHex(strings.Repeat("2", 64))
	require.NoError(t, err)
	f := &epochSignerFixture{service: service, client: client, alive: true}
	f.lease = WriterLease{Epoch: 7, OwnerPubkey: client.Public(), ExpiresAt: time.Now().Add(time.Hour)}
	f.response = func(ev *nostr.Event) (string, error) {
		require.NoError(t, ev.Sign(service))
		return ev.String(), nil
	}
	session := func() (epochSignerSession, error) {
		return epochSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return service.Public(), nil },
			rpc: func(_ context.Context, method string, params []string) (string, error) {
				require.Equal(t, "sign_event", method)
				f.requests = append(f.requests, append([]string(nil), params...))
				var ev nostr.Event
				require.NoError(t, json.Unmarshal([]byte(params[0]), &ev))
				return f.response(&ev)
			},
			alive: func() bool { return f.alive },
		}, nil
	}
	f.signer, err = newEpochSigner(service.Public().Hex(), client.Public(), func(context.Context) (WriterLease, error) { return f.lease, nil }, session)
	require.NoError(t, err)
	return f
}

func unsignedEpochEvent() nostr.Event {
	return nostr.Event{Kind: 30900, CreatedAt: 12345, Tags: nostr.Tags{{"d", "service:example"}, {"t", "bahia-cp-state"}}, Content: `{"ok":true}`}
}

func TestEpochSignerSendsEpochBoundNIP46RequestAndPinsIdentity(t *testing.T) {
	f := newEpochSignerFixture(t)
	pubkey, err := f.signer.GetPublicKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, f.service.Public(), pubkey)
	ev := unsignedEpochEvent()
	require.NoError(t, f.signer.SignEvent(context.Background(), &ev))
	require.Equal(t, f.service.Public(), ev.PubKey)
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	require.Len(t, f.requests, 1)
	require.Equal(t, strconv.FormatUint(f.lease.Epoch, 10), f.requests[0][1])
	require.Len(t, f.requests[0], 2)
	var requested nostr.Event
	require.NoError(t, json.Unmarshal([]byte(f.requests[0][0]), &requested))
	require.Equal(t, unsignedEpochEvent(), requested)
}

func TestEpochSignerRejectsAdversarialResponsesWithoutMutatingRequest(t *testing.T) {
	cases := map[string]func(*epochSignerFixture, *nostr.Event) (string, error){
		"changed kind": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			ev.Kind++
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed timestamp": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			ev.CreatedAt++
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed content": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			ev.Content += "!"
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed tag": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			ev.Tags[0][1] = "other"
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"wrong author": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.client))
			return ev.String(), nil
		},
		"invalid id": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			ev.ID = nostr.ID{}
			return ev.String(), nil
		},
		"invalid signature": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			ev.Sig[0] ^= 1
			return ev.String(), nil
		},
		"malformed response": func(_ *epochSignerFixture, _ *nostr.Event) (string, error) { return "{", nil },
		"RPC rejected":       func(_ *epochSignerFixture, _ *nostr.Event) (string, error) { return "", errors.New("lease fenced") },
		"disconnect during RPC": func(f *epochSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			f.alive = false
			return ev.String(), nil
		},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEpochSignerFixture(t)
			f.response = func(ev *nostr.Event) (string, error) { return response(f, ev) }
			ev := unsignedEpochEvent()
			original := unsignedEpochEvent()
			require.Error(t, f.signer.SignEvent(context.Background(), &ev))
			require.Equal(t, original, ev)
		})
	}
}

func TestEpochSignerRejectsMissingStaleAndWrongOwnerLease(t *testing.T) {
	for name, mutate := range map[string]func(*epochSignerFixture){
		"missing epoch":  func(f *epochSignerFixture) { f.lease.Epoch = 0 },
		"wrong owner":    func(f *epochSignerFixture) { f.lease.OwnerPubkey = f.service.Public() },
		"expired":        func(f *epochSignerFixture) { f.lease.ExpiresAt = time.Now().Add(-time.Second) },
		"missing expiry": func(f *epochSignerFixture) { f.lease.ExpiresAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newEpochSignerFixture(t)
			mutate(f)
			ev := unsignedEpochEvent()
			require.Error(t, f.signer.SignEvent(context.Background(), &ev))
			require.Empty(t, f.requests)
		})
	}
	f := newEpochSignerFixture(t)
	ev := unsignedEpochEvent()
	require.NoError(t, f.signer.SignEvent(context.Background(), &ev))
	f.lease.Epoch--
	ev = unsignedEpochEvent()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "stale")
	require.Len(t, f.requests, 1)
}

func TestEpochSignerRejectsMismatchedBunkerAndDisconnectedSession(t *testing.T) {
	f := newEpochSignerFixture(t)
	f.signer.session = func() (epochSignerSession, error) {
		return epochSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.client.Public(), nil },
			rpc: func(context.Context, string, []string) (string, error) {
				t.Fatal("RPC to wrong signer")
				return "", nil
			},
			alive: func() bool { return true },
		}, nil
	}
	ev := unsignedEpochEvent()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "pubkey mismatch")
	_, err := f.signer.GetPublicKey(context.Background())
	require.ErrorContains(t, err, "pubkey mismatch")

	f = newEpochSignerFixture(t)
	f.alive = false
	ev = unsignedEpochEvent()
	require.ErrorIs(t, f.signer.SignEvent(context.Background(), &ev), ErrNotConnected)
	require.Empty(t, f.requests)
}

func TestNewEpochSignerRequiresPinnedPubkeyAndDedicatedClientIdentity(t *testing.T) {
	service, err := nostr.SecretKeyFromHex(strings.Repeat("1", 64))
	require.NoError(t, err)
	lease := func(context.Context) (WriterLease, error) { return WriterLease{}, nil }
	client, err := NewClient(Config{BunkerURI: "bunker://" + service.Public().Hex() + "?relay=wss://relay.example", RequireReal: true}, nil)
	require.NoError(t, err)
	_, err = NewEpochSigner(client, service.Public().Hex(), lease)
	require.ErrorContains(t, err, "explicit dedicated")
	client, err = NewClient(Config{BunkerURI: "bunker://" + service.Public().Hex() + "?relay=wss://relay.example", ClientSecretKey: strings.Repeat("2", 64), RequireReal: true}, nil)
	require.NoError(t, err)
	_, err = NewEpochSigner(client, "", lease)
	require.ErrorContains(t, err, "expected existing")
	client.clientSecretKey = strings.Repeat("1", 64)
	_, err = NewEpochSigner(client, service.Public().Hex(), lease)
	require.ErrorContains(t, err, "must differ")
	client.clientSecretKey = strings.Repeat("2", 64)
	signer, err := NewEpochSigner(client, service.Public().Hex(), lease)
	require.NoError(t, err)
	_, err = signer.GetPublicKey(context.Background())
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestEpochSignerRejectsLeaseLossDuringRPC(t *testing.T) {
	f := newEpochSignerFixture(t)
	f.response = func(ev *nostr.Event) (string, error) {
		require.NoError(t, ev.Sign(f.service))
		f.lease.ExpiresAt = time.Now().Add(-time.Second)
		return ev.String(), nil
	}
	ev := unsignedEpochEvent()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "expired")
	require.Equal(t, unsignedEpochEvent(), ev)
}

func TestEpochSignerRejectsUnavailableLeaseAndPreSignedInput(t *testing.T) {
	f := newEpochSignerFixture(t)
	f.signer.lease = func(context.Context) (WriterLease, error) { return WriterLease{}, errors.New("lease unavailable") }
	ev := unsignedEpochEvent()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "lease unavailable")
	_, err := f.signer.GetPublicKey(context.Background())
	require.ErrorContains(t, err, "lease unavailable")
	require.Empty(t, f.requests)

	f = newEpochSignerFixture(t)
	ev = unsignedEpochEvent()
	ev.PubKey = f.client.Public()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "author differs")
	ev = unsignedEpochEvent()
	require.NoError(t, ev.Sign(f.service))
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "unsigned")
	require.Empty(t, f.requests)
}

func TestSignetClientSignUsesConfiguredEpochSignerWithoutLegacyFallback(t *testing.T) {
	f := newEpochSignerFixture(t)
	cfg := Config{
		BunkerURI:             "bunker://" + f.service.Public().Hex() + "?relay=wss://relay.example",
		ClientSecretKey:       f.client.Hex(),
		RequireReal:           true,
		EpochLease:            func(context.Context) (WriterLease, error) { return f.lease, nil },
		ExpectedServicePubkey: f.service.Public().Hex(),
	}
	client, err := NewClient(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, client.epochSigner)
	client.epochSigner.session = f.signer.session
	ev := unsignedEpochEvent()
	require.NoError(t, client.Sign(context.Background(), &ev))
	require.Len(t, f.requests, 1)
	require.Equal(t, "7", f.requests[0][1])

	f.lease.Epoch = 0
	ev = unsignedEpochEvent()
	require.ErrorContains(t, client.Sign(context.Background(), &ev), "lease")
	require.Len(t, f.requests, 1)
	require.Equal(t, unsignedEpochEvent(), ev)

	cfg.EpochLease = nil
	_, err = NewClient(cfg, nil)
	require.ErrorContains(t, err, "writer lease")
}
