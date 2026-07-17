package controlplane

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestRespondersFailWhenPublishingIsNotConfigured(t *testing.T) {
	ctx := context.Background()
	request := &nostr.Event{ID: nostr.ID{1}, PubKey: nostr.PubKey{2}}
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "llm",
			call: func() error {
				intent := &domain.LLMDeploymentIntent{ID: uuid.New(), Metadata: map[string]any{"nostr_event_id": "request", "nostr_request_pubkey": "requester"}}
				return (&LLMResponder{}).PublishResult(ctx, intent, nil, "succeeded", "")
			},
		},
		{
			name: "ml",
			call: func() error {
				intent := &domain.MLDeploymentIntent{ID: uuid.New(), Metadata: map[string]any{"nostr_event_id": "request", "nostr_request_pubkey": "requester"}}
				return (&MLResponder{}).PublishResult(ctx, intent, nil, "succeeded", "")
			},
		},
		{
			name: "tool",
			call: func() error {
				return (&ToolResponder{}).PublishResult(ctx, request, &domain.ToolProvisionIntent{ID: uuid.New()}, true, "")
			},
		},
		{
			name: "backup run",
			call: func() error {
				run := &domain.BackupRun{ID: uuid.New(), RequestEventID: "request", RequestedBy: "requester"}
				return (&BackupRunResponder{}).PublishBackupRunStatus(ctx, run, "start", "")
			},
		},
		{
			name: "backup restore",
			call: func() error {
				run := &domain.BackupRestoreRun{ID: uuid.New(), RequestEventID: "request", RequestedBy: "requester"}
				return (&BackupRestoreResponder{}).PublishBackupRestoreStatus(ctx, run, "start", "")
			},
		},
		{
			name: "backup retention",
			call: func() error {
				run := &domain.BackupRetentionRun{ID: uuid.New(), RequestEventID: "request", RequestedBy: "requester"}
				return (&BackupRetentionResponder{}).PublishBackupRetentionStatus(ctx, run, "start", "")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrResponderNotConfigured) {
				t.Fatalf("error = %v, want ErrResponderNotConfigured", err)
			}
		})
	}
}

func TestBackupResponderReportsMissingCorrelation(t *testing.T) {
	run := &domain.BackupRun{ID: uuid.New()}
	err := (&BackupRunResponder{}).PublishBackupRunResult(context.Background(), run, nil, "")
	if !errors.Is(err, ErrResponderCorrelationMissing) {
		t.Fatalf("error = %v, want ErrResponderCorrelationMissing", err)
	}
}

func TestBackupPublishResultRejectsZeroAcceptedRelays(t *testing.T) {
	if err := backupPublishResultError(nil, nil); !errors.Is(err, ErrResponderNoRelayAccepted) {
		t.Fatalf("error = %v, want ErrResponderNoRelayAccepted", err)
	}
}
