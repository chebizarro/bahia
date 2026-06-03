package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// AdoptionOperatorService is the narrow service surface required by the
// signer-first adoption control-plane transport.
type AdoptionOperatorService interface {
	Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error)
	Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error)
}

// RuntimeLifecycleOperatorService is the narrow service surface required by the
// signer-first direct-runtime control-plane transport.
type RuntimeLifecycleOperatorService interface {
	BuildDesiredStateSnapshot(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DesiredServiceSpec, error)
	Deploy(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*domain.RuntimeObservation, error)
	DeployWithStatus(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, service.DeployStatusCallback) (*domain.RuntimeObservation, error)
	Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
	Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
}

type directRuntimeActionEventRequest struct {
	Action        string `json:"action"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	ArtifactID    string `json:"artifact_id,omitempty"`
}

type parsedDirectRuntimeActionRequest struct {
	Action        string
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
	ArtifactID    *uuid.UUID
}

type adoptionScanEventRequest struct {
	Targets []adoptionEventTarget `json:"targets"`
}

type adoptionImportEventRequest struct {
	Targets    []adoptionEventTarget    `json:"targets"`
	Selections []adoptionEventSelection `json:"selections,omitempty"`
	ImportAll  bool                     `json:"import_all,omitempty"`
}

type adoptionEventTarget struct {
	Name            string  `json:"name"`
	EndpointRef     string  `json:"endpoint_ref"`
	EnvironmentName string  `json:"environment_name,omitempty"`
	DockerHost      *string `json:"docker_host,omitempty"`
}

type adoptionEventSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

func directRuntimeActionFromContent(content string) string {
	var raw directRuntimeActionEventRequest
	if json.Unmarshal([]byte(content), &raw) != nil {
		return ""
	}
	action := strings.ToLower(strings.TrimSpace(raw.Action))
	if !isDirectRuntimeAction(action) {
		return ""
	}
	return action
}

func parseDirectRuntimeActionRequest(event *nostr.Event) (parsedDirectRuntimeActionRequest, bool, error) {
	var raw directRuntimeActionEventRequest
	if strings.TrimSpace(event.Content) == "" {
		return parsedDirectRuntimeActionRequest{}, false, nil
	}
	if err := json.Unmarshal([]byte(event.Content), &raw); err != nil {
		return parsedDirectRuntimeActionRequest{}, false, nil
	}
	action := strings.ToLower(strings.TrimSpace(raw.Action))
	if !isDirectRuntimeAction(action) {
		return parsedDirectRuntimeActionRequest{}, false, nil
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(raw.ServiceID))
	if err != nil {
		return parsedDirectRuntimeActionRequest{}, true, fmt.Errorf("invalid service_id: %w", err)
	}
	environmentID, err := uuid.Parse(strings.TrimSpace(raw.EnvironmentID))
	if err != nil {
		return parsedDirectRuntimeActionRequest{}, true, fmt.Errorf("invalid environment_id: %w", err)
	}
	var artifactID *uuid.UUID
	if strings.TrimSpace(raw.ArtifactID) != "" {
		parsedArtifactID, err := uuid.Parse(strings.TrimSpace(raw.ArtifactID))
		if err != nil {
			return parsedDirectRuntimeActionRequest{}, true, fmt.Errorf("invalid artifact_id: %w", err)
		}
		artifactID = &parsedArtifactID
	}
	if action != "deploy" && artifactID != nil {
		return parsedDirectRuntimeActionRequest{}, true, fmt.Errorf("artifact_id is only valid for deploy actions")
	}
	return parsedDirectRuntimeActionRequest{Action: action, ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID}, true, nil
}

func isDirectRuntimeAction(action string) bool {
	switch action {
	case "deploy", "restart", "stop":
		return true
	default:
		return false
	}
}

func (r *Reactor) handleDirectRuntimeActionRequest(ctx context.Context, event *nostr.Event, req parsedDirectRuntimeActionRequest) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey, "action", req.Action, "service_id", req.ServiceID.String(), "environment_id", req.EnvironmentID.String())
	if !r.isAuthorizedFor(event.PubKey, operatorScopeDirectRuntime) {
		logger.Warn("unauthorized direct-runtime action request")
		r.publishActionResult(ctx, event, req.Action, "failed", fmt.Errorf("requester not in authorized direct-runtime list"))
		return
	}
	if r.runtimeLifecycle == nil {
		r.publishActionResult(ctx, event, req.Action, "failed", fmt.Errorf("runtime lifecycle service is not configured"))
		return
	}

	_ = r.publishActionStatus(ctx, event, req.Action, "executing", "Direct runtime action started")
	var obs *domain.RuntimeObservation
	var err error
	switch req.Action {
	case "deploy":
		statusFn := r.deployStatusCallbackFor(ctx, event, req.Action)
		obs, err = r.runtimeLifecycle.DeployWithStatus(ctx, req.ServiceID, req.EnvironmentID, req.ArtifactID, statusFn)
	case "restart":
		obs, err = r.runtimeLifecycle.Restart(ctx, req.ServiceID, req.EnvironmentID)
	case "stop":
		obs, err = r.runtimeLifecycle.Stop(ctx, req.ServiceID, req.EnvironmentID)
	}
	if err != nil {
		logger.Error("direct-runtime action failed", "error", err)
		r.publishActionResult(ctx, event, req.Action, "failed", err)
		return
	}
	if err := r.publishRuntimeActionResult(ctx, event, req.Action, req.ServiceID, req.EnvironmentID, obs); err != nil {
		logger.Error("failed to publish direct-runtime action result", "error", err)
	}
}

// deployStatusCallbackFor returns a DeployStatusCallback that publishes step
// progression through the direct handler status path used by legacy tests.
func (r *Reactor) deployStatusCallbackFor(ctx context.Context, event *nostr.Event, action string) service.DeployStatusCallback {
	return func(cbCtx context.Context, step service.DeployStep, message string) {
		_ = r.publishActionStatus(cbCtx, event, action, string(step), message)
	}
}

func (r *Reactor) deploymentStatusCallbackFor(ctx context.Context, event *nostr.Event) service.DeployStatusCallback {
	return func(cbCtx context.Context, step service.DeployStep, message string) {
		_ = r.publishStatus(cbCtx, event, string(step), message)
	}
}

func (r *Reactor) handleAdoptionScanRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey, "operation", "scan")
	if !r.isAuthorizedFor(event.PubKey, operatorScopeAdoption) {
		logger.Warn("unauthorized adoption scan request")
		r.publishAdoptionError(ctx, event, KindAdoptionScanResult, "scan", "unauthorized", "requester not in authorized adoption list")
		return
	}
	if r.adoption == nil {
		r.publishAdoptionError(ctx, event, KindAdoptionScanResult, "scan", "adoption_unavailable", "adoption service is not configured")
		return
	}

	var req adoptionScanEventRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishAdoptionError(ctx, event, KindAdoptionScanResult, "scan", "parse_error", err.Error())
		return
	}
	targets, err := mapAdoptionEventTargets(req.Targets)
	if err != nil {
		r.publishAdoptionError(ctx, event, KindAdoptionScanResult, "scan", "validation_error", err.Error())
		return
	}

	_ = r.publishAdoptionStatus(ctx, event, "scan", targets, "Adoption scan started")
	previews, err := r.adoption.Scan(ctx, service.AdoptionScanRequest{Targets: targets})
	if err != nil {
		logger.Error("adoption scan failed", "error", err)
		r.publishAdoptionError(ctx, event, KindAdoptionScanResult, "scan", "operation_failed", err.Error())
		return
	}
	status := adoptionScanStatus(previews)
	if err := r.publishAdoptionScanResult(ctx, event, status, previews); err != nil {
		logger.Error("failed to publish adoption scan result", "error", err)
	}
}

func (r *Reactor) handleAdoptionImportRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey, "operation", "import")
	if !r.isAuthorizedFor(event.PubKey, operatorScopeAdoption) {
		logger.Warn("unauthorized adoption import request")
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "unauthorized", "requester not in authorized adoption list")
		return
	}
	if r.adoption == nil {
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "adoption_unavailable", "adoption service is not configured")
		return
	}

	var req adoptionImportEventRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "parse_error", err.Error())
		return
	}
	targets, err := mapAdoptionEventTargets(req.Targets)
	if err != nil {
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "validation_error", err.Error())
		return
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "validation_error", "import requires import_all=true or at least one selection")
		return
	}
	selections, err := mapAdoptionEventSelections(req.Selections)
	if err != nil {
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "validation_error", err.Error())
		return
	}

	_ = r.publishAdoptionStatus(ctx, event, "import", targets, "Adoption import started")
	results, err := r.adoption.Import(ctx, service.AdoptionImportRequest{Targets: targets, Selections: selections, ImportAll: req.ImportAll})
	if err != nil {
		logger.Error("adoption import failed", "error", err)
		r.publishAdoptionError(ctx, event, KindAdoptionImportResult, "import", "operation_failed", err.Error())
		return
	}
	status := adoptionImportStatus(results)
	if err := r.publishAdoptionImportResult(ctx, event, status, results); err != nil {
		logger.Error("failed to publish adoption import result", "error", err)
	}
}

func mapAdoptionEventTargets(targets []adoptionEventTarget) ([]service.AdoptionTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one target is required")
	}
	mapped := make([]service.AdoptionTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		if target.DockerHost != nil {
			return nil, fmt.Errorf("target docker_host is forbidden for signer-first adoption; use endpoint_ref")
		}
		name := normalizeAdoptionEventName(target.Name)
		if name == "" {
			return nil, fmt.Errorf("target name is required")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("target names must be unique after normalization")
		}
		seen[name] = struct{}{}
		endpointRef := strings.TrimSpace(target.EndpointRef)
		if endpointRef == "" {
			return nil, fmt.Errorf("target endpoint_ref is required for signer-first adoption")
		}
		environmentName := normalizeAdoptionEventName(target.EnvironmentName)
		if strings.TrimSpace(target.EnvironmentName) != "" && environmentName == "" {
			return nil, fmt.Errorf("target environment_name is invalid")
		}
		mapped = append(mapped, service.AdoptionTarget{Name: name, EndpointRef: endpointRef, EnvironmentName: environmentName})
	}
	return mapped, nil
}

func mapAdoptionEventSelections(selections []adoptionEventSelection) ([]service.AdoptionSelection, error) {
	mapped := make([]service.AdoptionSelection, 0, len(selections))
	seen := map[string]struct{}{}
	for _, selection := range selections {
		targetName := normalizeAdoptionEventName(selection.TargetName)
		containerID := strings.TrimSpace(selection.ContainerID)
		if targetName == "" {
			return nil, fmt.Errorf("selection target_name is required")
		}
		if containerID == "" {
			return nil, fmt.Errorf("selection container_id is required")
		}
		serviceNameOverride := normalizeAdoptionEventName(selection.ServiceNameOverride)
		if strings.TrimSpace(selection.ServiceNameOverride) != "" && serviceNameOverride == "" {
			return nil, fmt.Errorf("selection service_name_override is invalid")
		}
		key := targetName + "/" + containerID
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("selection entries must be unique")
		}
		seen[key] = struct{}{}
		mapped = append(mapped, service.AdoptionSelection{TargetName: targetName, ContainerID: containerID, ServiceNameOverride: serviceNameOverride})
	}
	return mapped, nil
}

var invalidAdoptionEventNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeAdoptionEventName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidAdoptionEventNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func adoptionScanStatus(previews []service.AdoptionPreview) string {
	for _, preview := range previews {
		if preview.Error != "" {
			return "failed"
		}
	}
	return "success"
}

func adoptionImportStatus(results []service.AdoptionImportResult) string {
	successCount := 0
	failureCount := 0
	for _, result := range results {
		if result.Status == "failed" || result.Error != "" {
			failureCount++
		} else {
			successCount++
		}
	}
	if len(results) > 0 && successCount == 0 && failureCount > 0 {
		return "failed"
	}
	if failureCount > 0 {
		return "partial_failure"
	}
	return "success"
}

func (r *Reactor) publishActionStatus(ctx context.Context, requestEvent *nostr.Event, action, step, message string) error {
	tags := nostr.Tags{
		{"status", "processing"},
		{"action", action},
		{"step", step},
		{"category", "runtime_action"},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	return r.publishCanonicalStatus(ctx, requestEvent, tags, map[string]any{
		"status":  "processing",
		"action":  action,
		"step":    step,
		"message": message,
	})
}

func (r *Reactor) publishRuntimeActionResult(ctx context.Context, requestEvent *nostr.Event, action string, serviceID, environmentID uuid.UUID, obs *domain.RuntimeObservation) error {
	payload := dto.RuntimeActionResponseFromDomain(action, serviceID, environmentID, obs)
	tags := nostr.Tags{
		{"status", "success"},
		{"action", action},
		{"service", serviceID.String()},
		{"environment", environmentID.String()},
	}
	if obs != nil {
		tags = append(tags, nostr.Tag{"observation_id", obs.ID.String()})
		if obs.NormalizedHash != "" {
			tags = append(tags, nostr.Tag{"observed_hash", obs.NormalizedHash})
		}
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishAdoptionStatus(ctx context.Context, requestEvent *nostr.Event, operation string, targets []service.AdoptionTarget, message string) error {
	tags := nostr.Tags{
		{"status", "processing"},
		{"operation", operation},
		{"category", "adoption"},
	}
	tags = appendAdoptionTargetTags(tags, targets)
	return r.publishCanonicalStatus(ctx, requestEvent, tags, map[string]any{
		"status":    "processing",
		"operation": operation,
		"message":   message,
	})
}

func (r *Reactor) publishAdoptionScanResult(ctx context.Context, requestEvent *nostr.Event, status string, previews []service.AdoptionPreview) error {
	payload := dto.AdoptionPreviewResponsesFromService(previews)
	tags := nostr.Tags{
		{"status", status},
		{"operation", "scan"},
	}
	tags = appendAdoptionTargetTags(tags, adoptionTargetsFromPreviews(previews))
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishAdoptionImportResult(ctx context.Context, requestEvent *nostr.Event, status string, results []service.AdoptionImportResult) error {
	payload := dto.AdoptionImportResultResponsesFromService(results)
	tags := nostr.Tags{
		{"status", status},
		{"operation", "import"},
	}
	for _, result := range results {
		if result.TargetName != "" {
			tags = append(tags, nostr.Tag{"target", result.TargetName})
		}
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishAdoptionError(ctx context.Context, requestEvent *nostr.Event, _ int, operation, step, message string) error {
	tags := nostr.Tags{
		{"status", "failed"},
		{"operation", operation},
		{"step", step},
		{"error", message},
	}
	return r.publishContextVMResult(ctx, requestEvent, nil, tags, &JSONRPCError{Code: -32000, Message: message})
}

func appendAdoptionTargetTags(tags nostr.Tags, targets []service.AdoptionTarget) nostr.Tags {
	for _, target := range targets {
		if target.Name != "" {
			tags = append(tags, nostr.Tag{"target", target.Name})
		}
		if target.EndpointRef != "" {
			tags = append(tags, nostr.Tag{"endpoint_ref", target.EndpointRef})
		}
		if target.EnvironmentName != "" {
			tags = append(tags, nostr.Tag{"environment_name", target.EnvironmentName})
		}
	}
	return tags
}

func adoptionTargetsFromPreviews(previews []service.AdoptionPreview) []service.AdoptionTarget {
	targets := make([]service.AdoptionTarget, 0, len(previews))
	for _, preview := range previews {
		targets = append(targets, preview.Target)
	}
	return targets
}
