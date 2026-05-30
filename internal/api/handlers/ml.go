package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// MLCommandPublisher publishes signer-first ML command events and returns Nostr correlation metadata.
type MLCommandPublisher interface {
	PublishMLModelImportRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLRecipeRunRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceDeployRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceRollbackRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
}

// MLHandler exposes the generic AI/ML REST compatibility surface.
type MLHandler struct {
	registry *service.MLRegistryService
	commands MLCommandPublisher
}

func NewMLHandler(registry *service.MLRegistryService, commands MLCommandPublisher) *MLHandler {
	return &MLHandler{registry: registry, commands: commands}
}

func (h *MLHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	limit, offset := queryInt(r, "limit", 100), queryInt(r, "offset", 0)
	models, err := h.registry.ListModels(r.Context(), domain.MLTaskKind(r.URL.Query().Get("task")), limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: models, Limit: limit, Offset: offset})
}

func (h *MLHandler) GetModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid model id")
		return
	}
	model, err := h.registry.GetModel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if model == nil {
		writeError(w, http.StatusNotFound, "ML model not found")
		return
	}
	writeData(w, http.StatusOK, model)
}

func (h *MLHandler) ListModelVersions(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	modelID, err := uuidParam(r, "modelId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid model id")
		return
	}
	limit, offset := queryInt(r, "limit", 100), queryInt(r, "offset", 0)
	versions, err := h.registry.ListModelVersions(r.Context(), modelID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: versions, Limit: limit, Offset: offset})
}

func (h *MLHandler) GetModelVersion(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid model version id")
		return
	}
	version, err := h.registry.GetModelVersion(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if version == nil {
		writeError(w, http.StatusNotFound, "ML model version not found")
		return
	}
	writeData(w, http.StatusOK, version)
}

func (h *MLHandler) ListEndpoints(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	var envID uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("environment_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid environment_id")
			return
		}
		envID = parsed
	}
	limit, offset := queryInt(r, "limit", 100), queryInt(r, "offset", 0)
	endpoints, err := h.registry.ListInferenceEndpoints(r.Context(), envID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: endpoints, Limit: limit, Offset: offset})
}

func (h *MLHandler) GetEndpoint(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	endpoint, err := h.registry.GetInferenceEndpoint(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if endpoint == nil {
		writeError(w, http.StatusNotFound, "ML inference endpoint not found")
		return
	}
	writeData(w, http.StatusOK, endpoint)
}

func (h *MLHandler) ListState(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	states, err := h.registry.ListInferenceStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *MLHandler) GetState(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	endpointID, err := uuidParam(r, "endpointId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	state, err := h.registry.GetInferenceState(r.Context(), endpointID, envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if state == nil {
		writeError(w, http.StatusNotFound, "ML inference state not found")
		return
	}
	writeData(w, http.StatusOK, state)
}

func (h *MLHandler) GetArtifactProvenance(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistry(w) {
		return
	}
	artifactID, err := uuidParam(r, "artifactId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}
	artifact, err := h.registry.GetArtifactRef(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if artifact == nil {
		writeError(w, http.StatusNotFound, "ML artifact ref not found")
		return
	}
	edges, err := h.registry.ListProvenanceEdgesByArtifact(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"artifact": artifact, "edges": edges, "read_model_kind": controlplane.KindMLArtifactProvenanceGraph})
}

func (h *MLHandler) ImportModel(w http.ResponseWriter, r *http.Request) {
	h.publishAsync(w, r, func(ctx context.Context, payload controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
		return h.commands.PublishMLModelImportRequest(ctx, payload)
	})
}

func (h *MLHandler) RunRecipe(w http.ResponseWriter, r *http.Request) {
	h.publishAsync(w, r, func(ctx context.Context, payload controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
		return h.commands.PublishMLRecipeRunRequest(ctx, payload)
	})
}

func (h *MLHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	h.publishAsync(w, r, func(ctx context.Context, payload controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
		return h.commands.PublishMLInferenceDeployRequest(ctx, payload)
	})
}

func (h *MLHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	h.publishAsync(w, r, func(ctx context.Context, payload controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
		return h.commands.PublishMLInferenceRollbackRequest(ctx, payload)
	})
}

func (h *MLHandler) publishAsync(w http.ResponseWriter, r *http.Request, publish func(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)) {
	if h.commands == nil {
		writeError(w, http.StatusServiceUnavailable, "ML command publisher is not configured")
		return
	}
	body := map[string]any{}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := publish(r.Context(), mlPayloadFromBody(body))
	if err != nil {
		if strings.Contains(err.Error(), "required") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeData(w, http.StatusAccepted, dto.CommandReceipt{
		RequestEventID:  receipt.RequestEventID,
		RequestPubkey:   receipt.RequestPubkey,
		RequestKind:     receipt.RequestKind,
		ResultKind:      receipt.ResultKind,
		ReadModelKinds:  receipt.ReadModelKinds,
		IdempotencyKey:  receipt.IdempotencyKey,
		Status:          receipt.Status,
		Error:           receipt.Error,
		RetryHint:       receipt.RetryHint,
		PublishedRelays: receipt.PublishedRelays,
		TimeoutSeconds:  30,
		Message:         "request published; subscribe to Nostr result/read-model events for completion",
	})
}

func (h *MLHandler) requireRegistry(w http.ResponseWriter) bool {
	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "ML registry is not configured")
		return false
	}
	return true
}

func mlPayloadFromBody(body map[string]any) controlplane.MLCommandPayload {
	payload := controlplane.MLCommandPayload{Content: body, Tags: map[string]string{}}
	for _, key := range []string{"idempotency_key", "request_id", "d"} {
		if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
			payload.IdempotencyKey = strings.TrimSpace(value)
			break
		}
	}
	if raw, ok := body["tags"].(map[string]any); ok {
		for k, v := range raw {
			payload.Tags[k] = fmt.Sprint(v)
		}
	}
	return payload
}
