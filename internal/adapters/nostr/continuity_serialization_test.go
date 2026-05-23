package nostr

import (
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestContinuityProfileSerializationUsesProfileTagBlocks(t *testing.T) {
	profile := domain.ServiceContinuityProfile{
		ServiceKey:          "svc.api",
		PrimaryWorkerPubKey: "primarypubkey",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {
				Requires:   []string{"postgres", "redis"},
				Disables:   []string{"batch"},
				Limits:     map[string]string{"qps": "1000"},
				Attributes: map[string]string{"dns": "primary"},
			},
			domain.ContinuityModeDegraded: {
				Requires: []string{"postgres-replica"},
				Limits:   map[string]string{"qps": "100"},
			},
		},
	}

	event, err := EncodeContinuityProfileEvent(profile)
	require.NoError(t, err)
	require.Equal(t, KindContinuityProfile, event.Kind)
	require.Equal(t, "continuity-profile:svc.api", continuityTagValue(event.Tags, "d"))
	require.Equal(t, "svc.api", continuityTagValue(event.Tags, "service"))

	decoded, err := DecodeContinuityProfileEvent(&event)
	require.NoError(t, err)
	require.Equal(t, "svc.api", decoded.ServiceKey)
	require.Equal(t, "primarypubkey", decoded.PrimaryWorkerPubKey)
	require.ElementsMatch(t, []string{"postgres", "redis"}, decoded.Profiles[domain.ContinuityModeFull].Requires)
	require.Equal(t, []string{"batch"}, decoded.Profiles[domain.ContinuityModeFull].Disables)
	require.Equal(t, "1000", decoded.Profiles[domain.ContinuityModeFull].Limits["qps"])
	require.Equal(t, "100", decoded.Profiles[domain.ContinuityModeDegraded].Limits["qps"])
}

func TestContinuityProfileDecodeRejectsBlockTagsBeforeProfile(t *testing.T) {
	event := gonostr.Event{
		Kind:      KindContinuityProfile,
		CreatedAt: gonostr.Now(),
		Tags: gonostr.Tags{
			{"d", "continuity-profile:svc.api"},
			{"service", "svc.api"},
			{"requires", "postgres"},
		},
	}

	_, err := DecodeContinuityProfileEvent(&event)
	require.ErrorContains(t, err, "before any profile tag")
}

func TestFailoverPolicySerializationUsesIndexedTagsAndJSONContent(t *testing.T) {
	recipe := domain.ContinuityRecipe{
		Name:       "primary-heartbeat-loss",
		ServiceKey: "svc.api",
		Trigger: &domain.RecipeTrigger{
			Type:    domain.RecipeTriggerTypeHeartbeatLoss,
			Target:  "primary",
			Timeout: 45 * time.Second,
		},
		Steps: []domain.RecipeStep{
			{Name: "move service", Action: domain.RecipeActionMoveService, Timeout: 2 * time.Minute, Params: map[string]string{"mode": "degraded"}},
		},
	}

	event, err := EncodeFailoverPolicyEvent(recipe)
	require.NoError(t, err)
	require.Equal(t, KindFailoverPolicy, event.Kind)
	require.Equal(t, "svc.api", continuityTagValue(event.Tags, "service"))
	require.Equal(t, "failover", continuityTagValue(event.Tags, "recipe-kind"))
	require.NotEmpty(t, event.Content)

	decoded, err := DecodeFailoverPolicyEvent(&event)
	require.NoError(t, err)
	require.Equal(t, domain.ContinuityRecipeKindFailover, decoded.Kind)
	require.Equal(t, "primary-heartbeat-loss", decoded.Name)
	require.Equal(t, domain.RecipeActionMoveService, decoded.Steps[0].Action)
	require.Equal(t, 45*time.Second, decoded.Trigger.Timeout)
}

func TestRecoveryWorkflowSerializationUsesRecoveryRecipeKind(t *testing.T) {
	recipe := domain.ContinuityRecipe{
		Name:       "restore-primary",
		ServiceKey: "svc.api",
		Steps: []domain.RecipeStep{
			{Name: "restore dns", Action: domain.RecipeActionRestoreDNSRoutes, Timeout: time.Minute},
		},
	}

	event, err := EncodeRecoveryWorkflowEvent(recipe)
	require.NoError(t, err)
	require.Equal(t, KindRecoveryWorkflow, event.Kind)
	require.Equal(t, "recovery", continuityTagValue(event.Tags, "recipe-kind"))

	decoded, err := DecodeRecoveryWorkflowEvent(&event)
	require.NoError(t, err)
	require.Equal(t, domain.ContinuityRecipeKindRecovery, decoded.Kind)
	require.Nil(t, decoded.Trigger)
	require.Equal(t, domain.RecipeActionRestoreDNSRoutes, decoded.Steps[0].Action)
}

func TestStandbyNodeDefinitionSerializationUsesHostRoleSupportAndProfileTags(t *testing.T) {
	definition := StandbyNodeDefinition{
		WorkerPubKey: "standbypubkey",
		Host:         "edge-02",
		Role:         "standby",
		ServiceKey:   "svc.api",
		Tier:         domain.StandbyTierWarm,
		Supports:     []string{"postgres", "dns"},
		Profiles:     []domain.ContinuityMode{domain.ContinuityModeDegraded, domain.ContinuityModeEmergency},
	}

	event, err := EncodeStandbyNodeDefinitionEvent(definition)
	require.NoError(t, err)
	require.Equal(t, KindStandbyNodeDefinition, event.Kind)
	require.Equal(t, "edge-02", continuityTagValue(event.Tags, "host"))
	require.Equal(t, "standby", continuityTagValue(event.Tags, "role"))
	require.ElementsMatch(t, []string{"dns", "postgres"}, continuityTagValues(event.Tags, "supports"))
	require.ElementsMatch(t, []string{"degraded", "emergency"}, continuityTagValues(event.Tags, "profile"))

	decoded, err := DecodeStandbyNodeDefinitionEvent(&event)
	require.NoError(t, err)
	require.Equal(t, "standbypubkey", decoded.WorkerPubKey)
	require.Equal(t, domain.StandbyTierWarm, decoded.Tier)
	require.ElementsMatch(t, []domain.ContinuityMode{domain.ContinuityModeDegraded, domain.ContinuityModeEmergency}, decoded.Profiles)
}

func TestReplicationPolicySerializationUsesIndexedTagsAndJSONContent(t *testing.T) {
	policy := domain.ReplicationPolicy{
		ServiceKey: "svc.api",
		Targets: []domain.ReplicationTarget{
			{
				WorkerPubKey:     "worker-replica",
				Strategy:         "event_mirror",
				MaxStaleness:     30 * time.Second,
				RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded},
			},
		},
	}

	event, err := EncodeReplicationPolicyEvent(policy)
	require.NoError(t, err)
	require.Equal(t, KindReplicationPolicy, event.Kind)
	require.Equal(t, "replication-policy:svc.api", continuityTagValue(event.Tags, "d"))
	require.NotEmpty(t, event.Content)

	decoded, err := DecodeReplicationPolicyEvent(&event)
	require.NoError(t, err)
	require.Equal(t, "svc.api", decoded.ServiceKey)
	require.Equal(t, "worker-replica", decoded.Targets[0].WorkerPubKey)
	require.Equal(t, 30*time.Second, decoded.Targets[0].MaxStaleness)
}

func TestHeartbeatObservationSerializationUsesWorkerSequenceAndIntervalTags(t *testing.T) {
	observedAt := time.Unix(1710000000, 0).UTC()
	observation := domain.HeartbeatObservation{
		WorkerPubKey: "workerpubkey",
		ObservedAt:   observedAt,
		Sequence:     42,
		Interval:     15 * time.Second,
		ExpiresAfter: 45 * time.Second,
	}

	event, err := EncodeHeartbeatObservationEvent(observation)
	require.NoError(t, err)
	require.Equal(t, KindHeartbeatObservation, event.Kind)
	require.Equal(t, "heartbeat:workerpubkey", continuityTagValue(event.Tags, "d"))
	require.Equal(t, "workerpubkey", continuityTagValue(event.Tags, "worker"))
	require.Equal(t, "42", continuityTagValue(event.Tags, "sequence"))
	require.Equal(t, "15000", continuityTagValue(event.Tags, "interval_ms"))

	decoded, err := DecodeHeartbeatObservationEvent(&event)
	require.NoError(t, err)
	require.Equal(t, "workerpubkey", decoded.WorkerPubKey)
	require.Equal(t, uint64(42), decoded.Sequence)
	require.Equal(t, 15*time.Second, decoded.Interval)
	require.Equal(t, 45*time.Second, decoded.ExpiresAfter)
	require.Equal(t, observedAt, decoded.ObservedAt)
}

func TestContinuityCommandSerializationRequiresServiceTargetAndDTag(t *testing.T) {
	request := ContinuityCommandRequest{
		ServiceKey:         "svc.api",
		TargetWorkerPubKey: "standbypubkey",
		TargetProfile:      domain.ContinuityModeDegraded,
		RecipeName:         "primary-heartbeat-loss",
		IdempotencyKey:     "failover:svc.api:001",
		Reason:             "operator drill",
		Metadata:           map[string]any{"source": "test"},
	}

	event, err := EncodeFailoverRequestEvent(request)
	require.NoError(t, err)
	require.Equal(t, KindFailoverRequest, event.Kind)
	require.Equal(t, "svc.api", continuityTagValue(event.Tags, "service"))
	require.Equal(t, "standbypubkey", continuityTagValue(event.Tags, "target"))
	require.Equal(t, "degraded", continuityTagValue(event.Tags, "profile"))

	decoded, err := DecodeFailoverRequestEvent(&event)
	require.NoError(t, err)
	require.Equal(t, "svc.api", decoded.ServiceKey)
	require.Equal(t, "standbypubkey", decoded.TargetWorkerPubKey)
	require.Equal(t, domain.ContinuityModeDegraded, decoded.TargetProfile)
	require.Equal(t, "failover:svc.api:001", decoded.IdempotencyKey)

	event.Tags = gonostr.Tags{{"service", "svc.api"}, {"target", "standbypubkey"}}
	_, err = DecodeFailoverRequestEvent(&event)
	require.ErrorContains(t, err, "d tag is required")
}

func TestRecoveryRequestSerializationUsesRecoveryKind(t *testing.T) {
	request := ContinuityCommandRequest{
		ServiceKey:         "svc.api",
		TargetWorkerPubKey: "primarypubkey",
		TargetProfile:      domain.ContinuityModeFull,
		IdempotencyKey:     "recovery:svc.api:001",
	}

	event, err := EncodeRecoveryRequestEvent(request)
	require.NoError(t, err)
	require.Equal(t, KindRecoveryRequest, event.Kind)
	require.Equal(t, "recovery", continuityTagValue(event.Tags, "command"))

	decoded, err := DecodeRecoveryRequestEvent(&event)
	require.NoError(t, err)
	require.Equal(t, domain.ContinuityModeFull, decoded.TargetProfile)
	require.Equal(t, "primarypubkey", decoded.TargetWorkerPubKey)
}
