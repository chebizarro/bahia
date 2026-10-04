package controlplane

import (
	"fmt"
	"strings"
)

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

func normalizeBackupApprovalDecision(approved *bool, decision string) (bool, string, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if approved == nil {
		switch decision {
		case "approve", "approved":
			value := true
			approved = &value
		case "reject", "rejected", "deny", "denied":
			value := false
			approved = &value
		default:
			return false, "", fmt.Errorf("approved or decision is required")
		}
	}
	if *approved {
		return true, "approved", nil
	}
	return false, "rejected", nil
}
