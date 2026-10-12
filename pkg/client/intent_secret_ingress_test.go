package client

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type intentSecretMemoryRepo struct {
	values map[uuid.UUID]domain.ServiceSecret
}

func (r *intentSecretMemoryRepo) Create(_ context.Context, value *domain.ServiceSecret) error {
	r.values[value.ID] = *value
	return nil
}
func (r *intentSecretMemoryRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	value, ok := r.values[id]
	if !ok {
		return nil, nil
	}
	return &value, nil
}
func (r *intentSecretMemoryRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := []domain.ServiceSecret{}
	for _, value := range r.values {
		if value.ServiceID == serviceID {
			out = append(out, value)
		}
	}
	return out, nil
}
func (r *intentSecretMemoryRepo) Update(_ context.Context, value *domain.ServiceSecret) error {
	r.values[value.ID] = *value
	return nil
}
func (r *intentSecretMemoryRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.values, id)
	return nil
}

// A secret intent is a sensitive domain: it travels gift-wrapped to the
// daemon, which unwraps it at ingress. It carries only a credential
// reference; without a secret store the handler refuses it with the reason.
func TestSecretIntentPublisherGiftWrapsReferenceThroughDaemonIngress(t *testing.T) {
	ctx := context.Background()
	operator, daemon := nostr.Generate(), nostr.Generate()
	operatorSigner, daemonSigner := keyer.NewPlainKeySigner(operator), keyer.NewPlainKeySigner(daemon)
	orgID, serviceID, secretID := uuid.New(), uuid.New(), uuid.New()
	publisher, err := NewIntentPublisher(IntentPublisherConfig{Relays: []string{"wss://test.relay"}, Signer: operatorSigner,
		Pubkey: operator.Public().Hex(), ServicePubkey: daemon.Public().Hex(), Transport: &mockIntentTransport{}})
	require.NoError(t, err)
	defer publisher.Close()
	repo := &intentSecretMemoryRepo{values: map[uuid.UUID]domain.ServiceSecret{}}
	trust := controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator.Public().Hex()}))
	processor := controlplane.NewIntentProcessor(trust, nil, nil,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"secret": true}}, zap.NewNop())
	processor.RegisterHandler("secret", controlplane.NewSecretIntentHandler(controlplane.SecretIntentHandlerConfig{Registry: repo, Logger: zap.NewNop()}))
	ingress := controlplane.NewIntentGiftWrapIngress(controlplane.IntentGiftWrapIngressConfig{Signer: daemonSigner,
		Processor: processor, SensitiveDomains: []string{"org", "secret", "notification"}, Logger: zap.NewNop()})
	credentialID := "cred_" + strings.Repeat("ab", 32)
	prepared, err := publisher.PrepareIntent(ctx, PublishIntentRequest{Domain: "secret", Op: "create",
		Coordinate: secretID.String(), OrgID: orgID.String(), IntentID: uuid.NewString(),
		Content: map[string]interface{}{"id": secretID.String(), "service_id": serviceID.String(), "name": "API_TOKEN",
			"credential_ref": map[string]interface{}{"store": "signet", "credential_id": credentialID, "version": 1}}})
	require.NoError(t, err)
	require.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind)
	require.NotContains(t, prepared.Event.Content, credentialID)
	rumor, err := ingress.UnwrapIntent(ctx, &prepared.Event)
	require.NoError(t, err)
	require.NotNil(t, rumor)
	intent, err := controlplane.ParseIntent(rumor)
	require.NoError(t, err)
	intent.Actor = rumor.PubKey.Hex()
	err = processor.ProcessInProcess(ctx, intent)
	require.ErrorIs(t, err, secretstore.ErrNotConfigured)
	saved, err := repo.GetByID(ctx, secretID)
	require.NoError(t, err)
	require.Nil(t, saved)
}
