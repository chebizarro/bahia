package relaysidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// remoteSigner stands in for a NIP-46 or NIP-55L service signer: it reports
// its pubkey and signs, and exposes no key material.
type remoteSigner struct {
	secret nostr.SecretKey
	signed atomic.Int32
}

func (r *remoteSigner) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return r.secret.Public(), nil
}

func (r *remoteSigner) SignEvent(_ context.Context, event *nostr.Event) error {
	r.signed.Add(1)
	return event.Sign(r.secret)
}

func TestSidecarServiceIdentityComesFromRemoteSigner(t *testing.T) {
	remote := &remoteSigner{secret: nostr.Generate()}
	servicePubkey := remote.secret.Public()
	operator := nostr.Generate()
	cfg := sidecarTestConfig(t)
	cfg.Signer.Method = config.NostrSignerNIP46
	cfg.PublicKey = servicePubkey.Hex()
	require.Empty(t, cfg.PrivateKey)
	cfg.AuthorizedPubkeys = []string{operator.Public().Hex()}
	cfg.Sidecar.ServiceID = "relay-sidecar-test"
	cfg.Sidecar.Scope = "prod"
	cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")

	server, err := New(t.Context(), cfg, remote, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	require.NotNil(t, server.consumer, "trusted config authors need the config consumer")

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/nostr+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var info nip11.RelayInformationDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	require.NotNil(t, info.PubKey, "NIP-11 must advertise the service pubkey")
	require.Equal(t, servicePubkey, *info.PubKey)

	desired, err := service.ComposeConfigEvent(service.ConfigPublishRequest{
		Kind: service.ConfigFabricListKind, ServiceID: "relay-sidecar-test", PolicyName: "membership",
		Scope: "prod", Version: 1, Schema: configMembershipSchema,
		Items: []service.ConfigListItem{{Tag: "p", Value: operator.Public().Hex()}},
	}, time.Now())
	require.NoError(t, err)
	require.NoError(t, desired.Sign(operator))
	_, err = server.Relay().AddEvent(t.Context(), *desired)
	require.NoError(t, err)
	require.NoError(t, server.consumer.Handle(t.Context(), *desired))

	var acks []nostr.Event
	for event := range server.store.Query(t.Context(), nostr.Filter{Kinds: []nostr.Kind{configStatusKind}}, 10) {
		acks = append(acks, event)
	}
	require.NotEmpty(t, acks, "the consumer acknowledges desired config")
	for _, ack := range acks {
		require.Equal(t, servicePubkey, ack.PubKey)
		require.True(t, ack.VerifySignature())
	}
	require.Positive(t, remote.signed.Load())
}

func TestNewRequiresServiceSigner(t *testing.T) {
	_, err := New(t.Context(), sidecarTestConfig(t), nil, zap.NewNop())
	require.ErrorContains(t, err, "requires the service signer")
}
