package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SecurityPolicyView lists deployment policies from the daemon's own retained
// policy cp-state in the local event store. Security scan schedules and
// scan-time policy evaluation are derived from it (audit B-32) instead of
// enumerating the optional SQL policy index.
type SecurityPolicyView struct{ history ProjectionHistory }

// NewSecurityPolicyView returns a view over history, which must be limited to
// the daemon's own events (see LocalEventRepository.Authored).
func NewSecurityPolicyView(history ProjectionHistory) *SecurityPolicyView {
	return &SecurityPolicyView{history: history}
}

// ListSecurityPolicies returns every live (not tombstoned) policy, enabled or
// not. A record that cannot be decoded fails the read rather than silently
// dropping a policy's schedules.
func (v *SecurityPolicyView) ListSecurityPolicies(ctx context.Context) ([]domain.DeploymentPolicy, error) {
	if v == nil || v.history == nil {
		return nil, fmt.Errorf("canonical policy local view is unavailable")
	}
	records, err := v.history.FindByTag(ctx, "t", kinds.CPStateTopicPolicyRegistry, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("policy local view reached history limit")
	}
	out := make([]domain.DeploymentPolicy, 0, len(records))
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindPolicyRegistry) || isTombstoneTags(tags) {
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
