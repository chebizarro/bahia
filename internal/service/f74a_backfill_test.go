package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type f74aMemoryMarker struct {
	value  []byte
	writes int
}

func (m *f74aMemoryMarker) GetControlRecord(_, _ string) ([]byte, error) { return m.value, nil }
func (m *f74aMemoryMarker) PutControlRecord(_, _ string, v []byte) error {
	m.value = append([]byte(nil), v...)
	m.writes++
	return nil
}

type f74aBackfillLLM struct {
	route   domain.LLMRoute
	release domain.LLMRelease
}

func (s f74aBackfillLLM) ListRoutes(_ context.Context, _, offset int) ([]domain.LLMRoute, error) {
	if offset > 0 {
		return nil, nil
	}
	return []domain.LLMRoute{s.route}, nil
}
func (s f74aBackfillLLM) ListReleases(_ context.Context, id uuid.UUID, _, offset int) ([]domain.LLMRelease, error) {
	if offset > 0 || id != s.route.ID {
		return nil, nil
	}
	return []domain.LLMRelease{s.release}, nil
}

type f74aBackfillServices struct {
	repository.ServiceRepository
	svc domain.Service
}

func (s f74aBackfillServices) List(context.Context) ([]domain.Service, error) {
	return []domain.Service{s.svc}, nil
}

type f74aBackfillArtifacts struct {
	repository.ArtifactRepository
	svc      uuid.UUID
	artifact domain.Artifact
}

func (s f74aBackfillArtifacts) ListByService(_ context.Context, id uuid.UUID, _, offset int) ([]domain.Artifact, error) {
	if id != s.svc || offset > 0 {
		return nil, nil
	}
	return []domain.Artifact{s.artifact}, nil
}

type f74aBackfillSignatures struct {
	repository.ArtifactSignatureRepository
	sig domain.ArtifactSignature
}

func (s f74aBackfillSignatures) ListByArtifact(_ context.Context, id uuid.UUID) ([]domain.ArtifactSignature, error) {
	if id != s.sig.ArtifactID {
		return nil, nil
	}
	return []domain.ArtifactSignature{s.sig}, nil
}

type f74aBackfillSBOMs struct {
	sbom domain.ArtifactSBOM
	pkg  domain.SBOMPackage
}

func (s f74aBackfillSBOMs) ListAllSBOMs(_ context.Context, _, offset int) ([]domain.ArtifactSBOM, error) {
	if offset > 0 {
		return nil, nil
	}
	return []domain.ArtifactSBOM{s.sbom}, nil
}
func (s f74aBackfillSBOMs) ListAllPackages(_ context.Context, _, offset int) ([]domain.SBOMPackage, error) {
	if offset > 0 {
		return nil, nil
	}
	return []domain.SBOMPackage{s.pkg}, nil
}

type f74aBackfillStates struct {
	repository.EnvironmentServiceStateRepository
	state domain.EnvironmentServiceState
}

func (s f74aBackfillStates) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return []domain.EnvironmentServiceState{s.state}, nil
}

type f74aBackfillObservations struct {
	repository.RuntimeObservationRepository
	obs domain.RuntimeObservation
}

func (s f74aBackfillObservations) GetLatest(_ context.Context, svc, env uuid.UUID) (*domain.RuntimeObservation, error) {
	if svc == s.obs.ServiceID && env == s.obs.EnvironmentID {
		return &s.obs, nil
	}
	return nil, nil
}

type f74aFailingBackfillPub struct{ *f74aPublishCapture }

func (p f74aFailingBackfillPub) PublishSBOMPackage(context.Context, *domain.SBOMPackage) error {
	return errors.New("publish rejected")
}

func TestF74aBackfillOnceAndRetryAfterFailureDBLess(t *testing.T) {
	ctx := context.Background()
	marker := &f74aMemoryMarker{value: []byte("dirty")}
	capture := &f74aPublishCapture{}
	svcID, envID, artifactID, sbomID, routeID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cfg := F74aBackfillConfig{
		Marker: marker, Publisher: capture,
		LLM:          f74aBackfillLLM{route: domain.LLMRoute{ID: routeID}, release: domain.LLMRelease{ID: uuid.New(), RouteID: routeID}},
		Services:     f74aBackfillServices{svc: domain.Service{ID: svcID}},
		Artifacts:    f74aBackfillArtifacts{svc: svcID, artifact: domain.Artifact{ID: artifactID, ServiceID: svcID}},
		Signatures:   f74aBackfillSignatures{sig: domain.ArtifactSignature{ID: uuid.New(), ArtifactID: artifactID}},
		SBOMs:        f74aBackfillSBOMs{sbom: domain.ArtifactSBOM{ID: sbomID, ArtifactID: artifactID}, pkg: domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID}},
		States:       f74aBackfillStates{state: domain.EnvironmentServiceState{ServiceID: svcID, EnvironmentID: envID}},
		Observations: f74aBackfillObservations{obs: domain.RuntimeObservation{ID: uuid.New(), ServiceID: svcID, EnvironmentID: envID}},
	}
	cfg.Publisher = f74aFailingBackfillPub{capture}
	if err := BootstrapF74aCanonical(ctx, cfg); err == nil {
		t.Fatal("failed publish should abort backfill")
	}
	if marker.writes != 0 {
		t.Fatal("failed backfill wrote marker")
	}
	cfg.Publisher = capture
	if err := BootstrapF74aCanonical(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if capture.releases != 2 || capture.signatures != 2 || capture.sboms != 2 || capture.packages != 1 || capture.observations != 1 || marker.writes != 1 {
		t.Fatalf("unexpected backfill counts: %+v marker=%d", capture, marker.writes)
	}
	if err := BootstrapF74aCanonical(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if marker.writes != 1 || capture.packages != 1 {
		t.Fatal("completed backfill ran twice")
	}
}
