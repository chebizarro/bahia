package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	adoptionCISystem      = "adoption"
	adoptionImportedEvent = events.EventAdoptionImported
	adoptionStatusCreated = "created"
	adoptionStatusUpdated = "updated"
	adoptionStatusFailed  = "failed"
)

// AdoptionService scans Docker hosts and imports existing containers into Bahia models.
type AdoptionService struct {
	registry          *RegistryService
	services          repository.ServiceRepository
	environments      repository.EnvironmentRepository
	builds            repository.BuildRepository
	artifacts         repository.ArtifactRepository
	state             repository.EnvironmentServiceStateRepository
	observations      repository.RuntimeObservationRepository
	secrets           repository.SecretRepository
	organizations     repository.OrganizationRepository
	adoptedIdentities repository.AdoptedRuntimeIdentityRepository
	txExecutor        repository.TxExecutor
	publisher         events.Publisher
	logger            *zap.Logger

	secretEncryptor      *secretsAdapter.Encryptor
	runtimeCfg           config.RuntimeConfig
	allowRawDockerHosts  bool
	allowComposeTakeover bool
}

// AdoptionServiceOption configures adoption runtime governance behavior.
type AdoptionServiceOption func(*AdoptionService)

// WithAdoptionRuntimeConfig enables server-managed endpoint resolution and raw-host policy.
func WithAdoptionRuntimeConfig(runtimeCfg config.RuntimeConfig, allowRawDockerHosts bool) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.runtimeCfg = runtimeCfg
		s.allowRawDockerHosts = allowRawDockerHosts
	}
}

// WithAdoptionComposeTakeoverPolicy controls whether Compose-origin containers
// may be imported into Bahia's direct Docker runtime management mode.
func WithAdoptionComposeTakeoverPolicy(allow bool) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.allowComposeTakeover = allow
	}
}

// WithAdoptionSecrets wires imported sensitive environment values into Bahia's existing secrets path.
func WithAdoptionSecrets(repo repository.SecretRepository, encryptor *secretsAdapter.Encryptor) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.secrets = repo
		s.secretEncryptor = encryptor
	}
}

// WithAdoptionTxExecutor makes per-candidate import persistence atomic.
func WithAdoptionTxExecutor(txExecutor repository.TxExecutor) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.txExecutor = txExecutor
	}
}

// WithAdoptionOrganizations enables org ownership resolution for imported resources.
func WithAdoptionOrganizations(repo repository.OrganizationRepository) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.organizations = repo
	}
}

// WithAdoptionRuntimeIdentities enables persistent adopted workload fingerprint matching.
func WithAdoptionRuntimeIdentities(repo repository.AdoptedRuntimeIdentityRepository) AdoptionServiceOption {
	return func(s *AdoptionService) {
		s.adoptedIdentities = repo
	}
}

// NewAdoptionService creates an AdoptionService.
func NewAdoptionService(
	registry *RegistryService,
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	builds repository.BuildRepository,
	artifacts repository.ArtifactRepository,
	state repository.EnvironmentServiceStateRepository,
	observations repository.RuntimeObservationRepository,
	publisher events.Publisher,
	logger *zap.Logger,
	opts ...AdoptionServiceOption,
) *AdoptionService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	svc := &AdoptionService{
		registry:             registry,
		services:             services,
		environments:         environments,
		builds:               builds,
		artifacts:            artifacts,
		state:                state,
		observations:         observations,
		publisher:            publisher,
		logger:               logger,
		allowRawDockerHosts:  true,
		allowComposeTakeover: true,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// AdoptionScanRequest requests a scan of one or more Docker targets.
type AdoptionScanRequest struct {
	Targets []AdoptionTarget
}

// AdoptionTarget identifies a Docker host and target environment.
type AdoptionTarget struct {
	Name            string
	DockerHost      string
	EndpointRef     string
	Endpoint        config.RuntimeEndpointConfig
	EnvironmentName string
}

// AdoptionImportRequest imports selected discovered containers.
type AdoptionImportRequest struct {
	Targets    []AdoptionTarget
	Selections []AdoptionSelection
	ImportAll  bool
	OrgID      uuid.UUID
}

// AdoptionSelection selects one container on a target host.
type AdoptionSelection struct {
	TargetName          string
	ContainerID         string
	ServiceNameOverride string
}

// AdoptionPreview groups discovered containers for one target.
type AdoptionPreview struct {
	Target     AdoptionTarget
	Containers []AdoptionPreviewContainer
	Error      string
}

// AdoptionPreviewContainer is one discovered container plus Bahia import proposal metadata.
type AdoptionPreviewContainer struct {
	Discovered              runtime.DiscoveredContainer
	ProposedServiceName     string
	ExistingServiceID       *uuid.UUID
	WillUpdate              bool
	Warnings                []string
	Adoptable               bool
	SafeEnvironment         map[string]string
	SafeLabels              map[string]string
	RedactedEnvironmentKeys []string
	RedactedLabelKeys       []string
}

// AdoptionImportResult reports per-candidate import outcome.
type AdoptionImportResult struct {
	TargetName              string
	ContainerID             string
	ContainerName           string
	ServiceName             string
	ServiceID               *uuid.UUID
	EnvironmentID           *uuid.UUID
	BuildID                 *uuid.UUID
	ArtifactID              *uuid.UUID
	Status                  string
	Warnings                []string
	RedactedEnvironmentKeys []string
	RedactedLabelKeys       []string
	Error                   string
}

// Scan discovers containers and proposes Bahia service names.
func (s *AdoptionService) Scan(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error) {
	start := time.Now()
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		s.logger.Warn("adoption scan rejected", zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
		s.logger.Warn("adoption scan discovery failed", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	previews := s.buildPreviews(ctx, targets, results)
	candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrors := adoptionPreviewOperationalStats(previews)
	duration := time.Since(start)
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventAdoptionScanCompleted,
		EntityID: "adoption",
		Data: map[string]any{
			"target_count":             len(targets),
			"candidate_count":          candidateCount,
			"target_error_count":       targetErrors,
			"redacted_env_key_count":   redactedEnvKeyCount,
			"redacted_label_key_count": redactedLabelKeyCount,
			"duration_ms":              duration.Milliseconds(),
		},
	})
	s.logger.Info("adoption scan completed",
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", candidateCount),
		zap.Int("target_error_count", targetErrors),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.String("result", "success"),
	)
	return previews, nil
}

// Import scans targets and imports selected containers. Individual candidate failures are returned in result rows.
func (s *AdoptionService) Import(ctx context.Context, req AdoptionImportRequest) ([]AdoptionImportResult, error) {
	start := time.Now()
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		s.logger.Warn("adoption import rejected", zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	selectionSet, err := normalizeAdoptionSelections(req)
	if err != nil {
		s.logger.Warn("adoption import selections rejected", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	orgID, err := s.resolveImportOrgID(ctx, req.OrgID, targets)
	if err != nil {
		s.logger.Warn("adoption import org resolution rejected", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}

	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
		s.logger.Warn("adoption import discovery failed", zap.Int("target_count", len(targets)), zap.String("result", "failed"), zap.Error(err), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
		return nil, err
	}
	previews := s.buildPreviews(ctx, targets, results)

	var imported []AdoptionImportResult
	processedSelections := map[string]struct{}{}
	for _, preview := range previews {
		if preview.Error != "" {
			if req.ImportAll {
				imported = append(imported, AdoptionImportResult{TargetName: preview.Target.Name, Status: adoptionStatusFailed, Error: preview.Error})
			} else {
				for _, selection := range selectionsForTarget(selectionSet, preview.Target.Name) {
					processedSelections[selectionKey(selection.TargetName, selection.ContainerID)] = struct{}{}
					imported = append(imported, AdoptionImportResult{TargetName: preview.Target.Name, ContainerID: selection.ContainerID, Status: adoptionStatusFailed, Error: preview.Error})
				}
			}
			continue
		}
		for _, container := range preview.Containers {
			key := selectionKey(preview.Target.Name, container.Discovered.ContainerID)
			selection, selected := selectionSet[key]
			if selected {
				processedSelections[key] = struct{}{}
			}
			if !req.ImportAll && !selected {
				continue
			}
			serviceName := container.ProposedServiceName
			if selected && selection.ServiceNameOverride != "" {
				serviceName = normalizeResourceName(selection.ServiceNameOverride)
			}
			imported = append(imported, s.importCandidate(ctx, orgID, preview.Target, container, serviceName))
		}
	}
	if !req.ImportAll {
		for key, selection := range selectionSet {
			if _, ok := processedSelections[key]; ok {
				continue
			}
			imported = append(imported, AdoptionImportResult{TargetName: selection.TargetName, ContainerID: selection.ContainerID, Status: adoptionStatusFailed, Error: "selected container was not discovered"})
		}
	}
	successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionImportOperationalStats(imported)
	result := "success"
	if failureCount > 0 {
		result = "partial_failure"
	}
	if len(imported) > 0 && successCount == 0 && failureCount > 0 {
		result = "failed"
	}
	s.logger.Info("adoption import completed",
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", len(imported)),
		zap.Int("success_count", successCount),
		zap.Int("failure_count", failureCount),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		zap.String("result", result),
	)
	return imported, nil
}

func (s *AdoptionService) buildPreviews(ctx context.Context, targets []AdoptionTarget, results []runtime.DockerDiscoveryResult) []AdoptionPreview {
	previews := make([]AdoptionPreview, len(results))
	usedNames := map[string]int{}
	for i, result := range results {
		target := targets[i]
		preview := AdoptionPreview{Target: target, Error: result.Error}
		if result.Error != "" {
			previews[i] = preview
			continue
		}
		for _, discovered := range result.Containers {
			classified := classifyDiscoveredSensitiveData(discovered)
			proposed := s.proposedServiceName(ctx, target, discovered, usedNames)
			existing, _ := s.services.GetByName(ctx, proposed)
			warnings := append([]string(nil), discovered.Warnings...)
			adoptable := discovered.Adoptable
			if isComposeOrigin(discovered) {
				warnings = append(warnings, "compose-origin workload will be taken over by Bahia direct Docker runtime actions")
				if !s.allowComposeTakeover {
					warnings = append(warnings, "compose takeover is disabled by adoption policy")
					adoptable = false
				}
			}
			candidate := AdoptionPreviewContainer{
				Discovered:              discovered,
				ProposedServiceName:     proposed,
				Warnings:                warnings,
				Adoptable:               adoptable,
				SafeEnvironment:         classified.SafeEnvironment,
				SafeLabels:              classified.SafeLabels,
				RedactedEnvironmentKeys: classified.SensitiveEnvironmentKeys,
				RedactedLabelKeys:       classified.SensitiveLabelKeys,
			}
			if existing != nil {
				candidate.ExistingServiceID = &existing.ID
				candidate.WillUpdate = sameAdoptedTarget(existing, target, discovered)
			}
			preview.Containers = append(preview.Containers, candidate)
		}
		previews[i] = preview
	}
	return previews
}

func (s *AdoptionService) proposedServiceName(ctx context.Context, target AdoptionTarget, discovered runtime.DiscoveredContainer, usedNames map[string]int) string {
	base := proposedServiceName(discovered)
	if base == "" {
		base = "adopted-" + shortID(discovered.ContainerID)
	}
	name := base
	if usedNames[name] > 0 || s.serviceNameConflicts(ctx, name, target, discovered) {
		withEnv := normalizeResourceName(base + "-" + target.EnvironmentName)
		name = withEnv
		if usedNames[name] > 0 || s.serviceNameConflicts(ctx, name, target, discovered) {
			name = normalizeResourceName(base + "-" + shortID(discovered.ContainerID))
		}
	}
	usedNames[name]++
	return name
}

func (s *AdoptionService) serviceNameConflicts(ctx context.Context, name string, target AdoptionTarget, discovered runtime.DiscoveredContainer) bool {
	existing, err := s.services.GetByName(ctx, name)
	if err != nil || existing == nil {
		return false
	}
	return !sameAdoptedTarget(existing, target, discovered)
}

func (s *AdoptionService) importCandidate(ctx context.Context, orgID uuid.UUID, target AdoptionTarget, candidate AdoptionPreviewContainer, serviceName string) AdoptionImportResult {
	discovered := candidate.Discovered
	result := AdoptionImportResult{
		TargetName:              target.Name,
		ContainerID:             discovered.ContainerID,
		ContainerName:           discovered.ContainerName,
		ServiceName:             serviceName,
		Warnings:                append([]string(nil), candidate.Warnings...),
		RedactedEnvironmentKeys: append([]string(nil), candidate.RedactedEnvironmentKeys...),
		RedactedLabelKeys:       append([]string(nil), candidate.RedactedLabelKeys...),
	}
	if !candidate.Adoptable {
		result.Status = adoptionStatusFailed
		result.Error = "container has unsupported adoption warnings"
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}
	if discovered.ImageRepo == "" || discovered.ImageDigest == "" {
		result.Status = adoptionStatusFailed
		result.Error = "container image repo and digest are required for import"
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}
	if serviceName == "" {
		result.Status = adoptionStatusFailed
		result.Error = "service name is required"
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}

	classified := classifyDiscoveredSensitiveData(discovered)
	result.RedactedEnvironmentKeys = append([]string(nil), classified.SensitiveEnvironmentKeys...)
	result.RedactedLabelKeys = append([]string(nil), classified.SensitiveLabelKeys...)
	if len(classified.SensitiveEnvironment) > 0 && (s.secrets == nil || s.secretEncryptor == nil) {
		result.Status = adoptionStatusFailed
		result.Error = "sensitive environment values require configured secret storage and encryption"
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}

	var committedResult AdoptionImportResult
	var importEvent events.Event
	persist := func(repos repository.TxRepos) error {
		stagedResult := result
		repos = s.completeTxRepos(repos)
		registry := s.registryForRepos(repos)

		env, err := s.ensureAdoptionEnvironment(ctx, registry, repos.Environments, orgID, target)
		if err != nil {
			return err
		}
		stagedResult.EnvironmentID = &env.ID

		svc, createdService, err := s.ensureAdoptionService(ctx, registry, repos.Services, orgID, target, discovered, classified, serviceName)
		if err != nil {
			return err
		}
		stagedResult.ServiceID = &svc.ID
		stagedResult.ServiceName = svc.Name

		build, err := s.ensureAdoptionBuild(ctx, repos.Builds, target, discovered, svc.ID)
		if err != nil {
			return err
		}
		stagedResult.BuildID = &build.ID

		artifact, err := s.ensureAdoptionArtifact(ctx, repos.Artifacts, discovered, svc.ID, build.ID)
		if err != nil {
			return err
		}
		stagedResult.ArtifactID = &artifact.ID

		state := &domain.EnvironmentServiceState{
			ServiceID:         svc.ID,
			EnvironmentID:     env.ID,
			DesiredArtifactID: &artifact.ID,
			DriftStatus:       domain.DriftStatusUnknown,
		}
		if err := repos.State.Upsert(ctx, state); err != nil {
			return fmt.Errorf("seeding environment service state: %w", err)
		}

		obs := observationFromDiscovered(target, svc.ID, env.ID, discovered)
		if err := registry.RecordObservation(ctx, obs); err != nil {
			return fmt.Errorf("recording runtime observation: %w", err)
		}

		if err := s.importSensitiveEnvironmentSecrets(ctx, repos.Secrets, svc.ID, env.ID, classified.SensitiveEnvironment); err != nil {
			return err
		}
		if err := s.persistAdoptedRuntimeIdentities(ctx, repos.AdoptedIdentities, orgID, svc.ID, env.ID, target, discovered); err != nil {
			return err
		}

		status := adoptionStatusUpdated
		if createdService {
			status = adoptionStatusCreated
		}
		stagedResult.Status = status
		committedResult = stagedResult
		importEvent = events.Event{
			Type:     adoptionImportedEvent,
			EntityID: svc.ID.String(),
			Data: map[string]any{
				"service_id":     svc.ID,
				"environment_id": env.ID,
				"artifact_id":    artifact.ID,
				"target_name":    target.Name,
				"container_id":   discovered.ContainerID,
				"container_name": discovered.ContainerName,
				"status":         status,
			},
		}
		return nil
	}

	var err error
	if s.txExecutor != nil {
		for attempt := 0; attempt < 2; attempt++ {
			committedResult = AdoptionImportResult{}
			importEvent = events.Event{}
			err = s.txExecutor.WithinTx(ctx, persist)
			if err == nil || !isRetryableImportTxError(err) {
				break
			}
		}
	} else {
		err = persist(s.completeTxRepos(repository.TxRepos{}))
	}
	if err != nil {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		s.logAdoptionImportResult(target, result, "failed")
		return result
	}
	if committedResult.Status != "" {
		result = committedResult
	}
	if importEvent.Type != "" {
		s.publisher.Publish(ctx, importEvent)
	}
	s.logAdoptionImportResult(target, result, "success")
	return result
}

func (s *AdoptionService) logAdoptionImportResult(target AdoptionTarget, result AdoptionImportResult, outcome string) {
	fields := []zap.Field{
		zap.String("target_name", target.Name),
		zap.String("endpoint_ref", target.EndpointRef),
		zap.String("environment_name", target.EnvironmentName),
		zap.String("container_id", result.ContainerID),
		zap.String("container_name", result.ContainerName),
		zap.String("service_name", result.ServiceName),
		zap.String("status", result.Status),
		zap.String("result", outcome),
		zap.Int("redacted_env_key_count", len(result.RedactedEnvironmentKeys)),
		zap.Int("redacted_label_key_count", len(result.RedactedLabelKeys)),
	}
	if result.ServiceID != nil {
		fields = append(fields, zap.String("service_id", result.ServiceID.String()))
	}
	if result.EnvironmentID != nil {
		fields = append(fields, zap.String("environment_id", result.EnvironmentID.String()))
	}
	if result.ArtifactID != nil {
		fields = append(fields, zap.String("artifact_id", result.ArtifactID.String()))
	}
	if result.Error != "" {
		fields = append(fields, zap.String("error", result.Error))
	}
	s.logger.Info("adoption candidate import completed", fields...)
}

func isRetryableImportTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "23505", "40001", "40P01":
		return true
	default:
		return false
	}
}

func adoptionPreviewOperationalStats(previews []AdoptionPreview) (candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrorCount int) {
	for _, preview := range previews {
		if preview.Error != "" {
			targetErrorCount++
		}
		candidateCount += len(preview.Containers)
		for _, container := range preview.Containers {
			redactedEnvKeyCount += len(container.RedactedEnvironmentKeys)
			redactedLabelKeyCount += len(container.RedactedLabelKeys)
		}
	}
	return candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, targetErrorCount
}

func adoptionImportOperationalStats(results []AdoptionImportResult) (successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount int) {
	for _, result := range results {
		if result.Status == adoptionStatusFailed || result.Error != "" {
			failureCount++
		} else {
			successCount++
		}
		redactedEnvKeyCount += len(result.RedactedEnvironmentKeys)
		redactedLabelKeyCount += len(result.RedactedLabelKeys)
	}
	return successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount
}

func (s *AdoptionService) completeTxRepos(repos repository.TxRepos) repository.TxRepos {
	if repos.Services == nil {
		repos.Services = s.services
	}
	if repos.Environments == nil {
		repos.Environments = s.environments
	}
	if repos.Builds == nil {
		repos.Builds = s.builds
	}
	if repos.Artifacts == nil {
		repos.Artifacts = s.artifacts
	}
	if repos.State == nil {
		repos.State = s.state
	}
	if repos.Observations == nil {
		repos.Observations = s.observations
	}
	if repos.Secrets == nil {
		repos.Secrets = s.secrets
	}
	if repos.AdoptedIdentities == nil {
		repos.AdoptedIdentities = s.adoptedIdentities
	}
	return repos
}

func (s *AdoptionService) registryForRepos(repos repository.TxRepos) *RegistryService {
	return NewRegistryService(
		repos.Services,
		repos.Environments,
		repos.Builds,
		repos.Artifacts,
		nil,
		nil,
		repos.Observations,
		repos.State,
		nil,
		&events.NoopPublisher{},
		s.logger,
	)
}

func (s *AdoptionService) resolveImportOrgID(ctx context.Context, requested uuid.UUID, targets []AdoptionTarget) (uuid.UUID, error) {
	if s.organizations == nil {
		return requested, nil
	}
	if requested != uuid.Nil {
		org, err := s.organizations.GetByID(ctx, requested)
		if err != nil {
			return uuid.Nil, fmt.Errorf("resolving adoption org_id %s: %w", requested, err)
		}
		if org == nil {
			return uuid.Nil, fmt.Errorf("resolving adoption org_id %s: %w", requested, repository.ErrNotFound)
		}
		return requested, nil
	}

	resolvedFromEnvironments := map[uuid.UUID]struct{}{}
	for _, target := range targets {
		env, err := s.environments.GetByName(ctx, target.EnvironmentName)
		if err != nil {
			return uuid.Nil, fmt.Errorf("looking up environment %q for org resolution: %w", target.EnvironmentName, err)
		}
		if env != nil && env.OrgID != uuid.Nil {
			resolvedFromEnvironments[env.OrgID] = struct{}{}
		}
	}
	if len(resolvedFromEnvironments) == 1 {
		for orgID := range resolvedFromEnvironments {
			return orgID, nil
		}
	}
	if len(resolvedFromEnvironments) > 1 {
		return uuid.Nil, fmt.Errorf("adoption import org_id is required because target environments resolve to multiple orgs")
	}

	orgs, err := s.organizations.List(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("listing organizations for adoption org resolution: %w", err)
	}
	if len(orgs) == 1 {
		return orgs[0].ID, nil
	}
	if len(orgs) == 0 {
		return uuid.Nil, fmt.Errorf("adoption import requires org_id because no organization is available for inference")
	}
	return uuid.Nil, fmt.Errorf("adoption import requires org_id because %d organizations are available", len(orgs))
}

func (s *AdoptionService) ensureAdoptionEnvironment(ctx context.Context, registry *RegistryService, environments repository.EnvironmentRepository, orgID uuid.UUID, target AdoptionTarget) (*domain.Environment, error) {
	existing, err := environments.GetByName(ctx, target.EnvironmentName)
	if err != nil {
		return nil, fmt.Errorf("looking up environment %q: %w", target.EnvironmentName, err)
	}
	config := map[string]any{
		"type":            string(domain.RuntimeTypeDocker),
		"host_alias":      target.Name,
		"management_mode": "direct_runtime",
	}
	if target.EndpointRef != "" {
		config["endpoint_ref"] = target.EndpointRef
	} else {
		config["docker_host"] = target.DockerHost
	}
	if existing == nil {
		env := &domain.Environment{
			OrgID:         orgID,
			Name:          target.EnvironmentName,
			RuntimeConfig: config,
		}
		if err := registry.CreateEnvironment(ctx, env); err != nil {
			return nil, fmt.Errorf("creating environment %q: %w", target.EnvironmentName, err)
		}
		return env, nil
	}

	if existing.OrgID != uuid.Nil && existing.OrgID != orgID {
		return nil, fmt.Errorf("environment %q belongs to different org %s", target.EnvironmentName, existing.OrgID)
	}
	changed := false
	if existing.OrgID == uuid.Nil {
		existing.OrgID = orgID
		changed = true
	}
	if existing.RuntimeConfig == nil {
		existing.RuntimeConfig = map[string]any{}
	}
	if currentType, ok := stringFromAny(existing.RuntimeConfig["type"]); ok && currentType != "" && currentType != string(domain.RuntimeTypeDocker) {
		return nil, fmt.Errorf("environment %q has incompatible runtime type %q", target.EnvironmentName, currentType)
	}
	if currentMode, ok := stringFromAny(existing.RuntimeConfig["management_mode"]); ok && currentMode != "" && currentMode != "direct_runtime" {
		return nil, fmt.Errorf("environment %q has incompatible management_mode %q", target.EnvironmentName, currentMode)
	}
	if currentEndpointRef, ok := stringFromAny(existing.RuntimeConfig["endpoint_ref"]); ok && currentEndpointRef != "" && currentEndpointRef != target.EndpointRef {
		return nil, fmt.Errorf("environment %q already targets endpoint_ref %q", target.EnvironmentName, currentEndpointRef)
	}
	if currentHost, ok := stringFromAny(existing.RuntimeConfig["docker_host"]); ok && currentHost != "" && currentHost != target.DockerHost {
		return nil, fmt.Errorf("environment %q already targets docker_host %q", target.EnvironmentName, currentHost)
	}
	if target.EndpointRef != "" {
		if _, ok := existing.RuntimeConfig["docker_host"]; ok {
			delete(existing.RuntimeConfig, "docker_host")
			changed = true
		}
	} else if _, ok := existing.RuntimeConfig["endpoint_ref"]; ok {
		delete(existing.RuntimeConfig, "endpoint_ref")
		changed = true
	}
	for k, v := range config {
		if existing.RuntimeConfig[k] != v {
			existing.RuntimeConfig[k] = v
			changed = true
		}
	}
	if changed {
		if err := registry.UpdateEnvironment(ctx, existing); err != nil {
			return nil, fmt.Errorf("updating environment %q: %w", target.EnvironmentName, err)
		}
	}
	return existing, nil
}

func (s *AdoptionService) ensureAdoptionService(ctx context.Context, registry *RegistryService, services repository.ServiceRepository, orgID uuid.UUID, target AdoptionTarget, discovered runtime.DiscoveredContainer, classified sensitiveDataClassification, serviceName string) (*domain.Service, bool, error) {
	adopted := adoptedRuntimeConfig(target, discovered, classified)
	byIdentity, err := s.findServiceByAdoptedTarget(ctx, services, orgID, target, discovered)
	if err != nil {
		return nil, false, err
	}
	byName, err := services.GetByName(ctx, serviceName)
	if err != nil {
		return nil, false, fmt.Errorf("looking up service %q: %w", serviceName, err)
	}
	if byName != nil && byName.OrgID != uuid.Nil && byName.OrgID != orgID {
		return nil, false, fmt.Errorf("service name %q belongs to different org %s", serviceName, byName.OrgID)
	}
	if byName != nil && byIdentity != nil && byName.ID != byIdentity.ID {
		return nil, false, fmt.Errorf("service name %q already exists for a different target", serviceName)
	}
	if byName != nil && byIdentity == nil && !sameAdoptedTarget(byName, target, discovered) {
		return nil, false, fmt.Errorf("service name %q already exists for a different target", serviceName)
	}

	existing := byIdentity
	if existing == nil {
		existing = byName
	}
	if existing == nil {
		svc := &domain.Service{
			OrgID:         orgID,
			Name:          serviceName,
			ArtifactRepo:  discovered.ImageRepo,
			RuntimeType:   domain.RuntimeTypeDocker,
			RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adopted},
		}
		if err := registry.CreateService(ctx, svc); err != nil {
			return nil, false, fmt.Errorf("creating service %q: %w", serviceName, err)
		}
		return svc, true, nil
	}
	if existing.OrgID != uuid.Nil && existing.OrgID != orgID {
		return nil, false, fmt.Errorf("service %q belongs to different org %s", existing.Name, existing.OrgID)
	}
	existing.OrgID = orgID
	existing.RuntimeType = domain.RuntimeTypeDocker
	existing.ArtifactRepo = discovered.ImageRepo
	existing.RuntimeConfig = &domain.ServiceRuntimeConfig{Adopted: adopted}
	if err := registry.UpdateService(ctx, existing); err != nil {
		return nil, false, fmt.Errorf("updating service %q: %w", existing.Name, err)
	}
	return existing, false, nil
}

func (s *AdoptionService) findServiceByAdoptedTarget(ctx context.Context, serviceRepo repository.ServiceRepository, orgID uuid.UUID, target AdoptionTarget, discovered runtime.DiscoveredContainer) (*domain.Service, error) {
	if s.adoptedIdentities != nil {
		identities, err := s.adoptedIdentities.FindByFingerprints(ctx, orgID, adoptedRuntimeFingerprints(target, discovered))
		if err != nil {
			return nil, err
		}
		var matched *domain.Service
		for _, identity := range identities {
			if identity.OrgID != orgID {
				continue
			}
			svc, err := serviceRepo.GetByID(ctx, identity.ServiceID)
			if err != nil {
				return nil, fmt.Errorf("looking up service by adopted identity: %w", err)
			}
			if svc == nil {
				continue
			}
			if matched != nil && matched.ID != svc.ID {
				return nil, fmt.Errorf("adopted runtime identity matches multiple services in org %s", orgID)
			}
			matched = svc
		}
		if matched != nil {
			return matched, nil
		}
	}
	services, err := serviceRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing services for adopted target lookup: %w", err)
	}
	for i := range services {
		if services[i].OrgID == orgID && sameAdoptedTarget(&services[i], target, discovered) {
			return &services[i], nil
		}
	}
	return nil, nil
}

func (s *AdoptionService) ensureAdoptionBuild(ctx context.Context, builds repository.BuildRepository, target AdoptionTarget, discovered runtime.DiscoveredContainer, serviceID uuid.UUID) (*domain.Build, error) {
	runID := adoptionRunID(target, discovered)
	existing, err := builds.GetByCISystemRunID(ctx, adoptionCISystem, runID)
	if err != nil {
		return nil, fmt.Errorf("looking up adoption build: %w", err)
	}
	if existing != nil {
		if existing.ServiceID != serviceID {
			return nil, fmt.Errorf("adoption build %q already belongs to service %s", runID, existing.ServiceID)
		}
		return existing, nil
	}
	gitSHA := strings.TrimSpace(discovered.Labels["org.opencontainers.image.revision"])
	if gitSHA == "" {
		gitSHA = "adopted"
	}
	gitRef := discovered.ImageTag
	if gitRef == "" {
		gitRef = "adopted"
	}
	build := &domain.Build{
		ServiceID: serviceID,
		GitSHA:    gitSHA,
		GitRef:    gitRef,
		CISystem:  adoptionCISystem,
		CIRunID:   runID,
		Status:    domain.BuildStatusSucceeded,
		Metadata: map[string]any{
			"import_source":  "adoption",
			"target_name":    target.Name,
			"container_id":   discovered.ContainerID,
			"container_name": discovered.ContainerName,
			"image_ref":      discovered.ImageRef,
			"source_runtime": discovered.SourceRuntime,
			"compose":        discovered.Compose,
		},
	}
	for k, v := range targetTransportMetadata(target) {
		build.Metadata[k] = v
	}
	finished := time.Now().UTC()
	build.StartedAt = &finished
	build.FinishedAt = &finished
	if err := builds.Create(ctx, build); err != nil {
		return nil, fmt.Errorf("creating adoption build: %w", err)
	}
	return build, nil
}

func (s *AdoptionService) ensureAdoptionArtifact(ctx context.Context, artifacts repository.ArtifactRepository, discovered runtime.DiscoveredContainer, serviceID, buildID uuid.UUID) (*domain.Artifact, error) {
	existing, err := artifacts.GetByImageRepoDigest(ctx, discovered.ImageRepo, discovered.ImageDigest)
	if err != nil {
		return nil, fmt.Errorf("looking up adoption artifact: %w", err)
	}
	if existing != nil {
		if existing.ServiceID != serviceID {
			return nil, fmt.Errorf("artifact %s@%s already belongs to service %s", discovered.ImageRepo, discovered.ImageDigest, existing.ServiceID)
		}
		return existing, nil
	}
	imageTag := discovered.ImageTag
	if imageTag == "" {
		imageTag = "adopted"
	}
	artifact := &domain.Artifact{
		BuildID:     buildID,
		ServiceID:   serviceID,
		ImageRepo:   discovered.ImageRepo,
		ImageTag:    imageTag,
		ImageDigest: discovered.ImageDigest,
		ScanStatus:  domain.ScanStatusUnknown,
		Metadata: map[string]any{
			"import_source":  "adoption",
			"source_runtime": discovered.SourceRuntime,
			"container_id":   discovered.ContainerID,
			"container_name": discovered.ContainerName,
			"image_ref":      discovered.ImageRef,
		},
	}
	if err := artifacts.Create(ctx, artifact); err != nil {
		return nil, fmt.Errorf("creating adoption artifact: %w", err)
	}
	return artifact, nil
}

func (s *AdoptionService) normalizeAdoptionTargets(targets []AdoptionTarget) ([]AdoptionTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one adoption target is required")
	}
	out := make([]AdoptionTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		target.Name = normalizeResourceName(target.Name)
		target.DockerHost = strings.TrimSpace(target.DockerHost)
		target.EndpointRef = strings.TrimSpace(target.EndpointRef)
		if target.EnvironmentName == "" {
			target.EnvironmentName = target.Name
		}
		target.EnvironmentName = normalizeResourceName(target.EnvironmentName)
		if target.Name == "" {
			return nil, fmt.Errorf("adoption target name is required")
		}
		if target.EndpointRef == "" && target.DockerHost == "" {
			target.EndpointRef = target.Name
		}
		if target.EndpointRef != "" && target.DockerHost != "" {
			return nil, fmt.Errorf("adoption target %q cannot combine endpoint_ref with docker_host", target.Name)
		}
		if target.EndpointRef != "" {
			endpoint, ok := s.runtimeCfg.Endpoints[target.EndpointRef]
			if !ok {
				return nil, fmt.Errorf("adoption target %q references unknown endpoint_ref %q", target.Name, target.EndpointRef)
			}
			if strings.TrimSpace(endpoint.DockerHost) == "" {
				return nil, fmt.Errorf("adoption target %q endpoint_ref %q has no docker_host", target.Name, target.EndpointRef)
			}
			endpoint.Ref = target.EndpointRef
			target.Endpoint = endpoint
			target.DockerHost = strings.TrimSpace(endpoint.DockerHost)
		} else {
			if !s.allowRawDockerHosts {
				return nil, fmt.Errorf("raw docker_host targets are disabled by adoption policy")
			}
			if target.DockerHost == "" {
				return nil, fmt.Errorf("docker_host is required for target %q", target.Name)
			}
		}
		if _, ok := seen[target.Name]; ok {
			return nil, fmt.Errorf("duplicate adoption target %q", target.Name)
		}
		seen[target.Name] = struct{}{}
		out = append(out, target)
	}
	return out, nil
}

func normalizeAdoptionSelections(req AdoptionImportRequest) (map[string]AdoptionSelection, error) {
	if !req.ImportAll && len(req.Selections) == 0 {
		return nil, fmt.Errorf("import requires import_all or at least one selection")
	}
	out := map[string]AdoptionSelection{}
	for _, selection := range req.Selections {
		selection.TargetName = normalizeResourceName(selection.TargetName)
		selection.ContainerID = strings.TrimSpace(selection.ContainerID)
		selection.ServiceNameOverride = strings.TrimSpace(selection.ServiceNameOverride)
		if selection.TargetName == "" || selection.ContainerID == "" {
			return nil, fmt.Errorf("selection target_name and container_id are required")
		}
		if selection.ServiceNameOverride != "" {
			selection.ServiceNameOverride = normalizeResourceName(selection.ServiceNameOverride)
			if selection.ServiceNameOverride == "" {
				return nil, fmt.Errorf("selection service_name_override is invalid")
			}
		}
		out[selectionKey(selection.TargetName, selection.ContainerID)] = selection
	}
	return out, nil
}

func toDockerDiscoveryTargets(targets []AdoptionTarget) []runtime.DockerDiscoveryTarget {
	out := make([]runtime.DockerDiscoveryTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, runtime.DockerDiscoveryTarget{Name: target.Name, DockerHost: target.DockerHost, EndpointRef: target.EndpointRef, Endpoint: target.Endpoint, EnvironmentName: target.EnvironmentName})
	}
	return out
}

func proposedServiceName(discovered runtime.DiscoveredContainer) string {
	if discovered.Compose != nil && discovered.Compose.ProjectName != "" && discovered.Compose.ServiceName != "" {
		return normalizeResourceName(discovered.Compose.ProjectName + "-" + discovered.Compose.ServiceName)
	}
	return normalizeResourceName(discovered.ContainerName)
}

func adoptedRuntimeConfig(target AdoptionTarget, discovered runtime.DiscoveredContainer, classified sensitiveDataClassification) *domain.AdoptedRuntimeConfig {
	return &domain.AdoptedRuntimeConfig{
		TargetName:    discovered.TargetName,
		ContainerID:   discovered.ContainerID,
		ImageDigest:   discovered.ImageDigest,
		SourceRuntime: discovered.SourceRuntime,
		HostAlias:     target.Name,
		EndpointRef:   target.EndpointRef,
		Environment:   copyStringMap(classified.SafeEnvironment),
		Ports:         append([]string(nil), discovered.Ports...),
		Volumes:       append([]string(nil), discovered.Volumes...),
		Restart:       discovered.Restart,
		Command:       append([]string(nil), discovered.Command...),
		Entrypoint:    append([]string(nil), discovered.Entrypoint...),
		WorkingDir:    discovered.WorkingDir,
		NetworkMode:   discovered.NetworkMode,
		Labels:        copyStringMap(classified.SafeLabels),
		Compose:       discovered.Compose,
	}
}

func sameAdoptedTarget(svc *domain.Service, target AdoptionTarget, discovered runtime.DiscoveredContainer) bool {
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		return false
	}
	adopted := svc.RuntimeConfig.Adopted
	return adopted.TargetName == discovered.TargetName && adopted.HostAlias == target.Name
}

func isComposeOrigin(discovered runtime.DiscoveredContainer) bool {
	return strings.EqualFold(strings.TrimSpace(discovered.SourceRuntime), "compose") || discovered.Compose != nil
}

func adoptionRunID(target AdoptionTarget, discovered runtime.DiscoveredContainer) string {
	return strings.Join([]string{target.Name, discovered.TargetName, discovered.ImageDigest}, ":")
}

func (s *AdoptionService) importSensitiveEnvironmentSecrets(ctx context.Context, secrets repository.SecretRepository, serviceID, envID uuid.UUID, sensitiveEnv map[string]string) error {
	if len(sensitiveEnv) == 0 {
		return nil
	}
	if secrets == nil || s.secretEncryptor == nil {
		return fmt.Errorf("sensitive environment values require configured secret storage and encryption")
	}
	keys := sortedStringKeys(sensitiveEnv)
	for _, key := range keys {
		value := sensitiveEnv[key]
		ciphertext, err := s.secretEncryptor.Encrypt(value, domain.EncryptionAES256)
		if err != nil {
			return fmt.Errorf("encrypting imported secret %q: %w", key, err)
		}
		if err := secrets.DeleteByName(ctx, serviceID, &envID, key); err != nil {
			return fmt.Errorf("replacing imported secret %q: %w", key, err)
		}
		secret := &domain.ServiceSecret{
			ID:               uuid.New(),
			ServiceID:        serviceID,
			EnvironmentID:    &envID,
			Name:             key,
			EncryptedValue:   ciphertext,
			EncryptionMethod: domain.EncryptionAES256,
			Version:          1,
			CreatedBy:        "adoption",
		}
		if err := secrets.Create(ctx, secret); err != nil {
			return fmt.Errorf("creating imported secret %q: %w", key, err)
		}
	}
	return nil
}

func (s *AdoptionService) persistAdoptedRuntimeIdentities(ctx context.Context, repo repository.AdoptedRuntimeIdentityRepository, orgID, serviceID, envID uuid.UUID, target AdoptionTarget, discovered runtime.DiscoveredContainer) error {
	if repo == nil {
		return nil
	}
	fingerprints := adoptedRuntimeFingerprintsByKind(target, discovered)
	identities := make([]domain.AdoptedRuntimeIdentity, 0, len(fingerprints))
	for kind, fingerprint := range fingerprints {
		identities = append(identities, domain.AdoptedRuntimeIdentity{
			OrgID:           orgID,
			ServiceID:       serviceID,
			EnvironmentID:   envID,
			FingerprintKind: kind,
			Fingerprint:     fingerprint,
			ContainerID:     discovered.ContainerID,
			ImageDigest:     discovered.ImageDigest,
			EndpointRef:     target.EndpointRef,
			HostAlias:       target.Name,
			TargetName:      discovered.TargetName,
			Compose:         discovered.Compose,
		})
	}
	if len(identities) == 0 {
		return fmt.Errorf("adopted runtime identity requires at least one stable fingerprint")
	}
	if err := repo.UpsertMany(ctx, identities); err != nil {
		return fmt.Errorf("persisting adopted runtime identities: %w", err)
	}
	return nil
}

func adoptedRuntimeFingerprints(target AdoptionTarget, discovered runtime.DiscoveredContainer) []string {
	byKind := adoptedRuntimeFingerprintsByKind(target, discovered)
	keys := make([]string, 0, len(byKind))
	for kind := range byKind {
		keys = append(keys, kind)
	}
	sort.Strings(keys)
	fingerprints := make([]string, 0, len(keys))
	for _, kind := range keys {
		fingerprints = append(fingerprints, byKind[kind])
	}
	return fingerprints
}

func adoptedRuntimeFingerprintsByKind(target AdoptionTarget, discovered runtime.DiscoveredContainer) map[string]string {
	anchor := target.EndpointRef
	if anchor == "" {
		anchor = target.Name
	}
	out := map[string]string{}
	if discovered.ContainerID != "" {
		out["container_id"] = strings.Join([]string{"container_id", anchor, discovered.ContainerID}, "|")
	}
	if discovered.ImageDigest != "" && discovered.TargetName != "" {
		out["image_digest"] = strings.Join([]string{"image_digest", anchor, discovered.TargetName, discovered.ImageDigest}, "|")
	}
	if discovered.Compose != nil && discovered.Compose.ProjectName != "" && discovered.Compose.ServiceName != "" {
		parts := []string{"compose_coordinates", anchor, discovered.Compose.ProjectName, discovered.Compose.ServiceName, discovered.Compose.WorkingDir}
		parts = append(parts, discovered.Compose.ConfigFiles...)
		out["compose_coordinates"] = strings.Join(parts, "|")
	}
	if discovered.TargetName != "" {
		out["endpoint_target"] = strings.Join([]string{"endpoint_target", anchor, discovered.TargetName}, "|")
	}
	return out
}

type sensitiveDataClassification struct {
	SafeEnvironment          map[string]string
	SensitiveEnvironment     map[string]string
	SensitiveEnvironmentKeys []string
	SafeLabels               map[string]string
	SensitiveLabels          map[string]string
	SensitiveLabelKeys       []string
}

func classifyDiscoveredSensitiveData(discovered runtime.DiscoveredContainer) sensitiveDataClassification {
	return sensitiveDataClassification{
		SafeEnvironment:          safeEntries(discovered.Environment, isSensitiveEnvironmentKey),
		SensitiveEnvironment:     sensitiveEntries(discovered.Environment, isSensitiveEnvironmentKey),
		SensitiveEnvironmentKeys: sensitiveKeys(discovered.Environment, isSensitiveEnvironmentKey),
		SafeLabels:               safeEntries(discovered.Labels, isSensitiveLabelKey),
		SensitiveLabels:          sensitiveEntries(discovered.Labels, isSensitiveLabelKey),
		SensitiveLabelKeys:       sensitiveKeys(discovered.Labels, isSensitiveLabelKey),
	}
}

func isSensitiveEnvironmentKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, token := range []string{"PASSWORD", "PASSWD", "PASS", "TOKEN", "SECRET", "PRIVATE", "CREDENTIAL", "API_KEY", "ACCESS_KEY", "AUTH", "BEARER", "COOKIE", "SESSION", "JWT", "DATABASE_URL", "DB_URL", "REDIS_URL", "POSTGRES_DSN", "POSTGRES_URL", "MYSQL_DSN", "MYSQL_URL", "MONGODB_URI", "MONGO_URI", "AMQP_URL", "RABBITMQ_URL", "CONNECTION_STRING", "DSN"} {
		if strings.Contains(k, token) {
			return true
		}
	}
	for _, prefix := range []string{"AWS_", "GCP_", "GOOGLE_", "AZURE_", "DOCKER_AUTH", "NPM_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "SLACK_", "STRIPE_", "SENTRY_DSN"} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "_CERT") || strings.HasSuffix(k, "_CERTIFICATE")
}

func isSensitiveLabelKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, token := range []string{"PASSWORD", "PASSWD", "TOKEN", "SECRET", "PRIVATE", "CREDENTIAL", "API_KEY", "ACCESS_KEY", "AUTH", "BEARER", "COOKIE", "SESSION", "JWT"} {
		if strings.Contains(k, token) {
			return true
		}
	}
	return false
}

func safeEntries(in map[string]string, isSensitive func(string) bool) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if !isSensitive(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sensitiveEntries(in map[string]string, isSensitive func(string) bool) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if isSensitive(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sensitiveKeys(in map[string]string, isSensitive func(string) bool) []string {
	if len(in) == 0 {
		return nil
	}
	var keys []string
	for k := range in {
		if isSensitive(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func observationFromDiscovered(target AdoptionTarget, serviceID, envID uuid.UUID, discovered runtime.DiscoveredContainer) *domain.RuntimeObservation {
	obs := &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: discovered.ImageDigest,
		ObservedImageRepo:   discovered.ImageRepo,
		ObservedContainerID: discovered.ContainerID,
		ObservedHost:        target.Name,
		ObservedVersion:     discovered.ImageRef,
		HealthStatus:        discovered.HealthStatus,
		Source:              "adoption",
		Metadata: map[string]any{
			"import_source":  "adoption",
			"target_name":    target.Name,
			"container_name": discovered.ContainerName,
			"source_runtime": discovered.SourceRuntime,
			"warnings":       discovered.Warnings,
		},
		ObservedAt: time.Now().UTC(),
	}
	for k, v := range targetTransportMetadata(target) {
		obs.Metadata[k] = v
	}
	return obs
}

func targetTransportMetadata(target AdoptionTarget) map[string]any {
	if target.EndpointRef != "" {
		return map[string]any{"endpoint_ref": target.EndpointRef}
	}
	if target.DockerHost != "" {
		return map[string]any{"docker_host": target.DockerHost}
	}
	return nil
}

func selectionsForTarget(selections map[string]AdoptionSelection, targetName string) []AdoptionSelection {
	var out []AdoptionSelection
	prefix := targetName + "/"
	for key, selection := range selections {
		if strings.HasPrefix(key, prefix) {
			out = append(out, selection)
		}
	}
	return out
}

func selectionKey(targetName, containerID string) string {
	return targetName + "/" + containerID
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

var invalidResourceNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeResourceName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidResourceNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func stringFromAny(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
