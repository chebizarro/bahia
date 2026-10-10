package signet

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type serviceCryptoRequest struct {
	method string
	params []string
}

func testNIP44Payload() string {
	return base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))
}

func serviceKeyerFixture(t *testing.T) (*serviceSignerFixture, *[]serviceCryptoRequest, *func(string, []string) (string, error)) {
	t.Helper()
	f := newServiceSignerFixture(t)
	requests := &[]serviceCryptoRequest{}
	response := new(func(string, []string) (string, error))
	*response = func(method string, _ []string) (string, error) {
		if strings.HasPrefix(method, "nip44_encrypt") {
			return testNIP44Payload(), nil
		}
		return "plaintext", nil
	}
	f.signer.session = func() (serviceSignerSession, error) {
		return serviceSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.service.Public(), nil },
			rpc: func(_ context.Context, method string, params []string) (string, error) {
				*requests = append(*requests, serviceCryptoRequest{method: method, params: append([]string(nil), params...)})
				return (*response)(method, params)
			},
			alive: func() bool { return f.alive },
		}, nil
	}
	return f, requests, response
}

func TestServiceKeyerSendsStandardTwoParamNIP44RPC(t *testing.T) {
	f, requests, _ := serviceKeyerFixture(t)
	peer := f.client.Public()
	ctx := context.Background()
	sealed, err := f.signer.Encrypt(ctx, "hello", peer)
	require.NoError(t, err)
	require.Equal(t, testNIP44Payload(), sealed)
	plaintext, err := f.signer.Decrypt(ctx, sealed, peer)
	require.NoError(t, err)
	require.Equal(t, "plaintext", plaintext)
	binary := []byte{0xff, 0x00, 0x80}
	sealed, err = f.signer.EncryptBytes(ctx, binary, peer)
	require.NoError(t, err)
	require.Equal(t, testNIP44Payload(), sealed)
	require.Equal(t, []serviceCryptoRequest{
		{method: "nip44_encrypt", params: []string{peer.Hex(), "hello"}},
		{method: "nip44_decrypt", params: []string{peer.Hex(), testNIP44Payload()}},
		{method: "nip44_encrypt_b64", params: []string{peer.Hex(), base64.StdEncoding.EncodeToString(binary)}},
	}, *requests)
	_, err = f.signer.Nip04Encrypt(ctx, "hello", peer)
	require.ErrorIs(t, err, errServiceNIP04Unsupported)
	_, err = f.signer.Nip04Decrypt(ctx, "payload", peer)
	require.ErrorIs(t, err, errServiceNIP04Unsupported)
	require.Len(t, *requests, 3)
}

func TestServiceKeyerRejectsWrongIdentityAndDisconnectedSessionBeforeRPC(t *testing.T) {
	f, requests, _ := serviceKeyerFixture(t)
	f.alive = false
	result, err := f.signer.Encrypt(context.Background(), "hello", f.client.Public())
	require.ErrorIs(t, err, ErrNotConnected)
	require.Empty(t, result)
	require.Empty(t, *requests)

	f, requests, _ = serviceKeyerFixture(t)
	f.signer.session = func() (serviceSignerSession, error) {
		return serviceSignerSession{publicKey: func(context.Context) (nostr.PubKey, error) { return f.client.Public(), nil }, rpc: func(context.Context, string, []string) (string, error) {
			t.Fatal("RPC despite wrong signer pubkey")
			return "", nil
		}, alive: func() bool { return true }}, nil
	}
	_, err = f.signer.Decrypt(context.Background(), testNIP44Payload(), f.client.Public())
	require.ErrorContains(t, err, "pubkey mismatch")
	require.Empty(t, *requests)
}

func TestServiceKeyerDiscardsRejectedOrMalformedResults(t *testing.T) {
	for name, response := range map[string]func(*serviceSignerFixture) (string, error){
		"rpc_rejected":         func(*serviceSignerFixture) (string, error) { return "", errors.New("not the assigned writer") },
		"empty":                func(*serviceSignerFixture) (string, error) { return "", nil },
		"malformed_ciphertext": func(*serviceSignerFixture) (string, error) { return "not-nip44", nil },
		"disconnect_after_rpc": func(f *serviceSignerFixture) (string, error) { f.alive = false; return testNIP44Payload(), nil },
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, responder := serviceKeyerFixture(t)
			*responder = func(string, []string) (string, error) { return response(f) }
			result, err := f.signer.Encrypt(context.Background(), "hello", f.client.Public())
			require.Error(t, err)
			require.Empty(t, result)
			require.Len(t, *requests, 1)
		})
	}
}

func TestServiceKeyerNoResultFailsEveryMethod(t *testing.T) {
	for _, method := range []string{"nip44_encrypt", "nip44_decrypt", "nip44_encrypt_b64"} {
		t.Run(method, func(t *testing.T) {
			f, requests, responder := serviceKeyerFixture(t)
			*responder = func(string, []string) (string, error) { return "", nil }
			ctx, peer := context.Background(), f.client.Public()
			var value string
			var err error
			switch method {
			case "nip44_encrypt":
				value, err = f.signer.Encrypt(ctx, "hello", peer)
			case "nip44_decrypt":
				value, err = f.signer.Decrypt(ctx, testNIP44Payload(), peer)
			case "nip44_encrypt_b64":
				value, err = f.signer.EncryptBytes(ctx, []byte{0xff}, peer)
			}
			require.ErrorContains(t, err, "no result")
			require.Empty(t, value)
			require.Len(t, *requests, 1)
			require.Equal(t, method, (*requests)[0].method)
		})
	}
}

func TestServiceKeyerRejectsInvalidInputsWithoutRPC(t *testing.T) {
	f, requests, _ := serviceKeyerFixture(t)
	_, err := f.signer.Encrypt(context.Background(), string([]byte{0xff}), f.client.Public())
	require.ErrorContains(t, err, "UTF-8")
	_, err = f.signer.Encrypt(context.Background(), "before\x00after", f.client.Public())
	require.ErrorContains(t, err, "NUL-free")
	_, err = f.signer.Encrypt(context.Background(), "hello", nostr.ZeroPK)
	require.Error(t, err)
	_, err = f.signer.EncryptBytes(context.Background(), nil, f.client.Public())
	require.Error(t, err)
	_, err = f.signer.Decrypt(context.Background(), "not-nip44", f.client.Public())
	require.Error(t, err)
	_, err = f.signer.Decrypt(context.Background(), strings.Repeat("A", maxNIP44PayloadBase64+1), f.client.Public())
	require.ErrorContains(t, err, "invalid NIP-44 ciphertext")
	require.Empty(t, *requests)
}

func TestServiceKeyerRejectsInvalidTextAndOversizedCiphertextResults(t *testing.T) {
	for name, response := range map[string]struct {
		method   string
		response string
	}{
		"nul_plaintext":        {method: "nip44_decrypt", response: "before\x00after"},
		"oversized_ciphertext": {method: "nip44_encrypt", response: strings.Repeat("A", maxNIP44PayloadBase64+1)},
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, responder := serviceKeyerFixture(t)
			*responder = func(string, []string) (string, error) { return response.response, nil }
			var value string
			var err error
			if response.method == "nip44_decrypt" {
				value, err = f.signer.Decrypt(context.Background(), testNIP44Payload(), f.client.Public())
			} else {
				value, err = f.signer.Encrypt(context.Background(), "hello", f.client.Public())
			}
			require.Error(t, err)
			require.Empty(t, value)
			require.Len(t, *requests, 1)
		})
	}
}

func TestServiceClientNIP44CallsUsePinnedSignerWithStandardParams(t *testing.T) {
	f, requests, _ := serviceKeyerFixture(t)
	client, err := NewClient(Config{
		BunkerURI:       "bunker://" + f.service.Public().Hex() + "?relay=wss://relay.example",
		ClientSecretKey: f.client.Hex(), RequireReal: true,
		ExpectedServicePubkey: f.service.Public().Hex(),
	}, nil)
	require.NoError(t, err)
	client.serviceSigner.session = f.signer.session
	_, err = client.NIP44Encrypt(context.Background(), f.client.Public(), "hello")
	require.NoError(t, err)
	_, err = client.NIP44Decrypt(context.Background(), f.client.Public(), testNIP44Payload())
	require.NoError(t, err)
	_, err = client.NIP44EncryptBytes(context.Background(), f.client.Public(), []byte{0xff})
	require.NoError(t, err)
	require.Len(t, *requests, 3)
	for _, request := range *requests {
		require.Len(t, request.params, 2)
	}
	f.alive = false
	result, err := client.NIP44Encrypt(context.Background(), f.client.Public(), "hello")
	require.ErrorIs(t, err, ErrNotConnected)
	require.Empty(t, result)
	require.Len(t, *requests, 3)
}
