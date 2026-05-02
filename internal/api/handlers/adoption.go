package handlers

import (
	"context"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type adoptionService interface {
	Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error)
	Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error)
}

type adoptionMetrics interface {
	RecordAdoptionScan(targets, candidates, redactedKeys int, duration time.Duration, success bool)
	RecordAdoptionImport(candidates, successCount, failureCount, redactedKeys int, duration time.Duration)
}

// AdoptionHandler exposes adopted-workload scan/import endpoints.
type AdoptionHandler struct {
	adoption adoptionService
	logger   *zap.Logger
	metrics  adoptionMetrics
}

// NewAdoptionHandler creates an AdoptionHandler.
func NewAdoptionHandler(adoption adoptionService, opts ...AdoptionHandlerOption) *AdoptionHandler {
	if isNilHandlerDependency(adoption) {
		adoption = nil
	}
	h := &AdoptionHandler{adoption: adoption, logger: zap.NewNop()}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// AdoptionHandlerOption configures operational dependencies for AdoptionHandler.
type AdoptionHandlerOption func(*AdoptionHandler)

// WithAdoptionLogger enables structured adoption audit logs.
func WithAdoptionLogger(logger *zap.Logger) AdoptionHandlerOption {
	return func(h *AdoptionHandler) {
		if logger != nil {
			h.logger = logger
		}
	}
}

// WithAdoptionMetrics enables adoption operational metrics.
func WithAdoptionMetrics(metrics adoptionMetrics) AdoptionHandlerOption {
	return func(h *AdoptionHandler) {
		if !isNilHandlerDependency(metrics) {
			h.metrics = metrics
		}
	}
}

// Scan previews adoptable containers on one or more Docker targets.
func (h *AdoptionHandler) Scan(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.adoption == nil {
		writeError(w, http.StatusServiceUnavailable, "adoption service is not configured")
		h.recordScan(r, nil, 0, 0, 0, start, false, "adoption service is not configured")
		return
	}
	var req dto.ScanAdoptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		h.recordScan(r, req.Targets, 0, 0, 0, start, false, "invalid request body")
		return
	}
	if err := validateAdoptionTargets(req.Targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		h.recordScan(r, req.Targets, 0, 0, 0, start, false, err.Error())
		return
	}

	previews, err := h.adoption.Scan(r.Context(), service.AdoptionScanRequest{Targets: mapAdoptionTargets(req.Targets)})
	if err != nil {
		writeAdoptionServiceError(w, err)
		h.recordScan(r, req.Targets, 0, 0, 0, start, false, err.Error())
		return
	}
	candidateCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionPreviewStats(previews)
	h.recordScan(r, req.Targets, candidateCount, redactedEnvKeyCount, redactedLabelKeyCount, start, true, "")
	writeData(w, http.StatusOK, mapAdoptionPreviewResponses(previews))
}

// Import imports selected or all discovered containers into Bahia models.
func (h *AdoptionHandler) Import(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.adoption == nil {
		writeError(w, http.StatusServiceUnavailable, "adoption service is not configured")
		h.recordImport(r, nil, 0, 0, 0, 0, 0, start, "failed", "adoption service is not configured")
		return
	}
	var req dto.ImportAdoptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		h.recordImport(r, req.Targets, 0, 0, 0, 0, 0, start, "failed", "invalid request body")
		return
	}
	if err := validateAdoptionTargets(req.Targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		h.recordImport(r, req.Targets, 0, 0, 0, 0, 0, start, "failed", err.Error())
		return
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		writeError(w, http.StatusBadRequest, "import requires import_all=true or at least one selection")
		h.recordImport(r, req.Targets, 0, 0, 0, 0, 0, start, "failed", "import requires import_all=true or at least one selection")
		return
	}
	if err := validateAdoptionSelections(req.Selections); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		h.recordImport(r, req.Targets, 0, 0, 0, 0, 0, start, "failed", err.Error())
		return
	}

	results, err := h.adoption.Import(r.Context(), service.AdoptionImportRequest{
		Targets:    mapAdoptionTargets(req.Targets),
		Selections: mapAdoptionSelections(req.Selections),
		ImportAll:  req.ImportAll,
	})
	if err != nil {
		writeAdoptionServiceError(w, err)
		h.recordImport(r, req.Targets, 0, 0, 0, 0, 0, start, "failed", err.Error())
		return
	}
	successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionImportStats(results)
	result := "success"
	if failureCount > 0 {
		result = "partial_failure"
	}
	if len(results) > 0 && successCount == 0 && failureCount > 0 {
		result = "failed"
	}
	h.recordImport(r, req.Targets, len(results), successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount, start, result, "")
	writeData(w, http.StatusOK, mapAdoptionImportResultResponses(results))
}

func (h *AdoptionHandler) recordScan(r *http.Request, targets []dto.AdoptionTargetRequest, candidateCount, redactedEnvKeyCount, redactedLabelKeyCount int, start time.Time, success bool, errMsg string) {
	duration := time.Since(start)
	if h.metrics != nil {
		h.metrics.RecordAdoptionScan(len(targets), candidateCount, redactedEnvKeyCount+redactedLabelKeyCount, duration, success)
	}
	fields := adoptionLogFields(r, targets)
	fields = append(fields,
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", candidateCount),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.String("result", resultStatus(success, errMsg)),
	)
	if errMsg != "" {
		fields = append(fields, zap.String("error", errMsg))
	}
	h.logger.Info("adoption scan completed", fields...)
}

func (h *AdoptionHandler) recordImport(r *http.Request, targets []dto.AdoptionTargetRequest, candidateCount, successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount int, start time.Time, result, errMsg string) {
	duration := time.Since(start)
	metricsFailureCount := failureCount
	if h.metrics != nil {
		if result == "failed" && metricsFailureCount == 0 {
			metricsFailureCount = 1
		}
		h.metrics.RecordAdoptionImport(candidateCount, successCount, metricsFailureCount, redactedEnvKeyCount+redactedLabelKeyCount, duration)
	}
	fields := adoptionLogFields(r, targets)
	fields = append(fields,
		zap.Int("target_count", len(targets)),
		zap.Int("candidate_count", candidateCount),
		zap.Int("success_count", successCount),
		zap.Int("failure_count", failureCount),
		zap.Int("redacted_env_key_count", redactedEnvKeyCount),
		zap.Int("redacted_label_key_count", redactedLabelKeyCount),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.String("result", result),
	)
	if errMsg != "" {
		fields = append(fields, zap.String("error", errMsg))
	}
	h.logger.Info("adoption import completed", fields...)
}

func adoptionLogFields(r *http.Request, targets []dto.AdoptionTargetRequest) []zap.Field {
	fields := requestActorLogFields(r)
	fields = append(fields, zap.String("request_id", chimiddleware.GetReqID(r.Context())))
	if len(targets) == 1 {
		fields = append(fields,
			zap.String("target_name", normalizeAdoptionName(targets[0].Name)),
			zap.String("endpoint_ref", strings.TrimSpace(targets[0].EndpointRef)),
			zap.String("environment_name", normalizeAdoptionName(targets[0].EnvironmentName)),
		)
	}
	return fields
}

func adoptionPreviewStats(previews []service.AdoptionPreview) (candidateCount, redactedEnvKeyCount, redactedLabelKeyCount int) {
	for _, preview := range previews {
		candidateCount += len(preview.Containers)
		for _, container := range preview.Containers {
			redactedEnvKeyCount += len(container.RedactedEnvironmentKeys)
			redactedLabelKeyCount += len(container.RedactedLabelKeys)
		}
	}
	return candidateCount, redactedEnvKeyCount, redactedLabelKeyCount
}

func adoptionImportStats(results []service.AdoptionImportResult) (successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount int) {
	for _, result := range results {
		if result.Status == "failed" || result.Error != "" {
			failureCount++
		} else {
			successCount++
		}
		redactedEnvKeyCount += len(result.RedactedEnvironmentKeys)
		redactedLabelKeyCount += len(result.RedactedLabelKeys)
	}
	return successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount
}

func resultStatus(success bool, errMsg string) string {
	if success && errMsg == "" {
		return "success"
	}
	return "failed"
}

func requestActorLogFields(r *http.Request) []zap.Field {
	fields := []zap.Field{}
	if p := auth.GetPrincipal(r.Context()); p != nil && p.IsAuthenticated() {
		fields = append(fields,
			zap.String("actor_subject", p.Subject),
			zap.String("actor_pubkey", p.PubKey),
			zap.String("actor_method", string(p.Method)),
		)
	}
	return fields
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
		if strings.TrimSpace(target.EndpointRef) != "" && strings.TrimSpace(target.DockerHost) != "" {
			return errBadRequest("target endpoint_ref cannot be combined with docker_host")
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
	if strings.Contains(msg, "adoption target") || strings.Contains(msg, "docker_host") || strings.Contains(msg, "endpoint_ref") || strings.Contains(msg, "raw docker_host") || strings.Contains(msg, "import requires") || strings.Contains(msg, "selection") {
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
			EndpointRef:     strings.TrimSpace(target.EndpointRef),
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
				Discovered:          mapDiscoveredContainerResponse(container.Discovered, container.SafeEnvironment, container.SafeLabels, container.RedactedEnvironmentKeys, container.RedactedLabelKeys),
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

func mapAdoptionTargetResponse(target service.AdoptionTarget) dto.AdoptionTargetResponse {
	resp := dto.AdoptionTargetResponse{Name: target.Name, EndpointRef: target.EndpointRef, EnvironmentName: target.EnvironmentName}
	if target.EndpointRef == "" {
		resp.DockerHost = target.DockerHost
	}
	return resp
}

func mapDiscoveredContainerResponse(discovered runtime.DiscoveredContainer, safeEnvironment, safeLabels map[string]string, redactedEnvironmentKeys, redactedLabelKeys []string) dto.DiscoveredContainerResponse {
	return dto.DiscoveredContainerResponse{
		TargetName:              discovered.TargetName,
		EnvironmentName:         discovered.EnvironmentName,
		ContainerID:             discovered.ContainerID,
		ContainerName:           discovered.ContainerName,
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
		Compose:                 mapComposeMetadataResponse(discovered.Compose),
		HealthStatus:            string(discovered.HealthStatus),
		Warnings:                append([]string(nil), discovered.Warnings...),
		Adoptable:               discovered.Adoptable,
	}
}

func mapComposeMetadataResponse(compose *domain.ComposeMetadata) *dto.ComposeMetadataResponse {
	if compose == nil {
		return nil
	}
	return &dto.ComposeMetadataResponse{
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

func isNilHandlerDependency(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
