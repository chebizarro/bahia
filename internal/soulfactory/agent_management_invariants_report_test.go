package soulfactory

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// The production registry must satisfy the reporter's read-only service seam,
// so the operator view plugs into real canonical state rather than a bespoke
// parallel reader. This is a compile-time proof.
var _ ManagedServiceLister = (*service.RegistryService)(nil)

type fakeSoulLister struct {
	souls []ManagedSoulRecord
	err   error
	calls int
}

func (f *fakeSoulLister) ListManagedSouls(context.Context) ([]ManagedSoulRecord, error) {
	f.calls++
	return f.souls, f.err
}

type fakeServiceLister struct {
	services []domain.Service
	err      error
}

func (f fakeServiceLister) ListServices(context.Context) ([]domain.Service, error) {
	return f.services, f.err
}

type fakePlacementReader struct {
	units    []ManagedUnitRecord
	obs      []ManagedObservationRecord
	desired  []ManagedDesiredStateRecord
	releases []ManagedReleaseRecord
}

func (f fakePlacementReader) ListManagedUnits(context.Context) ([]ManagedUnitRecord, error) {
	return f.units, nil
}
func (f fakePlacementReader) ListManagedObservations(context.Context) ([]ManagedObservationRecord, error) {
	return f.obs, nil
}
func (f fakePlacementReader) ListManagedDesiredStates(context.Context) ([]ManagedDesiredStateRecord, error) {
	return f.desired, nil
}
func (f fakePlacementReader) ListManagedReleases(context.Context) ([]ManagedReleaseRecord, error) {
	return f.releases, nil
}

type fakeHistory struct{ counts map[string]int }

func (f fakeHistory) ConsecutiveFailures(context.Context) (map[string]int, error) {
	return f.counts, nil
}

// TestInvariantReporterInventoriesEveryActiveSoulWithoutMutation proves the
// production report walks all canonical reads, marks database-sourced services
// canonical, and yields a healthy inventory for a consistent fleet.
func TestInvariantReporterInventoriesEveryActiveSoulWithoutMutation(t *testing.T) {
	serviceID := uuid.New()
	souls := &fakeSoulLister{souls: []ManagedSoulRecord{{
		EventID: "soul-scout", AgentID: "scout", Status: "active",
		AgentPubkey: "pk-scout", RuntimeBinding: "bind-scout",
		BahiaServiceID: serviceID.String(), Provenance: ProvenanceCanonical, SourceRef: "relay:soul:scout",
	}}}
	services := fakeServiceLister{services: []domain.Service{{
		ID: serviceID, Name: soulServiceName("scout"),
		ArtifactRepo: soulServiceArtifactRepo("scout"), RuntimeType: domain.RuntimeType("container"),
	}}}
	placement := fakePlacementReader{
		units:    []ManagedUnitRecord{{ID: "u1", ServiceID: serviceID.String(), Provenance: ProvenanceCanonical, SourceRef: "db:unit"}},
		obs:      []ManagedObservationRecord{{ServiceID: serviceID.String(), AgentPubkey: "pk-scout", RuntimeBinding: "bind-scout", Provenance: ProvenanceCanonical, SourceRef: "db:obs"}},
		desired:  []ManagedDesiredStateRecord{{ServiceID: serviceID.String(), ArtifactRef: "sha256:a", Provenance: ProvenanceCanonical, SourceRef: "db:desired"}},
		releases: []ManagedReleaseRecord{{ServiceID: serviceID.String(), BoundReleaseID: "r1", LatestReleaseID: "r1", Provenance: ProvenanceCanonical, SourceRef: "db:rel"}},
	}

	reporter, err := NewAgentManagementInvariantReporter(souls, services, placement, fakeHistory{}, 3)
	if err != nil {
		t.Fatalf("NewAgentManagementInvariantReporter() error = %v", err)
	}
	report, err := reporter.Report(context.Background())
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if !report.ReadOnly || report.MutationAllowed {
		t.Fatalf("production report must be read-only: %+v", report)
	}
	if report.Summary.ActiveSouls != 1 || report.Summary.Healthy != 1 {
		t.Fatalf("summary = %+v, want one healthy active Soul", report.Summary)
	}
	if souls.calls != 1 {
		t.Fatalf("soul lister calls = %d, want exactly 1 collection pass", souls.calls)
	}
	if got := soulResult(t, report, "scout"); got.BahiaServiceID != serviceID.String() {
		t.Fatalf("inventory lost the service link: %+v", got)
	}
}

// TestInvariantReporterMarksAgentNamespaceForReverseOrphans proves a service in
// the canonical agent namespace with no Soul is reported as a reverse orphan,
// while a non-agent service is not falsely claimed.
func TestInvariantReporterMarksAgentNamespaceForReverseOrphans(t *testing.T) {
	ghost := uuid.New()
	unrelated := uuid.New()
	reporter, err := NewAgentManagementInvariantReporter(
		&fakeSoulLister{},
		fakeServiceLister{services: []domain.Service{
			{ID: ghost, Name: soulServiceName("ghost"), ArtifactRepo: soulServiceArtifactRepo("ghost")},
			{ID: unrelated, Name: "billing-api", ArtifactRepo: "svc/billing-api"},
		}},
		nil, nil, 0,
	)
	if err != nil {
		t.Fatalf("NewAgentManagementInvariantReporter() error = %v", err)
	}
	report, err := reporter.Report(context.Background())
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation for the orphaned agent service", err)
	}
	if report.Summary.ReverseOrphans != 1 || len(report.Orphans) != 1 {
		t.Fatalf("orphans = %+v, want exactly the agent-namespace service", report.Orphans)
	}
	if report.Orphans[0].ServiceID != ghost.String() {
		t.Fatalf("orphan = %s, want %s (non-agent service must not be claimed)", report.Orphans[0].ServiceID, ghost)
	}
}

// TestInvariantReporterMissingPlacementReaderFailsClosed proves that absent
// placement/observation/desired-state readers surface missing-invariant
// violations instead of silently passing.
func TestInvariantReporterMissingPlacementReaderFailsClosed(t *testing.T) {
	serviceID := uuid.New()
	reporter, err := NewAgentManagementInvariantReporter(
		&fakeSoulLister{souls: []ManagedSoulRecord{{
			EventID: "soul-scout", AgentID: "scout", Status: "active",
			BahiaServiceID: serviceID.String(), Provenance: ProvenanceCanonical, SourceRef: "relay:soul:scout",
		}}},
		fakeServiceLister{services: []domain.Service{{
			ID: serviceID, Name: soulServiceName("scout"), ArtifactRepo: soulServiceArtifactRepo("scout"),
		}}},
		nil, nil, 0,
	)
	if err != nil {
		t.Fatalf("NewAgentManagementInvariantReporter() error = %v", err)
	}
	report, err := reporter.Report(context.Background())
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation", err)
	}
	got := soulResult(t, report, "scout")
	for _, code := range []string{InvariantMissingUnit, InvariantMissingObservation, InvariantMissingDesiredState} {
		if !hasViolation(got, code) {
			t.Fatalf("violations = %+v, want %s (must fail closed, not silently pass)", got.Violations, code)
		}
	}
}

// TestInvariantReporterPropagatesReadErrors keeps collection failures explicit
// rather than reporting a falsely-healthy fleet.
func TestInvariantReporterPropagatesReadErrors(t *testing.T) {
	reporter, err := NewAgentManagementInvariantReporter(
		&fakeSoulLister{err: errors.New("relay unavailable")},
		fakeServiceLister{}, nil, nil, 0,
	)
	if err != nil {
		t.Fatalf("NewAgentManagementInvariantReporter() error = %v", err)
	}
	if _, err := reporter.Report(context.Background()); err == nil {
		t.Fatal("expected soul-read failure to propagate, not a healthy report")
	}
}

// TestInvariantReporterRequiresReaders keeps the constructor fail-closed.
func TestInvariantReporterRequiresReaders(t *testing.T) {
	if _, err := NewAgentManagementInvariantReporter(nil, fakeServiceLister{}, nil, nil, 0); err == nil {
		t.Fatal("expected missing soul lister to be rejected")
	}
	if _, err := NewAgentManagementInvariantReporter(&fakeSoulLister{}, nil, nil, nil, 0); err == nil {
		t.Fatal("expected missing service lister to be rejected")
	}
}
