package dto

import (
	"time"

	"github.com/openagentsinc/bahia/internal/service"
)

// ContinuityServiceStatusDTO is the HTTP response shape for one service's
// current continuity status read model.
type ContinuityServiceStatusDTO struct {
	ServiceKey          string            `json:"service_key"`
	ActiveProfile       string            `json:"active_profile"`
	OperationState      string            `json:"operation_state"`
	PrimaryWorkerPubKey string            `json:"primary_worker_pubkey"`
	ActiveWorkerPubKey  string            `json:"active_worker_pubkey"`
	StandbyWorkerPubKey string            `json:"standby_worker_pubkey,omitempty"`
	Reason              string            `json:"reason,omitempty"`
	ChangedAt           string            `json:"changed_at"`
	CurrentRun          *ContinuityRunDTO `json:"current_run,omitempty"`
}

// ContinuityRunDTO describes the currently executing failover or recovery step.
type ContinuityRunDTO struct {
	ID         string `json:"id"`
	StepIndex  int    `json:"step_index"`
	StepCount  int    `json:"step_count"`
	StepAction string `json:"step_action"`
}

func ContinuityServiceStatusDTOFromService(status service.ContinuityStatus) ContinuityServiceStatusDTO {
	out := ContinuityServiceStatusDTO{
		ServiceKey:          status.ServiceKey,
		ActiveProfile:       string(status.ActiveProfile),
		OperationState:      status.OperationState,
		PrimaryWorkerPubKey: status.PrimaryWorkerPubKey,
		ActiveWorkerPubKey:  status.ActiveWorkerPubKey,
		StandbyWorkerPubKey: status.StandbyWorkerPubKey,
		Reason:              status.Reason,
		ChangedAt:           formatContinuityTime(status.ChangedAt),
	}
	if status.CurrentRunID != "" {
		out.CurrentRun = &ContinuityRunDTO{
			ID:         status.CurrentRunID,
			StepIndex:  status.CurrentStepIndex,
			StepCount:  status.CurrentStepCount,
			StepAction: status.CurrentStepAction,
		}
	}
	return out
}

func ContinuityServiceStatusDTOsFromService(statuses []service.ContinuityStatus) []ContinuityServiceStatusDTO {
	out := make([]ContinuityServiceStatusDTO, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, ContinuityServiceStatusDTOFromService(status))
	}
	return out
}

// ContinuityAssessmentDTO is the HTTP response shape for continuity graph survivability.
type ContinuityAssessmentDTO struct {
	ServiceKey            string `json:"service_key"`
	Survivability         string `json:"survivability"`
	HasFailoverRecipe     bool   `json:"has_failover_recipe"`
	HasRecoveryRecipe     bool   `json:"has_recovery_recipe"`
	StandbyCount          int    `json:"standby_count"`
	ReplicationConfigured bool   `json:"replication_configured"`
	HeartbeatActive       bool   `json:"heartbeat_active"`
}

// SimulateFailureRequest is the POST body for continuity failure simulation.
type SimulateFailureRequest struct {
	WorkerPubKey string `json:"worker_pubkey"`
}

func ContinuityAssessmentDTOFromService(assessment service.ContinuityAssessment) ContinuityAssessmentDTO {
	standbyCount := 0
	if assessment.SelectedStandby != "" {
		standbyCount = 1
	}
	return ContinuityAssessmentDTO{
		ServiceKey:            assessment.ServiceKey,
		Survivability:         assessment.Survivability,
		HasFailoverRecipe:     assessment.SelectedStandby != "",
		HasRecoveryRecipe:     len(assessment.AvailableProfiles) > 0,
		StandbyCount:          standbyCount,
		ReplicationConfigured: assessment.ReplicationFreshness != "" && assessment.ReplicationFreshness != service.ReplicationFreshnessUnknown,
		HeartbeatActive:       assessment.StandbyHealth == service.StandbyHealthHealthy,
	}
}

func ContinuityAssessmentDTOsFromService(assessments []service.ContinuityAssessment) []ContinuityAssessmentDTO {
	out := make([]ContinuityAssessmentDTO, 0, len(assessments))
	for _, assessment := range assessments {
		out = append(out, ContinuityAssessmentDTOFromService(assessment))
	}
	return out
}

func formatContinuityTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
