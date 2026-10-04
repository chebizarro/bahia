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

func TestSecretIntentPublisherEncryptedGiftWrapThroughDaemonIngress(t *testing.T) {
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
	for _, tc := range []struct{ op, value string }{{"create", "first-secret-value"}, {"update", "second-secret-value"}} {
		prepared, err := publisher.PrepareIntent(ctx, PublishIntentRequest{Domain: "secret", Op: tc.op,
			Coordinate: secretID.String(), OrgID: orgID.String(), IntentID: uuid.NewString(),
			Content:              map[string]interface{}{"id": secretID.String(), "service_id": serviceID.String(), "name": "API_TOKEN", "encryption_method": "nip44"},
			PlaintextSecretValue: tc.value})
		require.NoError(t, err)
		require.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind)
		require.NotContains(t, prepared.Event.Content, tc.value)
		require.NotContains(t, prepared.InnerEvent.Content, tc.value)
		rumor, err := ingress.UnwrapIntent(ctx, &prepared.Event)
		require.NoError(t, err)
		require.NotNil(t, rumor)
		intent, err := controlplane.ParseIntent(rumor)
		require.NoError(t, err)
		intent.Actor = rumor.PubKey.Hex()
		require.NoError(t, processor.ProcessInProcess(ctx, intent))
		saved, err := repo.GetByID(ctx, secretID)
		require.NoError(t, err)
		require.NotNil(t, saved)
		require.False(t, strings.Contains(string(saved.EncryptedValue), tc.value))
		decrypted, err := daemonSigner.Decrypt(ctx, string(saved.EncryptedValue), operator.Public())
		require.NoError(t, err)
		require.Equal(t, tc.value, decrypted)
	}
}

type intentNotificationMemoryRepo struct {
	values map[uuid.UUID]domain.NotificationChannel
}

func (r *intentNotificationMemoryRepo) CreateChannel(_ context.Context, channel *domain.NotificationChannel) error {
	r.values[channel.ID] = *channel
	return nil
}
func (r *intentNotificationMemoryRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	value, ok := r.values[id]
	if !ok {
		return nil, nil
	}
	return &value, nil
}
func (r *intentNotificationMemoryRepo) UpdateChannel(_ context.Context, channel *domain.NotificationChannel) error {
	r.values[channel.ID] = *channel
	return nil
}
func (r *intentNotificationMemoryRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	delete(r.values, id)
	return nil
}
func (r *intentNotificationMemoryRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	out := []domain.NotificationChannel{}
	for _, value := range r.values {
		if !enabledOnly || value.Enabled {
			out = append(out, value)
		}
	}
	return out, nil
}

func TestNotificationIntentPublisherGiftWrapThroughDaemonIngress(t *testing.T) {
	ctx := context.Background()
	operator, daemon := nostr.Generate(), nostr.Generate()
	operatorSigner, daemonSigner := keyer.NewPlainKeySigner(operator), keyer.NewPlainKeySigner(daemon)
	orgID, channelID := uuid.New(), uuid.New()
	publisher, err := NewIntentPublisher(IntentPublisherConfig{Relays: []string{"wss://test.relay"}, Signer: operatorSigner,
		Pubkey: operator.Public().Hex(), ServicePubkey: daemon.Public().Hex(), Transport: &mockIntentTransport{}})
	require.NoError(t, err)
	defer publisher.Close()
	repo := &intentNotificationMemoryRepo{values: map[uuid.UUID]domain.NotificationChannel{}}
	trust := controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator.Public().Hex()}))
	processor := controlplane.NewIntentProcessor(trust, nil, nil,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"notification": true}}, zap.NewNop())
	processor.RegisterHandler("notification", controlplane.NewNotificationIntentHandler(controlplane.NotificationIntentHandlerConfig{Registry: repo, Logger: zap.NewNop()}))
	ingress := controlplane.NewIntentGiftWrapIngress(controlplane.IntentGiftWrapIngressConfig{Signer: daemonSigner,
		Processor: processor, SensitiveDomains: []string{"org", "secret", "notification"}, Logger: zap.NewNop()})
	prepared, err := publisher.PrepareIntent(ctx, PublishIntentRequest{Domain: "notification", Op: "create",
		Coordinate: channelID.String(), OrgID: orgID.String(), IntentID: uuid.NewString(),
		Content: map[string]interface{}{"id": channelID.String(), "name": "Alerts", "channel_type": "webhook", "enabled": true,
			"config": map[string]interface{}{"url": "https://hooks.example.test/alert", "secret": "webhook-secret-value"}}})
	require.NoError(t, err)
	require.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind)
	require.NotContains(t, prepared.Event.Content, "webhook-secret-value")
	rumor, err := ingress.UnwrapIntent(ctx, &prepared.Event)
	require.NoError(t, err)
	require.NotNil(t, rumor)
	intent, err := controlplane.ParseIntent(rumor)
	require.NoError(t, err)
	intent.Actor = rumor.PubKey.Hex()
	require.NoError(t, processor.ProcessInProcess(ctx, intent))
	saved, err := repo.GetChannelByID(ctx, channelID)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, "Alerts", saved.Name)
	require.True(t, saved.Enabled)
}
