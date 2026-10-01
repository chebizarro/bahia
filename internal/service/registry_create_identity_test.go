package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// bahia-irsry.35: entity ids are client-minted and fixed at creation.

func newIdentityTestRegistry(t *testing.T) (*RelayFirstRegistry, *relayFirstServiceRepo, *relayFirstCapturePublisher) {
	t.Helper()
	serviceRepo := &relayFirstServiceRepo{}
	delegate := NewRegistryService(serviceRepo, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{published: 1}
	return NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop()), serviceRepo, publisher
}

func relayFirstEventTag(ev gonostr.Event, name string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func relayFirstEventContentID(t *testing.T, ev gonostr.Event) string {
	t.Helper()
	var content struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode event content: %v", err)
	}
	return content.ID
}

func TestRelayFirstCreateServiceClientIDRoundTripsToRelayCoordinate(t *testing.T) {
	ctx := context.Background()
	registry, repo, publisher := newIdentityTestRegistry(t)
	clientID, err := domain.ParseClientEntityID(domain.NewEntityID().String())
	if err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: clientID, OrgID: uuid.New(), Name: "api", ArtifactRepo: "ghcr.io/acme/api"}

	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if svc.ID != clientID {
		t.Fatalf("service id = %s, want client id %s", svc.ID, clientID)
	}
	if repo.services[clientID] == nil {
		t.Fatal("service was not stored under the client-supplied id")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published %d events, want 1", len(publisher.events))
	}
	ev := publisher.events[0]
	d := relayFirstEventTag(ev, "d")
	if d != clientID.String() || relayFirstEventContentID(t, ev) != clientID.String() {
		t.Fatalf("relay coordinate d=%q content id=%q, want %s", d, relayFirstEventContentID(t, ev), clientID)
	}
}

func TestRelayFirstCreateServiceRetryWithSameIDAndContentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	registry, repo, publisher := newIdentityTestRegistry(t)
	id := domain.NewEntityID()
	org := uuid.New()
	intent := func() *domain.Service {
		return &domain.Service{ID: id, OrgID: org, Name: "api", RepoURL: " https://git.example/api ", ArtifactRepo: "ghcr.io/acme/api"}
	}

	if err := registry.CreateService(ctx, intent()); err != nil {
		t.Fatalf("first CreateService: %v", err)
	}
	stored := repo.services[id]
	retry := intent()
	if err := registry.CreateService(ctx, retry); err != nil {
		t.Fatalf("retried CreateService: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("retry published again: %d events, want 1", len(publisher.events))
	}
	if len(repo.services) != 1 || repo.services[id] != stored {
		t.Fatalf("retry wrote the repository again: %d services", len(repo.services))
	}
	if retry.ID != id || retry.Name != "api" || retry.RepoURL != "https://git.example/api" {
		t.Fatalf("retry did not return the stored service: %#v", retry)
	}
}

func TestRelayFirstCreateServiceSameIDDifferentContentConflicts(t *testing.T) {
	ctx := context.Background()
	registry, repo, publisher := newIdentityTestRegistry(t)
	id := domain.NewEntityID()
	org := uuid.New()
	if err := registry.CreateService(ctx, &domain.Service{ID: id, OrgID: org, Name: "api", ArtifactRepo: "ghcr.io/acme/api"}); err != nil {
		t.Fatalf("first CreateService: %v", err)
	}

	for name, conflicting := range map[string]*domain.Service{
		"different artifact": {ID: id, OrgID: org, Name: "api", ArtifactRepo: "ghcr.io/acme/other"},
		"different org":      {ID: id, OrgID: uuid.New(), Name: "api", ArtifactRepo: "ghcr.io/acme/api"},
	} {
		err := registry.CreateService(ctx, conflicting)
		var conflict *domain.EntityIDConflictError
		if !errors.As(err, &conflict) || !errors.Is(err, domain.ErrEntityIDConflict) || conflict.ID != id || conflict.Entity != "service" {
			t.Fatalf("%s: error = %v, want EntityIDConflictError for %s", name, err, id)
		}
	}
	if len(publisher.events) != 1 {
		t.Fatalf("conflicting create overwrote the relay coordinate: %d events", len(publisher.events))
	}
	if repo.services[id].ArtifactRepo != "ghcr.io/acme/api" {
		t.Fatalf("conflicting create changed the stored service: %#v", repo.services[id])
	}
}

func TestRelayFirstCreateServiceMintsUUIDv7WhenIDAbsent(t *testing.T) {
	ctx := context.Background()
	registry, repo, publisher := newIdentityTestRegistry(t)
	svc := &domain.Service{Name: "api", ArtifactRepo: "ghcr.io/acme/api"}

	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if svc.ID == uuid.Nil || svc.ID.Version() != 7 {
		t.Fatalf("minted id = %s (v%d), want a UUIDv7", svc.ID, svc.ID.Version())
	}
	if d := relayFirstEventTag(publisher.events[0], "d"); d != svc.ID.String() {
		t.Fatalf("published d = %q before the id was minted, want %s", d, svc.ID)
	}
	if repo.services[svc.ID] == nil {
		t.Fatal("minted service was not stored under its published id")
	}
}

func TestRelayFirstCreateServiceKeepsLegacyUUIDv4Coordinate(t *testing.T) {
	ctx := context.Background()
	registry, _, publisher := newIdentityTestRegistry(t)
	legacy := uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301")

	if err := registry.CreateService(ctx, &domain.Service{ID: legacy, Name: "api", ArtifactRepo: "ghcr.io/acme/api"}); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	d := relayFirstEventTag(publisher.events[0], "d")
	if d != legacy.String() {
		t.Fatalf("legacy coordinate d = %q, want %s", d, legacy)
	}
}

func TestRegistryCreateServiceResolvesConcurrentInsertOfSameID(t *testing.T) {
	ctx := context.Background()
	id := domain.NewEntityID()
	org := uuid.New()
	repo := &racingServiceRepo{winner: domain.Service{ID: id, OrgID: org, Name: "api", ArtifactRepo: "ghcr.io/acme/api", RuntimeType: domain.RuntimeTypeDocker, DefaultBranch: "main"}}
	registry := NewRegistryService(repo, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())

	if err := registry.CreateService(ctx, &domain.Service{ID: id, OrgID: org, Name: "api", ArtifactRepo: "ghcr.io/acme/api"}); err != nil {
		t.Fatalf("same-content create that lost the insert race = %v, want idempotent success", err)
	}
	repo.services = nil
	err := registry.CreateService(ctx, &domain.Service{ID: id, OrgID: org, Name: "api", ArtifactRepo: "ghcr.io/acme/other"})
	if !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("different-content create that lost the insert race = %v, want ErrEntityIDConflict", err)
	}
}

// racingServiceRepo simulates a concurrent writer that inserts the same id
// between the pre-create lookup and this create's insert.
type racingServiceRepo struct {
	relayFirstServiceRepo
	winner domain.Service
}

func (r *racingServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.ensure()
	winner := r.winner
	r.services[winner.ID] = &winner
	return repository.ErrAlreadyExists
}

func TestRelayFirstCreateEnvironmentClientIDIsIdempotentAndConflictsOnDifferentContent(t *testing.T) {
	ctx := context.Background()
	envRepo := &relayFirstEnvironmentRepo{}
	delegate := NewRegistryService(nil, envRepo, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{published: 1}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())
	id := domain.NewEntityID()
	org := uuid.New()
	intent := func() *domain.Environment {
		return &domain.Environment{ID: id, OrgID: org, Name: " prod ", RuntimeConfig: map[string]any{"type": "docker", "replicas": 2}}
	}

	if err := registry.CreateEnvironment(ctx, intent()); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if d := relayFirstEventTag(publisher.events[0], "d"); d != id.String() {
		t.Fatalf("environment coordinate d = %q, want client id %s", d, id)
	}
	if err := registry.CreateEnvironment(ctx, intent()); err != nil {
		t.Fatalf("retried CreateEnvironment: %v", err)
	}
	if len(publisher.events) != 1 || len(envRepo.environments) != 1 {
		t.Fatalf("retry was not idempotent: events=%d stored=%d", len(publisher.events), len(envRepo.environments))
	}
	conflicting := intent()
	conflicting.Protected = true
	if err := registry.CreateEnvironment(ctx, conflicting); !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("conflicting CreateEnvironment error = %v, want ErrEntityIDConflict", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("conflicting create published: %d events", len(publisher.events))
	}

	minted := &domain.Environment{Name: "staging"}
	if err := registry.CreateEnvironment(ctx, minted); err != nil {
		t.Fatalf("CreateEnvironment without id: %v", err)
	}
	if minted.ID.Version() != 7 || relayFirstEventTag(publisher.events[1], "d") != minted.ID.String() {
		t.Fatalf("absent environment id was not minted before publish: id=%s d=%q", minted.ID, relayFirstEventTag(publisher.events[1], "d"))
	}
}

func TestRelayFirstCreateEnvironmentWithUnitsReplayReturnsStoredUnits(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	delegate := newEnvironmentMutationRegistry(envs, units, &capturePublisher{})
	publisher := &relayFirstCapturePublisher{published: 1}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())
	id := domain.NewEntityID()
	intent := func(composeDir string) (*domain.Environment, []*domain.DeploymentUnit) {
		return &domain.Environment{
				ID:        id,
				Name:      "max",
				Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "max", DefaultReconcileMode: domain.ReconcileModeAutoApply},
			}, []*domain.DeploymentUnit{{
				ID:            uuid.New(), // handlers mint a fresh unit id per request
				Key:           "max",
				RuntimeType:   domain.RuntimeTypeCompose,
				EndpointRef:   "max",
				ComposeDir:    composeDir,
				OwnershipMode: domain.OwnershipModeBahiaManaged,
			}}
	}

	env, requested := intent("/srv/bahia/gastown")
	if err := registry.CreateEnvironmentWithDeploymentUnits(ctx, env, requested); err != nil {
		t.Fatalf("CreateEnvironmentWithDeploymentUnits: %v", err)
	}
	originalUnitID := requested[0].ID

	retryEnv, retryUnits := intent("/srv/bahia/gastown")
	if err := registry.CreateEnvironmentWithDeploymentUnits(ctx, retryEnv, retryUnits); err != nil {
		t.Fatalf("retried create: %v", err)
	}
	if retryUnits[0].ID != originalUnitID || len(publisher.events) != 1 {
		t.Fatalf("retry returned unit %s (want stored %s), events=%d", retryUnits[0].ID, originalUnitID, len(publisher.events))
	}

	conflictEnv, conflictUnits := intent("/srv/bahia/other")
	if err := registry.CreateEnvironmentWithDeploymentUnits(ctx, conflictEnv, conflictUnits); !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("different unit set error = %v, want ErrEntityIDConflict", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("conflicting create published: %d events", len(publisher.events))
	}
}

func TestRelayFirstConcurrentCreatesOfOneIDPublishOnce(t *testing.T) {
	ctx := context.Background()
	registry, repo, publisher := newIdentityTestRegistry(t)
	id := domain.NewEntityID()
	org := uuid.New()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = registry.CreateService(ctx, &domain.Service{ID: id, OrgID: org, Name: "api", ArtifactRepo: fmt.Sprintf("ghcr.io/acme/api-%d", i%2)})
		}(i)
	}
	wg.Wait()
	if len(publisher.events) != 1 || len(repo.services) != 1 {
		t.Fatalf("concurrent creates of one id: events=%d stored=%d, want 1/1", len(publisher.events), len(repo.services))
	}
	for i, err := range errs {
		sameAsWinner := fmt.Sprintf("ghcr.io/acme/api-%d", i%2) == repo.services[id].ArtifactRepo
		if sameAsWinner && err != nil {
			t.Fatalf("create %d with the winning content failed: %v", i, err)
		}
		if !sameAsWinner && !errors.Is(err, domain.ErrEntityIDConflict) {
			t.Fatalf("create %d with different content = %v, want ErrEntityIDConflict", i, err)
		}
	}
}
