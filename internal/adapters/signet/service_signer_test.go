package signet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type serviceSignerFixture struct {
	signer   *ServiceSigner
	service  nostr.SecretKey
	client   nostr.SecretKey
	alive    bool
	requests [][]string
	response func(*nostr.Event) (string, error)
}

func newServiceSignerFixture(t *testing.T) *serviceSignerFixture {
	t.Helper()
	service, err := nostr.SecretKeyFromHex(strings.Repeat("1", 64))
	require.NoError(t, err)
	client, err := nostr.SecretKeyFromHex(strings.Repeat("2", 64))
	require.NoError(t, err)
	f := &serviceSignerFixture{service: service, client: client, alive: true}
	f.response = func(ev *nostr.Event) (string, error) {
		require.NoError(t, ev.Sign(service))
		return ev.String(), nil
	}
	session := func() (serviceSignerSession, error) {
		return serviceSignerSession{
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
	f.signer, err = newServiceSigner(service.Public().Hex(), client.Public(), session)
	require.NoError(t, err)
	return f
}

func unsignedServiceEvent() nostr.Event {
	return nostr.Event{Kind: 30900, CreatedAt: 12345, Tags: nostr.Tags{{"d", "service:example"}, {"t", "bahia-cp-state"}}, Content: `{"ok":true}`}
}

func TestServiceSignerSendsStandardSignEventAndPinsIdentity(t *testing.T) {
	f := newServiceSignerFixture(t)
	pubkey, err := f.signer.GetPublicKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, f.service.Public(), pubkey)
	ev := unsignedServiceEvent()
	require.NoError(t, f.signer.SignEvent(context.Background(), &ev))
	require.Equal(t, f.service.Public(), ev.PubKey)
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	require.Len(t, f.requests, 1)
	require.Len(t, f.requests[0], 1, "standard NIP-46 sign_event carries only the event JSON")
	var requested nostr.Event
	require.NoError(t, json.Unmarshal([]byte(f.requests[0][0]), &requested))
	require.Equal(t, unsignedServiceEvent(), requested)
}

func TestServiceSignerRejectsAdversarialResponsesWithoutMutatingRequest(t *testing.T) {
	cases := map[string]func(*serviceSignerFixture, *nostr.Event) (string, error){
		"changed kind": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			ev.Kind++
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed timestamp": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			ev.CreatedAt++
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed content": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			ev.Content += "!"
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"changed tag": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			ev.Tags[0][1] = "other"
			require.NoError(t, ev.Sign(f.service))
			return ev.String(), nil
		},
		"wrong author": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.client))
			return ev.String(), nil
		},
		"invalid id": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			ev.ID = nostr.ID{}
			return ev.String(), nil
		},
		"invalid signature": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			ev.Sig[0] ^= 1
			return ev.String(), nil
		},
		"malformed response": func(_ *serviceSignerFixture, _ *nostr.Event) (string, error) { return "{", nil },
		"RPC rejected": func(_ *serviceSignerFixture, _ *nostr.Event) (string, error) {
			return "", errors.New("not the assigned writer")
		},
		"disconnect during RPC": func(f *serviceSignerFixture, ev *nostr.Event) (string, error) {
			require.NoError(t, ev.Sign(f.service))
			f.alive = false
			return ev.String(), nil
		},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			f := newServiceSignerFixture(t)
			f.response = func(ev *nostr.Event) (string, error) { return response(f, ev) }
			ev := unsignedServiceEvent()
			original := unsignedServiceEvent()
			require.Error(t, f.signer.SignEvent(context.Background(), &ev))
			require.Equal(t, original, ev)
		})
	}
}

func TestServiceSignerRejectsMismatchedBunkerAndDisconnectedSession(t *testing.T) {
	f := newServiceSignerFixture(t)
	f.signer.session = func() (serviceSignerSession, error) {
		return serviceSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.client.Public(), nil },
			rpc: func(context.Context, string, []string) (string, error) {
				t.Fatal("RPC to wrong signer")
				return "", nil
			},
			alive: func() bool { return true },
		}, nil
	}
	ev := unsignedServiceEvent()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "pubkey mismatch")
	_, err := f.signer.GetPublicKey(context.Background())
	require.ErrorContains(t, err, "pubkey mismatch")

	f = newServiceSignerFixture(t)
	f.alive = false
	ev = unsignedServiceEvent()
	require.ErrorIs(t, f.signer.SignEvent(context.Background(), &ev), ErrNotConnected)
	_, err = f.signer.GetPublicKey(context.Background())
	require.ErrorIs(t, err, ErrNotConnected)
	require.Empty(t, f.requests)
}

func TestNewServiceSignerRequiresPinnedPubkeyAndDedicatedClientIdentity(t *testing.T) {
	service, err := nostr.SecretKeyFromHex(strings.Repeat("1", 64))
	require.NoError(t, err)
	client, err := NewClient(Config{BunkerURI: "bunker://" + service.Public().Hex() + "?relay=wss://relay.example", RequireReal: true}, nil)
	require.NoError(t, err)
	_, err = NewServiceSigner(client, service.Public().Hex())
	require.ErrorContains(t, err, "explicit dedicated")
	client, err = NewClient(Config{BunkerURI: "bunker://" + service.Public().Hex() + "?relay=wss://relay.example", ClientSecretKey: strings.Repeat("2", 64), RequireReal: true}, nil)
	require.NoError(t, err)
	_, err = NewServiceSigner(client, "")
	require.ErrorContains(t, err, "expected existing")
	client.clientSecretKey = strings.Repeat("1", 64)
	_, err = NewServiceSigner(client, service.Public().Hex())
	require.ErrorContains(t, err, "must differ")
	client.clientSecretKey = strings.Repeat("2", 64)
	signer, err := NewServiceSigner(client, service.Public().Hex())
	require.NoError(t, err)
	_, err = signer.GetPublicKey(context.Background())
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestServiceSignerRejectsForeignAuthorAndPreSignedInput(t *testing.T) {
	f := newServiceSignerFixture(t)
	ev := unsignedServiceEvent()
	ev.PubKey = f.client.Public()
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "author differs")
	ev = unsignedServiceEvent()
	require.NoError(t, ev.Sign(f.service))
	require.ErrorContains(t, f.signer.SignEvent(context.Background(), &ev), "unsigned")
	require.Empty(t, f.requests)
}

func TestSignetClientSignUsesConfiguredServiceSignerWithoutUnpinnedFallback(t *testing.T) {
	f := newServiceSignerFixture(t)
	cfg := Config{
		BunkerURI:             "bunker://" + f.service.Public().Hex() + "?relay=wss://relay.example",
		ClientSecretKey:       f.client.Hex(),
		RequireReal:           true,
		ExpectedServicePubkey: f.service.Public().Hex(),
	}
	client, err := NewClient(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, client.serviceSigner)
	client.serviceSigner.session = f.signer.session
	ev := unsignedServiceEvent()
	require.NoError(t, client.Sign(context.Background(), &ev))
	require.Len(t, f.requests, 1)
	require.Len(t, f.requests[0], 1)

	f.alive = false
	ev = unsignedServiceEvent()
	require.ErrorIs(t, client.Sign(context.Background(), &ev), ErrNotConnected)
	require.Len(t, f.requests, 1)
	require.Equal(t, unsignedServiceEvent(), ev)

	cfg.ClientSecretKey = ""
	_, err = NewClient(cfg, nil)
	require.ErrorContains(t, err, "explicit dedicated")
}
