package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
)

func TestAppendRequestResourceTagsAddsDeployCorrelationTags(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	request := &nostr.Event{
		ID:      testNostrID("request-event"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindDeployRequest,
		Content: `{"service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + artifactID.String() + `"}`,
	}
	reactor := &Reactor{runs: map[string]*DeploymentRun{}}

	tags := reactor.appendRequestResourceTags(context.Background(), nostr.Tags{
		{"e", request.ID.Hex(), "", "reply"},
		{"p", request.PubKey.Hex()},
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
	request := &nostr.Event{ID: testNostrID("request-event"), PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey), Kind: KindDeployRequest}
	reactor := &Reactor{runs: map[string]*DeploymentRun{
		request.ID.Hex(): {
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

func TestRequestSubscriptionAuthorsFailClosedForMalformedConfiguredPubkeys(t *testing.T) {
	reactor := &Reactor{config: Config{AuthorizedPubkeys: []string{"not-a-pubkey"}}}

	authors := reactor.requestSubscriptionAuthors()
	if len(authors) == 0 {
		t.Fatal("requestSubscriptionAuthors() returned no authors for malformed configured allowlist; want fail-closed sentinel")
	}
	if authors[0].Hex() != invalidSubscriptionAuthorPubKey().Hex() {
		t.Fatalf("requestSubscriptionAuthors()[0] = %s, want fail-closed sentinel %s", authors[0].Hex(), invalidSubscriptionAuthorPubKey().Hex())
	}
}

func TestRequestSubscriptionScopesContextVMToServicePubkey(t *testing.T) {
	reactor := &Reactor{config: Config{PrivateKey: testServiceKey}}

	filters := reactor.buildRequestSubscriptionFilters(42)
	if len(filters) != 1 {
		t.Fatalf("filter count = %d, want 1", len(filters))
	}
	want := testNostrPubKeyFromPrivateKey(t, testServiceKey).Hex()
	got := filters[0].Tags["p"]
	if len(got) != 1 || got[0] != want {
		t.Fatalf("#p scope = %v, want [%s]", got, want)
	}
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
