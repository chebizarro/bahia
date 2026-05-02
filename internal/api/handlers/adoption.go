package handlers

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

type adoptionService interface {
	Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error)
	Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error)
}

// AdoptionHandler exposes adopted-workload scan/import endpoints.
type AdoptionHandler struct {
	adoption adoptionService
}

// NewAdoptionHandler creates an AdoptionHandler.
func NewAdoptionHandler(adoption adoptionService) *AdoptionHandler {
	return &AdoptionHandler{adoption: adoption}
}

// Scan previews adoptable containers on one or more Docker targets.
func (h *AdoptionHandler) Scan(w http.ResponseWriter, r *http.Request) {
	if h.adoption == nil {
		writeError(w, http.StatusServiceUnavailable, "adoption service is not configured")
		return
	}
	var req dto.ScanAdoptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateAdoptionTargets(req.Targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	previews, err := h.adoption.Scan(r.Context(), service.AdoptionScanRequest{Targets: mapAdoptionTargets(req.Targets)})
	if err != nil {
		writeAdoptionServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, mapAdoptionPreviewResponses(previews))
}

// Import imports selected or all discovered containers into Bahia models.
func (h *AdoptionHandler) Import(w http.ResponseWriter, r *http.Request) {
	if h.adoption == nil {
		writeError(w, http.StatusServiceUnavailable, "adoption service is not configured")
		return
	}
	var req dto.ImportAdoptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateAdoptionTargets(req.Targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		writeError(w, http.StatusBadRequest, "import requires import_all=true or at least one selection")
		return
	}
	if err := validateAdoptionSelections(req.Selections); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	results, err := h.adoption.Import(r.Context(), service.AdoptionImportRequest{
		Targets:    mapAdoptionTargets(req.Targets),
		Selections: mapAdoptionSelections(req.Selections),
		ImportAll:  req.ImportAll,
	})
	if err != nil {
		writeAdoptionServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, mapAdoptionImportResultResponses(results))
}

func validateAdoptionTargets(targets []dto.AdoptionTargetRequest) error {
	if len(targets) == 0 {
		return errBadRequest("at least one target is required")
	}
	seen := map[string]struct{}{}
	for _, target := range targets {
		name := normalizeAdoptionName(target.Name)
		if name == "" {
			return errBadRequest("target name is required")
		}
		if _, ok := seen[name]; ok {
			return errBadRequest("target names must be unique after normalization")
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(target.DockerHost) == "" {
			return errBadRequest("target docker_host is required")
		}
		if strings.TrimSpace(target.EnvironmentName) != "" && normalizeAdoptionName(target.EnvironmentName) == "" {
			return errBadRequest("target environment_name is invalid")
		}
	}
	return nil
}

func validateAdoptionSelections(selections []dto.AdoptionSelectionRequest) error {
	seen := map[string]struct{}{}
	for _, selection := range selections {
		targetName := normalizeAdoptionName(selection.TargetName)
		containerID := strings.TrimSpace(selection.ContainerID)
		if targetName == "" {
			return errBadRequest("selection target_name is required")
		}
		if containerID == "" {
			return errBadRequest("selection container_id is required")
		}
		if strings.TrimSpace(selection.ServiceNameOverride) != "" && normalizeAdoptionName(selection.ServiceNameOverride) == "" {
			return errBadRequest("selection service_name_override is invalid")
		}
		key := targetName + "/" + containerID
		if _, ok := seen[key]; ok {
			return errBadRequest("selection entries must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }

var invalidAdoptionNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeAdoptionName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidAdoptionNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func writeAdoptionServiceError(w http.ResponseWriter, err error) {
	msg := err.Error()
	if strings.Contains(msg, "adoption target") || strings.Contains(msg, "docker_host") || strings.Contains(msg, "import requires") || strings.Contains(msg, "selection") {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	writeError(w, http.StatusInternalServerError, msg)
}

func mapAdoptionTargets(targets []dto.AdoptionTargetRequest) []service.AdoptionTarget {
	mapped := make([]service.AdoptionTarget, 0, len(targets))
	for _, target := range targets {
		mapped = append(mapped, service.AdoptionTarget{
			Name:            normalizeAdoptionName(target.Name),
			DockerHost:      strings.TrimSpace(target.DockerHost),
			EnvironmentName: normalizeAdoptionName(target.EnvironmentName),
		})
	}
	return mapped
}

func mapAdoptionSelections(selections []dto.AdoptionSelectionRequest) []service.AdoptionSelection {
	mapped := make([]service.AdoptionSelection, 0, len(selections))
	for _, selection := range selections {
		mapped = append(mapped, service.AdoptionSelection{
			TargetName:          normalizeAdoptionName(selection.TargetName),
			ContainerID:         strings.TrimSpace(selection.ContainerID),
			ServiceNameOverride: normalizeAdoptionName(selection.ServiceNameOverride),
		})
	}
	return mapped
}

func mapAdoptionPreviewResponses(previews []service.AdoptionPreview) []dto.AdoptionPreviewResponse {
	mapped := make([]dto.AdoptionPreviewResponse, 0, len(previews))
	for _, preview := range previews {
		containers := make([]dto.AdoptionPreviewContainerResponse, 0, len(preview.Containers))
		for _, container := range preview.Containers {
			containers = append(containers, dto.AdoptionPreviewContainerResponse{
				Discovered:          mapDiscoveredContainerResponse(container.Discovered),
				ProposedServiceName: container.ProposedServiceName,
				ExistingServiceID:   container.ExistingServiceID,
				WillUpdate:          container.WillUpdate,
				Warnings:            append([]string(nil), container.Warnings...),
				Adoptable:           container.Adoptable,
			})
		}
		mapped = append(mapped, dto.AdoptionPreviewResponse{
			Target:     mapAdoptionTargetResponse(preview.Target),
			Containers: containers,
			Error:      preview.Error,
		})
	}
	return mapped
}

func mapAdoptionImportResultResponses(results []service.AdoptionImportResult) []dto.AdoptionImportResultResponse {
	mapped := make([]dto.AdoptionImportResultResponse, 0, len(results))
	for _, result := range results {
		mapped = append(mapped, dto.AdoptionImportResultResponse{
			TargetName:    result.TargetName,
			ContainerID:   result.ContainerID,
			ContainerName: result.ContainerName,
			ServiceName:   result.ServiceName,
			ServiceID:     result.ServiceID,
			EnvironmentID: result.EnvironmentID,
			BuildID:       result.BuildID,
			ArtifactID:    result.ArtifactID,
			Status:        result.Status,
			Warnings:      append([]string(nil), result.Warnings...),
			Error:         result.Error,
		})
	}
	return mapped
}

func mapAdoptionTargetResponse(target service.AdoptionTarget) dto.AdoptionTargetResponse {
	return dto.AdoptionTargetResponse{Name: target.Name, DockerHost: target.DockerHost, EnvironmentName: target.EnvironmentName}
}

func mapDiscoveredContainerResponse(discovered runtime.DiscoveredContainer) dto.DiscoveredContainerResponse {
	return dto.DiscoveredContainerResponse{
		TargetName:      discovered.TargetName,
		EnvironmentName: discovered.EnvironmentName,
		ContainerID:     discovered.ContainerID,
		ContainerName:   discovered.ContainerName,
		ImageRef:        discovered.ImageRef,
		ImageRepo:       discovered.ImageRepo,
		ImageTag:        discovered.ImageTag,
		ImageDigest:     discovered.ImageDigest,
		SourceRuntime:   discovered.SourceRuntime,
		Labels:          copyStringMap(discovered.Labels),
		Environment:     copyStringMap(discovered.Environment),
		Ports:           append([]string(nil), discovered.Ports...),
		Volumes:         append([]string(nil), discovered.Volumes...),
		Restart:         discovered.Restart,
		Command:         append([]string(nil), discovered.Command...),
		Entrypoint:      append([]string(nil), discovered.Entrypoint...),
		WorkingDir:      discovered.WorkingDir,
		NetworkMode:     discovered.NetworkMode,
		Compose:         discovered.Compose,
		HealthStatus:    discovered.HealthStatus,
		Warnings:        append([]string(nil), discovered.Warnings...),
		Adoptable:       discovered.Adoptable,
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
