package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/backup"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type backupSnapshotFake struct {
	creates          int
	found            bool
	panicAfterEffect bool
	createError      bool
	listError        bool
}

func (f *backupSnapshotFake) CreateCanonicalSnapshot(_ context.Context, _ *domain.BackupRepository, _ *domain.BackupRecipe, _ uuid.UUID, _ *secretstore.Value) (backup.CanonicalSnapshot, error) {
	f.creates++
	if f.createError {
		return backup.CanonicalSnapshot{}, errors.New("private CLI failure")
	}
	f.found = true
	if f.panicAfterEffect {
		f.panicAfterEffect = false
		panic("process killed after Kopia effect")
	}
	return backup.CanonicalSnapshot{ID: "snapshot-1"}, nil
}
func (f *backupSnapshotFake) ObserveCanonicalSnapshot(_ context.Context, _ *domain.BackupRepository, _ uuid.UUID, _ *secretstore.Value) (backup.CanonicalSnapshot, bool, error) {
	if f.listError {
		return backup.CanonicalSnapshot{}, false, errors.New("list failed")
	}
	if f.found {
		return backup.CanonicalSnapshot{ID: "snapshot-1"}, true, nil
	}
	return backup.CanonicalSnapshot{}, false, nil
}

type backupCredentialFake struct{ ref domain.SecretCredentialRef }

func (f backupCredentialFake) CurrentReference(context.Context, string) (domain.SecretCredentialRef, error) {
	return f.ref, nil
}
func (f backupCredentialFake) Resolve(context.Context, uuid.UUID) (secretstore.Resolved, error) {
	return secretstore.Resolved{Secret: domain.SecretRef{CredentialRef: &f.ref}, Value: secretstore.NewValue([]byte(`{"config":{},"password":"private"}`), time.Now().Add(time.Minute))}, nil
}

type backupStateSignerFake struct{ key nostr.SecretKey }

func (f backupStateSignerFake) SignBackupRunState(_ context.Context, run *domain.BackupRun, stamp nostr.Timestamp) (nostr.Event, error) {
	tags, content := nostradapter.BackupRunStateRecord(run, nil)
	kind, envelope := nostradapter.ControlStateEnvelope(kinds.BackupRunState, nostradapter.BackupRunDTag(run.ID), false)
	event := nostr.Event{Kind: nostr.Kind(kind), CreatedAt: stamp, Tags: append(envelope, tags...), Content: content}
	return event, event.Sign(f.key)
}

type backupLockFake struct{}

func (backupLockFake) TryLock(context.Context, uuid.UUID) (func(), bool, error) {
	return func() {}, true, nil
}

type backupExecutorFixture struct {
	outbox         *localstore.Outbox
	service, actor nostr.SecretKey
	intent         *Intent
	ref            domain.SecretCredentialRef
	effect         *backupSnapshotFake
	status         *IntentStatusPublisher
	state          backupStateSignerFake
	enqueueFail    bool
	staged         []nostr.Event
}

func newBackupExecutorFixture(t *testing.T) *backupExecutorFixture {
	t.Helper()
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })
	f := &backupExecutorFixture{outbox: outbox, service: nostr.Generate(), actor: nostr.Generate(), effect: &backupSnapshotFake{}}
	f.state = backupStateSignerFake{f.service}
	request := signedBackupRunFixture(t, f.actor, func(content map[string]any) {
		s := content["execution_snapshot"].(domain.BackupExecutionSnapshot)
		s.Policy.RequireVerification = false
		s.Policy.VerificationMode = domain.BackupVerificationNone
		s.Recipe.VerificationMode = domain.BackupVerificationNone
		s.Recipe.TargetRef = "fs:/database"
		content["target_ref"] = s.Recipe.TargetRef
		content["verification_mode"] = domain.BackupVerificationNone
		content["execution_snapshot"] = s
	})
	f.intent, err = ValidateSignedBackupRunRequest(&request, f.actor.Public().Hex())
	require.NoError(t, err)
	run, err := backupRunFromIntentContent(f.intent)
	require.NoError(t, err)
	f.ref = *run.ExecutionSnapshot.CredentialRef
	now := time.Now().UTC()
	expiresAt, err := signedBackupRunExpiration(&request)
	require.NoError(t, err)
	run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag = f.actor.Public().Hex(), request.ID.Hex(), int(request.Kind), f.intent.Coordinate
	run.Status, run.CreatedAt, run.UpdatedAt = domain.RunStatusQueued, now, now
	_, _, err = outbox.PutBackupRunPending(localstore.BackupRunPending{IntentID: f.intent.IntentID, Coordinate: f.intent.Coordinate, RequestEvent: request,
		Actor: f.actor.Public().Hex(), ServicePubkey: f.service.Public().Hex(), ReceivedAt: now, ExpiresAt: expiresAt})
	require.NoError(t, err)
	state, err := backupReceiptEventForAdmission(f.service, run)
	require.NoError(t, err)
	_, _, err = outbox.EnqueueBackupRun(localstore.OutboxEntry{Event: state, Target: "control-plane"}, f.intent.IntentID, f.intent.Coordinate, request.ID.Hex(), f.actor.Public().Hex())
	require.NoError(t, err)
	f.ack(t, state, "control-plane")
	signer, err := NewPrivateKeySigner(f.service.Hex())
	require.NoError(t, err)
	f.status = NewIntentStatusPublisher(func(context.Context, nostr.Event) error { return nil }, signer, zap.NewNop())
	reconciler, err := NewBackupRunStatusReconciler(outbox, f.status, "operator-relays", func() {}, nil, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	acceptedPending, err := outbox.GetBackupRunPending(f.intent.IntentID, f.intent.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, localstore.BackupRunAcceptedState, acceptedPending.State)
	admission, err := outbox.GetBackupRunAdmission(f.intent.IntentID, f.intent.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	statusID, err := nostr.IDFromHex(admission.StatusEventID)
	require.NoError(t, err)
	accepted, found, err := outbox.Get(statusID)
	require.NoError(t, err)
	require.True(t, found)
	f.ack(t, accepted.Event, "operator-relays")
	return f
}
func (f *backupExecutorFixture) ack(t *testing.T, event nostr.Event, target string) {
	t.Helper()
	_, err := f.outbox.CommitPublisherRound(event.ID, localstore.OutboxRound{Target: target, State: localstore.OutboxPublished, Delivered: true,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://operator.example"}, Required: 1},
		Relays: map[string]localstore.RelayDelivery{"wss://operator.example": {Accepted: true}}})
	require.NoError(t, err)
}
func (f *backupExecutorFixture) executor(t *testing.T) *BackupRunExecutor {
	t.Helper()
	r, err := NewBackupRunExecutor(f.outbox, f.status, f.state, f.effect, backupCredentialFake{f.ref}, backupCredentialFake{f.ref}, backupLockFake{}, "control-plane", "operator-relays",
		func(_ context.Context, event nostr.Event, _ string) error {
			f.staged = append(f.staged, event)
			if f.enqueueFail {
				f.enqueueFail = false
				return errors.New("kill before publish")
			}
			target := "control-plane"
			if event.Kind == 30315 {
				target = "operator-relays"
			}
			_, err := f.outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: target})
			return err
		}, zap.NewNop())
	require.NoError(t, err)
	return r
}
func (f *backupExecutorFixture) receipt(t *testing.T) backupRunEffectReceipt {
	t.Helper()
	raw, err := f.outbox.GetControlRecord(backupRunEffectFamily, f.intent.IntentID)
	require.NoError(t, err)
	var receipt backupRunEffectReceipt
	require.NoError(t, json.Unmarshal(raw, &receipt))
	return receipt
}

func TestBackupRunAcceptedExecutedTerminalAndKillWindows(t *testing.T) {
	for _, kill := range []string{"none", "after-effect", "before-checkpoint-publish"} {
		t.Run(kill, func(t *testing.T) {
			f := newBackupExecutorFixture(t)
			if kill == "after-effect" {
				f.effect.panicAfterEffect = true
			}
			if kill == "before-checkpoint-publish" {
				f.enqueueFail = true
			}
			r := f.executor(t)
			var err error
			if kill == "after-effect" {
				func() {
					defer func() { require.Equal(t, "process killed after Kopia effect", recover()) }()
					_ = r.ReconcileOnce(t.Context())
				}()
			} else {
				err = r.ReconcileOnce(t.Context())
			}
			switch kill {
			case "none":
				require.NoError(t, err)
			case "before-checkpoint-publish":
				require.Error(t, err)
			}
			if kill == "after-effect" {
				receipt := f.receipt(t)
				require.Nil(t, receipt.Checkpoint)
				// A restarted executor observes the tag instead of repeating create.
				r = f.executor(t)
				require.NoError(t, r.ReconcileOnce(t.Context()))
			}
			if kill == "before-checkpoint-publish" {
				receipt := f.receipt(t)
				require.NotNil(t, receipt.Checkpoint)
				first := receipt.Checkpoint.ID
				r = f.executor(t)
				require.NoError(t, r.ReconcileOnce(t.Context()))
				require.Equal(t, first, f.staged[len(f.staged)-1].ID)
			}
			require.Equal(t, 1, f.effect.creates, "external snapshot effect must occur once")
			receipt := f.receipt(t)
			require.NotNil(t, receipt.Checkpoint)
			f.ack(t, *receipt.Checkpoint, "operator-relays")
			require.NoError(t, r.ReconcileOnce(t.Context()))
			receipt = f.receipt(t)
			require.NotNil(t, receipt.TerminalState)
			f.ack(t, *receipt.TerminalState, "control-plane")
			require.NoError(t, r.ReconcileOnce(t.Context()))
			receipt = f.receipt(t)
			require.NotNil(t, receipt.TerminalStatus)
			f.ack(t, *receipt.TerminalStatus, "operator-relays")
			require.NoError(t, r.ReconcileOnce(t.Context()))
			retained, err := f.outbox.GetBackupRunPending(f.intent.IntentID, f.intent.Coordinate, f.intent.Event.ID.Hex())
			require.NoError(t, err)
			require.Equal(t, f.intent.Event.ID, retained.RequestEvent.ID)
			require.Equal(t, f.intent.Event.Sig, retained.RequestEvent.Sig)
			require.Empty(t, retained.RequestEvent.Content)
			require.Empty(t, retained.RequestEvent.Tags)
			require.NoError(t, r.ReconcileOnce(t.Context()), "compacted terminal replay must not require the request body")
			require.Equal(t, 1, f.effect.creates)
			require.Equal(t, "snapshot-1", receipt.SnapshotID)
		})
	}
}

func TestBackupRunObservationFailureRequiresOperatorRecovery(t *testing.T) {
	f := newBackupExecutorFixture(t)
	f.effect.listError = true
	err := f.executor(t).ReconcileOnce(t.Context())
	require.ErrorIs(t, err, ErrBackupRunOperatorRecovery)
	require.Zero(t, f.effect.creates)
}

func TestBackupRunReceiptWithoutTagRetriesExactlyOneSnapshot(t *testing.T) {
	f := newBackupExecutorFixture(t)
	runID, err := uuid.Parse(f.intent.Coordinate[len("backup-run:"):])
	require.NoError(t, err)
	args, err := backup.CanonicalSnapshotEffectArgs(&domain.BackupRecipe{TargetRef: "fs:/database"}, runID)
	require.NoError(t, err)
	receipt := backupRunEffectReceipt{IntentID: f.intent.IntentID, Coordinate: f.intent.Coordinate, RequestEventID: f.intent.Event.ID.Hex(),
		CredentialRef: f.ref, StepID: backupRunStep, KopiaArgs: args}
	require.NoError(t, f.executor(t).save(f.intent.IntentID, &receipt))
	require.NoError(t, f.executor(t).ReconcileOnce(t.Context()))
	require.Equal(t, 1, f.effect.creates)
	require.Equal(t, "snapshot-1", f.receipt(t).SnapshotID)
}

func TestBackupRunConfirmedCreateFailureStagesTerminalFailure(t *testing.T) {
	f := newBackupExecutorFixture(t)
	f.effect.createError = true
	r := f.executor(t)
	require.NoError(t, r.ReconcileOnce(t.Context()))
	receipt := f.receipt(t)
	require.Nil(t, receipt.Checkpoint)
	require.NotNil(t, receipt.TerminalState)
	require.Contains(t, receipt.Failure, "Kopia snapshot create failed")
	require.NotContains(t, receipt.Failure, "private CLI failure")
	f.ack(t, *receipt.TerminalState, "control-plane")
	require.NoError(t, r.ReconcileOnce(t.Context()))
	require.NotNil(t, f.receipt(t).TerminalStatus)
}

func TestBackupRunPinnedCredentialMovedRefusesWithoutEffect(t *testing.T) {
	f := newBackupExecutorFixture(t)
	f.ref.Version++
	r := f.executor(t)
	require.NoError(t, r.ReconcileOnce(t.Context()))
	receipt := f.receipt(t)
	require.Contains(t, receipt.Failure, "fresh signed run")
	require.Zero(t, f.effect.creates)
	require.Nil(t, receipt.Checkpoint)
	require.NotNil(t, receipt.TerminalState)
	f.ack(t, *receipt.TerminalState, "control-plane")
	require.NoError(t, r.ReconcileOnce(t.Context()))
	receipt = f.receipt(t)
	require.NotNil(t, receipt.TerminalStatus)
	require.Equal(t, "error", backupReceiptTag(receipt.TerminalStatus.Tags, "status"))
	require.Contains(t, receipt.TerminalStatus.Content, "fresh signed run")
	f.ack(t, *receipt.TerminalStatus, "operator-relays")
	require.NoError(t, r.ReconcileOnce(t.Context()))
}

func TestBackupRunIntakeRefusesUnsupportedSnapshotBeforePending(t *testing.T) {
	f := newBackupExecutorFixture(t)
	run, err := backupRunFromIntentContent(f.intent)
	require.NoError(t, err)
	run.ExecutionSnapshot.Recipe.TargetRef, run.TargetRef = "/database", "/database"
	err = (&BackupIntentHandler{}).verifyRunInputs(t.Context(), run)
	require.ErrorContains(t, err, "unsupported Kopia snapshot input")
	run.ExecutionSnapshot.Recipe.TargetRef, run.TargetRef = "fs:/database", "fs:/database"
	run.VerificationMode, run.ExecutionSnapshot.Recipe.VerificationMode = domain.BackupVerificationKopiaSnapshotVerify, domain.BackupVerificationKopiaSnapshotVerify
	err = (&BackupIntentHandler{}).verifyRunInputs(t.Context(), run)
	require.ErrorContains(t, err, "only a single unfiltered Kopia snapshot")
}
