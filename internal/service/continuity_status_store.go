package service

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	ContinuityOperationSteady             = "steady"
	ContinuityOperationFailoverInProgress = "failover_in_progress"
	ContinuityOperationRecoveryInProgress = "recovery_in_progress"
	ContinuityOperationFailed             = "failed"
)

// ContinuityStatus is the latest queryable continuity state for one service.
type ContinuityStatus struct {
	ServiceKey          string                `json:"service_key"`
	ActiveProfile       domain.ContinuityMode `json:"active_profile"`
	OperationState      string                `json:"operation_state"`
	PrimaryWorkerPubKey string                `json:"primary_worker_pubkey,omitempty"`
	ActiveWorkerPubKey  string                `json:"active_worker_pubkey,omitempty"`
	StandbyWorkerPubKey string                `json:"standby_worker_pubkey,omitempty"`
	Reason              string                `json:"reason,omitempty"`
	ChangedAt           time.Time             `json:"changed_at"`
	CurrentRunID        string                `json:"current_run_id,omitempty"`
	CurrentStepIndex    int                   `json:"current_step_index,omitempty"`
	CurrentStepCount    int                   `json:"current_step_count,omitempty"`
	CurrentStepAction   string                `json:"current_step_action,omitempty"`
}

// ContinuityStatusReader is consumed by read-model users such as DNS projection.
type ContinuityStatusReader interface {
	GetServiceStatus(serviceKey string) (ContinuityStatus, bool)
	GetServiceContinuityStatus(serviceKey string) (*ContinuityStatus, bool)
	ListAllStatuses() []ContinuityStatus
}

var _ ContinuityStatusReader = (*InMemoryContinuityStatusStore)(nil)

// InMemoryContinuityStatusStore keeps the latest continuity status per service.
type InMemoryContinuityStatusStore struct {
	mu       sync.RWMutex
	statuses map[string]ContinuityStatus
}

func NewInMemoryContinuityStatusStore() *InMemoryContinuityStatusStore {
	return &InMemoryContinuityStatusStore{statuses: make(map[string]ContinuityStatus)}
}

func (s *InMemoryContinuityStatusStore) Update(status ContinuityStatus) {
	if s == nil {
		return
	}
	status = normalizeContinuityStatus(status)
	if status.ServiceKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statuses == nil {
		s.statuses = make(map[string]ContinuityStatus)
	}
	s.statuses[status.ServiceKey] = status
}

func (s *InMemoryContinuityStatusStore) GetServiceStatus(serviceKey string) (ContinuityStatus, bool) {
	if s == nil {
		return ContinuityStatus{}, false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.statuses[serviceKey]
	return status, ok
}

func (s *InMemoryContinuityStatusStore) GetServiceContinuityStatus(serviceKey string) (*ContinuityStatus, bool) {
	status, ok := s.GetServiceStatus(serviceKey)
	if !ok {
		return nil, false
	}
	return &status, true
}

func (s *InMemoryContinuityStatusStore) ListAllStatuses() []ContinuityStatus {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ContinuityStatus, 0, len(s.statuses))
	for _, status := range s.statuses {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ServiceKey < out[j].ServiceKey
	})
	return out
}

func normalizeContinuityStatus(status ContinuityStatus) ContinuityStatus {
	status.ServiceKey = strings.TrimSpace(status.ServiceKey)
	status.PrimaryWorkerPubKey = strings.TrimSpace(status.PrimaryWorkerPubKey)
	status.ActiveWorkerPubKey = strings.TrimSpace(status.ActiveWorkerPubKey)
	status.StandbyWorkerPubKey = strings.TrimSpace(status.StandbyWorkerPubKey)
	status.Reason = strings.TrimSpace(status.Reason)
	status.CurrentRunID = strings.TrimSpace(status.CurrentRunID)
	status.CurrentStepAction = strings.TrimSpace(status.CurrentStepAction)
	return status
}

func validContinuityOperationState(state string) bool {
	switch state {
	case ContinuityOperationSteady, ContinuityOperationFailoverInProgress, ContinuityOperationRecoveryInProgress, ContinuityOperationFailed:
		return true
	default:
		return false
	}
}
