package controlplane

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

// policyHistory serves policy cp-state records the way the local event store
// does: by their t topic.
type policyHistory struct{ records []repository.NostrEventRecord }

func (h policyHistory) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return h.records, nil
}

func (h policyHistory) FindByTag(_ context.Context, name, value string, _ []int, _ int) ([]repository.NostrEventRecord, error) {
	var out []repository.NostrEventRecord
	for _, record := range h.records {
		var tags nostr.Tags
		if err := json.Unmarshal(record.Tags, &tags); err != nil {
			return nil, err
		}
		for _, tag := range tags {
			if len(tag) >= 2 && tag[0] == name && tag[1] == value {
				out = append(out, record)
				break
			}
		}
	}
	return out, nil
}

// policyStateRecord wraps PolicyRegistryRecord in the 30900 envelope the
// policy state publisher signs.
func policyStateRecord(t *testing.T, policy *domain.DeploymentPolicy, deleted bool) repository.NostrEventRecord {
	t.Helper()
	recordTags, content := PolicyRegistryRecord(policy, deleted)
	tags := nostr.Tags{
		{"d", policy.ID.String()},
		{"domain", "policy"},
		{"schema", kinds.CASControlStateSchema},
		{"legacy_kind", strconv.Itoa(nostrAdapter.KindPolicyRegistry)},
		{"deleted", strconv.FormatBool(deleted)},
		{"t", kinds.CPStateTopicPolicyRegistry},
	}
	encoded, err := json.Marshal(append(tags, recordTags...))
	require.NoError(t, err)
	return repository.NostrEventRecord{ID: uuid.NewString(), Kind: nostrAdapter.KindCASControlState, Tags: encoded, Content: content, CreatedAt: time.Now().UTC()}
}

// Security scan schedules are derived from retained policy cp-state (audit
// ), so the view must decode exactly what the policy publisher emits.
func TestSecurityPolicyViewDecodesPublishedPolicyState(t *testing.T) {
	envID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	scheduled := &domain.DeploymentPolicy{
		ID: uuid.New(), Name: "daily-osv", EnvironmentID: &envID, Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Rules: []domain.PolicyRule{
			{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"interval_seconds": 7200, "source_types": []string{"sbom"}}},
			{Type: domain.RuleMaxHighVulns, Params: map[string]any{"max": 0}},
		},
	}
	disabled := &domain.DeploymentPolicy{ID: uuid.New(), Name: "paused", Enforcement: domain.PolicyEnforcementWarn, CreatedAt: now, UpdatedAt: now}
	removed := &domain.DeploymentPolicy{ID: uuid.New(), Name: "removed", UpdatedAt: now}
	view := nostrAdapter.NewSecurityPolicyView(policyHistory{records: []repository.NostrEventRecord{
		policyStateRecord(t, scheduled, false),
		policyStateRecord(t, disabled, false),
		policyStateRecord(t, removed, true),
	}})

	policies, err := view.ListSecurityPolicies(context.Background())
	require.NoError(t, err)
	byID := map[uuid.UUID]domain.DeploymentPolicy{}
	for _, policy := range policies {
		byID[policy.ID] = policy
	}
	require.Len(t, byID, 2, "a tombstoned policy is not listed")
	require.NotContains(t, byID, removed.ID)

	got := byID[scheduled.ID]
	require.Equal(t, "daily-osv", got.Name)
	require.True(t, got.Enabled)
	require.Equal(t, domain.PolicyEnforcementBlock, got.Enforcement)
	require.Equal(t, envID, *got.EnvironmentID)
	require.True(t, got.UpdatedAt.Equal(now))
	require.Len(t, got.Rules, 2)
	require.Equal(t, domain.RuleSecurityOSVScan, got.Rules[0].Type)
	require.EqualValues(t, 7200, got.Rules[0].Params["interval_seconds"])
	require.Equal(t, []any{"sbom"}, got.Rules[0].Params["source_types"])

	require.False(t, byID[disabled.ID].Enabled)
	require.Nil(t, byID[disabled.ID].EnvironmentID, "a global policy has no environment")
}
