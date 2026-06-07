package dto

// CommandReceipt is the canonical correlation handle returned by control-plane
// write surfaces after publishing a Nostr command event.
type CommandReceipt struct {
	RequestEventID  string         `json:"request_event_id"`
	RequestPubkey   string         `json:"request_pubkey,omitempty"`
	RequestKind     int            `json:"request_kind"`
	StatusKind      int            `json:"status_kind,omitempty"`
	ResultKind      int            `json:"result_kind,omitempty"`
	ReadModelKinds  map[string]int `json:"read_model_kinds,omitempty"`
	DTag            string         `json:"d_tag,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	RetryHint       string         `json:"retry_hint,omitempty"`
	PublishedRelays int            `json:"published_relays"`
	TimeoutSeconds  int            `json:"timeout_seconds,omitempty"`
	Message         string         `json:"message,omitempty"`
}
