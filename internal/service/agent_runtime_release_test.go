package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type memoryAgentReleaseRepo struct {
	sources  map[uuid.UUID]domain.AgentRuntimeSource
	releases map[uuid.UUID]domain.AgentRuntimeRelease
	bindings []domain.AgentServiceReleaseBinding
}

func newMemoryAgentReleaseRepo() *memoryAgentReleaseRepo {
	return &memoryAgentReleaseRepo{sources: map[uuid.UUID]domain.AgentRuntimeSource{}, releases: map[uuid.UUID]domain.AgentRuntimeRelease{}}
}
func (m *memoryAgentReleaseRepo) CreateSource(_ context.Context, v *domain.AgentRuntimeSource) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	v.CreatedAt = time.Now().UTC()
	m.sources[v.ID] = *v
	return nil
}
func (m *memoryAgentReleaseRepo) GetSource(_ context.Context, org, id uuid.UUID) (*domain.AgentRuntimeSource, error) {
	v, ok := m.sources[id]
	if !ok || v.OrgID != org {
		return nil, nil
	}
	return &v, nil
}
func (m *memoryAgentReleaseRepo) CreateRelease(_ context.Context, v *domain.AgentRuntimeRelease) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	v.CreatedAt = time.Now().UTC()
	m.releases[v.ID] = *v
	return nil
}
func (m *memoryAgentReleaseRepo) GetRelease(_ context.Context, org, id uuid.UUID) (*domain.AgentRuntimeRelease, error) {
	v, ok := m.releases[id]
	if !ok || v.OrgID != org {
		return nil, nil
	}
	return &v, nil
}
func (m *memoryAgentReleaseRepo) GetReleaseByDigest(_ context.Context, org uuid.UUID, repo, digest string) (*domain.AgentRuntimeRelease, error) {
	for _, v := range m.releases {
		if v.OrgID == org && v.ImageRepo == repo && v.ImageDigest == digest {
			x := v
			return &x, nil
		}
	}
	return nil, nil
}
func (m *memoryAgentReleaseRepo) BindRelease(_ context.Context, v *domain.AgentServiceReleaseBinding) error {
	// Event-based idempotency: same source_event_id -> return the existing binding.
	for i := range m.bindings {
		b := m.bindings[i]
		if b.OrgID == v.OrgID && b.SourceEventID == v.SourceEventID {
			*v = b
			return nil
		}
	}
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	// Compute previous_binding_id as the current chain head, so A->B->A produces
	// three distinct bindings and rollback resolves to the exact prior release.
	for i := len(m.bindings) - 1; i >= 0; i-- {
		b := m.bindings[i]
		if b.OrgID == v.OrgID && b.AgentID == v.AgentID && b.ServiceID == v.ServiceID && b.ReleaseChannel == v.ReleaseChannel {
			id := b.ID
			v.PreviousBindingID = &id
			break
		}
	}
	v.CreatedAt = time.Now().UTC()
	m.bindings = append(m.bindings, *v)
	return nil
}
func (m *memoryAgentReleaseRepo) ListServiceReleases(_ context.Context, org, service uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error) {
	var out []domain.AgentServiceRuntimeRelease
	for _, b := range m.bindings {
		if b.OrgID == org && b.ServiceID == service {
			out = append(out, domain.AgentServiceRuntimeRelease{Binding: b, Release: m.releases[b.ReleaseID]})
		}
	}
	return out, nil
}
func (m *memoryAgentReleaseRepo) GetRollbackRelease(_ context.Context, org uuid.UUID, agent string, service uuid.UUID, channel string) (*domain.AgentServiceRuntimeRelease, error) {
	var current *domain.AgentServiceReleaseBinding
	for i := range m.bindings {
		b := &m.bindings[i]
		if b.OrgID == org && b.AgentID == agent && b.ServiceID == service && b.ReleaseChannel == channel {
			current = b
		}
	}
	if current == nil || current.PreviousBindingID == nil {
		return nil, nil
	}
	for _, b := range m.bindings {
		if b.ID == *current.PreviousBindingID {
			return &domain.AgentServiceRuntimeRelease{Binding: b, Release: m.releases[b.ReleaseID]}, nil
		}
	}
	return nil, nil
}

type memoryServiceRepo struct{ values map[uuid.UUID]domain.Service }

func (m memoryServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (m memoryServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	v, ok := m.values[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (m memoryServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (m memoryServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (m memoryServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (m memoryServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (m memoryServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

func verifiedRuntimeRelease(org, source uuid.UUID, digit string) domain.AgentRuntimeRelease {
	digest := "sha256:"
	for i := 0; i < 64; i++ {
		digest += digit
	}
	return domain.AgentRuntimeRelease{OrgID: org, SourceID: source, ImageRepo: "registry.example/metiq", ImageDigest: digest, VerifiedAt: time.Unix(1_800_000_000, 0).UTC(), Provenance: domain.RuntimeReleaseProvenance{Provider: "metiq-hiveci", ReleaseEventID: "release-" + digit, WorkflowRunEventID: "run-" + digit, ManifestDigest: digest, SBOMDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProvenanceDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AttestorPubkey: "attestor"}}
}

func TestAgentRuntimeReleaseSharedPromotionDedupeTenancyAndRollback(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	otherOrg := uuid.New()
	serviceID := uuid.New()
	sourceID := uuid.New()
	repo := newMemoryAgentReleaseRepo()
	repo.sources[sourceID] = domain.AgentRuntimeSource{ID: sourceID, OrgID: org, ReleaseChannel: "stable"}
	svc := NewAgentRuntimeReleaseService(repo, memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: org}}})
	r1 := verifiedRuntimeRelease(org, sourceID, "1")
	if err := svc.RegisterVerifiedRelease(ctx, &r1); err != nil {
		t.Fatal(err)
	}
	replay := verifiedRuntimeRelease(org, sourceID, "1")
	if err := svc.RegisterVerifiedRelease(ctx, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != r1.ID || len(repo.releases) != 1 {
		t.Fatalf("digest replay duplicated release: first=%s replay=%s count=%d", r1.ID, replay.ID, len(repo.releases))
	}
	for _, agent := range []string{"agent-a", "agent-b"} {
		b := domain.AgentServiceReleaseBinding{OrgID: org, AgentID: agent, ServiceID: serviceID, ReleaseID: r1.ID, ReleaseChannel: "stable", SourceEventID: "bind-" + agent}
		if err := svc.BindRelease(ctx, &b); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.bindings) != 2 {
		t.Fatalf("shared release bindings=%d, want 2", len(repo.bindings))
	}
	crossTenant := domain.AgentServiceReleaseBinding{OrgID: otherOrg, AgentID: "agent-x", ServiceID: serviceID, ReleaseID: r1.ID, ReleaseChannel: "stable", SourceEventID: "cross"}
	if err := svc.BindRelease(ctx, &crossTenant); err == nil {
		t.Fatal("cross-tenant binding accepted")
	}
	r2 := verifiedRuntimeRelease(org, sourceID, "2")
	if err := svc.RegisterVerifiedRelease(ctx, &r2); err != nil {
		t.Fatal(err)
	}
	b2 := domain.AgentServiceReleaseBinding{OrgID: org, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: r2.ID, ReleaseChannel: "stable", SourceEventID: "bind-agent-a-v2"}
	if err := svc.BindRelease(ctx, &b2); err != nil {
		t.Fatal(err)
	}
	rollback, err := svc.GetRollbackRelease(ctx, org, "agent-a", serviceID, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if rollback == nil || rollback.Release.ID != r1.ID {
		t.Fatalf("rollback=%+v, want release %s", rollback, r1.ID)
	}
}

func TestAgentRuntimeReleaseRejectsConflictingProvenance(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	source := uuid.New()
	repo := newMemoryAgentReleaseRepo()
	repo.sources[source] = domain.AgentRuntimeSource{ID: source, OrgID: org, ReleaseChannel: "stable"}
	svc := NewAgentRuntimeReleaseService(repo, memoryServiceRepo{})
	r := verifiedRuntimeRelease(org, source, "3")
	if err := svc.RegisterVerifiedRelease(ctx, &r); err != nil {
		t.Fatal(err)
	}
	conflict := verifiedRuntimeRelease(org, source, "3")
	conflict.Provenance.ReleaseEventID = "different"
	if err := svc.RegisterVerifiedRelease(ctx, &conflict); !errors.Is(err, ErrRuntimeReleaseConflict) {
		t.Fatalf("error=%v", err)
	}
}

// TestAgentRuntimeReleaseBindReleaseAppendsOnAToBToAPromotion pins the
// append-only promotion semantics for A->B->A: three real promotion events
// must produce three durable bindings, with the latest re-binding A resolving
// back to the intermediate B via rollback.
func TestAgentRuntimeReleaseBindReleaseAppendsOnAToBToAPromotion(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	serviceID := uuid.New()
	sourceID := uuid.New()
	repo := newMemoryAgentReleaseRepo()
	repo.sources[sourceID] = domain.AgentRuntimeSource{ID: sourceID, OrgID: org, ReleaseChannel: "stable"}
	svc := NewAgentRuntimeReleaseService(repo, memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: org}}})

	rA := verifiedRuntimeRelease(org, sourceID, "1")
	rB := verifiedRuntimeRelease(org, sourceID, "2")
	if err := svc.RegisterVerifiedRelease(ctx, &rA); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterVerifiedRelease(ctx, &rB); err != nil {
		t.Fatal(err)
	}

	bind := func(releaseID uuid.UUID, event string) domain.AgentServiceReleaseBinding {
		b := domain.AgentServiceReleaseBinding{OrgID: org, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: releaseID, ReleaseChannel: "stable", SourceEventID: event}
		if err := svc.BindRelease(ctx, &b); err != nil {
			t.Fatalf("BindRelease(%s) error = %v", event, err)
		}
		return b
	}
	a1 := bind(rA.ID, "promo-a1")
	b1 := bind(rB.ID, "promo-b1")
	a2 := bind(rA.ID, "promo-a2")

	if a1.ID == a2.ID {
		t.Fatalf("second A promotion collapsed onto first A: a1=%s a2=%s", a1.ID, a2.ID)
	}
	if a2.PreviousBindingID == nil || *a2.PreviousBindingID != b1.ID {
		t.Fatalf("A->B->A: latest A previous_binding_id=%v, want %s", a2.PreviousBindingID, b1.ID)
	}
	if len(repo.bindings) != 3 {
		t.Fatalf("bindings=%d, want 3 after A->B->A", len(repo.bindings))
	}

	list, err := svc.ListServiceReleases(ctx, org, serviceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("ListServiceReleases=%d, want 3", len(list))
	}
	rollback, err := svc.GetRollbackRelease(ctx, org, "agent-a", serviceID, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if rollback == nil || rollback.Binding.ID != b1.ID || rollback.Release.ID != rB.ID {
		t.Fatalf("rollback binding/release = %+v, want binding %s release %s", rollback, b1.ID, rB.ID)
	}
}

// TestAgentRuntimeReleaseBindReleaseSameSourceEventIsIdempotent pins the
// event-based idempotency contract: replaying the same source_event_id must
// return the stored binding and never append.
func TestAgentRuntimeReleaseBindReleaseSameSourceEventIsIdempotent(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	serviceID := uuid.New()
	sourceID := uuid.New()
	repo := newMemoryAgentReleaseRepo()
	repo.sources[sourceID] = domain.AgentRuntimeSource{ID: sourceID, OrgID: org, ReleaseChannel: "stable"}
	svc := NewAgentRuntimeReleaseService(repo, memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: org}}})
	release := verifiedRuntimeRelease(org, sourceID, "1")
	if err := svc.RegisterVerifiedRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}

	first := domain.AgentServiceReleaseBinding{OrgID: org, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: release.ID, ReleaseChannel: "stable", SourceEventID: "promo"}
	if err := svc.BindRelease(ctx, &first); err != nil {
		t.Fatal(err)
	}
	replay := domain.AgentServiceReleaseBinding{OrgID: org, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: release.ID, ReleaseChannel: "stable", SourceEventID: "promo"}
	if err := svc.BindRelease(ctx, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay ID = %s, want stable %s", replay.ID, first.ID)
	}
	if len(repo.bindings) != 1 {
		t.Fatalf("bindings=%d, want 1 for same source_event_id replay", len(repo.bindings))
	}
}
