package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestContinuityHandlerStatusReturnsMappedStatuses(t *testing.T) {
	changedAt := time.Date(2026, 5, 23, 15, 4, 5, 123456789, time.UTC)
	store := service.NewInMemoryContinuityStatusStore()
	store.Update(service.ContinuityStatus{
		ServiceKey:          "svc-api",
		ActiveProfile:       domain.ContinuityModeDegraded,
		OperationState:      service.ContinuityOperationFailoverInProgress,
		PrimaryWorkerPubKey: "primary-worker",
		ActiveWorkerPubKey:  "active-worker",
		StandbyWorkerPubKey: "standby-worker",
		Reason:              "heartbeat expired",
		ChangedAt:           changedAt,
		CurrentRunID:        "run-123",
		CurrentStepIndex:    2,
		CurrentStepCount:    5,
		CurrentStepAction:   "restore_backup",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/continuity/status", nil)
	w := httptest.NewRecorder()

	NewContinuityHandler(store, nil).Status(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	payload, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, payload, 1)
	status, ok := payload[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "svc-api", status["service_key"])
	require.Equal(t, "degraded", status["active_profile"])
	require.Equal(t, "failover_in_progress", status["operation_state"])
	require.Equal(t, "primary-worker", status["primary_worker_pubkey"])
	require.Equal(t, "active-worker", status["active_worker_pubkey"])
	require.Equal(t, "standby-worker", status["standby_worker_pubkey"])
	require.Equal(t, "heartbeat expired", status["reason"])
	require.Equal(t, changedAt.Format(time.RFC3339Nano), status["changed_at"])
	run, ok := status["current_run"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "run-123", run["id"])
	require.Equal(t, float64(2), run["step_index"])
	require.Equal(t, float64(5), run["step_count"])
	require.Equal(t, "restore_backup", run["step_action"])
}

func TestContinuityHandlerStatusReturnsEmptyList(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/continuity/status", nil)
	w := httptest.NewRecorder()

	NewContinuityHandler(service.NewInMemoryContinuityStatusStore(), nil).Status(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	payload, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Empty(t, payload)
}

func TestContinuityHandlerStatusFailsClosedWithoutStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/continuity/status", nil)
	w := httptest.NewRecorder()

	NewContinuityHandler(nil, nil).Status(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "continuity status store unavailable", resp.Error)
}
