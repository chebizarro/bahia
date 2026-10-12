package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
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

func TestReactorWithoutDatabaseKeepsRequestTagsWithoutSQLLookup(t *testing.T) {
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	reactor := NewReactor(Config{}, registry, nil, nil, zap.NewNop())
	if reactor.registry != nil {
		t.Fatal("partially wired SQL registry was retained by Reactor")
	}
	intentID := uuid.New()
	serviceID := uuid.New()
	request := &nostr.Event{
		ID:   testNostrID("db-less-approval"),
		Kind: KindDeploymentApproval,
		Tags: nostr.Tags{{"intent", intentID.String()}, {"service", serviceID.String()}},
	}
	tags := reactor.appendRequestResourceTags(t.Context(), nil, request)
	assertReactorTag(t, tags, "intent", intentID.String())
	assertReactorTag(t, tags, "service", serviceID.String())
}

func TestReactorWithoutDatabaseRejectsSQLBackedCommands(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureNostrPublisher{published: 1}
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	backup := service.NewBackupRegistryService(repository.NewPgBackupControlPlaneRepository(nil), nil, zap.NewNop())
	ml := service.NewMLRegistryService(nil, nil, zap.NewNop())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, registry, nil, signer, zap.NewNop(),
		WithControlPlanePublisher(capture), WithBackupRegistry(backup), WithMLRegistry(ml))
	if reactor.backupRegistry != nil || reactor.mlRegistry != nil {
		t.Fatal("SQL-backed command registries were retained without a database")
	}
	backupRequest := signedLLMRequest(t, requestKey, KindBackupRepositoryRegister,
		`{"name":"archive","backend":"kopia","repository_uri":"kopia://archive"}`, nostr.Tags{{"d", "repository:archive"}})
	reactor.handleBackupRepositoryRegisterRequest(t.Context(), backupRequest)
	mlRequest := signedLLMRequest(t, requestKey, KindMLRecipeRunRequest, `{"recipe_id":"`+uuid.NewString()+`"}`, nostr.Tags{{"d", "recipe-run:test"}})
	reactor.handleMLRecipeRunRequest(t.Context(), mlRequest)
	if len(capture.events) != 2 {
		t.Fatalf("published results = %d, want two capability failures", len(capture.events))
	}
	assertReactorTag(t, capture.events[0].Tags, "status", "failed")
	assertReactorTag(t, capture.events[0].Tags, "result", "backup_repository_register_unavailable")
	assertReactorTag(t, capture.events[1].Tags, "status", "failed")
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

func TestRequestSubscriptionDoesNotUseContextVMRecipientTag(t *testing.T) {
	reactor := &Reactor{}

	filters := reactor.buildRequestSubscriptionFilters(42)
	if len(filters) != 1 {
		t.Fatalf("filter count = %d, want 1", len(filters))
	}
	got := filters[0].Tags["p"]
	if len(got) != 0 {
		t.Fatalf("#p scope = %v, want no ContextVM recipient tag", got)
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
