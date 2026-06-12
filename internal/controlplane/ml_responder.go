package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// MLResponder publishes terminal Nostr replies for ML inference provisioning.
// ML progress is represented by 3198x read models, so PublishStatus is a no-op.
type MLResponder struct {
	pool      *nostrpool.RelayPool
	signer    canonicalnostr.Signer
	logger    *zap.Logger
	eventRepo repository.NostrEventRepository
}

func NewMLResponder(pool *nostrpool.RelayPool, signer canonicalnostr.Signer, logger *zap.Logger, eventRepos ...repository.NostrEventRepository) *MLResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	var eventRepo repository.NostrEventRepository
	if len(eventRepos) > 0 {
		eventRepo = eventRepos[0]
	}
	return &MLResponder{pool: pool, signer: signer, logger: logger.Named("ml-responder"), eventRepo: eventRepo}
}

func (r *MLResponder) PublishStatus(context.Context, *domain.MLDeploymentIntent, *domain.MLDeploymentRun, string, string) error {
	return nil
}

func (r *MLResponder) PublishRecipeRunStatus(context.Context, *domain.MLRecipe, *domain.MLRecipeRun, string, string) error {
	return nil
}

func (r *MLResponder) PublishRecipeRunResult(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, status, message string) error {
	return r.publishRecipe(ctx, recipe, run, normalizeMLTerminalStatus(status), message, nil)
}

func (r *MLResponder) PublishRecipeRunError(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return r.publishRecipe(ctx, recipe, run, "failed", firstNonEmpty(msg, step), cause)
}

func (r *MLResponder) PublishResult(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, status, message string) error {
	return r.publish(ctx, intent, run, normalizeMLTerminalStatus(status), message, nil)
}

func (r *MLResponder) PublishError(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, step string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return r.publish(ctx, intent, run, "failed", firstNonEmpty(msg, step), cause)
}

func (r *MLResponder) publishRecipe(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, status, message string, cause error) error {
	if r == nil || r.pool == nil || run == nil {
		return nil
	}
	requestEventID, requestPubkey, _ := mlRecipeNostrCorrelation(run)
	if requestEventID == "" || requestPubkey == "" {
		return nil
	}
	recipeCoord := metadataString(run.Metadata, "nostr_recipe_coord")
	if recipeCoord == "" && recipe != nil {
		recipeCoord = fmt.Sprintf("recipe:%s:%s", recipe.Name, recipe.Version)
	}
	content := map[string]any{
		"request_event_id": requestEventID,
		"status":           status,
		"message":          message,
		"run":              run.ID.String(),
		"recipe_id":        run.RecipeID.String(),
		"created_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if recipeCoord != "" {
		content["recipe"] = recipeCoord
	}
	if run.Result != nil {
		content["result"] = run.Result
	}
	if cause != nil {
		content["error"] = map[string]any{"code": "recipe_error", "message": cause.Error()}
	}
	tags := nostr.Tags{{"e", requestEventID, "", "reply"}, {"p", requestPubkey}, {"status", status}, {"run", run.ID.String()}, {"recipe_id", run.RecipeID.String()}, {ContextVMRoutingTag, ContextVMWireVersion}}
	if recipeCoord != "" {
		tags = append(tags, nostr.Tag{"recipe", recipeCoord})
	}
	response := ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: mlContextVMReplyID(run.Metadata, requestEventID), Result: content}
	if cause != nil {
		response.Result = nil
		response.Error = &JSONRPCError{Code: -32000, Message: cause.Error()}
	}
	body, _ := json.Marshal(response)
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := SignGoNostrEvent(ctx, r.signer, event); err != nil {
		return err
	}
	if _, err := r.pool.Publish(ctx, *event); err != nil {
		return err
	}
	r.recordRecipe(ctx, event, run)
	return nil
}

func (r *MLResponder) publish(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, status, message string, cause error) error {
	if r == nil || r.pool == nil || intent == nil {
		return nil
	}
	requestEventID, requestPubkey, _ := mlNostrCorrelation(intent)
	if requestEventID == "" || requestPubkey == "" {
		return nil
	}
	endpointCoord := metadataString(intent.Metadata, "nostr_endpoint_coord")
	modelVersionCoord := metadataString(intent.Metadata, "nostr_model_version_coord")
	environmentCoord := firstNonEmpty(metadataString(intent.Metadata, "nostr_environment_coord"), mlEnvironmentFromEndpointCoord(endpointCoord))
	content := map[string]any{
		"request_event_id": requestEventID,
		"status":           status,
		"message":          message,
		"intent_id":        intent.ID.String(),
		"endpoint_id":      intent.EndpointID.String(),
		"environment_id":   intent.EnvironmentID.String(),
		"model_version_id": intent.ModelVersionID.String(),
		"created_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if endpointCoord != "" {
		content["endpoint"] = endpointCoord
	}
	if environmentCoord != "" {
		content["environment"] = environmentCoord
	}
	if modelVersionCoord != "" {
		content["model_version"] = modelVersionCoord
	}
	if run != nil {
		content["run"] = run.ID.String()
		content["runtime"] = string(run.RuntimeKind)
		content["backend_endpoint"] = run.BackendEndpoint
	}
	if cause != nil {
		content["error"] = map[string]any{"code": "provisioning_error", "message": cause.Error()}
	}
	tags := nostr.Tags{
		{"e", requestEventID, "", "reply"},
		{"p", requestPubkey},
		{"status", status},
		{"endpoint_id", intent.EndpointID.String()},
		{"environment_id", intent.EnvironmentID.String()},
		{"model_version_id", intent.ModelVersionID.String()},
		{"deployment", intent.ID.String()},
		{"intent", intent.ID.String()},
		{ContextVMRoutingTag, ContextVMWireVersion},
	}
	if endpointCoord != "" {
		tags = append(tags, nostr.Tag{"endpoint", endpointCoord})
	}
	if environmentCoord != "" {
		tags = append(tags, nostr.Tag{"environment", environmentCoord})
	}
	if modelVersionCoord != "" {
		tags = append(tags, nostr.Tag{"model_version", modelVersionCoord})
	}
	if run != nil {
		tags = append(tags, nostr.Tag{"run", run.ID.String()})
		if run.RuntimeKind != "" {
			tags = append(tags, nostr.Tag{"runtime", string(run.RuntimeKind)})
		}
		if run.WorkerPubkey != "" {
			tags = append(tags, nostr.Tag{"worker", run.WorkerPubkey})
		}
	}
	response := ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: mlContextVMReplyID(intent.Metadata, requestEventID), Result: content}
	if cause != nil {
		response.Result = nil
		response.Error = &JSONRPCError{Code: -32000, Message: cause.Error()}
	}
	body, _ := json.Marshal(response)
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := SignGoNostrEvent(ctx, r.signer, event); err != nil {
		return err
	}
	if _, err := r.pool.Publish(ctx, *event); err != nil {
		return err
	}
	r.record(ctx, event, intent)
	return nil
}

func mlContextVMReplyID(metadata map[string]any, fallback string) json.RawMessage {
	if dTag := metadataString(metadata, "nostr_d_tag"); dTag != "" {
		body, _ := json.Marshal(dTag)
		return body
	}
	body, _ := json.Marshal(fallback)
	return body
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return value
	}
	if value, ok := metadata[key]; ok {
		return fmt.Sprint(value)
	}
	return ""
}

func mlResultKindForRequest(requestKind int) (int, error) {
	switch requestKind {
	case KindMLRecipeRunRequest:
		return KindMLRecipeRunResult, nil
	case KindMLInferenceDeployRequest:
		return KindMLInferenceDeployResult, nil
	case KindMLInferenceRollbackRequest:
		return KindMLInferenceRollbackResult, nil
	default:
		return 0, fmt.Errorf("unsupported ML result request kind %d", requestKind)
	}
}

func normalizeMLTerminalStatus(status string) string {
	switch status {
	case "completed", "success", "succeeded":
		return "succeeded"
	case "reject", "rejected":
		return "rejected"
	case "failed", "error":
		return "failed"
	default:
		if status == "" {
			return "succeeded"
		}
		return status
	}
}

func mlRecipeNostrCorrelation(run *domain.MLRecipeRun) (eventID, pubkey string, kind int) {
	if run == nil || run.Metadata == nil {
		return "", "", 0
	}
	if v, ok := run.Metadata["nostr_event_id"].(string); ok {
		eventID = v
	}
	if v, ok := run.Metadata["nostr_request_pubkey"].(string); ok {
		pubkey = v
	}
	switch v := run.Metadata["nostr_request_kind"].(type) {
	case int:
		kind = v
	case int64:
		kind = int(v)
	case float64:
		kind = int(v)
	case json.Number:
		n, _ := v.Int64()
		kind = int(n)
	}
	return eventID, pubkey, kind
}

func mlNostrCorrelation(intent *domain.MLDeploymentIntent) (eventID, pubkey string, kind int) {
	if intent == nil || intent.Metadata == nil {
		return "", "", 0
	}
	if v, ok := intent.Metadata["nostr_event_id"].(string); ok {
		eventID = v
	}
	if v, ok := intent.Metadata["nostr_request_pubkey"].(string); ok {
		pubkey = v
	}
	switch v := intent.Metadata["nostr_request_kind"].(type) {
	case int:
		kind = v
	case int64:
		kind = int(v)
	case float64:
		kind = int(v)
	case json.Number:
		n, _ := v.Int64()
		kind = int(n)
	}
	return eventID, pubkey, kind
}

func (r *MLResponder) recordRecipe(ctx context.Context, ev *nostr.Event, run *domain.MLRecipeRun) {
	if r.eventRepo == nil || ev == nil || run == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	entityID := run.ID
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: "ml.recipe.reply", EntityID: &entityID}); err != nil {
		r.logger.Warn("failed to record ML recipe reply", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}

func (r *MLResponder) record(ctx context.Context, ev *nostr.Event, intent *domain.MLDeploymentIntent) {
	if r.eventRepo == nil || ev == nil || intent == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	entityID := intent.EndpointID
	if entityID == uuid.Nil {
		entityID = intent.ID
	}
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: "ml.inference.reply", EntityID: &entityID}); err != nil {
		r.logger.Warn("failed to record ML provisioning reply", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}
