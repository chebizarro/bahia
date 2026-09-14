package nostr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestAgentRuntimeReleaseAndBindingProjectionsPreserveProvenanceAndSeparation(t *testing.T) {
	orgID, sourceID, releaseID, serviceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := domain.AgentRuntimeSource{ID: sourceID, OrgID: orgID, Repository: "git.example/runtime/metiq", Branch: "release/2026.09", ReleaseChannel: "stable"}
	release := domain.AgentRuntimeRelease{ID: releaseID, OrgID: orgID, SourceID: sourceID, ImageRepo: "registry.example/metiq", ImageDigest: digest, VerifiedAt: time.Unix(1_800_000_000, 0).UTC(), Provenance: domain.RuntimeReleaseProvenance{Provider: "metiq-hiveci", ReleaseEventID: "release-event", WorkflowRunEventID: "run-event", ManifestDigest: digest, SBOMDigest: digest, ProvenanceDigest: digest, AttestorPubkey: "attestor"}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	if err := projector.PublishAgentRuntimeRelease(context.Background(), release, source); err != nil {
		t.Fatal(err)
	}
	previous := uuid.New()
	binding := domain.AgentServiceReleaseBinding{ID: uuid.New(), OrgID: orgID, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: releaseID, ReleaseChannel: "stable", SourceEventID: "promotion-event", PreviousBindingID: &previous}
	if err := projector.PublishAgentServiceReleaseBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("events=%d", len(sink.events))
	}
	releaseEvent := sink.events[0]
	assertTag(t, releaseEvent, "domain", "agent-runtime-release")
	assertTag(t, releaseEvent, "digest", digest)
	assertTag(t, releaseEvent, "branch", source.Branch)
	assertTag(t, releaseEvent, "release_channel", source.ReleaseChannel)
	var content map[string]any
	if err := json.Unmarshal([]byte(releaseEvent.Content), &content); err != nil {
		t.Fatal(err)
	}
	if _, exists := content["workspace_repo"]; exists {
		t.Fatal("runtime release projection conflates workspace repository")
	}
	provenance, ok := content["provenance"].(map[string]any)
	if !ok || provenance["release_event_id"] != "release-event" {
		t.Fatalf("provenance=%v", content["provenance"])
	}
	bindingEvent := sink.events[1]
	assertTag(t, bindingEvent, "domain", "agent-service-release")
	assertTag(t, bindingEvent, "agent", "agent-a")
	assertTag(t, bindingEvent, "previous_binding", previous.String())
}
