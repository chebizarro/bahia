package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type releaseDecisionEventRepo struct {
	recordErr error
}

func (r *releaseDecisionEventRepo) Record(context.Context, *repository.NostrEventRecord) (bool, error) {
	if r.recordErr != nil {
		return false, r.recordErr
	}
	return true, nil
}
func (*releaseDecisionEventRepo) GetByID(context.Context, string) (*repository.NostrEventRecord, error) {
	return nil, repository.ErrNotFound
}
func (*releaseDecisionEventRepo) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*releaseDecisionEventRepo) ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*releaseDecisionEventRepo) FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*releaseDecisionEventRepo) ListByEntity(context.Context, string, uuid.UUID, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (*releaseDecisionEventRepo) LatestCreatedAtForKinds(context.Context, []int) (*time.Time, error) {
	return nil, nil
}
func (*releaseDecisionEventRepo) LatestCreatedAtForKindsAndAuthors(context.Context, []int, []string) (*time.Time, error) {
	return nil, nil
}

type releaseDecisionTxExecutor struct {
	services     *mockServiceRepo
	environments *mockEnvRepo
	builds       *mockBuildRepo
	artifacts    *mockArtifactRepo
	intents      *mockIntentRepo
	state        *mockStateRepo
	events       repository.NostrEventRepository
}

func (e *releaseDecisionTxExecutor) WithinTx(ctx context.Context, fn func(repository.TxRepos) error) error {
	txServices := cloneMockServiceRepo(e.services)
	txEnvironments := cloneMockEnvRepo(e.environments)
	txBuilds := cloneMockBuildRepo(e.builds)
	txArtifacts := cloneMockArtifactRepo(e.artifacts)
	txIntents := cloneReleaseDecisionIntentRepo(e.intents)
	txState := cloneMockStateRepo(e.state)
	if err := fn(repository.TxRepos{
		Services: txServices, Environments: txEnvironments, Builds: txBuilds,
		Artifacts: txArtifacts, Intents: txIntents, State: txState, NostrEvents: e.events,
	}); err != nil {
		return err
	}
	e.services.services = txServices.services
	e.environments.envs = txEnvironments.envs
	e.builds.builds = txBuilds.builds
	e.artifacts.artifacts = txArtifacts.artifacts
	e.intents.intents = txIntents.intents
	e.state.states = txState.states
	return nil
}

func cloneReleaseDecisionIntentRepo(src *mockIntentRepo) *mockIntentRepo {
	clone := newMockIntentRepo()
	clone.updateStatusErr = src.updateStatusErr
	clone.getByIDErr = src.getByIDErr
	for id, intent := range src.intents {
		copy := *intent
		clone.intents[id] = &copy
	}
	return clone
}

func newReleaseDecisionRegistry(
	services *mockServiceRepo,
	environments *mockEnvRepo,
	builds *mockBuildRepo,
	artifacts *mockArtifactRepo,
	intents *mockIntentRepo,
	state *mockStateRepo,
) *RegistryService {
	tx := &releaseDecisionTxExecutor{
		services: services, environments: environments, builds: builds,
		artifacts: artifacts, intents: intents, state: state,
		events: &releaseDecisionEventRepo{},
	}
	return NewRegistryService(
		services, environments, builds, artifacts, intents, newMockRunRepo(),
		newMockObsRepo(), state, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithRegistryTxExecutor(tx),
	)
}

func TestRegisterReleaseArtifactAuditFailureRollsBackBuildAndArtifact(t *testing.T) {
	serviceID := uuid.New()
	repositoryName := "harbor.example/team/bahia"
	services, environments := newMockServiceRepo(), newMockEnvRepo()
	builds, artifacts := newMockBuildRepo(), newMockArtifactRepo()
	intents, state := newMockIntentRepo(), newMockStateRepo()
	services.services[serviceID] = &domain.Service{ID: serviceID, ArtifactRepo: repositoryName}
	registry := newReleaseDecisionRegistry(services, environments, builds, artifacts, intents, state)

	digest := "sha256:" + strings.Repeat("a", 64)
	build := &domain.Build{
		ServiceID: serviceID, GitSHA: strings.Repeat("b", 40), CISystem: domain.CISystemHiveCI,
		CIRunID: strings.Repeat("c", 64), Status: domain.BuildStatusSucceeded,
	}
	artifact := &domain.Artifact{ServiceID: serviceID, ImageRepo: repositoryName, ImageDigest: digest}
	release := domain.HiveCIAcceptedRelease{
		ResultEventID: strings.Repeat("d", 64), ContentDigest: "sha256:" + strings.Repeat("e", 64),
		Result: domain.HiveCIReleaseResult{
			ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + strings.Repeat("f", 64),
			Manifest: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: digest,
				MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100,
			},
			SBOM: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: "sha256:" + strings.Repeat("1", 64),
			},
		},
	}
	injected := errors.New("audit signing failed")
	err := registry.RegisterReleaseArtifactWithAudit(
		context.Background(), build, artifact,
		ReleaseArtifactVerificationProof{Release: release, VerifiedAt: time.Now().UTC()},
		func(*domain.Artifact) (*repository.NostrEventRecord, error) { return nil, injected },
	)
	if !errors.Is(err, injected) {
		t.Fatalf("error=%v, want injected audit failure", err)
	}
	if len(builds.builds) != 0 || len(artifacts.artifacts) != 0 {
		t.Fatalf("audit failure leaked registration: builds=%d artifacts=%d", len(builds.builds), len(artifacts.artifacts))
	}
}

func TestRegisterReleaseArtifactOutboxFailureRollsBackBuildAndArtifact(t *testing.T) {
	serviceID := uuid.New()
	repositoryName := "harbor.example/team/bahia"
	services, environments := newMockServiceRepo(), newMockEnvRepo()
	builds, artifacts := newMockBuildRepo(), newMockArtifactRepo()
	intents, state := newMockIntentRepo(), newMockStateRepo()
	services.services[serviceID] = &domain.Service{ID: serviceID, ArtifactRepo: repositoryName}
	registry := newReleaseDecisionRegistry(services, environments, builds, artifacts, intents, state)
	injected := errors.New("audit outbox unavailable")
	registry.txExecutor.(*releaseDecisionTxExecutor).events = &releaseDecisionEventRepo{recordErr: injected}

	digest := "sha256:" + strings.Repeat("a", 64)
	build := &domain.Build{
		ServiceID: serviceID, GitSHA: strings.Repeat("b", 40), CISystem: domain.CISystemHiveCI,
		CIRunID: strings.Repeat("c", 64), Status: domain.BuildStatusSucceeded,
	}
	artifact := &domain.Artifact{ServiceID: serviceID, ImageRepo: repositoryName, ImageDigest: digest}
	release := domain.HiveCIAcceptedRelease{
		ResultEventID: strings.Repeat("d", 64), ContentDigest: "sha256:" + strings.Repeat("e", 64),
		Result: domain.HiveCIReleaseResult{
			ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + strings.Repeat("f", 64),
			Manifest: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: digest,
				MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100,
			},
			SBOM: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: "sha256:" + strings.Repeat("1", 64),
			},
		},
	}
	err := registry.RegisterReleaseArtifactWithAudit(
		context.Background(), build, artifact,
		ReleaseArtifactVerificationProof{Release: release, VerifiedAt: time.Now().UTC()},
		func(*domain.Artifact) (*repository.NostrEventRecord, error) {
			return &repository.NostrEventRecord{ID: strings.Repeat("2", 64)}, nil
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("error=%v, want injected outbox failure", err)
	}
	if len(builds.builds) != 0 || len(artifacts.artifacts) != 0 {
		t.Fatalf("outbox failure leaked registration: builds=%d artifacts=%d", len(builds.builds), len(artifacts.artifacts))
	}
}

func TestRelayFirstRegistryForwardsAtomicAcceptedReleaseRegistration(t *testing.T) {
	serviceID := uuid.New()
	repositoryName := "harbor.example/team/bahia"
	services, environments := newMockServiceRepo(), newMockEnvRepo()
	builds, artifacts := newMockBuildRepo(), newMockArtifactRepo()
	intents, state := newMockIntentRepo(), newMockStateRepo()
	services.services[serviceID] = &domain.Service{ID: serviceID, ArtifactRepo: repositoryName}
	base := newReleaseDecisionRegistry(services, environments, builds, artifacts, intents, state)
	relayFirst := NewRelayFirstRegistry(base, nil, nil, zap.NewNop())

	digest := "sha256:" + strings.Repeat("a", 64)
	build := &domain.Build{
		ServiceID: serviceID, GitSHA: strings.Repeat("b", 40), CISystem: domain.CISystemHiveCI,
		CIRunID: strings.Repeat("c", 64), Status: domain.BuildStatusSucceeded,
	}
	artifact := &domain.Artifact{ServiceID: serviceID, ImageRepo: repositoryName, ImageDigest: digest}
	release := domain.HiveCIAcceptedRelease{
		ResultEventID: strings.Repeat("d", 64), ContentDigest: "sha256:" + strings.Repeat("e", 64),
		Result: domain.HiveCIReleaseResult{
			ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + strings.Repeat("f", 64),
			Manifest: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: digest,
				MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100,
			},
			SBOM: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: "sha256:" + strings.Repeat("1", 64),
			},
		},
	}
	err := relayFirst.RegisterReleaseArtifactWithAudit(
		context.Background(), build, artifact,
		ReleaseArtifactVerificationProof{Release: release, VerifiedAt: time.Now().UTC()},
		func(committed *domain.Artifact) (*repository.NostrEventRecord, error) {
			if committed.ID == uuid.Nil {
				t.Fatal("audit prepared before artifact received its durable identity")
			}
			return &repository.NostrEventRecord{ID: strings.Repeat("2", 64)}, nil
		},
	)
	if err != nil {
		t.Fatalf("relay-first accepted release registration: %v", err)
	}
	if len(builds.builds) != 1 || len(artifacts.artifacts) != 1 || artifact.ID == uuid.Nil {
		t.Fatalf("forwarded registration did not commit atomically: builds=%d artifacts=%d artifact=%+v",
			len(builds.builds), len(artifacts.artifacts), artifact)
	}
}

func TestCreatePromotionAuditFailureRollsBackIntentAndDesiredState(t *testing.T) {
	serviceID, environmentID, artifactID := uuid.New(), uuid.New(), uuid.New()
	services, environments := newMockServiceRepo(), newMockEnvRepo()
	builds, artifacts := newMockBuildRepo(), newMockArtifactRepo()
	intents, state := newMockIntentRepo(), newMockStateRepo()
	services.services[serviceID] = &domain.Service{ID: serviceID}
	environments.envs[environmentID] = &domain.Environment{ID: environmentID}
	artifacts.artifacts[artifactID] = &domain.Artifact{ID: artifactID, ServiceID: serviceID}
	registry := newReleaseDecisionRegistry(services, environments, builds, artifacts, intents, state)
	intent := &domain.DeploymentIntent{
		ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID,
		Status: domain.IntentStatusApproved, ApprovalStatus: domain.ApprovalStatusNotRequired,
	}
	injected := errors.New("audit signing failed")
	err := registry.CreateDeploymentIntentWithAudit(context.Background(), intent, func() (*repository.NostrEventRecord, error) {
		return nil, injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error=%v, want injected audit failure", err)
	}
	if len(intents.intents) != 0 || len(state.states) != 0 {
		t.Fatalf("audit failure leaked promotion: intents=%d states=%d", len(intents.intents), len(state.states))
	}
}

func TestProtectedPromotionDecisionAuditFailureRollsBackApprovalAndRejection(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "reject"}[approve], func(t *testing.T) {
			serviceID, environmentID, artifactID, intentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			services, environments := newMockServiceRepo(), newMockEnvRepo()
			builds, artifacts := newMockBuildRepo(), newMockArtifactRepo()
			intents, state := newMockIntentRepo(), newMockStateRepo()
			services.services[serviceID] = &domain.Service{ID: serviceID}
			environments.envs[environmentID] = &domain.Environment{ID: environmentID, Protected: true}
			artifacts.artifacts[artifactID] = &domain.Artifact{ID: artifactID, ServiceID: serviceID}
			intents.intents[intentID] = &domain.DeploymentIntent{
				ID: intentID, ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID,
				Status: domain.IntentStatusPending, ApprovalStatus: domain.ApprovalStatusPending,
			}
			registry := newReleaseDecisionRegistry(services, environments, builds, artifacts, intents, state)
			injected := errors.New("audit signing failed")
			err := registry.DecideDeploymentIntentWithAudit(context.Background(), intentID, approve, func() (*repository.NostrEventRecord, error) {
				return nil, injected
			})
			if !errors.Is(err, injected) {
				t.Fatalf("error=%v, want injected audit failure", err)
			}
			got := intents.intents[intentID]
			if got.Status != domain.IntentStatusPending || got.ApprovalStatus != domain.ApprovalStatusPending || len(state.states) != 0 {
				t.Fatalf("audit failure leaked protected decision: intent=%+v states=%d", got, len(state.states))
			}
		})
	}
}
