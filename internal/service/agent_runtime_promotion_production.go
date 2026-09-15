package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	runtimePromotionEnabledMetadataKey   = "runtime_promotion"
	runtimePromotionAgentMetadataKey     = "runtime_promotion_agent_id"
	runtimePromotionChannelMetadataKey   = "runtime_promotion_release_channel"
	runtimePromotionPublisherMetadataKey = "runtime_promotion_publisher"
	runtimePromotionCommitMetadataKey    = "runtime_promotion_commit"
	runtimePromotionDigestMetadataKey    = "runtime_promotion_digest"
)

type runtimePromotionPolicyRepository interface {
	ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
}

type hiveCIRuntimePromotionSubscriptions struct {
	policies     runtimePromotionPolicyRepository
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
}

func (s *hiveCIRuntimePromotionSubscriptions) ListRuntimeReleaseSubscriptions(
	ctx context.Context,
	orgID uuid.UUID,
	channel string,
) ([]RuntimePromotionSubscription, error) {
	policies, err := s.policies.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	var subscriptions []RuntimePromotionSubscription
	for _, policy := range policies {
		if !policy.Enabled || !runtimePromotionEnabled(policy.Metadata) ||
			promotionMetadataString(policy.Metadata, runtimePromotionChannelMetadataKey) != strings.TrimSpace(channel) {
			continue
		}
		svc, err := s.services.GetByID(ctx, policy.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("resolve runtime promotion service %s: %w", policy.ServiceID, err)
		}
		env, err := s.environments.GetByID(ctx, policy.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("resolve runtime promotion environment %s: %w", policy.EnvironmentID, err)
		}
		if svc == nil || env == nil || svc.OrgID != orgID || env.OrgID != orgID {
			continue
		}
		sub := RuntimePromotionSubscription{
			AgentID:        promotionMetadataString(policy.Metadata, runtimePromotionAgentMetadataKey),
			ServiceID:      policy.ServiceID,
			EnvironmentID:  policy.EnvironmentID,
			ReleaseChannel: promotionMetadataString(policy.Metadata, runtimePromotionChannelMetadataKey),
			Repository:     strings.TrimSpace(policy.RepoCoordinate),
			Branch:         strings.TrimSpace(policy.BranchPattern),
			Publisher:      promotionMetadataString(policy.Metadata, runtimePromotionPublisherMetadataKey),
			Commit:         promotionMetadataString(policy.Metadata, runtimePromotionCommitMetadataKey),
			Digest:         promotionMetadataString(policy.Metadata, runtimePromotionDigestMetadataKey),
			Protected:      env.Protected,
		}
		if sub.AgentID == "" || sub.Repository == "" || sub.Branch == "" || sub.Publisher == "" || sub.Commit == "" || sub.Digest == "" {
			return nil, fmt.Errorf("enabled runtime promotion policy %s is missing required gate metadata", policy.ID)
		}
		subscriptions = append(subscriptions, sub)
	}
	return subscriptions, nil
}

// NewProductionAgentRuntimePromotionService wires the durable runtime release
// repositories, Hive-CI promotion policies, and RegistryService's release-backed
// intent sink. It persists intent state only; publishing a live kind-25910
// command remains an explicitly gated operator action.
func NewProductionAgentRuntimePromotionService(
	releases repository.AgentRuntimeReleaseRepository,
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	policies runtimePromotionPolicyRepository,
	registry *RegistryService,
) (*AgentRuntimePromotionService, error) {
	if releases == nil || services == nil || environments == nil || policies == nil || registry == nil {
		return nil, fmt.Errorf("production runtime promotion requires release, service, environment, policy, and registry dependencies")
	}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releases, services),
		&hiveCIRuntimePromotionSubscriptions{policies: policies, services: services, environments: environments},
		registry,
	)
	svc.services = services
	return svc, nil
}

// PromoteAcceptedHiveCIRelease is the accepted-release consumer used by the
// Hive-CI subscriber. Policies without the explicit runtime_promotion gate are
// ignored. Enabled policies fail closed unless every subscription gate is set.
func (s *AgentRuntimePromotionService) PromoteAcceptedHiveCIRelease(
	ctx context.Context,
	commit domain.HiveCIReleaseCommitResult,
) (RuntimePromotionReport, error) {
	var report RuntimePromotionReport
	accepted := commit.Release
	if !runtimePromotionEnabled(accepted.Policy.Metadata) {
		return report, nil
	}
	if s == nil || s.services == nil {
		return report, fmt.Errorf("accepted Hive-CI runtime promotion consumer is not configured")
	}
	svc, err := s.services.GetByID(ctx, accepted.Policy.ServiceID)
	if err != nil {
		return report, fmt.Errorf("resolve accepted release tenant: %w", err)
	}
	if svc == nil || svc.OrgID == uuid.Nil {
		return report, fmt.Errorf("accepted release policy service is not tenant-scoped")
	}
	channel := promotionMetadataString(accepted.Policy.Metadata, runtimePromotionChannelMetadataKey)
	if channel == "" {
		return report, fmt.Errorf("enabled runtime promotion policy requires %s", runtimePromotionChannelMetadataKey)
	}
	return s.PromoteSubscribedSouls(ctx, RuntimePromotionRequest{
		Source: &domain.AgentRuntimeSource{
			OrgID:          svc.OrgID,
			Repository:     accepted.Result.Lineage.RepoAddress,
			Branch:         accepted.Branch,
			ReleaseChannel: channel,
		},
		Release: accepted,
	})
}

func runtimePromotionEnabled(metadata map[string]any) bool {
	enabled, _ := metadata[runtimePromotionEnabledMetadataKey].(bool)
	return enabled
}

func promotionMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
