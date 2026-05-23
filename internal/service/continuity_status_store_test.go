package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestInMemoryContinuityStatusStoreUpdatesAndQueriesByService(t *testing.T) {
	changedAt := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	store := NewInMemoryContinuityStatusStore()

	store.Update(ContinuityStatus{
		ServiceKey:          " svc-api ",
		ActiveProfile:       domain.ContinuityModeDegraded,
		OperationState:      ContinuityOperationFailoverInProgress,
		PrimaryWorkerPubKey: " primary ",
		ActiveWorkerPubKey:  " standby ",
		Reason:              " heartbeat expired ",
		ChangedAt:           changedAt,
		CurrentRunID:        " run-1 ",
		CurrentStepIndex:    2,
		CurrentStepCount:    4,
		CurrentStepAction:   " restore_backup ",
	})

	status, ok := store.GetServiceStatus("svc-api")
	require.True(t, ok)
	require.Equal(t, "svc-api", status.ServiceKey)
	require.Equal(t, domain.ContinuityModeDegraded, status.ActiveProfile)
	require.Equal(t, ContinuityOperationFailoverInProgress, status.OperationState)
	require.Equal(t, "primary", status.PrimaryWorkerPubKey)
	require.Equal(t, "standby", status.ActiveWorkerPubKey)
	require.Equal(t, "heartbeat expired", status.Reason)
	require.Equal(t, "run-1", status.CurrentRunID)
	require.Equal(t, 2, status.CurrentStepIndex)
	require.Equal(t, 4, status.CurrentStepCount)
	require.Equal(t, "restore_backup", status.CurrentStepAction)
}

func TestInMemoryContinuityStatusStoreReturnsCopiesAndSortedLists(t *testing.T) {
	store := NewInMemoryContinuityStatusStore()
	store.Update(ContinuityStatus{ServiceKey: "svc-z", ActiveProfile: domain.ContinuityModeFull, OperationState: ContinuityOperationSteady})
	store.Update(ContinuityStatus{ServiceKey: "svc-a", ActiveProfile: domain.ContinuityModeEmergency, OperationState: ContinuityOperationFailed})

	status, ok := store.GetServiceContinuityStatus("svc-a")
	require.True(t, ok)
	status.ActiveWorkerPubKey = "mutated"

	again, ok := store.GetServiceStatus("svc-a")
	require.True(t, ok)
	require.Empty(t, again.ActiveWorkerPubKey)

	all := store.ListAllStatuses()
	require.Len(t, all, 2)
	require.Equal(t, "svc-a", all[0].ServiceKey)
	require.Equal(t, "svc-z", all[1].ServiceKey)
}

func TestInMemoryContinuityStatusStoreMissingService(t *testing.T) {
	store := NewInMemoryContinuityStatusStore()

	_, ok := store.GetServiceStatus("missing")
	require.False(t, ok)
	status, ok := store.GetServiceContinuityStatus("missing")
	require.False(t, ok)
	require.Nil(t, status)
	require.Empty(t, store.ListAllStatuses())
}
