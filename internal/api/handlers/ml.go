package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// MLCommandPublisher publishes signer-first ML command events and returns Nostr correlation metadata.
type MLCommandPublisher interface {
	PublishMLModelImportRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLRecipeRunRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceDeployRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceRollbackRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
}

// MLHandler exposes the generic AI/ML compatibility HTTP surface for external clients.
type MLHandler struct {
	commands MLCommandPublisher
}

func NewMLHandler(commands MLCommandPublisher) *MLHandler {
	return &MLHandler{commands: commands}
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
