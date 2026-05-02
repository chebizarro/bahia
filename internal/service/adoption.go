package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	adoptionCISystem      = "adoption"
	adoptionImportedEvent = events.EventType("adoption.imported")
	adoptionStatusCreated = "created"
	adoptionStatusUpdated = "updated"
	adoptionStatusFailed  = "failed"
)

// AdoptionService scans Docker hosts and imports existing containers into Bahia models.
type AdoptionService struct {
	registry     *RegistryService
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	builds       repository.BuildRepository
	artifacts    repository.ArtifactRepository
	state        repository.EnvironmentServiceStateRepository
	observations repository.RuntimeObservationRepository
	publisher    events.Publisher
	logger       *zap.Logger

	runtimeCfg          config.RuntimeConfig
	allowRawDockerHosts bool
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
		registry:            registry,
		services:            services,
		environments:        environments,
		builds:              builds,
		artifacts:           artifacts,
		state:               state,
		observations:        observations,
		publisher:           publisher,
		logger:              logger,
		allowRawDockerHosts: true,
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
	Discovered          runtime.DiscoveredContainer
	ProposedServiceName string
	ExistingServiceID   *uuid.UUID
	WillUpdate          bool
	Warnings            []string
	Adoptable           bool
}

// AdoptionImportResult reports per-candidate import outcome.
type AdoptionImportResult struct {
	TargetName    string
	ContainerID   string
	ContainerName string
	ServiceName   string
	ServiceID     *uuid.UUID
	EnvironmentID *uuid.UUID
	BuildID       *uuid.UUID
	ArtifactID    *uuid.UUID
	Status        string
	Warnings      []string
	Error         string
}

// Scan discovers containers and proposes Bahia service names.
func (s *AdoptionService) Scan(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error) {
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
		return nil, err
	}
	return s.buildPreviews(ctx, targets, results), nil
}

// Import scans targets and imports selected containers. Individual candidate failures are returned in result rows.
func (s *AdoptionService) Import(ctx context.Context, req AdoptionImportRequest) ([]AdoptionImportResult, error) {
	targets, err := s.normalizeAdoptionTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	selectionSet, err := normalizeAdoptionSelections(req)
	if err != nil {
		return nil, err
	}

	results, err := runtime.DiscoverDockerTargets(ctx, toDockerDiscoveryTargets(targets), s.logger)
	if err != nil {
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
			imported = append(imported, s.importCandidate(ctx, preview.Target, container, serviceName))
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
			proposed := s.proposedServiceName(ctx, target, discovered, usedNames)
			existing, _ := s.services.GetByName(ctx, proposed)
			candidate := AdoptionPreviewContainer{
				Discovered:          discovered,
				ProposedServiceName: proposed,
				Warnings:            append([]string(nil), discovered.Warnings...),
				Adoptable:           discovered.Adoptable,
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

func (s *AdoptionService) importCandidate(ctx context.Context, target AdoptionTarget, candidate AdoptionPreviewContainer, serviceName string) AdoptionImportResult {
	discovered := candidate.Discovered
	result := AdoptionImportResult{
		TargetName:    target.Name,
		ContainerID:   discovered.ContainerID,
		ContainerName: discovered.ContainerName,
		ServiceName:   serviceName,
		Warnings:      append([]string(nil), candidate.Warnings...),
	}
	if !candidate.Adoptable {
		result.Status = adoptionStatusFailed
		result.Error = "container has unsupported adoption warnings"
		return result
	}
	if discovered.ImageRepo == "" || discovered.ImageDigest == "" {
		result.Status = adoptionStatusFailed
		result.Error = "container image repo and digest are required for import"
		return result
	}
	if serviceName == "" {
		result.Status = adoptionStatusFailed
		result.Error = "service name is required"
		return result
	}

	env, err := s.ensureAdoptionEnvironment(ctx, target)
	if err != nil {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		return result
	}
	result.EnvironmentID = &env.ID

	svc, createdService, err := s.ensureAdoptionService(ctx, target, discovered, serviceName)
	if err != nil {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		return result
	}
	result.ServiceID = &svc.ID
	result.ServiceName = svc.Name

	build, err := s.ensureAdoptionBuild(ctx, target, discovered, svc.ID)
	if err != nil {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		return result
	}
	result.BuildID = &build.ID

	artifact, err := s.ensureAdoptionArtifact(ctx, discovered, svc.ID, build.ID)
	if err != nil {
		result.Status = adoptionStatusFailed
		result.Error = err.Error()
		return result
	}
	result.ArtifactID = &artifact.ID

	state := &domain.EnvironmentServiceState{
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		DesiredArtifactID: &artifact.ID,
		DriftStatus:       domain.DriftStatusUnknown,
	}
	if err := s.state.Upsert(ctx, state); err != nil {
		result.Status = adoptionStatusFailed
		result.Error = fmt.Errorf("seeding environment service state: %w", err).Error()
		return result
	}

	obs := observationFromDiscovered(target, svc.ID, env.ID, discovered)
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		result.Status = adoptionStatusFailed
		result.Error = fmt.Errorf("recording runtime observation: %w", err).Error()
		return result
	}

	status := adoptionStatusUpdated
	if createdService {
		status = adoptionStatusCreated
	}
	result.Status = status
	s.publisher.Publish(ctx, events.Event{
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
	})
	return result
}

func (s *AdoptionService) ensureAdoptionEnvironment(ctx context.Context, target AdoptionTarget) (*domain.Environment, error) {
	existing, err := s.environments.GetByName(ctx, target.EnvironmentName)
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
			Name:          target.EnvironmentName,
			RuntimeConfig: config,
		}
		if err := s.registry.CreateEnvironment(ctx, env); err != nil {
			return nil, fmt.Errorf("creating environment %q: %w", target.EnvironmentName, err)
		}
		return env, nil
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
	changed := false
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
		if err := s.registry.UpdateEnvironment(ctx, existing); err != nil {
			return nil, fmt.Errorf("updating environment %q: %w", target.EnvironmentName, err)
		}
	}
	return existing, nil
}

func (s *AdoptionService) ensureAdoptionService(ctx context.Context, target AdoptionTarget, discovered runtime.DiscoveredContainer, serviceName string) (*domain.Service, bool, error) {
	adopted := adoptedRuntimeConfig(target, discovered)
	byIdentity, err := s.findServiceByAdoptedTarget(ctx, target, discovered)
	if err != nil {
		return nil, false, err
	}
	byName, err := s.services.GetByName(ctx, serviceName)
	if err != nil {
		return nil, false, fmt.Errorf("looking up service %q: %w", serviceName, err)
	}
	if byName != nil && byIdentity != nil && byName.ID != byIdentity.ID {
		return nil, false, fmt.Errorf("service name %q already exists for a different target", serviceName)
	}
	if byName != nil && !sameAdoptedTarget(byName, target, discovered) {
		return nil, false, fmt.Errorf("service name %q already exists for a different target", serviceName)
	}

	existing := byIdentity
	if existing == nil {
		existing = byName
	}
	if existing == nil {
		svc := &domain.Service{
			Name:          serviceName,
			ArtifactRepo:  discovered.ImageRepo,
			RuntimeType:   domain.RuntimeTypeDocker,
			RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adopted},
		}
		if err := s.registry.CreateService(ctx, svc); err != nil {
			return nil, false, fmt.Errorf("creating service %q: %w", serviceName, err)
		}
		return svc, true, nil
	}
	existing.RuntimeType = domain.RuntimeTypeDocker
	existing.ArtifactRepo = discovered.ImageRepo
	existing.RuntimeConfig = &domain.ServiceRuntimeConfig{Adopted: adopted}
	if err := s.registry.UpdateService(ctx, existing); err != nil {
		return nil, false, fmt.Errorf("updating service %q: %w", existing.Name, err)
	}
	return existing, false, nil
}

func (s *AdoptionService) findServiceByAdoptedTarget(ctx context.Context, target AdoptionTarget, discovered runtime.DiscoveredContainer) (*domain.Service, error) {
	services, err := s.services.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing services for adopted target lookup: %w", err)
	}
	for i := range services {
		if sameAdoptedTarget(&services[i], target, discovered) {
			return &services[i], nil
		}
	}
	return nil, nil
}

func (s *AdoptionService) ensureAdoptionBuild(ctx context.Context, target AdoptionTarget, discovered runtime.DiscoveredContainer, serviceID uuid.UUID) (*domain.Build, error) {
	runID := adoptionRunID(target, discovered)
	existing, err := s.builds.GetByCISystemRunID(ctx, adoptionCISystem, runID)
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
	if err := s.builds.Create(ctx, build); err != nil {
		return nil, fmt.Errorf("creating adoption build: %w", err)
	}
	return build, nil
}

func (s *AdoptionService) ensureAdoptionArtifact(ctx context.Context, discovered runtime.DiscoveredContainer, serviceID, buildID uuid.UUID) (*domain.Artifact, error) {
	existing, err := s.artifacts.GetByImageRepoDigest(ctx, discovered.ImageRepo, discovered.ImageDigest)
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
	if err := s.artifacts.Create(ctx, artifact); err != nil {
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
		out = append(out, runtime.DockerDiscoveryTarget{Name: target.Name, DockerHost: target.DockerHost, Endpoint: target.Endpoint, EnvironmentName: target.EnvironmentName})
	}
	return out
}

func proposedServiceName(discovered runtime.DiscoveredContainer) string {
	if discovered.Compose != nil && discovered.Compose.ProjectName != "" && discovered.Compose.ServiceName != "" {
		return normalizeResourceName(discovered.Compose.ProjectName + "-" + discovered.Compose.ServiceName)
	}
	return normalizeResourceName(discovered.ContainerName)
}

func adoptedRuntimeConfig(target AdoptionTarget, discovered runtime.DiscoveredContainer) *domain.AdoptedRuntimeConfig {
	return &domain.AdoptedRuntimeConfig{
		TargetName:    discovered.TargetName,
		SourceRuntime: discovered.SourceRuntime,
		HostAlias:     target.Name,
		EndpointRef:   target.EndpointRef,
		Environment:   copyStringMap(discovered.Environment),
		Ports:         append([]string(nil), discovered.Ports...),
		Volumes:       append([]string(nil), discovered.Volumes...),
		Restart:       discovered.Restart,
		Command:       append([]string(nil), discovered.Command...),
		Entrypoint:    append([]string(nil), discovered.Entrypoint...),
		WorkingDir:    discovered.WorkingDir,
		NetworkMode:   discovered.NetworkMode,
		Labels:        copyStringMap(discovered.Labels),
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

func adoptionRunID(target AdoptionTarget, discovered runtime.DiscoveredContainer) string {
	return target.Name + ":" + discovered.ContainerName
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
