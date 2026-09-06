package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

const testLiveDigest = "sha256:29d6ae830d9d96f99a7f7c13d013a8855ea3ba5c4120f44c361697ce9b590512"

// liveImportFixture wires a registry whose service is already running an image
// Bahia observes, but for which no artifact record exists — the exact live
// Astillero shape this path exists to govern.
type liveImportFixture struct {
	registry *RegistryService
	builds   *mockBuildRepo
	arts     *mockArtifactRepo
	svc      *domain.Service
	env      *domain.Environment
	unitID   uuid.UUID
	input    ImportObservedArtifactInput
}

func newLiveImportFixture(t *testing.T, allow bool, labels map[string]string) *liveImportFixture {
	t.Helper()
	registry, svcRepo, envRepo, buildRepo, artRepo, _, _, obsRepo, stateRepo := newTestRegistryAll()
	registry.allowLiveArtifactImport = allow
	registry.txExecutor = newMockAdoptionTxExecutor(svcRepo, envRepo, buildRepo, artRepo, stateRepo, obsRepo, nil)

	svc, env := seedServiceAndEnv(t, registry)
	unitID := uuid.New()

	obs := &domain.RuntimeObservation{
		ID: uuid.New(), ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unitID,
		ObservedImageDigest: testLiveDigest, ObservedImageRepo: "astillero",
		ObservedContainerID: "container-1", HealthStatus: domain.HealthStatusHealthy,
		Source: "docker", ObservedAt: time.Now().UTC(),
	}
	if labels != nil {
		obs.NormalizedState = &domain.NormalizedObservation{BahiaLabels: labels}
	}
	if err := obsRepo.Create(context.Background(), obs); err != nil {
		t.Fatal(err)
	}

	return &liveImportFixture{
		registry: registry, builds: buildRepo, arts: artRepo, svc: svc, env: env, unitID: unitID,
		input: ImportObservedArtifactInput{
			ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unitID,
			ImageRepo: "astillero", ImageTag: "2729a7c", ImageDigest: testLiveDigest,
			RequestedBy: "operator-pubkey",
		},
	}
}

func TestImportObservedArtifactRefusedWhenPolicyDisabledLeavesNoState(t *testing.T) {
	f := newLiveImportFixture(t, false, nil)

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil {
		t.Fatal("expected refusal when live artifact import is disabled")
	}
	// The refusal must steer the operator, never leave them inferring table state.
	for _, want := range []string{"disabled by policy", "hiveci.allow_live_artifact_import", "BAHIA_ARTIFACT", "Never edit the database"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("policy refusal %q does not mention %q", err.Error(), want)
		}
	}
	if len(f.builds.builds) != 0 || len(f.arts.artifacts) != 0 {
		t.Fatalf("denied import wrote partial state: %d builds, %d artifacts", len(f.builds.builds), len(f.arts.artifacts))
	}
}

func TestImportObservedArtifactCreatesGovernedLineage(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	// Identity labels that genuinely match, plus the OCI revision label the
	// build lineage should recover the source commit from.
	for _, obs := range f.registry.observations.(*mockObsRepo).observations {
		obs.NormalizedState = &domain.NormalizedObservation{
			BahiaLabels: map[string]string{
				labelServiceID:        f.svc.ID.String(),
				labelEnvironmentID:    f.env.ID.String(),
				labelDeploymentUnitID: f.unitID.String(),
			},
		}
	}

	f.input.GitSHA = "2729a7ccce3e72deceb0f741fe89bd70a764e88a"

	result, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if result.Status != "imported" {
		t.Fatalf("status = %q, want imported", result.Status)
	}
	if result.Artifact == nil || result.Build == nil {
		t.Fatal("import must create both build and artifact lineage")
	}
	if result.Artifact.ImageDigest != testLiveDigest || result.Artifact.ServiceID != f.svc.ID {
		t.Fatalf("artifact not pinned to the observed digest/service: %#v", result.Artifact)
	}
	if result.Build.CISystem != liveImportCISystem || result.Build.Status != domain.BuildStatusSucceeded {
		t.Fatalf("build lineage must be marked as operator live-import: %#v", result.Build)
	}
	// The operator's declared source commit is preserved as lineage.
	if result.Build.GitSHA != "2729a7ccce3e72deceb0f741fe89bd70a764e88a" {
		t.Fatalf("git sha = %q, want the operator-supplied commit", result.Build.GitSHA)
	}
	if len(f.builds.builds) != 1 || len(f.arts.artifacts) != 1 {
		t.Fatalf("expected exactly one build and one artifact, got %d/%d", len(f.builds.builds), len(f.arts.artifacts))
	}
	if result.DesiredStateNote == "" {
		t.Fatal("result must state that desired state is unchanged")
	}
	// Every identity label present must be recorded as verified evidence.
	for _, want := range []string{labelServiceID, labelEnvironmentID, labelDeploymentUnitID} {
		if !slices.Contains(result.VerifiedLabels, want) {
			t.Fatalf("verified labels %v missing %q", result.VerifiedLabels, want)
		}
	}
}

func TestImportObservedArtifactIsIdempotent(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	ctx := context.Background()

	first, err := f.registry.ImportObservedArtifact(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.registry.ImportObservedArtifact(ctx, f.input)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if second.Status != "already_imported" {
		t.Fatalf("replay status = %q, want already_imported", second.Status)
	}
	if second.Artifact.ID != first.Artifact.ID {
		t.Fatalf("replay created a second artifact %s (first %s)", second.Artifact.ID, first.Artifact.ID)
	}
	if len(f.builds.builds) != 1 || len(f.arts.artifacts) != 1 {
		t.Fatalf("replay duplicated lineage: %d builds, %d artifacts", len(f.builds.builds), len(f.arts.artifacts))
	}
}

func TestImportObservedArtifactRejectsDigestMismatch(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	f.input.ImageDigest = "sha256:" + strings.Repeat("b", 64)

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil || !strings.Contains(err.Error(), "does not match the observed running digest") {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if len(f.builds.builds) != 0 || len(f.arts.artifacts) != 0 {
		t.Fatal("digest mismatch must not write state")
	}
}

func TestImportObservedArtifactRejectsForgedIdentityLabels(t *testing.T) {
	otherService := uuid.New()
	f := newLiveImportFixture(t, true, map[string]string{labelServiceID: otherService.String()})

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil || !strings.Contains(err.Error(), labelServiceID) {
		t.Fatalf("forged label error = %v", err)
	}
	if len(f.arts.artifacts) != 0 {
		t.Fatal("forged identity must not write state")
	}
}

func TestImportObservedArtifactRequiresAnObservation(t *testing.T) {
	registry, svcRepo, envRepo, buildRepo, artRepo, _, _, obsRepo, stateRepo := newTestRegistryAll()
	registry.allowLiveArtifactImport = true
	registry.txExecutor = newMockAdoptionTxExecutor(svcRepo, envRepo, buildRepo, artRepo, stateRepo, obsRepo, nil)
	svc, env := seedServiceAndEnv(t, registry)

	_, err := registry.ImportObservedArtifact(context.Background(), ImportObservedArtifactInput{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		ImageRepo: "astillero", ImageTag: "2729a7c", ImageDigest: testLiveDigest,
	})
	if err == nil || !strings.Contains(err.Error(), "no runtime observation") {
		t.Fatalf("missing observation error = %v", err)
	}
}

func TestImportObservedArtifactRejectsRegistryDigestDisagreement(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	// A registry that knows this repository must agree with what is running.
	f.registry.verifier = &mockVerifier{result: &ImageVerification{
		Exists: true, Digest: "sha256:" + strings.Repeat("c", 64),
	}}

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil || !strings.Contains(err.Error(), "registry reports digest") {
		t.Fatalf("registry disagreement error = %v", err)
	}
	if len(f.arts.artifacts) != 0 {
		t.Fatal("registry disagreement must not write state")
	}
}

func TestImportObservedArtifactProceedsWhenImageIsRegistryLocal(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	// A locally built image absent from any registry is exactly the case this
	// path governs; the running system remains the digest authority.
	f.registry.verifier = &mockVerifier{err: errors.New("manifest unknown")}

	result, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err != nil {
		t.Fatalf("local image import failed: %v", err)
	}
	if result.RegistryVerified {
		t.Fatal("registry verification must not be claimed when the registry does not know the image")
	}
	if result.Artifact.Metadata["registry_verified"] != false {
		t.Fatalf("artifact evidence must record registry_verified=false, got %#v", result.Artifact.Metadata["registry_verified"])
	}
}

func TestImportObservedArtifactRequiresTransactionalRepositories(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	f.registry.txExecutor = nil

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil || !strings.Contains(err.Error(), "transactional repositories") {
		t.Fatalf("non-transactional error = %v", err)
	}
}

func TestImportObservedArtifactRejectsMismatchedImageRepo(t *testing.T) {
	f := newLiveImportFixture(t, true, nil)
	f.input.ImageRepo = "someone-elses/repo"

	_, err := f.registry.ImportObservedArtifact(context.Background(), f.input)
	if err == nil || !strings.Contains(err.Error(), "does not match the observed running repository") {
		t.Fatalf("image repo mismatch error = %v", err)
	}
	if len(f.arts.artifacts) != 0 {
		t.Fatal("repository mismatch must not write state")
	}
}

func TestImportObservedArtifactRejectsStaleOrStoppedObservation(t *testing.T) {
	stale := newLiveImportFixture(t, true, nil)
	for _, obs := range stale.registry.observations.(*mockObsRepo).observations {
		obs.ObservedAt = time.Now().UTC().Add(-2 * maxLiveImportObservationAge)
	}
	if _, err := stale.registry.ImportObservedArtifact(context.Background(), stale.input); err == nil ||
		!strings.Contains(err.Error(), "re-observe the service") {
		t.Fatalf("stale observation error = %v", err)
	}

	stopped := newLiveImportFixture(t, true, nil)
	for _, obs := range stopped.registry.observations.(*mockObsRepo).observations {
		obs.HealthStatus = domain.HealthStatusStopped
	}
	if _, err := stopped.registry.ImportObservedArtifact(context.Background(), stopped.input); err == nil ||
		!strings.Contains(err.Error(), "container stopped") {
		t.Fatalf("stopped observation error = %v", err)
	}
}
