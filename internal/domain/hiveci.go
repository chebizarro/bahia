package domain

import (
	"time"

	"github.com/google/uuid"
)

// HiveCIProcessingState tracks where a Hive-CI run/result pair is in the bridge pipeline.
type HiveCIProcessingState string

const (
	HiveCIProcessingStatePendingRun      HiveCIProcessingState = "pending_run"
	HiveCIProcessingStatePendingResult   HiveCIProcessingState = "pending_result"
	HiveCIProcessingStateVerified        HiveCIProcessingState = "verified"
	HiveCIProcessingStateArtifactPending HiveCIProcessingState = "artifact_pending"
	HiveCIProcessingStateRejected        HiveCIProcessingState = "rejected"
	HiveCIProcessingStateProcessed       HiveCIProcessingState = "processed"
	HiveCIProcessingStateFailed          HiveCIProcessingState = "failed"
)

// HiveCIWorkflowRun represents parsed Hive-CI kind retired-kind event data.
type HiveCIWorkflowRun struct {
	RunEventID      string                `json:"run_event_id"`
	RepoCoordinate  string                `json:"repo_coordinate"`
	CommitSHA       string                `json:"commit_sha"`
	Branch          string                `json:"branch"`
	WorkflowPath    string                `json:"workflow_path"`
	TriggerType     string                `json:"trigger_type,omitempty"`
	TriggeredBy     string                `json:"triggered_by,omitempty"`
	PublisherPubkey string                `json:"publisher_pubkey"`
	EventCreatedAt  time.Time             `json:"event_created_at"`
	ProcessingState HiveCIProcessingState `json:"processing_state"`
	ProcessingError string                `json:"processing_error,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

// HiveCIWorkflowResult represents parsed Hive-CI kind retired-kind event data.
type HiveCIWorkflowResult struct {
	ResultEventID   string                `json:"result_event_id"`
	RunEventID      string                `json:"run_event_id"`
	Status          string                `json:"status"`
	ExitCode        int                   `json:"exit_code"`
	DurationSeconds int                   `json:"duration_seconds"`
	LogURL          string                `json:"log_url,omitempty"`
	Error           string                `json:"error,omitempty"`
	ImageRepo       string                `json:"image_repo,omitempty"`
	ImageTag        string                `json:"image_tag,omitempty"`
	ImageDigest     string                `json:"image_digest,omitempty"`
	PSTFGateName    string                `json:"pstf_gate_name,omitempty"`
	PSTFGateStatus  string                `json:"pstf_gate_status,omitempty"`
	PublisherPubkey string                `json:"publisher_pubkey"`
	EventCreatedAt  time.Time             `json:"event_created_at"`
	ProcessingState HiveCIProcessingState `json:"processing_state"`
	ProcessingError string                `json:"processing_error,omitempty"`
	RetryCount      int                   `json:"retry_count"`
	LastRetryAt     *time.Time            `json:"last_retry_at,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

// HiveCIPipelinePolicy maps Hive-CI repo/workflow selectors to Bahia service/environment targets.
type HiveCIPipelinePolicy struct {
	ID             uuid.UUID      `json:"id"`
	RepoCoordinate string         `json:"repo_coordinate"`
	WorkflowPath   string         `json:"workflow_path"`
	BranchPattern  string         `json:"branch_pattern,omitempty"`
	ServiceID      uuid.UUID      `json:"service_id"`
	EnvironmentID  uuid.UUID      `json:"environment_id"`
	Enabled        bool           `json:"enabled"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// HiveCIResultState is the daemon's processing state of one signed kind-5402
// workflow result. The signed result itself is the evidence; this record is
// what the daemon decided about it and how often it tried, so pending results
// are found and retried from canonical state instead of a SQL scan.
type HiveCIResultState struct {
	ResultEventID   string                `json:"result_event_id"`
	RunEventID      string                `json:"run_event_id"`
	ProcessingState HiveCIProcessingState `json:"processing_state"`
	ProcessingError string                `json:"processing_error,omitempty"`
	RetryCount      int                   `json:"retry_count"`
	LastRetryAt     *time.Time            `json:"last_retry_at,omitempty"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

// Terminal reports whether no further automatic processing of the result is
// expected: arrival, the arrival of its run and the per-start resume leave it
// alone. A failed result is terminal for the daemon but not for the operator:
// the signer-first build-result action (pipeline.Bridge.RegisterBuildResult)
// may still finish it, which is the recovery contract once automatic attempts
// are exhausted (bahia-xjdo9).
func (s HiveCIProcessingState) Terminal() bool {
	switch s {
	case HiveCIProcessingStateProcessed, HiveCIProcessingStateRejected, HiveCIProcessingStateFailed:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether the bridge may move a result from s to
// next. It is the transition table the SQL index enforces.
func (s HiveCIProcessingState) CanTransitionTo(next HiveCIProcessingState) bool {
	if next == "" {
		return false
	}
	if s == next {
		return true
	}
	allowed := map[HiveCIProcessingState][]HiveCIProcessingState{
		HiveCIProcessingStatePendingRun:      {HiveCIProcessingStatePendingResult, HiveCIProcessingStateRejected, HiveCIProcessingStateFailed},
		HiveCIProcessingStatePendingResult:   {HiveCIProcessingStateProcessed, HiveCIProcessingStateVerified, HiveCIProcessingStateArtifactPending, HiveCIProcessingStateRejected, HiveCIProcessingStateFailed},
		HiveCIProcessingStateVerified:        {HiveCIProcessingStateArtifactPending, HiveCIProcessingStateProcessed, HiveCIProcessingStateRejected, HiveCIProcessingStateFailed},
		HiveCIProcessingStateArtifactPending: {HiveCIProcessingStateProcessed, HiveCIProcessingStateFailed},
		// Operator recovery of a result whose automatic attempts were exhausted.
		HiveCIProcessingStateFailed: {HiveCIProcessingStateArtifactPending, HiveCIProcessingStateProcessed, HiveCIProcessingStateRejected},
	}
	for _, candidate := range allowed[s] {
		if candidate == next {
			return true
		}
	}
	return false
}
