package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeStateLoader struct {
	states map[uuid.UUID][]domain.EnvironmentServiceState
	err    error
}

func (f *fakeStateLoader) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.states[envID], nil
}

type fakeStateWriter struct {
	upserted []*domain.EnvironmentServiceState
	err      error
}

func (f *fakeStateWriter) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	if f.err != nil {
		return f.err
	}
	cp := *state
	f.upserted = append(f.upserted, &cp)
	return nil
}

type fakeServiceLoader struct {
	services map[uuid.UUID]*domain.Service
	err      error
}

func (f *fakeServiceLoader) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.services[id], nil
}

type fakeArtifactLoader struct {
	artifacts map[uuid.UUID]*domain.Artifact
	err       error
}

func (f *fakeArtifactLoader) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.artifacts[id], nil
}

type fakeSecretLister struct {
	secrets map[string][]domain.ServiceSecret // key: "serviceID:envID"
	err     error
}

func (f *fakeSecretLister) ListEffective(_ context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := fmt.Sprintf("%s:%s", serviceID, envID)
	return f.secrets[key], nil
}

// ---------------------------------------------------------------------------
// Deterministic test IDs
// ---------------------------------------------------------------------------

var (
	planEnvID       = uuid.MustParse("00000000-0000-0000-0000-000000000010")
	planTargetSvcID = uuid.MustParse("00000000-0000-0000-0000-000000000011")
	planSiblingSvcID1 = uuid.MustParse("00000000-0000-0000-0000-000000000012")
	planSiblingSvcID2 = uuid.MustParse("00000000-0000-0000-0000-000000000013")
	planDeletedSvcID  = uuid.MustParse("00000000-0000-0000-0000-000000000014")
	planArtifactID1   = uuid.MustParse("00000000-0000-0000-0000-000000000021")
	planArtifactID2   = uuid.MustParse("00000000-0000-0000-0000-000000000022")
	planBuildID       = uuid.MustParse("00000000-0000-0000-0000-000000000030")
)

func makeTargetSpec() *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        planTargetSvcID,
		EnvironmentID:    planEnvID,
		ArtifactID:       planArtifactID1,
		StableServiceKey: "target-app",
		ImageRef:         "ghcr.io/org/target@sha256:aaa",
		Env:              map[string]string{"APP_ENV": "prod"},
		Labels:           map[string]string{"bahia.managed": "true"},
		RestartPolicy:    "unless-stopped",
	}
	spec.ComputeDesiredHash()
	return spec
}

func makeSiblingSpec(serviceID uuid.UUID, key string) *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        serviceID,
		EnvironmentID:    planEnvID,
		ArtifactID:       planArtifactID2,
		StableServiceKey: key,
		ImageRef:         "ghcr.io/org/" + key + "@sha256:bbb",
		Env:              map[string]string{"MODE": "sibling"},
		Labels:           map[string]string{"bahia.managed": "true"},
		RestartPolicy:    "always",
	}
	spec.ComputeDesiredHash()
	return spec
}

func newAssembler(
	stateLoader *fakeStateLoader,
	stateWriter *fakeStateWriter,
	services *fakeServiceLoader,
	artifacts *fakeArtifactLoader,
	secrets *fakeSecretLister,
) *EnvironmentPlanAssembler {
	return NewEnvironmentPlanAssembler(EnvironmentPlanAssemblerDeps{
		StateLoader: stateLoader,
		StateWriter: stateWriter,
		Services:    services,
		Artifacts:   artifacts,
		Secrets:     secrets,
		Builder:     NewDesiredStateBuilder(),
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAssemble_TargetReplacesExisting(t *testing.T) {
	// Existing state has an old spec for the target service.
	oldSpec := makeSiblingSpec(planTargetSvcID, "target-app")
	oldSpec.ImageRef = "ghcr.io/org/target@sha256:old"
	oldSpec.ComputeDesiredHash()

	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:       planTargetSvcID,
					EnvironmentID:   planEnvID,
					DesiredRuntimeState: oldSpec,
					DesiredHash:     oldSpec.DesiredHash,
				},
			},
		},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, &fakeServiceLoader{services: map[uuid.UUID]*domain.Service{}}, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(plan.Services))
	}
	if plan.Services[0].ImageRef != targetSpec.ImageRef {
		t.Errorf("expected target spec image %q, got %q", targetSpec.ImageRef, plan.Services[0].ImageRef)
	}
	if plan.Services[0].DesiredHash != targetSpec.DesiredHash {
		t.Errorf("expected target hash, got %q", plan.Services[0].DesiredHash)
	}
}

func TestAssemble_SiblingFromStoredState(t *testing.T) {
	siblingSpec := makeSiblingSpec(planSiblingSvcID1, "sibling-one")

	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:       planTargetSvcID,
					EnvironmentID:   planEnvID,
				},
				{
					ServiceID:           planSiblingSvcID1,
					EnvironmentID:       planEnvID,
					DesiredRuntimeState: siblingSpec,
					DesiredHash:         siblingSpec.DesiredHash,
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {ID: planSiblingSvcID1, Name: "sibling-one"},
		},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(plan.Services))
	}

	// Find sibling in sorted output.
	var found bool
	for _, svc := range plan.Services {
		if svc.ServiceID == planSiblingSvcID1 {
			found = true
			if svc.DesiredHash != siblingSpec.DesiredHash {
				t.Errorf("sibling hash mismatch: got %q, want %q", svc.DesiredHash, siblingSpec.DesiredHash)
			}
		}
	}
	if !found {
		t.Error("sibling service not found in plan")
	}
}

func TestAssemble_LegacySiblingHydratedFromServiceArtifact(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
				{
					ServiceID:        planSiblingSvcID1,
					EnvironmentID:    planEnvID,
					DesiredArtifactID: &planArtifactID2,
					// No DesiredRuntimeState — legacy.
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {
				ID:          planSiblingSvcID1,
				Name:        "legacy-svc",
				RuntimeType: domain.RuntimeTypeDocker,
				RuntimeConfig: &domain.ServiceRuntimeConfig{
					Adopted: &domain.AdoptedRuntimeConfig{
						TargetName:  "legacy-svc",
						Environment: map[string]string{"MODE": "legacy"},
						Restart:     "always",
					},
				},
			},
		},
	}

	artLoader := &fakeArtifactLoader{
		artifacts: map[uuid.UUID]*domain.Artifact{
			planArtifactID2: {
				ID:          planArtifactID2,
				BuildID:     planBuildID,
				ServiceID:   planSiblingSvcID1,
				ImageRepo:   "ghcr.io/org/legacy-svc",
				ImageTag:    "v0.9",
				ImageDigest: "sha256:legacy",
			},
		},
	}

	writer := &fakeStateWriter{}
	secretLister := &fakeSecretLister{secrets: map[string][]domain.ServiceSecret{}}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, writer, svcLoader, artLoader, secretLister)

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(plan.Services))
	}

	// Find the hydrated legacy sibling.
	var legacySvc *domain.DesiredServiceSpec
	for i := range plan.Services {
		if plan.Services[i].ServiceID == planSiblingSvcID1 {
			legacySvc = &plan.Services[i]
			break
		}
	}
	if legacySvc == nil {
		t.Fatal("legacy sibling not found in plan")
	}
	if legacySvc.ImageRef != "ghcr.io/org/legacy-svc@sha256:legacy" {
		t.Errorf("unexpected image ref: %s", legacySvc.ImageRef)
	}
	if legacySvc.DesiredHash == "" {
		t.Error("legacy sibling should have a computed desired hash")
	}
	// Docker extension should be populated.
	if legacySvc.DockerExtension == nil {
		t.Error("expected DockerExtension for Docker runtime type sibling")
	}
}

func TestAssemble_HydratedSpecPersisted(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
				{
					ServiceID:         planSiblingSvcID1,
					EnvironmentID:     planEnvID,
					DesiredArtifactID: &planArtifactID2,
					// Legacy — no DesiredRuntimeState.
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {
				ID:          planSiblingSvcID1,
				Name:        "legacy-svc",
				RuntimeType: domain.RuntimeTypeCompose,
				RuntimeConfig: &domain.ServiceRuntimeConfig{
					Adopted: &domain.AdoptedRuntimeConfig{
						TargetName: "legacy-svc",
						Restart:    "always",
					},
				},
			},
		},
	}

	artLoader := &fakeArtifactLoader{
		artifacts: map[uuid.UUID]*domain.Artifact{
			planArtifactID2: {
				ID:          planArtifactID2,
				BuildID:     planBuildID,
				ServiceID:   planSiblingSvcID1,
				ImageRepo:   "ghcr.io/org/legacy-svc",
				ImageTag:    "v1.0",
				ImageDigest: "sha256:abc",
			},
		},
	}

	writer := &fakeStateWriter{}
	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, writer, svcLoader, artLoader, &fakeSecretLister{secrets: map[string][]domain.ServiceSecret{}})

	_, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the hydrated spec was persisted.
	if len(writer.upserted) != 1 {
		t.Fatalf("expected 1 upsert for hydrated legacy sibling, got %d", len(writer.upserted))
	}
	persisted := writer.upserted[0]
	if persisted.ServiceID != planSiblingSvcID1 {
		t.Errorf("persisted wrong service: %s", persisted.ServiceID)
	}
	if persisted.DesiredRuntimeState == nil {
		t.Fatal("persisted state should have DesiredRuntimeState")
	}
	if persisted.DesiredHash == "" {
		t.Error("persisted state should have DesiredHash")
	}
}

func TestAssemble_PersistFailureDoesNotBreakAssembly(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
				{
					ServiceID:         planSiblingSvcID1,
					EnvironmentID:     planEnvID,
					DesiredArtifactID: &planArtifactID2,
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {
				ID:          planSiblingSvcID1,
				Name:        "legacy-svc",
				RuntimeType: domain.RuntimeTypeDocker,
				RuntimeConfig: &domain.ServiceRuntimeConfig{
					Adopted: &domain.AdoptedRuntimeConfig{
						TargetName: "legacy-svc",
					},
				},
			},
		},
	}

	artLoader := &fakeArtifactLoader{
		artifacts: map[uuid.UUID]*domain.Artifact{
			planArtifactID2: {
				ID:          planArtifactID2,
				BuildID:     planBuildID,
				ServiceID:   planSiblingSvcID1,
				ImageRepo:   "ghcr.io/org/legacy-svc",
				ImageDigest: "sha256:x",
			},
		},
	}

	// Writer that always fails.
	writer := &fakeStateWriter{err: errors.New("db down")}
	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, writer, svcLoader, artLoader, &fakeSecretLister{secrets: map[string][]domain.ServiceSecret{}})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("assembly should succeed despite persist failure: %v", err)
	}
	if len(plan.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(plan.Services))
	}
}

func TestAssemble_TombstonedServiceExcluded(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
				{
					// Service that no longer exists in the repository.
					ServiceID:           planDeletedSvcID,
					EnvironmentID:       planEnvID,
					DesiredRuntimeState: makeSiblingSpec(planDeletedSvcID, "deleted-svc"),
				},
			},
		},
	}

	// Service loader returns nil for deleted service.
	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 1 {
		t.Fatalf("expected 1 service (deleted excluded), got %d", len(plan.Services))
	}
	if plan.Services[0].ServiceID != planTargetSvcID {
		t.Errorf("expected target service, got %s", plan.Services[0].ServiceID)
	}
}

func TestAssemble_DeterministicOrdering(t *testing.T) {
	specA := makeSiblingSpec(planSiblingSvcID1, "alpha-svc")
	specZ := makeSiblingSpec(planSiblingSvcID2, "zeta-svc")

	// Insert in reverse alphabetical order to prove sorting.
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:           planSiblingSvcID2,
					EnvironmentID:       planEnvID,
					DesiredRuntimeState: specZ,
				},
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
				{
					ServiceID:           planSiblingSvcID1,
					EnvironmentID:       planEnvID,
					DesiredRuntimeState: specA,
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {ID: planSiblingSvcID1, Name: "alpha-svc"},
			planSiblingSvcID2: {ID: planSiblingSvcID2, Name: "zeta-svc"},
		},
	}

	targetSpec := makeTargetSpec() // StableServiceKey = "target-app"
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(plan.Services))
	}

	// Expected order: alpha-svc, target-app, zeta-svc
	expectedKeys := []string{"alpha-svc", "target-app", "zeta-svc"}
	for i, want := range expectedKeys {
		if plan.Services[i].StableServiceKey != want {
			t.Errorf("position %d: expected key %q, got %q", i, want, plan.Services[i].StableServiceKey)
		}
	}
}

func TestAssemble_RevisionHashComputed(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{
					ServiceID:     planTargetSvcID,
					EnvironmentID: planEnvID,
				},
			},
		},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, &fakeServiceLoader{services: map[uuid.UUID]*domain.Service{}}, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.RevisionHash == "" {
		t.Error("revision hash should be computed")
	}
	if plan.EnvironmentID != planEnvID {
		t.Errorf("environment ID mismatch: got %s", plan.EnvironmentID)
	}

	// Same inputs should produce same hash.
	plan2, _ := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if plan.RevisionHash != plan2.RevisionHash {
		t.Errorf("revision hash not deterministic: %q vs %q", plan.RevisionHash, plan2.RevisionHash)
	}
}

func TestAssemble_RevisionHashChangesWithDifferentSpecs(t *testing.T) {
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{ServiceID: planTargetSvcID, EnvironmentID: planEnvID},
			},
		},
	}
	svcLoader := &fakeServiceLoader{services: map[uuid.UUID]*domain.Service{}}
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, &fakeArtifactLoader{}, &fakeSecretLister{})

	spec1 := makeTargetSpec()
	plan1, _ := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, spec1)

	spec2 := makeTargetSpec()
	spec2.ImageRef = "ghcr.io/org/target@sha256:different"
	spec2.ComputeDesiredHash()
	plan2, _ := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, spec2)

	if plan1.RevisionHash == plan2.RevisionHash {
		t.Error("different specs should produce different revision hashes")
	}
}

func TestAssemble_NilTargetSpecReturnsError(t *testing.T) {
	asm := newAssembler(&fakeStateLoader{}, &fakeStateWriter{}, &fakeServiceLoader{}, &fakeArtifactLoader{}, &fakeSecretLister{})

	_, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, nil)
	if err == nil {
		t.Fatal("expected error for nil targetSpec")
	}
}

func TestAssemble_FirstDeployTargetNotInState(t *testing.T) {
	// Empty environment — no existing state rows at all.
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, &fakeServiceLoader{services: map[uuid.UUID]*domain.Service{}}, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plan.Services) != 1 {
		t.Fatalf("expected 1 service for first deploy, got %d", len(plan.Services))
	}
	if plan.Services[0].ServiceID != planTargetSvcID {
		t.Errorf("expected target service, got %s", plan.Services[0].ServiceID)
	}
}

func TestAssemble_LegacySiblingWithNoArtifactExcluded(t *testing.T) {
	// Legacy sibling with no DesiredArtifactID — cannot be hydrated.
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{ServiceID: planTargetSvcID, EnvironmentID: planEnvID},
				{
					ServiceID:     planSiblingSvcID1,
					EnvironmentID: planEnvID,
					// No DesiredArtifactID, no DesiredRuntimeState.
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {ID: planSiblingSvcID1, Name: "no-artifact-svc"},
		},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, &fakeArtifactLoader{}, &fakeSecretLister{})

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sibling should be excluded (hydration fails gracefully).
	if len(plan.Services) != 1 {
		t.Fatalf("expected 1 service (non-hydratable excluded), got %d", len(plan.Services))
	}
}

func TestAssemble_StateLoadError(t *testing.T) {
	loader := &fakeStateLoader{err: errors.New("connection refused")}
	asm := newAssembler(loader, &fakeStateWriter{}, &fakeServiceLoader{}, &fakeArtifactLoader{}, &fakeSecretLister{})

	_, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, makeTargetSpec())
	if err == nil {
		t.Fatal("expected error when state loading fails")
	}
}

func TestAssemble_LegacySiblingWithSecrets(t *testing.T) {
	secretID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	loader := &fakeStateLoader{
		states: map[uuid.UUID][]domain.EnvironmentServiceState{
			planEnvID: {
				{ServiceID: planTargetSvcID, EnvironmentID: planEnvID},
				{
					ServiceID:         planSiblingSvcID1,
					EnvironmentID:     planEnvID,
					DesiredArtifactID: &planArtifactID2,
				},
			},
		},
	}

	svcLoader := &fakeServiceLoader{
		services: map[uuid.UUID]*domain.Service{
			planSiblingSvcID1: {
				ID:          planSiblingSvcID1,
				Name:        "secret-svc",
				RuntimeType: domain.RuntimeTypeCompose,
				RuntimeConfig: &domain.ServiceRuntimeConfig{
					Adopted: &domain.AdoptedRuntimeConfig{
						TargetName:  "secret-svc",
						Environment: map[string]string{"DB_URL": "postgres://...", "API_KEY": "should-be-removed"},
					},
				},
			},
		},
	}

	artLoader := &fakeArtifactLoader{
		artifacts: map[uuid.UUID]*domain.Artifact{
			planArtifactID2: {
				ID:          planArtifactID2,
				BuildID:     planBuildID,
				ServiceID:   planSiblingSvcID1,
				ImageRepo:   "ghcr.io/org/secret-svc",
				ImageDigest: "sha256:sec",
			},
		},
	}

	secretKey := fmt.Sprintf("%s:%s", planSiblingSvcID1, planEnvID)
	secrets := &fakeSecretLister{
		secrets: map[string][]domain.ServiceSecret{
			secretKey: {
				{
					ID:               secretID,
					ServiceID:        planSiblingSvcID1,
					EnvironmentID:    &planEnvID,
					Name:             "API_KEY",
					EncryptedValue:   []byte("enc"),
					EncryptionMethod: "aes256gcm",
					CreatedAt:        time.Now(),
				},
			},
		},
	}

	targetSpec := makeTargetSpec()
	asm := newAssembler(loader, &fakeStateWriter{}, svcLoader, artLoader, secrets)

	plan, err := asm.Assemble(context.Background(), planEnvID, planTargetSvcID, targetSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the hydrated sibling and verify secret handling.
	var sibling *domain.DesiredServiceSpec
	for i := range plan.Services {
		if plan.Services[i].ServiceID == planSiblingSvcID1 {
			sibling = &plan.Services[i]
			break
		}
	}
	if sibling == nil {
		t.Fatal("sibling not found")
	}

	// API_KEY should be in SecretRefs, not in Env.
	if _, ok := sibling.Env["API_KEY"]; ok {
		t.Error("API_KEY should not be in literal env (it's a secret)")
	}
	if len(sibling.SecretRefs) != 1 {
		t.Fatalf("expected 1 secret ref, got %d", len(sibling.SecretRefs))
	}
	if sibling.SecretRefs[0].EnvVar != "API_KEY" {
		t.Errorf("expected secret ref for API_KEY, got %s", sibling.SecretRefs[0].EnvVar)
	}
	// DB_URL should remain in literal env.
	if sibling.Env["DB_URL"] != "postgres://..." {
		t.Errorf("DB_URL should remain in literal env, got %q", sibling.Env["DB_URL"])
	}
}
