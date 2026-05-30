package dto

import (
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// AdoptionPreviewResponsesFromService maps service adoption previews to the
// transport-safe public response shape used by HTTP and control-plane results.
func AdoptionPreviewResponsesFromService(previews []service.AdoptionPreview) []AdoptionPreviewResponse {
	mapped := make([]AdoptionPreviewResponse, 0, len(previews))
	for _, preview := range previews {
		containers := make([]AdoptionPreviewContainerResponse, 0, len(preview.Containers))
		for _, container := range preview.Containers {
			containers = append(containers, AdoptionPreviewContainerResponse{
				Discovered:          discoveredContainerResponseFromRuntime(container.Discovered, container.SafeEnvironment, container.SafeLabels, container.RedactedEnvironmentKeys, container.RedactedLabelKeys),
				ProposedServiceName: container.ProposedServiceName,
				ExistingServiceID:   container.ExistingServiceID,
				WillUpdate:          container.WillUpdate,
				Warnings:            append([]string(nil), container.Warnings...),
				Adoptable:           container.Adoptable,
			})
		}
		mapped = append(mapped, AdoptionPreviewResponse{
			Target:     adoptionTargetResponseFromService(preview.Target),
			Containers: containers,
			Error:      preview.Error,
		})
	}
	return mapped
}

// AdoptionImportResultResponsesFromService maps service import outcomes to the
// transport-safe public response shape used by HTTP and control-plane results.
func AdoptionImportResultResponsesFromService(results []service.AdoptionImportResult) []AdoptionImportResultResponse {
	mapped := make([]AdoptionImportResultResponse, 0, len(results))
	for _, result := range results {
		mapped = append(mapped, AdoptionImportResultResponse{
			TargetName:              result.TargetName,
			ContainerID:             result.ContainerID,
			ContainerName:           result.ContainerName,
			ServiceName:             result.ServiceName,
			ServiceID:               result.ServiceID,
			EnvironmentID:           result.EnvironmentID,
			BuildID:                 result.BuildID,
			ArtifactID:              result.ArtifactID,
			Status:                  result.Status,
			Warnings:                append([]string(nil), result.Warnings...),
			RedactedEnvironmentKeys: append([]string(nil), result.RedactedEnvironmentKeys...),
			RedactedLabelKeys:       append([]string(nil), result.RedactedLabelKeys...),
			Error:                   result.Error,
		})
	}
	return mapped
}

// RuntimeObservationResponseFromDomain maps a runtime observation domain model
// to the public runtime observation response contract.
func RuntimeObservationResponseFromDomain(obs *domain.RuntimeObservation) *RuntimeObservationResponse {
	if obs == nil {
		return nil
	}
	metadata := make(map[string]any, len(obs.Metadata))
	for k, v := range obs.Metadata {
		metadata[k] = v
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return &RuntimeObservationResponse{
		ID:                  obs.ID,
		ServiceID:           obs.ServiceID,
		EnvironmentID:       obs.EnvironmentID,
		ObservedImageDigest: obs.ObservedImageDigest,
		ObservedImageRepo:   obs.ObservedImageRepo,
		ObservedContainerID: obs.ObservedContainerID,
		ObservedHost:        obs.ObservedHost,
		ObservedVersion:     obs.ObservedVersion,
		HealthStatus:        string(obs.HealthStatus),
		Source:              obs.Source,
		Metadata:            metadata,
		ObservedAt:          obs.ObservedAt,
	}
}

// RuntimeActionResponseFromDomain builds the public direct runtime action
// response from route/action context plus an optional runtime observation.
func RuntimeActionResponseFromDomain(action string, serviceID, environmentID uuid.UUID, obs *domain.RuntimeObservation) RuntimeActionResponse {
	return RuntimeActionResponse{
		Action:        action,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		Observation:   RuntimeObservationResponseFromDomain(obs),
	}
}

func adoptionTargetResponseFromService(target service.AdoptionTarget) AdoptionTargetResponse {
	resp := AdoptionTargetResponse{Name: target.Name, EndpointRef: target.EndpointRef, EnvironmentName: target.EnvironmentName}
	if target.EndpointRef == "" {
		resp.DockerHost = target.DockerHost
	}
	return resp
}

func discoveredContainerResponseFromRuntime(discovered runtime.DiscoveredContainer, safeEnvironment, safeLabels map[string]string, redactedEnvironmentKeys, redactedLabelKeys []string) DiscoveredContainerResponse {
	return DiscoveredContainerResponse{
		TargetName:              discovered.TargetName,
		EnvironmentName:         discovered.EnvironmentName,
		ContainerID:             discovered.ContainerID,
		ContainerName:           discovered.ContainerName,
		EndpointRef:             discovered.EndpointRef,
		ImageRef:                discovered.ImageRef,
		ImageRepo:               discovered.ImageRepo,
		ImageTag:                discovered.ImageTag,
		ImageDigest:             discovered.ImageDigest,
		SourceRuntime:           discovered.SourceRuntime,
		Labels:                  copyStringMap(safeLabels),
		Environment:             copyStringMap(safeEnvironment),
		RedactedEnvironmentKeys: append([]string(nil), redactedEnvironmentKeys...),
		RedactedLabelKeys:       append([]string(nil), redactedLabelKeys...),
		Ports:                   append([]string(nil), discovered.Ports...),
		Volumes:                 append([]string(nil), discovered.Volumes...),
		Restart:                 discovered.Restart,
		Command:                 append([]string(nil), discovered.Command...),
		Entrypoint:              append([]string(nil), discovered.Entrypoint...),
		WorkingDir:              discovered.WorkingDir,
		NetworkMode:             discovered.NetworkMode,
		Compose:                 composeMetadataResponseFromDomain(discovered.Compose),
		HealthStatus:            string(discovered.HealthStatus),
		Warnings:                append([]string(nil), discovered.Warnings...),
		Adoptable:               discovered.Adoptable,
	}
}

func composeMetadataResponseFromDomain(compose *domain.ComposeMetadata) *ComposeMetadataResponse {
	if compose == nil {
		return nil
	}
	return &ComposeMetadataResponse{
		ProjectName: compose.ProjectName,
		ServiceName: compose.ServiceName,
		WorkingDir:  compose.WorkingDir,
		ConfigFiles: append([]string(nil), compose.ConfigFiles...),
	}
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
