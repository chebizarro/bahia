package controlplane

const backupDelegationVersion = "bahia.backup.delegation.v1"

type backupDelegationRecord struct {
	Version          string `json:"version"`
	RequesterPubkey  string `json:"requester_pubkey"`
	RequestEventID   string `json:"request_event_id"`
	RequestEventKind int    `json:"request_event_kind"`
	TenantID         string `json:"tenant_id"`
	Capability       string `json:"capability"`
	ServicePubkey    string `json:"service_pubkey"`
}
