package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestInMemoryContinuityDefinitionStoreProfileLatestValueRules(t *testing.T) {
	store := NewInMemoryContinuityDefinitionStore()
	base := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)

	stored, err := store.StoreProfile(domain.ServiceContinuityProfile{
		ServiceKey:          " svc-api ",
		PrimaryWorkerPubKey: "primary-a",
		UpdatedAt:           base,
		SourceEventID:       "event-b",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {Requires: []string{"db"}},
		},
	})
	require.NoError(t, err)
	require.True(t, stored)

	stored, err = store.StoreProfile(domain.ServiceContinuityProfile{
		ServiceKey:          "svc-api",
		PrimaryWorkerPubKey: "older-primary",
		UpdatedAt:           base.Add(-time.Second),
		SourceEventID:       "event-z",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {},
		},
	})
	require.NoError(t, err)
	require.False(t, stored)

	profile, ok := store.GetProfile("svc-api")
	require.True(t, ok)
	require.Equal(t, "primary-a", profile.PrimaryWorkerPubKey)

	stored, err = store.StoreProfile(domain.ServiceContinuityProfile{
		ServiceKey:          "svc-api",
		PrimaryWorkerPubKey: "tie-break-primary",
		UpdatedAt:           base,
		SourceEventID:       "event-c",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {Attributes: map[string]string{"tier": "full"}},
		},
	})
	require.NoError(t, err)
	require.True(t, stored)

	profile, ok = store.GetProfile("svc-api")
	require.True(t, ok)
	require.Equal(t, "tie-break-primary", profile.PrimaryWorkerPubKey)

	profile.Profiles[domain.ContinuityModeFull] = domain.ContinuityProfileSpec{Requires: []string{"mutated"}}
	again, ok := store.GetProfile("svc-api")
	require.True(t, ok)
	require.Equal(t, map[string]string{"tier": "full"}, again.Profiles[domain.ContinuityModeFull].Attributes)
}

func TestInMemoryContinuityDefinitionStoreListsProfilesDeterministically(t *testing.T) {
	store := NewInMemoryContinuityDefinitionStore()
	updatedAt := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)

	_, err := store.StoreProfile(validProfile("svc-z", "primary-z", updatedAt, "event-z"))
	require.NoError(t, err)
	_, err = store.StoreProfile(validProfile("svc-a", "primary-a", updatedAt, "event-a"))
	require.NoError(t, err)

	profiles := store.ListProfiles()
	require.Len(t, profiles, 2)
	require.Equal(t, "svc-a", profiles[0].ServiceKey)
	require.Equal(t, "svc-z", profiles[1].ServiceKey)
}

func TestInMemoryContinuityDefinitionStoreReplicationPolicyLatestValueRules(t *testing.T) {
	store := NewInMemoryContinuityDefinitionStore()
	base := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)

	stored, err := store.StoreReplicationPolicy(domain.ReplicationPolicy{
		ServiceKey:    " svc-api ",
		UpdatedAt:     base,
		SourceEventID: "event-a",
		Targets: []domain.ReplicationTarget{
			{WorkerPubKey: " standby-a ", Strategy: "event_mirror", RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}},
		},
	})
	require.NoError(t, err)
	require.True(t, stored)

	stored, err = store.StoreReplicationPolicy(domain.ReplicationPolicy{
		ServiceKey:    "svc-api",
		UpdatedAt:     base.Add(-time.Second),
		SourceEventID: "event-z",
		Targets:       []domain.ReplicationTarget{{WorkerPubKey: "standby-older"}},
	})
	require.NoError(t, err)
	require.False(t, stored)

	policy, ok := store.GetReplicationPolicy("svc-api")
	require.True(t, ok)
	require.Equal(t, "standby-a", policy.Targets[0].WorkerPubKey)

	policy.Targets[0].WorkerPubKey = "mutated"
	again, ok := store.GetReplicationPolicy("svc-api")
	require.True(t, ok)
	require.Equal(t, "standby-a", again.Targets[0].WorkerPubKey)
}

func TestInMemoryContinuityDefinitionStoreRecipeLatestValueRulesAndList(t *testing.T) {
	store := NewInMemoryContinuityDefinitionStore()
	base := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)

	stored, err := store.StoreRecipe(validFailoverRecipe("svc-api", "failover-a", base, "event-a"))
	require.NoError(t, err)
	require.True(t, stored)

	older := validFailoverRecipe("svc-api", "failover-older", base.Add(-time.Second), "event-z")
	stored, err = store.StoreRecipe(older)
	require.NoError(t, err)
	require.False(t, stored)

	recovery := domain.ContinuityRecipe{
		Name:          "recovery-a",
		ServiceKey:    "svc-api",
		Kind:          domain.ContinuityRecipeKindRecovery,
		UpdatedAt:     base,
		SourceEventID: "event-r",
		Steps:         []domain.RecipeStep{{Name: "restore dns", Action: domain.RecipeActionRestoreDNSRoutes}},
	}
	stored, err = store.StoreRecipe(recovery)
	require.NoError(t, err)
	require.True(t, stored)

	recipe, ok := store.GetRecipe("svc-api", domain.ContinuityRecipeKindFailover)
	require.True(t, ok)
	require.Equal(t, "failover-a", recipe.Name)
	recipe.Steps[0].Params["worker"] = "mutated"

	again, ok := store.GetRecipe("svc-api", domain.ContinuityRecipeKindFailover)
	require.True(t, ok)
	require.Equal(t, "standby-a", again.Steps[0].Params["worker"])

	recipes := store.ListRecipesForService("svc-api")
	require.Len(t, recipes, 2)
	require.Equal(t, domain.ContinuityRecipeKindFailover, recipes[0].Kind)
	require.Equal(t, domain.ContinuityRecipeKindRecovery, recipes[1].Kind)
}

func TestInMemoryContinuityDefinitionStoreRejectsInvalidDefinitions(t *testing.T) {
	store := NewInMemoryContinuityDefinitionStore()

	_, err := store.StoreProfile(domain.ServiceContinuityProfile{})
	require.Error(t, err)

	_, err = store.StoreReplicationPolicy(domain.ReplicationPolicy{})
	require.Error(t, err)

	_, err = store.StoreRecipe(domain.ContinuityRecipe{})
	require.Error(t, err)
}

func validProfile(serviceKey string, primary string, updatedAt time.Time, eventID string) domain.ServiceContinuityProfile {
	return domain.ServiceContinuityProfile{
		ServiceKey:          serviceKey,
		PrimaryWorkerPubKey: primary,
		UpdatedAt:           updatedAt,
		SourceEventID:       eventID,
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {},
		},
	}
}

func validFailoverRecipe(serviceKey string, name string, updatedAt time.Time, eventID string) domain.ContinuityRecipe {
	return domain.ContinuityRecipe{
		Name:       name,
		ServiceKey: serviceKey,
		Kind:       domain.ContinuityRecipeKindFailover,
		Trigger: &domain.RecipeTrigger{
			Type:    domain.RecipeTriggerTypeHeartbeatLoss,
			Target:  "primary-a",
			Timeout: 30 * time.Second,
		},
		UpdatedAt:     updatedAt,
		SourceEventID: eventID,
		Steps: []domain.RecipeStep{
			{Name: "move service", Action: domain.RecipeActionMoveService, Params: map[string]string{"worker": "standby-a"}},
		},
	}
}
