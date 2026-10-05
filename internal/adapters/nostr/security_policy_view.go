package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SecurityPolicyView derives schedules from retained, service-authored policy
// cp-state rather than enumerating the optional SQL policy index.
type SecurityPolicyView struct{ history ProjectionHistory }

func NewSecurityPolicyView(history ProjectionHistory) *SecurityPolicyView {
	return &SecurityPolicyView{history: history}
}
func (v *SecurityPolicyView) ListSecurityPolicies(ctx context.Context) ([]domain.DeploymentPolicy, error) {
	if v == nil || v.history == nil {
		return nil, fmt.Errorf("canonical policy local view is unavailable")
	}
	const limit = 1000000
	records, err := v.history.FindByTag(ctx, "t", kinds.CPStateTopicPolicyRegistry, []int{KindCASControlState}, limit)
	if err != nil {
		return nil, err
	}
	if len(records) >= limit {
		return nil, fmt.Errorf("policy local view reached history limit")
	}
	out := make([]domain.DeploymentPolicy, 0, len(records))
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindPolicyRegistry) || tagValue(tags, "deleted") == "true" {
			continue
		}
		var policy domain.DeploymentPolicy
		if err := json.Unmarshal([]byte(record.Content), &policy); err != nil {
			return nil, fmt.Errorf("decode policy %s: %w", record.ID, err)
		}
		if policy.ID.String() != tagValue(tags, "d") {
			return nil, fmt.Errorf("policy %s coordinate mismatch", record.ID)
		}
		out = append(out, policy)
	}
	return out, nil
}
