package signet

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type epochCryptoRequest struct {
	method string
	params []string
}

func testNIP44Payload() string {
	return base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))
}

func epochKeyerFixture(t *testing.T) (*epochSignerFixture, *[]epochCryptoRequest, *func(string, []string) (string, error)) {
	t.Helper()
	f := newEpochSignerFixture(t)
	requests := &[]epochCryptoRequest{}
	response := new(func(string, []string) (string, error))
	*response = func(method string, _ []string) (string, error) {
		if strings.HasPrefix(method, "nip44_encrypt") {
			return testNIP44Payload(), nil
		}
		if method == "nip44_decrypt_b64" {
			return base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0x80}), nil
		}
		return "plaintext", nil
	}
	f.signer.session = func() (epochSignerSession, error) {
		return epochSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.service.Public(), nil },
			rpc: func(_ context.Context, method string, params []string) (string, error) {
				*requests = append(*requests, epochCryptoRequest{method: method, params: append([]string(nil), params...)})
				return (*response)(method, params)
			},
			alive: func() bool { return f.alive },
		}, nil
	}
	return f, requests, response
}

func TestEpochKeyerFencesTextAndBinaryNIP44RPC(t *testing.T) {
	f, requests, _ := epochKeyerFixture(t)
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
	opened, err := f.signer.decryptBytes(ctx, sealed, peer)
	require.NoError(t, err)
	require.True(t, bytes.Equal(binary, opened))
	require.Equal(t, []string{"nip44_encrypt", "nip44_decrypt", "nip44_encrypt_b64", "nip44_decrypt_b64"}, []string{(*requests)[0].method, (*requests)[1].method, (*requests)[2].method, (*requests)[3].method})
	for _, request := range *requests {
		require.Len(t, request.params, 3)
		require.Equal(t, peer.Hex(), request.params[0])
		require.Equal(t, "7", request.params[2])
	}
	require.Equal(t, "hello", (*requests)[0].params[1])
	require.Equal(t, testNIP44Payload(), (*requests)[1].params[1])
	require.Equal(t, base64.StdEncoding.EncodeToString(binary), (*requests)[2].params[1])
	require.Equal(t, testNIP44Payload(), (*requests)[3].params[1])
	_, err = f.signer.Nip04Encrypt(ctx, "hello", peer)
	require.ErrorIs(t, err, errEpochNIP04Unsupported)
	_, err = f.signer.Nip04Decrypt(ctx, "payload", peer)
	require.ErrorIs(t, err, errEpochNIP04Unsupported)
	require.Len(t, *requests, 4)
}

func TestEpochKeyerRejectsMissingLeaseIdentityAndSessionBeforeRPC(t *testing.T) {
	for name, mutate := range map[string]func(*epochSignerFixture){
		"missing_epoch": func(f *epochSignerFixture) { f.lease.Epoch = 0 },
		"wrong_owner":   func(f *epochSignerFixture) { f.lease.OwnerPubkey = f.service.Public() },
		"expired":       func(f *epochSignerFixture) { f.lease.ExpiresAt = time.Now().Add(-time.Second) },
		"disconnected":  func(f *epochSignerFixture) { f.alive = false },
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, _ := epochKeyerFixture(t)
			mutate(f)
			result, err := f.signer.Encrypt(context.Background(), "hello", f.client.Public())
			require.Error(t, err)
			require.Empty(t, result)
			require.Empty(t, *requests)
		})
	}
	f, requests, _ := epochKeyerFixture(t)
	f.signer.session = func() (epochSignerSession, error) {
		return epochSignerSession{publicKey: func(context.Context) (nostr.PubKey, error) { return f.client.Public(), nil }, rpc: func(context.Context, string, []string) (string, error) {
			t.Fatal("RPC despite wrong signer pubkey")
			return "", nil
		}, alive: func() bool { return true }}, nil
	}
	_, err := f.signer.Decrypt(context.Background(), testNIP44Payload(), f.client.Public())
	require.ErrorContains(t, err, "pubkey mismatch")
	require.Empty(t, *requests)
}

func TestEpochKeyerDiscardsUnfencedOrMalformedResults(t *testing.T) {
	for name, response := range map[string]func(*epochSignerFixture) (string, error){
		"rpc_rejected":         func(*epochSignerFixture) (string, error) { return "", errors.New("fenced") },
		"empty":                func(*epochSignerFixture) (string, error) { return "", nil },
		"malformed_ciphertext": func(*epochSignerFixture) (string, error) { return "not-nip44", nil },
		"lease_changed":        func(f *epochSignerFixture) (string, error) { f.lease.Epoch++; return testNIP44Payload(), nil },
		"expired_after_rpc": func(f *epochSignerFixture) (string, error) {
			f.lease.ExpiresAt = time.Now().Add(-time.Second)
			return testNIP44Payload(), nil
		},
		"disconnect_after_rpc": func(f *epochSignerFixture) (string, error) { f.alive = false; return testNIP44Payload(), nil },
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, responder := epochKeyerFixture(t)
			*responder = func(string, []string) (string, error) { return response(f) }
			result, err := f.signer.Encrypt(context.Background(), "hello", f.client.Public())
			require.Error(t, err)
			require.Empty(t, result)
			require.Len(t, *requests, 1)
		})
	}
	f, _, responder := epochKeyerFixture(t)
	*responder = func(string, []string) (string, error) { return "%%%", nil }
	result, err := f.signer.decryptBytes(context.Background(), testNIP44Payload(), f.client.Public())
	require.ErrorContains(t, err, "malformed")
	require.Nil(t, result)
}

func TestEpochKeyerNoResultFailsEveryMethod(t *testing.T) {
	for _, method := range []string{"nip44_encrypt", "nip44_decrypt", "nip44_encrypt_b64", "nip44_decrypt_b64"} {
		t.Run(method, func(t *testing.T) {
			f, requests, responder := epochKeyerFixture(t)
			*responder = func(string, []string) (string, error) { return "", nil }
			ctx, peer := context.Background(), f.client.Public()
			switch method {
			case "nip44_encrypt":
				value, err := f.signer.Encrypt(ctx, "hello", peer)
				require.ErrorContains(t, err, "no result")
				require.Empty(t, value)
			case "nip44_decrypt":
				value, err := f.signer.Decrypt(ctx, testNIP44Payload(), peer)
				require.ErrorContains(t, err, "no result")
				require.Empty(t, value)
			case "nip44_encrypt_b64":
				value, err := f.signer.EncryptBytes(ctx, []byte{0xff}, peer)
				require.ErrorContains(t, err, "no result")
				require.Empty(t, value)
			case "nip44_decrypt_b64":
				value, err := f.signer.decryptBytes(ctx, testNIP44Payload(), peer)
				require.ErrorContains(t, err, "no result")
				require.Nil(t, value)
			}
			require.Len(t, *requests, 1)
			require.Equal(t, method, (*requests)[0].method)
		})
	}
}

func TestEpochKeyerRejectsInvalidInputsWithoutRPC(t *testing.T) {
	f, requests, _ := epochKeyerFixture(t)
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
	_, err = f.signer.Decrypt(context.Background(), strings.Repeat("A", maxEpochNIP44PayloadBase64+1), f.client.Public())
	require.ErrorContains(t, err, "invalid NIP-44 ciphertext")
	require.Empty(t, *requests)
}

func TestEpochKeyerRejectsInvalidTextAndOversizedCiphertextResults(t *testing.T) {
	for name, response := range map[string]struct {
		method   string
		response string
	}{
		"nul_plaintext":        {method: "nip44_decrypt", response: "before\x00after"},
		"oversized_ciphertext": {method: "nip44_encrypt", response: strings.Repeat("A", maxEpochNIP44PayloadBase64+1)},
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, responder := epochKeyerFixture(t)
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

func TestEpochKeyerRejectsOversizedBinaryPlaintextBeforeDecode(t *testing.T) {
	f, requests, responder := epochKeyerFixture(t)
	*responder = func(method string, _ []string) (string, error) {
		require.Equal(t, "nip44_decrypt_b64", method)
		return strings.Repeat("A", maxEpochNIP44PlaintextBase64+1), nil
	}
	plaintext, err := f.signer.decryptBytes(context.Background(), testNIP44Payload(), f.client.Public())
	require.ErrorContains(t, err, "oversized")
	require.Nil(t, plaintext)
	require.Len(t, *requests, 1)
}

func TestEpochClientNIP44CallsNeverFallBackToLegacyNoEpochRPC(t *testing.T) {
	f, requests, _ := epochKeyerFixture(t)
	client, err := NewClient(Config{
		BunkerURI:       "bunker://" + f.service.Public().Hex() + "?relay=wss://relay.example",
		ClientSecretKey: f.client.Hex(), RequireReal: true,
		EpochLease:            func(context.Context) (WriterLease, error) { return f.lease, nil },
		ExpectedServicePubkey: f.service.Public().Hex(),
	}, nil)
	require.NoError(t, err)
	client.epochSigner.session = f.signer.session
	_, err = client.NIP44Encrypt(context.Background(), f.client.Public(), "hello")
	require.NoError(t, err)
	_, err = client.NIP44Decrypt(context.Background(), f.client.Public(), testNIP44Payload())
	require.NoError(t, err)
	_, err = client.NIP44EncryptBytes(context.Background(), f.client.Public(), []byte{0xff})
	require.NoError(t, err)
	require.Len(t, *requests, 3)
	for _, request := range *requests {
		require.Len(t, request.params, 3)
	}
	f.lease.Epoch = 0
	result, err := client.NIP44Encrypt(context.Background(), f.client.Public(), "hello")
	require.ErrorContains(t, err, "lease")
	require.Empty(t, result)
	require.Len(t, *requests, 3)
}
