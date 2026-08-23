package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	relayFirstCanonicalStateKind = kinds.CASControlState
	relayFirstStateSchema        = "bahia.cp-state.v1"
)

// RelayFirstPublisher publishes signed Nostr events and returns the number of relay OK acceptances.
type RelayFirstPublisher interface {
	Publish(ctx context.Context, ev gonostr.Event) (int, error)
}

// RelayFirstSigner signs outbound canonical registry events.
type RelayFirstSigner interface {
	Sign(ctx context.Context, ev *gonostr.Event) error
}

// RelayFirstPrivateKeySigner signs registry events with a Nostr hex private key.
type RelayFirstPrivateKeySigner string

func (s RelayFirstPrivateKeySigner) Sign(_ context.Context, ev *gonostr.Event) error {
	privateKey := strings.TrimSpace(string(s))
	if privateKey == "" {
		return fmt.Errorf("nostr registry signer private key is not configured")
	}
	if ev == nil {
		return fmt.Errorf("nostr registry event is nil")
	}
	secret, err := gonostr.SecretKeyFromHex(privateKey)
	if err != nil {
		return fmt.Errorf("decode nostr registry signer private key: %w", err)
	}
	return ev.Sign(secret)
}

// RelayFirstRegistry wraps RegistryService so canonical relay publication succeeds before local cache writes.
type RelayFirstRegistry struct {
	delegate  *RegistryService
	publisher RelayFirstPublisher
	signer    RelayFirstSigner
	logger    *zap.Logger
}

func NewRelayFirstRegistry(delegate *RegistryService, publisher RelayFirstPublisher, signer RelayFirstSigner, logger *zap.Logger) *RelayFirstRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RelayFirstRegistry{delegate: delegate, publisher: publisher, signer: signer, logger: logger}
}

func (r *RelayFirstRegistry) CreateService(ctx context.Context, svc *domain.Service) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	if svc.RuntimeType == "" {
		svc.RuntimeType = domain.RuntimeTypeDocker
	}
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	normalizeServiceRepositoryForWrite(svc)
	if err := r.publishServiceRegistry(ctx, svc, false); err != nil {
		return err
	}
	return r.delegate.CreateService(ctx, svc)
}

func (r *RelayFirstRegistry) UpdateService(ctx context.Context, svc *domain.Service) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	normalizeServiceRepositoryForWrite(svc)
	if err := r.publishServiceRegistry(ctx, svc, false); err != nil {
		return err
	}
	return r.delegate.UpdateService(ctx, svc)
}

func (r *RelayFirstRegistry) DeleteService(ctx context.Context, id uuid.UUID, force bool) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if !force {
		if pgRepo, ok := r.delegate.services.(*repository.PgServiceRepository); ok {
			builds, artifacts, intents, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			if total := builds + artifacts + intents; total > 0 {
				return fmt.Errorf("service has dependent resources (%d builds, %d artifacts, %d deployment intents); use force=true to cascade delete or remove dependents first", builds, artifacts, intents)
			}
		}
	}
	if err := r.publishServiceRegistry(ctx, &domain.Service{ID: id, UpdatedAt: time.Now().UTC()}, true); err != nil {
		return err
	}
	return r.delegate.DeleteService(ctx, id, force)
}

func (r *RelayFirstRegistry) CreateEnvironment(ctx context.Context, env *domain.Environment) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	if env.DeployStrategy == "" {
		env.DeployStrategy = domain.DeployStrategyReplace
	}
	if err := r.publishEnvironmentRegistry(ctx, env, false); err != nil {
		return err
	}
	return r.delegate.CreateEnvironment(ctx, env)
}

// CreateEnvironmentWithDeploymentUnits publishes the complete desired environment contract before atomically caching it.
func (r *RelayFirstRegistry) CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	if err := r.publishEnvironmentRegistryWithUnits(ctx, env, units, false); err != nil {
		return err
	}
	return r.delegate.CreateEnvironmentWithDeploymentUnits(ctx, env, units)
}

func (r *RelayFirstRegistry) UpdateEnvironment(ctx context.Context, env *domain.Environment) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	if err := r.publishEnvironmentRegistry(ctx, env, false); err != nil {
		return err
	}
	return r.delegate.UpdateEnvironment(ctx, env)
}

// UpdateEnvironmentWithDeploymentUnits publishes the complete desired environment
// contract after the revision has been checked under lock and before atomically
// caching it.
func (r *RelayFirstRegistry) UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	return r.delegate.updateEnvironmentWithDeploymentUnits(ctx, env, units, expectedUpdatedAt, func() error {
		return r.publishEnvironmentRegistryWithUnits(ctx, env, units, false)
	})
}

func (r *RelayFirstRegistry) DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if !force {
		if pgRepo, ok := r.delegate.environments.(*repository.PgEnvironmentRepository); ok {
			intents, states, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			if total := intents + states; total > 0 {
				return fmt.Errorf("environment has dependent resources (%d deployment intents, %d state records); use force=true to cascade delete or remove dependents first", intents, states)
			}
		}
	}
	if err := r.publishEnvironmentRegistry(ctx, &domain.Environment{ID: id, UpdatedAt: time.Now().UTC()}, true); err != nil {
		return err
	}
	return r.delegate.DeleteEnvironment(ctx, id, force)
}

func (r *RelayFirstRegistry) publishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	content := map[string]any{"deleted": deleted, "id": svc.ID.String()}
	if !deleted {
		content["org_id"] = svc.OrgID.String()
		content["name"] = svc.Name
		content["repo_url"] = svc.RepoURL
		if svc.Repository != nil {
			content["repository"] = svc.Repository
		}
		content["artifact_repo"] = svc.ArtifactRepo
		content["default_branch"] = svc.DefaultBranch
		content["runtime_type"] = string(svc.RuntimeType)
		content["created_at"] = relayFirstFormatTime(svc.CreatedAt)
		content["updated_at"] = relayFirstFormatTime(svc.UpdatedAt)
	} else {
		content["updated_at"] = relayFirstFormatTime(svc.UpdatedAt)
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("encode service registry event: %w", err)
	}
	tags := relayFirstCanonicalStateTags("service", "registry", svc.ID.String(), deleted)
	if !deleted {
		tags = append(tags, gonostr.Tag{"name", svc.Name}, gonostr.Tag{"runtime", string(svc.RuntimeType)})
	}
	return r.publishCanonical(ctx, relayFirstCanonicalStateKind, tags, string(contentJSON), "service registry")
}

func (r *RelayFirstRegistry) publishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	return r.publishEnvironmentRegistryWithUnits(ctx, env, nil, deleted)
}

func (r *RelayFirstRegistry) publishEnvironmentRegistryWithUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, deleted bool) error {
	content := map[string]any{"deleted": deleted, "id": env.ID.String()}
	if !deleted {
		if env.OrgID != uuid.Nil {
			content["org_id"] = env.OrgID.String()
		}
		content["name"] = env.Name
		content["loom_worker_selector"] = env.LoomWorkerSelector
		content["runtime_config"] = env.RuntimeConfig
		content["targeting"] = env.Targeting
		content["protected"] = env.Protected
		content["deploy_strategy"] = string(env.DeployStrategy)
		if units != nil {
			content["deployment_units"] = units
		}
		content["created_at"] = relayFirstFormatTime(env.CreatedAt)
		content["updated_at"] = relayFirstFormatTime(env.UpdatedAt)
	} else {
		content["updated_at"] = relayFirstFormatTime(env.UpdatedAt)
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("encode environment registry event: %w", err)
	}
	tags := relayFirstCanonicalStateTags("environment", "registry", env.ID.String(), deleted)
	if !deleted {
		tags = append(tags, gonostr.Tag{"name", env.Name}, gonostr.Tag{"protected", fmt.Sprintf("%t", env.Protected)})
	}
	return r.publishCanonical(ctx, relayFirstCanonicalStateKind, tags, string(contentJSON), "environment registry")
}

func (r *RelayFirstRegistry) publishCanonical(ctx context.Context, kind int, tags gonostr.Tags, content, label string) error {
	if r.publisher == nil {
		return fmt.Errorf("nostr registry publisher is not configured")
	}
	if r.signer == nil {
		return fmt.Errorf("nostr registry signer is not configured")
	}
	ev := gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Now(), Tags: tags, Content: content}
	if err := r.signer.Sign(ctx, &ev); err != nil {
		return fmt.Errorf("sign %s event: %w", label, err)
	}
	published, err := r.publisher.Publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publish %s event: %w", label, err)
	}
	if published == 0 {
		return fmt.Errorf("publish %s event: no relay accepted the event", label)
	}
	r.logger.Debug("relay-first registry event published", zap.Int("kind", kind), zap.String("event_id", ev.ID.Hex()), zap.Int("relays", published))
	return nil
}

func relayFirstCanonicalStateTags(domain, entity, dTag string, deleted bool) gonostr.Tags {
	return gonostr.Tags{
		{"d", dTag},
		{"domain", domain},
		{"entity", entity},
		{"schema", relayFirstStateSchema},
		{"deleted", fmt.Sprintf("%t", deleted)},
	}
}

func relayFirstFormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (r *RelayFirstRegistry) GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return r.delegate.GetService(ctx, id)
}
func (r *RelayFirstRegistry) GetServiceByName(ctx context.Context, name string) (*domain.Service, error) {
	return r.delegate.GetServiceByName(ctx, name)
}
func (r *RelayFirstRegistry) ListServices(ctx context.Context) ([]domain.Service, error) {
	return r.delegate.ListServices(ctx)
}
func (r *RelayFirstRegistry) ListServicesByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	return r.delegate.ListServicesByOrg(ctx, orgID)
}
func (r *RelayFirstRegistry) GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.delegate.GetEnvironment(ctx, id)
}
func (r *RelayFirstRegistry) GetEnvironmentByName(ctx context.Context, name string) (*domain.Environment, error) {
	return r.delegate.GetEnvironmentByName(ctx, name)
}
func (r *RelayFirstRegistry) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	return r.delegate.ListEnvironments(ctx)
}
func (r *RelayFirstRegistry) ListEnvironmentsByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	return r.delegate.ListEnvironmentsByOrg(ctx, orgID)
}
func (r *RelayFirstRegistry) RegisterBuild(ctx context.Context, b *domain.Build) error {
	return r.delegate.RegisterBuild(ctx, b)
}
func (r *RelayFirstRegistry) GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	return r.delegate.GetBuild(ctx, id)
}
func (r *RelayFirstRegistry) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	return r.delegate.ListBuilds(ctx, serviceID, limit, offset)
}
func (r *RelayFirstRegistry) UpdateBuildStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	return r.delegate.UpdateBuildStatus(ctx, id, status)
}
func (r *RelayFirstRegistry) RegisterArtifact(ctx context.Context, a *domain.Artifact) error {
	return r.delegate.RegisterArtifact(ctx, a)
}
func (r *RelayFirstRegistry) RegisterVerifiedArtifact(ctx context.Context, a *domain.Artifact, proof ArtifactVerificationProof) error {
	return r.delegate.RegisterVerifiedArtifact(ctx, a, proof)
}
func (r *RelayFirstRegistry) RegisterReleaseArtifact(ctx context.Context, a *domain.Artifact, proof ReleaseArtifactVerificationProof) error {
	return r.delegate.RegisterReleaseArtifact(ctx, a, proof)
}

// RegisterReleaseArtifactWithAudit preserves the atomic build/artifact/audit
// boundary used by Hive-CI registration. Release artifacts have no separate
// relay-first canonical state event; their signed decision evidence is written
// to the delegate's durable Nostr outbox in the same transaction.
func (r *RelayFirstRegistry) RegisterReleaseArtifactWithAudit(
	ctx context.Context,
	build *domain.Build,
	artifact *domain.Artifact,
	proof ReleaseArtifactVerificationProof,
	prepareAudit ReleaseArtifactAuditPreparer,
) error {
	if r == nil || r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	return r.delegate.RegisterReleaseArtifactWithAudit(ctx, build, artifact, proof, prepareAudit)
}
func (r *RelayFirstRegistry) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return r.delegate.GetArtifact(ctx, id)
}
func (r *RelayFirstRegistry) GetArtifactByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error) {
	return r.delegate.GetArtifactByDigest(ctx, repo, digest)
}
func (r *RelayFirstRegistry) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	return r.delegate.ListArtifacts(ctx, serviceID, limit, offset)
}
func (r *RelayFirstRegistry) ListArtifactsByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	return r.delegate.ListArtifactsByBuild(ctx, buildID)
}
func (r *RelayFirstRegistry) CreateDeploymentIntent(ctx context.Context, di *domain.DeploymentIntent) error {
	return r.delegate.CreateDeploymentIntent(ctx, di)
}
func (r *RelayFirstRegistry) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.delegate.GetDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	return r.delegate.ListDeploymentIntents(ctx, serviceID, envID, limit, offset)
}
func (r *RelayFirstRegistry) ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	return r.delegate.ApproveDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	return r.delegate.RejectDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) CreateDeploymentRun(ctx context.Context, dr *domain.DeploymentRun) error {
	return r.delegate.CreateDeploymentRun(ctx, dr)
}
func (r *RelayFirstRegistry) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return r.delegate.GetDeploymentRun(ctx, id)
}
func (r *RelayFirstRegistry) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	return r.delegate.ListDeploymentRuns(ctx, intentID)
}
func (r *RelayFirstRegistry) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	return r.delegate.CompleteDeploymentRun(ctx, id, status, exitCode)
}
func (r *RelayFirstRegistry) Rollback(ctx context.Context, serviceID, envID uuid.UUID, requestedBy string) (*domain.DeploymentIntent, error) {
	return r.delegate.Rollback(ctx, serviceID, envID, requestedBy)
}
func (r *RelayFirstRegistry) RecordObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	return r.delegate.RecordObservation(ctx, obs)
}
func (r *RelayFirstRegistry) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.delegate.GetLatestObservation(ctx, serviceID, envID)
}
func (r *RelayFirstRegistry) GetEnvironmentServiceState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return r.delegate.GetEnvironmentServiceState(ctx, serviceID, envID)
}
func (r *RelayFirstRegistry) ListEnvironmentStates(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListEnvironmentStates(ctx, envID)
}
func (r *RelayFirstRegistry) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListDriftedStates(ctx)
}
func (r *RelayFirstRegistry) ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListAllStates(ctx)
}
