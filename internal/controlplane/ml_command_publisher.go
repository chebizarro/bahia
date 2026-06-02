package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	KindMLModelRegistry             = kinds.MLModelRegistry
	KindMLModelVersionRegistry      = kinds.MLModelVersionRegistry
	KindMLDatasetRegistry           = kinds.MLDatasetRegistry
	KindMLRecipeRegistry            = kinds.MLRecipeRegistry
	KindMLRecipeRunState            = kinds.MLRecipeRunState
	KindMLInferenceEndpointRegistry = kinds.MLInferenceEndpointRegistry
	KindMLInferenceEndpointState    = kinds.MLInferenceEndpointState
	KindMLEvaluationExperimentState = kinds.MLEvaluationExperimentState
	KindMLArtifactProvenanceGraph   = kinds.MLArtifactProvenanceGraph
	KindMLRuntimeCapabilityProfile  = kinds.MLRuntimeCapabilityProfile
)

// MLCommandPublisher emits generic AI/ML control-plane request events.
type MLCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

// NewMLCommandPublisher creates a publisher for REST/MCP-originated AI/ML commands.
func NewMLCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *MLCommandPublisher {
	return &MLCommandPublisher{publisher: publisher, signer: signer}
}

// MLCommandPayload is the compatibility payload accepted by REST/MCP tools.
// Long-running semantics remain Nostr-native: the content describes intent and
// scoped tags/d-tag provide relay correlation; completion arrives as a result event.
type MLCommandPayload struct {
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Content        map[string]any    `json:"content,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// MLCommandReceipt is the correlation handle returned to synchronous callers.
type MLCommandReceipt struct {
	RequestEventID  string         `json:"request_event_id"`
	RequestPubkey   string         `json:"request_pubkey"`
	RequestKind     int            `json:"request_kind"`
	ResultKind      int            `json:"result_kind"`
	ReadModelKinds  map[string]int `json:"read_model_kinds,omitempty"`
	DTag            string         `json:"d_tag"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	RetryHint       string         `json:"retry_hint,omitempty"`
	PublishedRelays int            `json:"published_relays"`
	TimeoutSeconds  int            `json:"timeout_seconds,omitempty"`
	Endpoint        string         `json:"endpoint,omitempty"`
	EndpointID      string         `json:"endpoint_id,omitempty"`
	Environment     string         `json:"environment,omitempty"`
	EnvironmentID   string         `json:"environment_id,omitempty"`
	ModelVersion    string         `json:"model_version,omitempty"`
	ModelVersionID  string         `json:"model_version_id,omitempty"`
	Model           string         `json:"model,omitempty"`
	Recipe          string         `json:"recipe,omitempty"`
	Run             string         `json:"run,omitempty"`
	Artifact        string         `json:"artifact,omitempty"`
	Runtime         string         `json:"runtime,omitempty"`
}

func (p *MLCommandPublisher) PublishMLModelImportRequest(ctx context.Context, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if !hasAnyMLField(cmd.Content, "source", "uri", "repo", "model", "model_version", "artifact") {
		return nil, fmt.Errorf("source, uri, repo, model, model_version, or artifact is required")
	}
	return p.publish(ctx, KindMLModelImportRequest, KindMLModelImportResult, mlImportReadModels(), "ml-model-import", cmd)
}

func (p *MLCommandPublisher) PublishMLRecipeRunRequest(ctx context.Context, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if !hasAnyMLField(cmd.Content, "recipe") {
		return nil, fmt.Errorf("recipe is required")
	}
	return p.publish(ctx, KindMLRecipeRunRequest, KindMLRecipeRunResult, mlRecipeReadModels(), "ml-recipe-run", cmd)
}

func (p *MLCommandPublisher) PublishMLInferenceDeployRequest(ctx context.Context, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if !hasAnyMLField(cmd.Content, "endpoint", "endpoint_id") {
		return nil, fmt.Errorf("endpoint or endpoint_id is required")
	}
	if !hasAnyMLField(cmd.Content, "model_version", "model_version_id") {
		return nil, fmt.Errorf("model_version or model_version_id is required")
	}
	return p.publish(ctx, KindMLInferenceDeployRequest, KindMLInferenceDeployResult, mlEndpointReadModels(), "ml-inference-deploy", cmd)
}

func (p *MLCommandPublisher) PublishMLInferenceApprovalRequest(ctx context.Context, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if !hasAnyMLField(cmd.Content, "intent_id") {
		return nil, fmt.Errorf("intent_id is required")
	}
	return p.publish(ctx, KindMLInferenceDeploymentApproval, KindMLInferenceApprovalResult, mlEndpointReadModels(), "ml-inference-approval", cmd)
}

func (p *MLCommandPublisher) PublishMLInferenceRollbackRequest(ctx context.Context, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if !hasAnyMLField(cmd.Content, "endpoint", "endpoint_id") {
		return nil, fmt.Errorf("endpoint or endpoint_id is required")
	}
	return p.publish(ctx, KindMLInferenceRollbackRequest, KindMLInferenceRollbackResult, mlRollbackReadModels(), "ml-inference-rollback", cmd)
}

func (p *MLCommandPublisher) publish(ctx context.Context, kind, resultKind int, readModels map[string]int, defaultPrefix string, cmd MLCommandPayload) (*MLCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("ML command publisher is not configured")
	}
	content := map[string]any{}
	for k, v := range cmd.Content {
		if strings.TrimSpace(k) == "" || k == "tags" || k == "idempotency_key" || k == "request_id" || k == "d" {
			continue
		}
		content[k] = v
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal ML command content: %w", err)
	}
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = defaultPrefix + ":" + uuid.NewString()
	}
	tags := nostr.Tags{{"d", dTag}}
	tags = append(tags, mlScopedTags(content, cmd.Tags)...)
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign ML command event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		if published > 0 {
			receipt := &MLCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: kind, ResultKind: resultKind, ReadModelKinds: readModels, DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}
			return receipt, nil
		}
		return nil, fmt.Errorf("publish ML command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish ML command event: no relay accepted the request; retry after relay reconnect")
	}
	receipt := &MLCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: kind, ResultKind: resultKind, ReadModelKinds: readModels, DTag: dTag, IdempotencyKey: dTag, Status: "submitted", PublishedRelays: published}
	receipt.Endpoint = tagValueNostr(ev.Tags, "endpoint")
	receipt.EndpointID = tagValueNostr(ev.Tags, "endpoint_id")
	receipt.Environment = tagValueNostr(ev.Tags, "environment")
	receipt.EnvironmentID = tagValueNostr(ev.Tags, "environment_id")
	receipt.ModelVersion = tagValueNostr(ev.Tags, "model_version")
	receipt.ModelVersionID = tagValueNostr(ev.Tags, "model_version_id")
	receipt.Model = tagValueNostr(ev.Tags, "model")
	receipt.Recipe = tagValueNostr(ev.Tags, "recipe")
	receipt.Run = tagValueNostr(ev.Tags, "run")
	receipt.Artifact = tagValueNostr(ev.Tags, "artifact")
	receipt.Runtime = tagValueNostr(ev.Tags, "runtime")
	return receipt, nil
}

func hasAnyMLField(content map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := content[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func mlScopedTags(content map[string]any, explicit map[string]string) nostr.Tags {
	allowed := []string{"model", "model_version", "model_version_id", "recipe", "run", "endpoint", "endpoint_id", "environment", "environment_id", "deployment", "artifact", "worker", "runtime", "task", "accelerator", "source"}
	seen := map[string]bool{}
	out := nostr.Tags{}
	for _, key := range allowed {
		value := strings.TrimSpace(explicit[key])
		if value == "" {
			value, _ = content[key].(string)
		}
		if value == "" && key == "runtime" {
			value, _ = content["runtime_preference"].(string)
		}
		if value == "" && key == "accelerator" {
			if placement, ok := content["placement"].(map[string]any); ok {
				value, _ = placement["accelerator"].(string)
			}
		}
		if value == "" && key == "environment" {
			if endpoint, _ := content["endpoint"].(string); endpoint != "" {
				value = mlEnvironmentFromEndpointCoord(endpoint)
			}
		}
		if value == "" {
			continue
		}
		dedupe := key + "\x00" + value
		if seen[dedupe] {
			continue
		}
		seen[dedupe] = true
		out = append(out, nostr.Tag{key, value})
	}
	return out
}

func mlImportReadModels() map[string]int {
	return map[string]int{"model_registry": KindMLModelRegistry, "model_version_registry": KindMLModelVersionRegistry, "artifact_provenance_graph": KindMLArtifactProvenanceGraph}
}

func mlRecipeReadModels() map[string]int {
	return map[string]int{"recipe_registry": KindMLRecipeRegistry, "recipe_run_state": KindMLRecipeRunState}
}

func mlEndpointReadModels() map[string]int {
	return map[string]int{"endpoint_registry": KindMLInferenceEndpointRegistry, "endpoint_state": KindMLInferenceEndpointState}
}

func mlRollbackReadModels() map[string]int {
	return map[string]int{"endpoint_state": KindMLInferenceEndpointState}
}
