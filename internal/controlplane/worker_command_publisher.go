package controlplane

const (
	WorkerCommandCordon           = "worker.cordon.request"
	WorkerCommandUncordon         = "worker.uncordon.request"
	WorkerCommandDrain            = "worker.drain.request"
	WorkerCommandUndrain          = "worker.undrain.request"
	WorkerCommandMaintenanceEnter = "worker.maintenance.enter.request"
	WorkerCommandMaintenanceExit  = "worker.maintenance.exit.request"
	WorkerCommandLabelsUpdate     = "worker.labels.update.request"
	WorkerPolicyApplyRequest      = "worker-policy.apply.request"
	WorkloadPinRequest            = "workload.pin.request"
	WorkerCommandCleanupRequest   = "worker.cleanup.request"
)

type WorkerLifecycleCommand struct {
	WorkerPubKey     string         `json:"worker_pubkey"`
	Reason           string         `json:"reason,omitempty"`
	OperatorMetadata map[string]any `json:"operator_metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
}

type WorkerLabelsUpdateCommand struct {
	WorkerPubKey     string            `json:"worker_pubkey"`
	Labels           map[string]string `json:"labels"`
	Reason           string            `json:"reason,omitempty"`
	OperatorMetadata map[string]any    `json:"operator_metadata,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	AgentID          string            `json:"agent_id,omitempty"`
}

type WorkerPolicyApplyCommand struct {
	EnvironmentID    string         `json:"environment_id"`
	Policy           map[string]any `json:"policy"`
	Reason           string         `json:"reason,omitempty"`
	OperatorMetadata map[string]any `json:"operator_metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
}

type WorkloadPinCommand struct {
	EnvironmentID    string         `json:"environment_id,omitempty"`
	WorkloadID       string         `json:"workload_id,omitempty"`
	WorkloadKind     string         `json:"workload_kind,omitempty"`
	WorkerPubKey     string         `json:"worker_pubkey"`
	Reason           string         `json:"reason,omitempty"`
	OperatorMetadata map[string]any `json:"operator_metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
}

type WorkerCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	StateKind       int    `json:"state_kind"`
	DTag            string `json:"d_tag"`
	PublishedRelays int    `json:"published_relays"`
	WorkerPubKey    string `json:"worker_pubkey,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	WorkloadID      string `json:"workload_id,omitempty"`
	WorkloadKind    string `json:"workload_kind,omitempty"`
	Command         string `json:"command"`
}
