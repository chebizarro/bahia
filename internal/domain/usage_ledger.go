package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type UsageResourceType string

const (
	UsageResourceTypeCompute   UsageResourceType = "compute"
	UsageResourceTypeInference UsageResourceType = "inference"
	UsageResourceTypeStorage   UsageResourceType = "storage"
)

type UsageLedgerRecord struct {
	ID           uuid.UUID         `json:"id"`
	AgentPubkey  string            `json:"agent_pubkey"`
	TaskID       string            `json:"task_id,omitempty"`
	ResourceType UsageResourceType `json:"resource_type"`
	Amount       int64             `json:"amount"`
	RecordedAt   time.Time         `json:"recorded_at"`
	RecordedBy   string            `json:"recorded_by"`
	Signature    string            `json:"signature"`
	CorrectionOf *uuid.UUID        `json:"correction_of,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type UsageLedgerFilter struct {
	AgentPubkey  string
	TaskID       string
	ResourceType UsageResourceType
	Since        time.Time
	Until        time.Time
	Limit        int
	Offset       int
}

func ValidateUsageLedgerRecord(r *UsageLedgerRecord) error {
	if r == nil {
		return fmt.Errorf("%w: usage ledger record must not be nil", ErrInvalidValue)
	}
	r.AgentPubkey = strings.TrimSpace(r.AgentPubkey)
	r.TaskID = strings.TrimSpace(r.TaskID)
	r.RecordedBy = strings.TrimSpace(r.RecordedBy)
	r.Signature = strings.TrimSpace(r.Signature)
	r.ResourceType = UsageResourceType(strings.TrimSpace(string(r.ResourceType)))
	if r.AgentPubkey == "" {
		return fmt.Errorf("%w: agent_pubkey must not be empty", ErrEmptyField)
	}
	if r.Amount == 0 {
		return fmt.Errorf("%w: amount must not be zero", ErrInvalidValue)
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("%w: recorded_at must not be zero", ErrEmptyField)
	}
	if r.RecordedBy == "" {
		return fmt.Errorf("%w: recorded_by must not be empty", ErrEmptyField)
	}
	if r.Signature == "" {
		return fmt.Errorf("%w: signature must not be empty", ErrEmptyField)
	}
	switch r.ResourceType {
	case UsageResourceTypeCompute, UsageResourceTypeInference, UsageResourceTypeStorage:
	default:
		return fmt.Errorf("%w: resource_type %q is not valid", ErrInvalidValue, r.ResourceType)
	}
	if r.CorrectionOf != nil && *r.CorrectionOf == uuid.Nil {
		return fmt.Errorf("%w: correction_of must not be nil UUID", ErrNilUUID)
	}
	return nil
}
