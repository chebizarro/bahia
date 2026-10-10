package signet

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func dsseFixture(t *testing.T) (*epochSignerFixture, *[]epochCryptoRequest, *func() (string, error)) {
	t.Helper()
	f := newEpochSignerFixture(t)
	requests := &[]epochCryptoRequest{}
	response := new(func() (string, error))
	*response = func() (string, error) {
		return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)), nil
	}
	f.signer.session = func() (epochSignerSession, error) {
		return epochSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.service.Public(), nil },
			rpc: func(_ context.Context, method string, params []string) (string, error) {
				*requests = append(*requests, epochCryptoRequest{method: method, params: append([]string(nil), params...)})
				return (*response)()
			},
			alive: func() bool { return f.alive },
		}, nil
	}
	return f, requests, response
}

func TestEpochSBOMDSSEStatementBytesAndEpoch(t *testing.T) {
	f, requests, _ := dsseFixture(t)
	statement := []byte("{\"statement\":\"exact\\nbytes\"}")
	signature, err := f.signer.SignStatement(context.Background(), statement)
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 64), signature)
	require.Equal(t, f.service.Public().Hex(), f.signer.KeyID())
	require.Equal(t, []epochCryptoRequest{{method: "sign_bahia_sbom_dsse", params: []string{base64.StdEncoding.EncodeToString(statement), "7"}}}, *requests)
}

func TestEpochSBOMDSSERejectsMissingLeaseAndBadInputBeforeRPC(t *testing.T) {
	f, requests, _ := dsseFixture(t)
	_, err := f.signer.SignStatement(context.Background(), nil)
	require.Error(t, err)
	_, err = f.signer.SignStatement(context.Background(), bytes.Repeat([]byte{'x'}, maxSBOMDSSEStatementBytes+1))
	require.ErrorContains(t, err, "oversized")
	f.lease.Epoch = 0
	_, err = f.signer.SignStatement(context.Background(), []byte("statement"))
	require.ErrorContains(t, err, "lease")
	require.Empty(t, *requests)
}

func TestEpochSBOMDSSERejectsWrongBunkerPubkeyBeforeRPC(t *testing.T) {
	f, requests, _ := dsseFixture(t)
	f.signer.session = func() (epochSignerSession, error) {
		return epochSignerSession{
			publicKey: func(context.Context) (nostr.PubKey, error) { return f.client.Public(), nil },
			rpc: func(context.Context, string, []string) (string, error) {
				t.Fatal("RPC reached wrong bunker")
				return "", nil
			},
			alive: func() bool { return true },
		}, nil
	}
	signature, err := f.signer.SignStatement(context.Background(), []byte("statement"))
	require.ErrorContains(t, err, "pubkey mismatch")
	require.Nil(t, signature)
	require.Empty(t, *requests)
}

func TestEpochSBOMDSSEDiscardsUnfencedOrMalformedResults(t *testing.T) {
	for name, respond := range map[string]func(*epochSignerFixture) (string, error){
		"no_result": func(*epochSignerFixture) (string, error) { return "", nil },
		"rpc_error": func(*epochSignerFixture) (string, error) { return "", errors.New("fenced") },
		"malformed": func(*epochSignerFixture) (string, error) { return "not base64", nil },
		"oversized": func(*epochSignerFixture) (string, error) {
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 65)), nil
		},
		"changed_epoch": func(f *epochSignerFixture) (string, error) {
			f.lease.Epoch++
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)), nil
		},
		"expired": func(f *epochSignerFixture) (string, error) {
			f.lease.ExpiresAt = time.Now().Add(-time.Second)
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)), nil
		},
		"disconnected": func(f *epochSignerFixture) (string, error) {
			f.alive = false
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, requests, response := dsseFixture(t)
			*response = func() (string, error) { return respond(f) }
			signature, err := f.signer.SignStatement(context.Background(), []byte("statement"))
			require.Error(t, err)
			require.Nil(t, signature)
			require.Len(t, *requests, 1)
		})
	}
}
