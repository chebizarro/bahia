package controlplane

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
)

func TestAppendRequestResourceTagsAddsDeployCorrelationTags(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	request := &nostr.Event{
		ID:      "request-event",
		PubKey:  "requester",
		Kind:    KindDeployRequest,
		Content: `{"service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + artifactID.String() + `"}`,
	}
	reactor := &Reactor{runs: map[string]*DeploymentRun{}}

	tags := reactor.appendRequestResourceTags(context.Background(), nostr.Tags{
		{"e", request.ID, "", "reply"},
		{"p", request.PubKey},
		{"status", "processing"},
	}, request)

	assertReactorTag(t, tags, "service", serviceID.String())
	assertReactorTag(t, tags, "environment", envID.String())
	assertReactorTag(t, tags, "artifact", artifactID.String())
}

func TestAppendRequestResourceTagsAddsTrackedRunCorrelationTags(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()
	request := &nostr.Event{ID: "request-event", PubKey: "requester", Kind: KindDeployRequest}
	reactor := &Reactor{runs: map[string]*DeploymentRun{
		request.ID: {
			ID:            runID,
			ServiceID:     serviceID,
			EnvironmentID: envID,
			ArtifactID:    artifactID,
			IntentID:      &intentID,
		},
	}}

	tags := reactor.appendRequestResourceTags(context.Background(), nostr.Tags{}, request)

	assertReactorTag(t, tags, "service", serviceID.String())
	assertReactorTag(t, tags, "environment", envID.String())
	assertReactorTag(t, tags, "artifact", artifactID.String())
	assertReactorTag(t, tags, "intent", intentID.String())
	assertReactorTag(t, tags, "run", runID.String())
}

func assertReactorTag(t *testing.T, tags nostr.Tags, key, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %v", key, value, tags)
}
