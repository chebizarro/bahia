package nostr

import (
	"context"
	"encoding/json"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const agentRuntimeReleaseProjectionSchema = "bahia.agent-runtime-release.v1"

// PublishAgentRuntimeRelease projects immutable verified provenance. Consumers
// can safely reuse this release across agents without inventing per-agent build evidence.
func (p *Projector) PublishAgentRuntimeRelease(ctx context.Context, release domain.AgentRuntimeRelease, source domain.AgentRuntimeSource) error {
	content, err := json.Marshal(map[string]any{
		"schema": agentRuntimeReleaseProjectionSchema, "id": release.ID.String(),
		"org_id": release.OrgID.String(), "source_id": release.SourceID.String(),
		"runtime_source": map[string]any{"repository": source.Repository, "branch": source.Branch, "release_channel": source.ReleaseChannel},
		"image_repo":     release.ImageRepo, "image_digest": release.ImageDigest,
		"provenance": release.Provenance, "verified_at": formatTime(release.VerifiedAt), "created_at": formatTime(release.CreatedAt),
	})
	if err != nil {
		return err
	}
	tags := gonostr.Tags{
		{kinds.CASControlStateTagD, "runtime-release:" + release.ID.String()},
		{kinds.CASControlStateTagDomain, "agent-runtime-release"},
		{kinds.CASControlStateTagSchema, agentRuntimeReleaseProjectionSchema},
		{"org", release.OrgID.String()}, {"source", release.SourceID.String()},
		{"digest", release.ImageDigest}, {"branch", source.Branch}, {"release_channel", source.ReleaseChannel},
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(content), "agent_runtime_release.projection", &release.ID)
}

// PublishAgentServiceReleaseBinding projects one append-only many-to-many binding.
func (p *Projector) PublishAgentServiceReleaseBinding(ctx context.Context, binding domain.AgentServiceReleaseBinding) error {
	content, err := json.Marshal(map[string]any{
		"schema": agentRuntimeReleaseProjectionSchema, "id": binding.ID.String(),
		"org_id": binding.OrgID.String(), "agent_id": binding.AgentID,
		"service_id": binding.ServiceID.String(), "release_id": binding.ReleaseID.String(),
		"release_channel": binding.ReleaseChannel, "source_event_id": binding.SourceEventID,
		"previous_binding_id": uuidStringPtr(binding.PreviousBindingID), "created_at": formatTime(binding.CreatedAt),
	})
	if err != nil {
		return err
	}
	tags := gonostr.Tags{
		{kinds.CASControlStateTagD, "agent-service-release:" + binding.ID.String()},
		{kinds.CASControlStateTagDomain, "agent-service-release"},
		{kinds.CASControlStateTagSchema, agentRuntimeReleaseProjectionSchema},
		{"org", binding.OrgID.String()}, {"agent", binding.AgentID}, {"service", binding.ServiceID.String()},
		{"release", binding.ReleaseID.String()}, {"release_channel", binding.ReleaseChannel}, {"source_event", binding.SourceEventID},
	}
	if binding.PreviousBindingID != nil {
		tags = append(tags, gonostr.Tag{"previous_binding", binding.PreviousBindingID.String()})
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(content), "agent_service_release.projection", &binding.ID)
}
