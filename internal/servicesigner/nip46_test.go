package servicesigner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

type rpcRequest struct {
	method string
	params []string
}

// fakeBunker answers like a NIP-46 bunker holding testService; respond may
// be replaced to model adversarial or missing behaviour.
type fakeBunker struct {
	t        *testing.T
	requests []rpcRequest
	respond  func(method string, params []string) (string, error)
}

func newFakeBunker(t *testing.T) (*fakeBunker, *nip46Keyer, context.CancelFunc) {
	t.Helper()
	f := &fakeBunker{t: t}
	f.respond = f.honest
	lifetime, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	keyer := &nip46Keyer{expected: testService.Public(), timeout: time.Minute, closed: lifetime.Done(), cancel: cancel,
		rpc: func(_ context.Context, method string, params []string) (string, error) {
			f.requests = append(f.requests, rpcRequest{method, append([]string(nil), params...)})
			return f.respond(method, params)
		}}
	return f, keyer, cancel
}

func (f *fakeBunker) honest(method string, params []string) (string, error) {
	switch method {
	case "sign_event":
		var ev nostr.Event
		require.NoError(f.t, json.Unmarshal([]byte(params[0]), &ev))
		require.NoError(f.t, ev.Sign(testService))
		return ev.String(), nil
	}
	peer, err := nostr.PubKeyFromHex(params[0])
	require.NoError(f.t, err)
	key, err := nip44.GenerateConversationKey(peer, testService)
	require.NoError(f.t, err)
	switch method {
	case "nip44_encrypt":
		return nip44.Encrypt(params[1], key)
	case "nip44_decrypt":
		return nip44.Decrypt(params[1], key)
	case methodNIP44EncryptBinary:
		plain, err := base64.StdEncoding.DecodeString(params[1])
		require.NoError(f.t, err)
		return nip44.Encrypt(string(plain), key)
	case methodNIP44DecryptBinary:
		plain, err := nip44.Decrypt(params[1], key)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString([]byte(plain)), nil
	}
	return "", errors.New("response error: unsupported method")
}

func unsignedEvent() nostr.Event {
	return nostr.Event{Kind: 30900, CreatedAt: 12345, Tags: nostr.Tags{{"d", "service:example"}, {"t", "bahia-cp-state"}}, Content: `{"ok":true}`}
}

func TestNIP46SignEventSendsStandardRequestAndPinsIdentity(t *testing.T) {
	f, keyer, _ := newFakeBunker(t)
	pubkey, err := keyer.GetPublicKey(t.Context())
	require.NoError(t, err)
	require.Equal(t, testService.Public(), pubkey)
	ev := unsignedEvent()
	require.NoError(t, keyer.SignEvent(t.Context(), &ev))
	require.Equal(t, testService.Public(), ev.PubKey)
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	require.Len(t, f.requests, 1)
	require.Equal(t, "sign_event", f.requests[0].method)
	require.Len(t, f.requests[0].params, 1, "standard NIP-46 sign_event carries only the event JSON")
	var requested nostr.Event
	require.NoError(t, json.Unmarshal([]byte(f.requests[0].params[0]), &requested))
	require.Equal(t, unsignedEvent(), requested)
}

func TestNIP46SignEventRejectsAdversarialResponsesWithoutMutatingRequest(t *testing.T) {
	resign := func(t *testing.T, params []string, mutate func(*nostr.Event), key nostr.SecretKey) string {
		var ev nostr.Event
		require.NoError(t, json.Unmarshal([]byte(params[0]), &ev))
		mutate(&ev)
		require.NoError(t, ev.Sign(key))
		return ev.String()
	}
	cases := map[string]func(*testing.T, []string, context.CancelFunc) (string, error){
		"changed kind": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			return resign(t, p, func(e *nostr.Event) { e.Kind++ }, testService), nil
		},
		"changed timestamp": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			return resign(t, p, func(e *nostr.Event) { e.CreatedAt++ }, testService), nil
		},
		"changed content": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			return resign(t, p, func(e *nostr.Event) { e.Content += "!" }, testService), nil
		},
		"changed tag": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			return resign(t, p, func(e *nostr.Event) { e.Tags[0][1] = "other" }, testService), nil
		},
		"wrong author": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			return resign(t, p, func(*nostr.Event) {}, testClient), nil
		},
		"invalid signature": func(t *testing.T, p []string, _ context.CancelFunc) (string, error) {
			var ev nostr.Event
			require.NoError(t, json.Unmarshal([]byte(p[0]), &ev))
			require.NoError(t, ev.Sign(testService))
			ev.Sig[0] ^= 1
			return ev.String(), nil
		},
		"malformed response": func(*testing.T, []string, context.CancelFunc) (string, error) { return "{", nil },
		"remote refusal": func(*testing.T, []string, context.CancelFunc) (string, error) {
			return "", errors.New("response error: not the assigned writer")
		},
		"closed during RPC": func(t *testing.T, p []string, cancel context.CancelFunc) (string, error) {
			cancel()
			return resign(t, p, func(*nostr.Event) {}, testService), nil
		},
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			f, keyer, cancel := newFakeBunker(t)
			f.respond = func(_ string, params []string) (string, error) { return respond(t, params, cancel) }
			ev := unsignedEvent()
			require.Error(t, keyer.SignEvent(t.Context(), &ev))
			require.Equal(t, unsignedEvent(), ev)
		})
	}
}

func TestNIP46SignEventRejectsForeignAuthorPreSignedAndClosed(t *testing.T) {
	f, keyer, cancel := newFakeBunker(t)
	ev := unsignedEvent()
	ev.PubKey = testClient.Public()
	require.ErrorContains(t, keyer.SignEvent(t.Context(), &ev), "author differs")
	ev = unsignedEvent()
	require.NoError(t, ev.Sign(testService))
	require.ErrorContains(t, keyer.SignEvent(t.Context(), &ev), "unsigned")
	cancel()
	ev = unsignedEvent()
	require.ErrorIs(t, keyer.SignEvent(t.Context(), &ev), errClosed)
	_, err := keyer.GetPublicKey(t.Context())
	require.ErrorIs(t, err, errClosed)
	require.Empty(t, f.requests)
}

func TestNIP46CipherUsesStandardTwoParamRequests(t *testing.T) {
	f, keyer, _ := newFakeBunker(t)
	peer := testClient.Public()
	conversation, err := nip44.GenerateConversationKey(testService.Public(), testClient)
	require.NoError(t, err)

	sealed, err := keyer.Encrypt(t.Context(), "hello", peer)
	require.NoError(t, err)
	opened, err := nip44.Decrypt(sealed, conversation)
	require.NoError(t, err)
	require.Equal(t, "hello", opened)
	plain, err := keyer.Decrypt(t.Context(), sealed, peer)
	require.NoError(t, err)
	require.Equal(t, "hello", plain)

	binary := []byte{0, 0xff, 0x80, 0x7f}
	sealedBinary, err := keyer.EncryptBytes(t.Context(), binary, peer)
	require.NoError(t, err)
	openedBinary, err := nip44.Decrypt(sealedBinary, conversation)
	require.NoError(t, err)
	require.Equal(t, binary, []byte(openedBinary))
	roundTrip, err := keyer.DecryptBytes(t.Context(), sealedBinary, peer)
	require.NoError(t, err)
	require.Equal(t, binary, roundTrip)

	require.Equal(t, []rpcRequest{
		{"nip44_encrypt", []string{peer.Hex(), "hello"}},
		{"nip44_decrypt", []string{peer.Hex(), sealed}},
		{methodNIP44EncryptBinary, []string{peer.Hex(), base64.StdEncoding.EncodeToString(binary)}},
		{methodNIP44DecryptBinary, []string{peer.Hex(), sealedBinary}},
	}, f.requests)

	_, err = keyer.Nip04Encrypt(t.Context(), "hello", peer)
	require.ErrorIs(t, err, errors.ErrUnsupported)
	_, err = keyer.Nip04Decrypt(t.Context(), "x", peer)
	require.ErrorIs(t, err, errors.ErrUnsupported)
	require.Len(t, f.requests, 4)
}

func TestNIP46BinaryCipherReportsUnsupportedBunker(t *testing.T) {
	for _, reply := range []string{"unsupported method", "Unknown method: nip44_encrypt_b64", "method not found", "not implemented"} {
		t.Run(reply, func(t *testing.T) {
			f, keyer, _ := newFakeBunker(t)
			f.respond = func(string, []string) (string, error) { return "", errors.New("response error: " + reply) }
			_, err := keyer.EncryptBytes(t.Context(), []byte{0xff}, testClient.Public())
			require.ErrorIs(t, err, errors.ErrUnsupported)
			_, err = keyer.DecryptBytes(t.Context(), validPayloadFor(), testClient.Public())
			require.ErrorIs(t, err, errors.ErrUnsupported)
		})
	}
	f, keyer, _ := newFakeBunker(t)
	f.respond = func(string, []string) (string, error) {
		return "", errors.New("response error: not the assigned writer")
	}
	_, err := keyer.EncryptBytes(t.Context(), []byte{0xff}, testClient.Public())
	require.Error(t, err)
	require.NotErrorIs(t, err, errors.ErrUnsupported, "a refusal is not a missing capability")
}

func TestNIP46CipherDiscardsMalformedResults(t *testing.T) {
	for name, tc := range map[string]struct {
		call     func(*nip46Keyer) (any, error)
		response string
	}{
		"empty ciphertext":     {call: func(k *nip46Keyer) (any, error) { return k.Encrypt(context.Background(), "hello", testClient.Public()) }, response: ""},
		"malformed ciphertext": {call: func(k *nip46Keyer) (any, error) { return k.Encrypt(context.Background(), "hello", testClient.Public()) }, response: "not-nip44"},
		"oversized ciphertext": {call: func(k *nip46Keyer) (any, error) { return k.Encrypt(context.Background(), "hello", testClient.Public()) }, response: strings.Repeat("A", maxNIP44PayloadBase64+1)},
		"binary malformed": {call: func(k *nip46Keyer) (any, error) {
			return k.EncryptBytes(context.Background(), []byte{1}, testClient.Public())
		}, response: "not-nip44"},
		"NUL text plaintext": {call: func(k *nip46Keyer) (any, error) {
			return k.Decrypt(context.Background(), validPayloadFor(), testClient.Public())
		}, response: "before\x00after"},
		"invalid UTF-8 text": {call: func(k *nip46Keyer) (any, error) {
			return k.Decrypt(context.Background(), validPayloadFor(), testClient.Public())
		}, response: string([]byte{0xff})},
		"non-base64 binary": {call: func(k *nip46Keyer) (any, error) {
			return k.DecryptBytes(context.Background(), validPayloadFor(), testClient.Public())
		}, response: "!!"},
	} {
		t.Run(name, func(t *testing.T) {
			f, keyer, _ := newFakeBunker(t)
			f.respond = func(string, []string) (string, error) { return tc.response, nil }
			_, err := tc.call(keyer)
			require.Error(t, err)
			require.Len(t, f.requests, 1)
		})
	}
}

func validPayloadFor() string {
	key, _ := nip44.GenerateConversationKey(testClient.Public(), testService)
	sealed, _ := nip44.Encrypt("x", key)
	return sealed
}

func TestNIP46CipherRejectsInvalidInputsWithoutRPC(t *testing.T) {
	f, keyer, _ := newFakeBunker(t)
	ctx, peer := t.Context(), testClient.Public()
	_, err := keyer.Encrypt(ctx, string([]byte{0xff}), peer)
	require.ErrorContains(t, err, "UTF-8")
	_, err = keyer.Encrypt(ctx, "before\x00after", peer)
	require.ErrorContains(t, err, "NUL-free")
	_, err = keyer.Encrypt(ctx, "hello", nostr.ZeroPK)
	require.Error(t, err)
	_, err = keyer.EncryptBytes(ctx, nil, peer)
	require.Error(t, err)
	_, err = keyer.Decrypt(ctx, "not-nip44", peer)
	require.Error(t, err)
	_, err = keyer.DecryptBytes(ctx, strings.Repeat("A", maxNIP44PayloadBase64+1), peer)
	require.ErrorContains(t, err, "invalid NIP-44 ciphertext")
	require.Empty(t, f.requests)
}

func TestOpenNIP46RejectsClientKeyReuseBeforeNetwork(t *testing.T) {
	cfg := config.NostrConfig{PublicKey: testService.Public().Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerNIP46, BunkerURI: "bunker://" + testClient.Public().Hex() + "?relay=ws%3A%2F%2F127.0.0.1%3A1", ClientSecretKey: testService.Hex()}}
	_, err := Open(t.Context(), cfg, Options{})
	require.ErrorContains(t, err, "must differ from the service key")

	path := filepath.Join(t.TempDir(), "client.key")
	require.NoError(t, os.WriteFile(path, []byte(testService.Hex()+"\n"), 0o600))
	cfg.Signer.ClientSecretKey, cfg.Signer.ClientSecretKeyFile = "", path
	_, err = Open(t.Context(), cfg, Options{})
	require.ErrorContains(t, err, "must differ from the service key")
}

func TestOpenNIP46UnreachableBunkerFailsWithinTimeoutWithoutLeakingPairingSecret(t *testing.T) {
	const pairing = "private-pairing-secret"
	uri := "bunker://" + testClient.Public().Hex() + "?relay=ws%3A%2F%2F127.0.0.1%3A1&secret=" + pairing
	cfg := config.NostrConfig{PublicKey: testService.Public().Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerNIP46, BunkerURI: uri, ClientSecretKey: strings.Repeat("3", 64), Timeout: 500 * time.Millisecond}}
	started := time.Now()
	_, err := Open(t.Context(), cfg, Options{Admission: nostrout.New(nostrout.DefaultConfig())})
	require.Error(t, err)
	require.Less(t, time.Since(started), 10*time.Second)
	// A silent bunker may be ignoring a reused connect secret; the error says
	// what to do about it.
	require.ErrorContains(t, err, "remove the secret from nostr.signer.bunker_uri or pair it again with a fresh one")
	require.NotContains(t, err.Error(), pairing)
	require.NotContains(t, err.Error(), strings.Repeat("3", 64))
}

func TestRedactErrorRemovesPairingURI(t *testing.T) {
	uri := "bunker://abc?secret=s3cr3t"
	err := redactError(errors.New(`invalid bunker: parse "`+uri+`": bad`), uri)
	require.NotContains(t, err.Error(), "s3cr3t")
	plain := errors.New("unchanged")
	require.Same(t, plain, redactError(plain, uri))
}
