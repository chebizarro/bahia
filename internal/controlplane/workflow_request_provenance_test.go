package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func retainTestWorkflowEvent(t *testing.T, store *localstore.Store, event *nostr.Event) {
	t.Helper()
	if _, err := store.SaveEvent(*event); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreApprovalRefusesSQLOnlyRow(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.Generate().Hex()
	operator := testNostrPubKeyHexFromPrivateKey(t, operatorKey)
	serviceKey := nostr.Generate().Hex()
	signer, err := NewPrivateKeySigner(serviceKey)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	restoreID := uuid.New()
	row := &domain.BackupRestoreRun{
		ID: restoreID, BackupRunID: uuid.New(), RestoreTargetRef: "fs:/restore",
		RequestedBy: operator, RequestEventID: strings.Repeat("1", 64),
		RequestKind: KindBackupRestoreRequest, RequestDTag: "restore:sql-only",
		ApprovalStatus: domain.BackupApprovalPending, Status: domain.RunStatusQueued,
	}
	registry, _ := newBackupRequestRegistryFixture()
	registry.restores[restoreID] = row
	executor := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
	responder := &recordingBackupRestoreResponder{}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{operator}}, nil, nil, signer, zap.NewNop(), WithCanonicalWorkflowEvents(store))
	reactor.backupRegistry = registry
	reactor.backupRestoreExecutor = executor
	reactor.backupRestoreResponder = responder
	approval := signedLLMRequest(t, operatorKey, KindBackupRestoreApproval, fmt.Sprintf(`{"restore_id":%q,"approved":true}`, restoreID.String()), nostr.Tags{{"d", "approve:sql-only"}})
	reactor.handleBackupRestoreApproval(ctx, approval)
	if row.ApprovalStatus != domain.BackupApprovalPending || len(executor.calls) != 0 || len(responder.approvals) != 0 {
		t.Fatal("SQL-only restore caused approval, execution or signed restore outcome")
	}
	request := signedLLMRequest(t, operatorKey, KindBackupRestoreRequest,
		fmt.Sprintf(`{"backup_run_id":%q,"restore_target_ref":"fs:/restore"}`, row.BackupRunID.String()),
		nostr.Tags{{"d", row.RequestDTag}},
	)
	row.RequestEventID = request.ID.Hex()
	otherRestore := *row
	otherRestore.ID = uuid.New()
	retainTestRestoreAcceptance(t, store, request, serviceKey, &otherRestore)
	reactor.handleBackupRestoreApproval(ctx, approval)
	if row.ApprovalStatus != domain.BackupApprovalPending || len(executor.calls) != 0 || len(responder.approvals) != 0 {
		t.Fatal("acceptance receipt for another restore authorized SQL-only row")
	}

	intentRegistry := newFakeBackupIntentRegistry()
	intentRegistry.restores[restoreID] = row
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: intentRegistry, CanonicalEvents: store, ServicePubkey: testNostrPubKeyHexFromPrivateKey(t, serviceKey), Logger: zap.NewNop()})
	intent := &Intent{Op: "restore-approval", Actor: operator, Content: map[string]any{"restore_id": restoreID.String(), "approved": true}, Event: approval}
	if err := handler.HandleIntent(ctx, intent); err == nil {
		t.Fatal("signed approval intent accepted a SQL-only restore row")
	}
	if intentRegistry.restoreApprovals != 0 || row.ApprovalStatus != domain.BackupApprovalPending {
		t.Fatal("signed approval intent mutated SQL-only restore")
	}
}

func retainTestRestoreAcceptance(t *testing.T, store *localstore.Store, request *nostr.Event, serviceKey string, restore *domain.BackupRestoreRun) {
	t.Helper()
	retainTestWorkflowEvent(t, store, request)
	body, err := json.Marshal(map[string]any{
		"request_event_id":   request.ID.Hex(),
		"restore_id":         restore.ID.String(),
		"backup_run_id":      restore.BackupRunID.String(),
		"restore_target_ref": restore.RestoreTargetRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := signedLLMRequest(t, serviceKey, KindBackupRestoreStatus, string(body), nostr.Tags{
		{"d", "status:" + restore.ID.String() + ":pending_approval"},
		{"e", request.ID.Hex(), "", "reply"},
		{"restore_id", restore.ID.String()},
		{"step", "pending_approval"},
	})
	retainTestWorkflowEvent(t, store, receipt)
}
