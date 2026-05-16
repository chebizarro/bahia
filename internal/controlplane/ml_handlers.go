package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (r *Reactor) authorizeMLRequest(ctx context.Context, event *nostr.Event, resultKind int, step string) bool {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishMLResult(ctx, event, resultKind, "rejected", step, "requester not in authorized list", nil, nil)
		return false
	}
	if tagValueNostr(event.Tags, "d") == "" {
		_ = r.publishMLResult(ctx, event, resultKind, "failed", "validation_error", "d tag is required for addressable ML command events", nil, nil)
		return false
	}
	if r.mlRegistry == nil {
		_ = r.publishMLResult(ctx, event, resultKind, "failed", step+"_unavailable", "ML registry is not configured", nil, nil)
		return false
	}
	return true
}

func (r *Reactor) handleMLRecipeRunRequest(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishMLResult(ctx, event, KindMLRecipeRunResult, "rejected", "unauthorized", "requester not in authorized list", nil, nil)
		return
	}
	_ = r.publishMLResult(ctx, event, KindMLRecipeRunResult, "failed", "recipe_runs_not_enabled", "ML recipe execution is not enabled in D1", nil, nil)
}

func (r *Reactor) handleMLModelImportRequest(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishMLResult(ctx, event, KindMLModelImportResult, "rejected", "unauthorized", "requester not in authorized list", nil, nil)
		return
	}
	_ = r.publishMLResult(ctx, event, KindMLModelImportResult, "failed", "model_import_not_enabled", "ML model import orchestration is not enabled in D1", nil, nil)
}

func (r *Reactor) handleMLInferenceDeployRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeMLRequest(ctx, event, KindMLInferenceDeployResult, "ml_deploy") {
		return
	}
	if r.mlExecutor == nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "ml_inference_provisioning_unavailable", "ML inference provisioning executor is not configured", nil, nil)
		return
	}
	req, err := parseMLDeployRequest(event)
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "parse_error", err.Error(), nil, nil)
		return
	}
	endpoint, err := r.resolveMLEndpoint(ctx, req.EndpointID, req.Endpoint)
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "endpoint_resolution_error", err.Error(), nil, nil)
		return
	}
	version, err := r.resolveMLModelVersion(ctx, req.ModelVersionID, req.ModelVersion)
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "model_version_resolution_error", err.Error(), endpoint, nil)
		return
	}
	metadata := mlNostrMetadata(event, map[string]any{
		"nostr_request_command":     "ml_inference_deploy",
		"nostr_endpoint_coord":      firstNonEmpty(req.Endpoint, tagValueNostr(event.Tags, "endpoint")),
		"nostr_model_version_coord": firstNonEmpty(req.ModelVersion, tagValueNostr(event.Tags, "model_version")),
		"nostr_environment_coord":   firstNonEmpty(tagValueNostr(event.Tags, "environment"), mlEnvironmentFromEndpointCoord(req.Endpoint)),
	})
	intent := &domain.MLDeploymentIntent{
		EndpointID:        endpoint.ID,
		EnvironmentID:     endpoint.EnvironmentID,
		ModelVersionID:    version.ID,
		RequestedBy:       event.PubKey,
		SourceKind:        domain.SourceKindEventTriggered,
		RuntimePreference: domain.MLRuntimeKind(firstNonEmpty(req.RuntimePreference, tagValueNostr(event.Tags, "runtime"))),
		Metadata:          metadata,
	}
	if err := r.mlRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "intent_error", err.Error(), endpoint, version)
		return
	}
	go func() {
		if err := r.mlExecutor.ProcessDeploymentIntent(ctx, intent.ID); err != nil {
			_ = r.publishMLResult(ctx, event, KindMLInferenceDeployResult, "failed", "executor_error", err.Error(), endpoint, version, nostr.Tag{"intent", intent.ID.String()})
		}
	}()
}

func (r *Reactor) handleMLInferenceDeploymentApproval(ctx context.Context, event *nostr.Event) {
	if !r.authorizeMLRequest(ctx, event, KindMLInferenceApprovalResult, "ml_approval") {
		return
	}
	var req struct {
		IntentID string `json:"intent_id,omitempty"`
		Decision string `json:"decision,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.IntentID == "" {
		req.IntentID = tagValueNostr(event.Tags, "intent")
	}
	if req.Decision == "" {
		req.Decision = tagValueNostr(event.Tags, "decision")
	}
	intentID, err := uuid.Parse(req.IntentID)
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceApprovalResult, "failed", "validation_error", fmt.Sprintf("invalid intent_id: %v", err), nil, nil)
		return
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		_ = r.publishMLResult(ctx, event, KindMLInferenceApprovalResult, "failed", "validation_error", "decision must be 'approve' or 'reject'", nil, nil, nostr.Tag{"intent", intentID.String()})
		return
	}
	if req.Decision == "approve" {
		err = r.mlRegistry.ApproveDeploymentIntent(ctx, intentID)
	} else {
		err = r.mlRegistry.RejectDeploymentIntent(ctx, intentID)
	}
	intent, _ := r.mlRegistry.GetDeploymentIntent(ctx, intentID)
	var endpoint *domain.MLInferenceEndpoint
	var version *domain.MLModelVersion
	if intent != nil {
		endpoint, _ = r.mlRegistry.GetInferenceEndpoint(ctx, intent.EndpointID)
		version, _ = r.mlRegistry.GetModelVersion(ctx, intent.ModelVersionID)
	}
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceApprovalResult, "failed", "approval_error", err.Error(), endpoint, version, nostr.Tag{"intent", intentID.String()})
		return
	}
	status := "succeeded"
	if req.Decision == "reject" {
		status = "rejected"
	}
	_ = r.publishMLResult(ctx, event, KindMLInferenceApprovalResult, status, req.Decision, "ML inference deployment approval decision recorded", endpoint, version, nostr.Tag{"intent", intentID.String()})
}

func (r *Reactor) handleMLInferenceRollbackRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeMLRequest(ctx, event, KindMLInferenceRollbackResult, "ml_rollback") {
		return
	}
	if r.mlExecutor == nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceRollbackResult, "failed", "ml_inference_provisioning_unavailable", "ML inference provisioning executor is not configured", nil, nil)
		return
	}
	var req struct {
		EndpointID  string `json:"endpoint_id,omitempty"`
		Endpoint    string `json:"endpoint,omitempty"`
		RequestedBy string `json:"requested_by,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	endpoint, err := r.resolveMLEndpoint(ctx, req.EndpointID, req.Endpoint)
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceRollbackResult, "failed", "endpoint_resolution_error", err.Error(), nil, nil)
		return
	}
	requestedBy := req.RequestedBy
	if requestedBy == "" {
		requestedBy = event.PubKey
	}
	endpointCoord := firstNonEmpty(req.Endpoint, tagValueNostr(event.Tags, "endpoint"))
	intent, err := r.mlRegistry.RollbackWithMetadata(ctx, endpoint.ID, endpoint.EnvironmentID, requestedBy, mlNostrMetadata(event, map[string]any{
		"nostr_request_command":   "ml_inference_rollback",
		"nostr_endpoint_coord":    endpointCoord,
		"nostr_environment_coord": firstNonEmpty(tagValueNostr(event.Tags, "environment"), mlEnvironmentFromEndpointCoord(endpointCoord)),
	}))
	if err != nil {
		_ = r.publishMLResult(ctx, event, KindMLInferenceRollbackResult, "failed", "rollback_error", err.Error(), endpoint, nil)
		return
	}
	go func() {
		if err := r.mlExecutor.ProcessDeploymentIntent(ctx, intent.ID); err != nil {
			_ = r.publishMLResult(ctx, event, KindMLInferenceRollbackResult, "failed", "executor_error", err.Error(), endpoint, nil, nostr.Tag{"intent", intent.ID.String()})
		}
	}()
}

type mlDeployRequest struct {
	EndpointID        string         `json:"endpoint_id,omitempty"`
	Endpoint          string         `json:"endpoint,omitempty"`
	ModelVersionID    string         `json:"model_version_id,omitempty"`
	ModelVersion      string         `json:"model_version,omitempty"`
	RuntimePreference string         `json:"runtime_preference,omitempty"`
	Placement         map[string]any `json:"placement,omitempty"`
}

func parseMLDeployRequest(event *nostr.Event) (*mlDeployRequest, error) {
	var req mlDeployRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.Endpoint == "" {
		req.Endpoint = tagValueNostr(event.Tags, "endpoint")
	}
	if req.ModelVersion == "" {
		req.ModelVersion = tagValueNostr(event.Tags, "model_version")
	}
	if req.RuntimePreference == "" {
		req.RuntimePreference = tagValueNostr(event.Tags, "runtime")
	}
	if req.EndpointID == "" && req.Endpoint == "" {
		return nil, fmt.Errorf("endpoint or endpoint_id is required")
	}
	if req.ModelVersionID == "" && req.ModelVersion == "" {
		return nil, fmt.Errorf("model_version or model_version_id is required")
	}
	return &req, nil
}

func (r *Reactor) resolveMLEndpoint(ctx context.Context, endpointID, coord string) (*domain.MLInferenceEndpoint, error) {
	if endpointID != "" {
		id, err := uuid.Parse(endpointID)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint_id: %w", err)
		}
		endpoint, err := r.mlRegistry.GetInferenceEndpoint(ctx, id)
		if err != nil {
			return nil, err
		}
		if endpoint == nil {
			return nil, fmt.Errorf("ML endpoint %s not found", id)
		}
		return endpoint, nil
	}
	if coord == "" {
		return nil, fmt.Errorf("endpoint coordinate is required")
	}
	name, envRef, ok := strings.Cut(strings.TrimPrefix(coord, "endpoint:"), ":")
	if !ok || name == "" || envRef == "" {
		return nil, fmt.Errorf("endpoint coordinate must be endpoint:<name>:<environment>")
	}
	var envID uuid.UUID
	if parsed, err := uuid.Parse(envRef); err == nil {
		envID = parsed
	} else {
		if r.registry == nil {
			return nil, fmt.Errorf("environment name resolution is unavailable")
		}
		env, err := r.registry.GetEnvironmentByName(ctx, envRef)
		if err != nil {
			return nil, err
		}
		if env == nil {
			return nil, fmt.Errorf("environment %q not found", envRef)
		}
		envID = env.ID
	}
	endpoint, err := r.mlRegistry.GetInferenceEndpointByNameEnv(ctx, name, envID)
	if err != nil {
		return nil, err
	}
	if endpoint == nil {
		return nil, fmt.Errorf("ML endpoint %s not found in environment %s", name, envRef)
	}
	return endpoint, nil
}

func (r *Reactor) resolveMLModelVersion(ctx context.Context, versionID, coord string) (*domain.MLModelVersion, error) {
	if versionID != "" {
		id, err := uuid.Parse(versionID)
		if err != nil {
			return nil, fmt.Errorf("invalid model_version_id: %w", err)
		}
		version, err := r.mlRegistry.GetModelVersion(ctx, id)
		if err != nil {
			return nil, err
		}
		if version == nil {
			return nil, fmt.Errorf("ML model version %s not found", id)
		}
		return version, nil
	}
	if coord == "" {
		return nil, fmt.Errorf("model version coordinate is required")
	}
	parts := strings.SplitN(strings.TrimPrefix(coord, "model-version:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("model version coordinate must be model-version:<model-slug>:<version>")
	}
	model, err := r.mlRegistry.GetModelBySlug(ctx, parts[0])
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, fmt.Errorf("ML model %q not found", parts[0])
	}
	version, err := r.mlRegistry.GetModelVersionByModelVersion(ctx, model.ID, parts[1])
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, fmt.Errorf("ML model version %q not found for model %q", parts[1], parts[0])
	}
	return version, nil
}

func mlNostrMetadata(event *nostr.Event, extra map[string]any) map[string]any {
	metadata := map[string]any{
		"nostr_event_id":       event.ID,
		"nostr_request_pubkey": event.PubKey,
		"nostr_request_kind":   event.Kind,
		"nostr_d_tag":          tagValueNostr(event.Tags, "d"),
	}
	for k, v := range extra {
		if v != nil && fmt.Sprint(v) != "" {
			metadata[k] = v
		}
	}
	return metadata
}

func (r *Reactor) publishMLResult(ctx context.Context, requestEvent *nostr.Event, kind int, status, code, message string, endpoint *domain.MLInferenceEndpoint, version *domain.MLModelVersion, extraTags ...nostr.Tag) error {
	endpointCoord := mlRequestString(requestEvent, "endpoint")
	modelVersionCoord := mlRequestString(requestEvent, "model_version")
	environmentCoord := firstNonEmpty(tagValueNostr(requestEvent.Tags, "environment"), mlEnvironmentFromEndpointCoord(endpointCoord))
	content := map[string]any{
		"request_event_id": requestEvent.ID,
		"status":           status,
		"message":          message,
	}
	if status == "failed" || status == "rejected" {
		content["error"] = map[string]any{"code": code, "message": message}
	}
	if endpoint != nil {
		content["endpoint_id"] = endpoint.ID.String()
		content["environment_id"] = endpoint.EnvironmentID.String()
	}
	if endpointCoord != "" {
		content["endpoint"] = endpointCoord
	}
	if environmentCoord != "" {
		content["environment"] = environmentCoord
	}
	if version != nil {
		content["model_version_id"] = version.ID.String()
		if version.ModelID != uuid.Nil {
			content["model_id"] = version.ModelID.String()
		}
		content["version"] = version.Version
	}
	if modelVersionCoord != "" {
		content["model_version"] = modelVersionCoord
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"d", "result:" + requestEvent.ID},
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", status},
	}
	if endpointCoord != "" {
		tags = append(tags, nostr.Tag{"endpoint", endpointCoord})
	}
	if environmentCoord != "" {
		tags = append(tags, nostr.Tag{"environment", environmentCoord})
	}
	if endpoint != nil {
		tags = append(tags, nostr.Tag{"endpoint_id", endpoint.ID.String()}, nostr.Tag{"environment_id", endpoint.EnvironmentID.String()})
	}
	if modelVersionCoord != "" {
		tags = append(tags, nostr.Tag{"model_version", modelVersionCoord})
	}
	if version != nil {
		tags = append(tags, nostr.Tag{"model_version_id", version.ID.String()}, nostr.Tag{"version", version.Version})
		if version.ModelID != uuid.Nil {
			tags = append(tags, nostr.Tag{"model_id", version.ModelID.String()})
		}
	}
	tags = appendMLRequestTags(tags, requestEvent)
	tags = append(tags, extraTags...)
	if code != "" {
		tags = append(tags, nostr.Tag{"result", code})
	}
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign ML result: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func appendMLRequestTags(tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	allowed := map[string]struct{}{"model": {}, "model_version": {}, "recipe": {}, "run": {}, "endpoint": {}, "environment": {}, "deployment": {}, "artifact": {}, "worker": {}, "runtime": {}, "task": {}, "accelerator": {}}
	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		if _, ok := allowed[tag[0]]; ok {
			tags = append(tags, nostr.Tag{tag[0], tag[1]})
		}
	}
	return tags
}

func dedupeTags(tags nostr.Tags) nostr.Tags {
	seen := map[string]struct{}{}
	out := make(nostr.Tags, 0, len(tags))
	for _, tag := range tags {
		if len(tag) < 2 {
			out = append(out, tag)
			continue
		}
		key := tag[0] + "\x00" + tag[1] + "\x00" + strconv.Itoa(len(tag))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func mlRequestString(event *nostr.Event, key string) string {
	if event == nil {
		return ""
	}
	if value := tagValueNostr(event.Tags, key); value != "" {
		return value
	}
	if strings.TrimSpace(event.Content) == "" {
		return ""
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(event.Content), &body); err != nil {
		return ""
	}
	if value, ok := body[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func mlEnvironmentFromEndpointCoord(coord string) string {
	trimmed := strings.TrimPrefix(coord, "endpoint:")
	parts := strings.Split(trimmed, ":")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
