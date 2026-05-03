package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

// AdoptionOperatorService is the narrow service surface required by the
// signer-first adoption control-plane transport.
type AdoptionOperatorService interface {
	Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error)
	Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error)
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

func (r *Reactor) publishAdoptionStatus(ctx context.Context, requestEvent *nostr.Event, operation string, targets []service.AdoptionTarget, message string) error {
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "processing"},
		{"operation", operation},
	}
	tags = appendAdoptionTargetTags(tags, targets)
	event := &nostr.Event{Kind: KindAdoptionStatus, CreatedAt: nostr.Now(), Tags: tags, Content: message}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign adoption status: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishAdoptionScanResult(ctx context.Context, requestEvent *nostr.Event, status string, previews []service.AdoptionPreview) error {
	payload := dto.AdoptionPreviewResponsesFromService(previews)
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal adoption scan result: %w", err)
	}
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", status},
		{"operation", "scan"},
	}
	tags = appendAdoptionTargetTags(tags, adoptionTargetsFromPreviews(previews))
	event := &nostr.Event{Kind: KindAdoptionScanResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign adoption scan result: %w", err)
	}
	_, err = r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishAdoptionImportResult(ctx context.Context, requestEvent *nostr.Event, status string, results []service.AdoptionImportResult) error {
	payload := dto.AdoptionImportResultResponsesFromService(results)
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal adoption import result: %w", err)
	}
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", status},
		{"operation", "import"},
	}
	for _, result := range results {
		if result.TargetName != "" {
			tags = append(tags, nostr.Tag{"target", result.TargetName})
		}
	}
	event := &nostr.Event{Kind: KindAdoptionImportResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign adoption import result: %w", err)
	}
	_, err = r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishAdoptionError(ctx context.Context, requestEvent *nostr.Event, kind int, operation, step, message string) error {
	content, _ := json.Marshal(map[string]any{"status": "failed", "operation": operation, "step": step, "error": message})
	event := &nostr.Event{
		Kind:      kind,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "failed"},
			{"operation", operation},
			{"step", step},
			{"error", message},
		},
		Content: string(content),
	}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign adoption error: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
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
