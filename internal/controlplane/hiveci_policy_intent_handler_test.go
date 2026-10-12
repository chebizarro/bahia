package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip59"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type policyIntentStore struct {
	policies   map[uuid.UUID]domain.HiveCIPipelinePolicy
	writes     int
	deletes    int
	failEnsure bool
}

const hiveCIPolicyOperatorKey = "1111111111111111111111111111111111111111111111111111111111111111"

func signHiveCIPolicyIntent(t *testing.T, intent *Intent, key string) string {
	t.Helper()
	signer, err := NewPrivateKeySigner(key)
	require.NoError(t, err)
	content, err := json.Marshal(intent.Content)
	require.NoError(t, err)
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", intent.Coordinate}, {"domain", intent.Domain}, {"schema", intent.Schema},
		{"t", "bahia-intent"}, {"t", "hiveci-policy"}, {"op", intent.Op},
		{"intent_id", intent.IntentID}}, Content: string(content)}
	require.NoError(t, signer.SignEvent(context.Background(), &event))
	intent.Event = &event
	intent.Actor = event.PubKey.Hex()
	return intent.Actor
}

func (s *policyIntentStore) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	out := make([]domain.HiveCIPipelinePolicy, 0, len(s.policies))
	for _, policy := range s.policies {
		out = append(out, policy)
	}
	return out, nil
}
func (s *policyIntentStore) EnsurePipelinePolicy(_ context.Context, policy domain.HiveCIPipelinePolicy) error {
	if s.failEnsure {
		return errors.New("canonical publisher unavailable")
	}
	s.writes++
	if old, ok := s.policies[policy.ID]; ok {
		policy.CreatedAt = old.CreatedAt
	}
	policy.UpdatedAt = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Add(time.Duration(s.writes) * time.Second)
	s.policies[policy.ID] = policy
	return nil
}

func TestHiveCIPolicyIntentRetriesAfterCanonicalFailure(t *testing.T) {
	ctx := context.Background()
	store := &policyIntentStore{policies: make(map[uuid.UUID]domain.HiveCIPipelinePolicy), failEnsure: true}
	id := uuid.Must(uuid.NewV7())
	intent := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	operator := signHiveCIPolicyIntent(t, intent, hiveCIPolicyOperatorKey)
	statuses := &statusCollector{}
	processor := NewIntentProcessor(NewTrustSet([]string{operator}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"hiveci-policy": true}}, zap.NewNop())
	processor.RegisterHandler("hiveci-policy", NewHiveCIPolicyIntentHandler(store))
	require.ErrorContains(t, processor.ProcessRelayIntent(ctx, intent.Event), "canonical publisher unavailable")
	require.False(t, processor.IsProcessed(intent.IntentID))
	require.Zero(t, store.writes)
	store.failEnsure = false
	require.NoError(t, processor.ProcessRelayIntent(ctx, intent.Event))
	require.True(t, processor.IsProcessed(intent.IntentID))
	require.Equal(t, 1, store.writes)
}

func TestHiveCIPolicyNIP59RumorReachesCanonicalHandler(t *testing.T) {
	ctx := context.Background()
	operatorSecret := nostr.Generate()
	serviceSecret := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorSecret)
	serviceSigner := keyer.NewPlainKeySigner(serviceSecret)
	id := uuid.Must(uuid.NewV7())
	request := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	content, err := json.Marshal(request.Content)
	require.NoError(t, err)
	inner := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", request.Coordinate}, {"domain", request.Domain}, {"schema", request.Schema},
		{"t", "bahia-intent"}, {"t", "hiveci-policy"}, {"op", request.Op},
		{"intent_id", request.IntentID}}, Content: string(content)}
	require.NoError(t, operatorSigner.SignEvent(ctx, &inner))
	outer, err := nip59.GiftWrap(inner, serviceSecret.Public(),
		func(plaintext string) (string, error) {
			return operatorSigner.Encrypt(ctx, plaintext, serviceSecret.Public())
		},
		func(seal *nostr.Event) error { return operatorSigner.SignEvent(ctx, seal) }, nil)
	require.NoError(t, err)
	policyStore := &policyIntentStore{policies: make(map[uuid.UUID]domain.HiveCIPipelinePolicy)}
	statuses := &statusCollector{}
	processor := NewIntentProcessor(NewTrustSet([]string{operatorSecret.Public().Hex()}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"hiveci-policy": true}}, zap.NewNop())
	processor.RegisterHandler("hiveci-policy", NewHiveCIPolicyIntentHandler(policyStore))
	ingress := NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{Signer: serviceSigner, Processor: processor,
		SensitiveDomains: []string{"hiveci-policy"}, Logger: zap.NewNop()})
	rumor, err := ingress.UnwrapIntent(ctx, &outer)
	require.NoError(t, err)
	require.NotNil(t, rumor)
	require.Equal(t, [64]byte{}, rumor.Sig)
	require.NoError(t, ingress.processVerifiedRumor(ctx, rumor))
	require.Equal(t, 1, policyStore.writes)
	require.Equal(t, id, policyStore.policies[id].ID)
}
func (s *policyIntentStore) DeletePipelinePolicy(_ context.Context, id uuid.UUID) error {
	s.deletes++
	delete(s.policies, id)
	return nil
}

func hiveCIPolicyIntent(id uuid.UUID, intentID, op string) *Intent {
	content := map[string]any{"id": id.String(), "intent_id": intentID, "repo_coordinate": "30617:author:api",
		"workflow_path": ".gitea/workflows/release.yml", "branch_pattern": "main",
		"service_id":     uuid.MustParse("0199c693-7b5f-7a42-b13e-fb6107ff039c").String(),
		"environment_id": uuid.MustParse("0199c693-7b5f-7a42-b13e-fb6107ff039d").String(),
		"enabled":        true, "metadata": map[string]any{"release": true}}
	if op == "delete" {
		content = map[string]any{"id": id.String(), "deleted": true, "intent_id": intentID}
	}
	return &Intent{Domain: "hiveci-policy", Op: op, Schema: "bahia.intent.hiveci-policy.v1",
		Coordinate: "hiveci:policy:" + id.String(), IntentID: intentID, Actor: testPubkey, Content: content}
}

func TestHiveCIPolicyIntentLevelTriggeredReplayConflictAndDelete(t *testing.T) {
	ctx := context.Background()
	store := &policyIntentStore{policies: make(map[uuid.UUID]domain.HiveCIPipelinePolicy)}
	statuses := &statusCollector{}
	id := uuid.Must(uuid.NewV7())
	first := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "update")
	operator := signHiveCIPolicyIntent(t, first, hiveCIPolicyOperatorKey)
	processor := NewIntentProcessor(NewTrustSet([]string{operator}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"hiveci-policy": true}}, zap.NewNop())
	processor.RegisterHandler("hiveci-policy", NewHiveCIPolicyIntentHandler(store))
	require.NoError(t, processor.ProcessRelayIntent(ctx, first.Event), "signed update without observed create must materialize full state")
	require.Equal(t, 1, store.writes)
	require.Equal(t, id, store.policies[id].ID)
	require.NoError(t, processor.ProcessInProcess(ctx, first))
	require.Equal(t, 1, store.writes, "same intent_id must not publish again")
	conflictingReplay := hiveCIPolicyIntent(uuid.Must(uuid.NewV7()), first.IntentID, "update")
	signHiveCIPolicyIntent(t, conflictingReplay, hiveCIPolicyOperatorKey)
	require.ErrorContains(t, processor.ProcessInProcess(ctx, conflictingReplay), "conflicts")
	require.Equal(t, 1, store.writes)

	stale := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "update")
	stale.Content["expected_updated_at"] = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	signHiveCIPolicyIntent(t, stale, hiveCIPolicyOperatorKey)
	require.Error(t, processor.ProcessInProcess(ctx, stale))
	require.Equal(t, 1, store.writes)
	current := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "update")
	current.Content["expected_updated_at"] = store.policies[id].UpdatedAt.Format(time.RFC3339Nano)
	current.Content["enabled"] = false
	signHiveCIPolicyIntent(t, current, hiveCIPolicyOperatorKey)
	require.NoError(t, processor.ProcessInProcess(ctx, current))
	require.False(t, store.policies[id].Enabled)

	deleteIntent := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "delete")
	signHiveCIPolicyIntent(t, deleteIntent, hiveCIPolicyOperatorKey)
	require.NoError(t, processor.ProcessInProcess(ctx, deleteIntent))
	require.Equal(t, 1, store.deletes)
	require.Empty(t, store.policies)
	require.NoError(t, processor.ProcessInProcess(ctx, deleteIntent))
	require.Equal(t, 1, store.deletes)
	statusesByIntent := make(map[string][]string)
	for _, event := range statuses.events {
		var status, eventIntentID string
		for _, tag := range event.Tags {
			if len(tag) < 2 {
				continue
			}
			switch tag[0] {
			case "status":
				status = tag[1]
			case "intent_id":
				eventIntentID = tag[1]
			}
		}
		statusesByIntent[eventIntentID] = append(statusesByIntent[eventIntentID], status)
	}
	require.Contains(t, statusesByIntent[first.IntentID], "accepted")
	require.Contains(t, statusesByIntent[stale.IntentID], "conflict")
	require.Contains(t, statusesByIntent[deleteIntent.IntentID], "accepted")
}

func TestHiveCIPolicyIntentRejectsInvalidAndUntrusted(t *testing.T) {
	ctx := context.Background()
	store := &policyIntentStore{policies: make(map[uuid.UUID]domain.HiveCIPipelinePolicy)}
	id := uuid.Must(uuid.NewV7())
	first := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	operator := signHiveCIPolicyIntent(t, first, hiveCIPolicyOperatorKey)
	processor := NewIntentProcessor(NewTrustSet([]string{operator}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher((&statusCollector{}).publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"hiveci-policy": true}}, zap.NewNop())
	processor.RegisterHandler("hiveci-policy", NewHiveCIPolicyIntentHandler(store))
	unsigned := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	unsigned.Actor = operator
	require.ErrorContains(t, processor.ProcessInProcess(ctx, unsigned), "signature")
	bad := hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	delete(bad.Content, "enabled")
	signHiveCIPolicyIntent(t, bad, hiveCIPolicyOperatorKey)
	require.ErrorContains(t, processor.ProcessInProcess(ctx, bad), "enabled")
	bad = hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	bad.Coordinate = "hiveci:policy:" + uuid.Must(uuid.NewV7()).String()
	signHiveCIPolicyIntent(t, bad, hiveCIPolicyOperatorKey)
	require.ErrorContains(t, processor.ProcessInProcess(ctx, bad), "coordinate")
	bad = hiveCIPolicyIntent(id, uuid.Must(uuid.NewV7()).String(), "create")
	signHiveCIPolicyIntent(t, bad, "2222222222222222222222222222222222222222222222222222222222222222")
	require.ErrorContains(t, processor.ProcessInProcess(ctx, bad), "untrusted")
	require.Zero(t, store.writes)
}

func TestHiveCIPolicyUntrustedPlaintextDoesNotAmplifyStatus(t *testing.T) {
	ctx := context.Background()
	statuses := &statusCollector{}
	processor := NewIntentProcessor(NewTrustSet(nil, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"hiveci-policy": true}}, zap.NewNop())
	processor.RegisterHandler("hiveci-policy", NewHiveCIPolicyIntentHandler(&policyIntentStore{policies: make(map[uuid.UUID]domain.HiveCIPipelinePolicy)}))
	processor.SetGiftWrapIngress(NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{
		Processor: processor, SensitiveDomains: []string{"hiveci-policy"}, Logger: zap.NewNop()}))
	intent := hiveCIPolicyIntent(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()).String(), "create")
	signHiveCIPolicyIntent(t, intent, hiveCIPolicyOperatorKey)
	require.NoError(t, processor.ProcessRelayIntent(ctx, intent.Event))
	require.Empty(t, statuses.events)
}
