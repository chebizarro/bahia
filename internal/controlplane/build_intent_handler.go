package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BuildIntentHandler requests a daemon-authored queued HiveCI build through the
// same validation and registration path as the encrypted operator method.
type BuildIntentHandler struct{ builds *EncryptedBuildHandlers }

func NewBuildIntentHandler(builds *EncryptedBuildHandlers) *BuildIntentHandler {
	return &BuildIntentHandler{builds: builds}
}

func (*BuildIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }

func (h *BuildIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent.Op != "request" {
		return fmt.Errorf("unsupported build operation %q", intent.Op)
	}
	if h.builds == nil {
		return fmt.Errorf("build request handler is not configured")
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("build request does not support expected_updated_at")
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	var payload ArcanaBuildRequest
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if payload.ServiceID == uuid.Nil || intent.Coordinate != "build-request:"+payload.ServiceID.String() {
		return fmt.Errorf("build request coordinate does not match service_id")
	}
	if intent.Event == nil {
		return fmt.Errorf("build request requires a signed source event")
	}
	result, err := h.builds.requestBuild(ctx, intent.Event, payload)
	if err != nil {
		return err
	}
	intent.Result = result
	intent.StatusData = result
	return nil
}
