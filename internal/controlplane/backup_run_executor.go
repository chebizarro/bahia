package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/backup"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// ErrBackupRunOperatorRecovery means the effect may have happened, but Kopia
// cannot currently prove its outcome. No second effect is attempted.
var ErrBackupRunOperatorRecovery = errors.New("bahia-cd0wr: backup snapshot requires operator recovery")

const backupRunEffectFamily = "backup-run-effect-v1"
const backupRunStep = "snapshot-create"

type backupSnapshotEffect interface {
	CreateCanonicalSnapshot(context.Context, *domain.BackupRepository, *domain.BackupRecipe, uuid.UUID, *secretstore.Value) (backup.CanonicalSnapshot, error)
	ObserveCanonicalSnapshot(context.Context, *domain.BackupRepository, uuid.UUID, *secretstore.Value) (backup.CanonicalSnapshot, bool, error)
}

type backupRunStateSigner interface {
	SignBackupRunState(context.Context, *domain.BackupRun, nostr.Timestamp) (nostr.Event, error)
}

type backupRunEffectLock interface {
	TryLock(context.Context, uuid.UUID) (func(), bool, error)
}

// backupRunEffectReceipt contains only nonsecret, signed inputs and exact
// events to replay. Its creation precedes the external snapshot effect.
type backupRunEffectReceipt struct {
	IntentID       string                     `json:"intent_id"`
	Coordinate     string                     `json:"coordinate"`
	RequestEventID string                     `json:"request_event_id"`
	CredentialRef  domain.SecretCredentialRef `json:"credential_ref"`
	StepID         string                     `json:"step_id"`
	KopiaArgs      []string                   `json:"kopia_args"`
	SnapshotID     string                     `json:"snapshot_id,omitempty"`
	Failure        string                     `json:"failure,omitempty"`
	Checkpoint     *nostr.Event               `json:"checkpoint,omitempty"`
	TerminalState  *nostr.Event               `json:"terminal_state,omitempty"`
	TerminalStatus *nostr.Event               `json:"terminal_status,omitempty"`
}

// BackupRunExecutor is a bbolt/outbox runner; PostgreSQL is never consulted
// for admission, effect recovery, checkpointing, or terminal authority.
type BackupRunExecutor struct {
	Outbox            *localstore.Outbox
	Status            *IntentStatusPublisher
	State             backupRunStateSigner
	Effect            backupSnapshotEffect
	CredentialRecords backup.KopiaCredentialRecords
	CredentialValues  backup.KopiaCredentialValues
	Lock              backupRunEffectLock
	ControlTarget     string
	StatusTarget      string
	Enqueue           func(context.Context, nostr.Event, string) error
	Logger            *zap.Logger
	wake              chan struct{}
	lastError         atomic.Value
}

func NewBackupRunExecutor(outbox *localstore.Outbox, status *IntentStatusPublisher, state backupRunStateSigner, effect backupSnapshotEffect,
	records backup.KopiaCredentialRecords, values backup.KopiaCredentialValues, lock backupRunEffectLock, controlTarget, statusTarget string,
	enqueue func(context.Context, nostr.Event, string) error, logger *zap.Logger) (*BackupRunExecutor, error) {
	if outbox == nil || status == nil || status.signer == nil || state == nil || effect == nil || records == nil || values == nil || lock == nil || controlTarget == "" || statusTarget == "" || enqueue == nil {
		return nil, fmt.Errorf("bahia-cd0wr: backup run executor preconditions unavailable")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	r := &BackupRunExecutor{Outbox: outbox, Status: status, State: state, Effect: effect, CredentialRecords: records,
		CredentialValues: values, Lock: lock, ControlTarget: controlTarget, StatusTarget: statusTarget, Enqueue: enqueue, Logger: logger.Named("backup-run-executor"), wake: make(chan struct{}, 1)}
	r.lastError.Store("")
	return r, nil
}

func (r *BackupRunExecutor) Name() string      { return "backup-run-executor" }
func (r *BackupRunExecutor) LastError() string { value, _ := r.lastError.Load().(string); return value }
func (r *BackupRunExecutor) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *BackupRunExecutor) Notify(ev nostr.Event) {
	if ev.Kind == 30315 || ev.Kind == 30900 {
		r.Wake()
	}
}

func (r *BackupRunExecutor) Run(ctx context.Context) error {
	for {
		err := r.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.Logger.Warn("backup run execution delayed", zap.Error(err))
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-r.wake:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		}
	}
}

func (r *BackupRunExecutor) ReconcileOnce(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("backup run executor unavailable")
	}
	var after string
	var failures []error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, next, err := r.Outbox.ListBackupRunAdmissionsReadyForExecution(after, 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := r.reconcile(ctx, record); err != nil {
				failures = append(failures, fmt.Errorf("backup run %s: %w", record.IntentID, err))
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	err := errors.Join(failures...)
	if err != nil {
		r.lastError.Store(err.Error())
	} else {
		r.lastError.Store("")
	}
	return err
}

func (r *BackupRunExecutor) reconcile(ctx context.Context, record localstore.BackupRunAdmission) error {
	current, err := r.Outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	if err != nil || current == nil {
		return fmt.Errorf("accepted run proof unavailable: %w", err)
	}
	if !current.Delivered || !current.StatusDelivered || current.StatusOutcome != "accepted" {
		return nil
	}
	runID, err := uuid.Parse(strings.TrimPrefix(current.Coordinate, "backup-run:"))
	if err != nil || runID == uuid.Nil {
		return fmt.Errorf("accepted run coordinate is invalid")
	}
	unlock, ok, err := r.Lock.TryLock(ctx, runID)
	if err != nil || !ok {
		return err
	}
	defer unlock()
	pending, err := r.Outbox.GetBackupRunPending(current.IntentID, current.Coordinate, current.RequestEventID)
	if err != nil || pending == nil {
		return fmt.Errorf("accepted signed request unavailable: %w", err)
	}
	intent, err := ParseSignedBackupRunRequestForReplay(&pending.RequestEvent, current.Actor)
	if err != nil {
		return err
	}
	run, err := backupRunFromIntentContent(intent)
	if err != nil || run.ID != runID {
		return fmt.Errorf("accepted run snapshot identity changed")
	}
	if err := validateBackupExecutionSnapshot(run, run); err != nil {
		return err
	}
	run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag = current.Actor, current.RequestEventID, int(pending.RequestEvent.Kind), current.Coordinate
	run.Status = domain.RunStatusQueued
	run.CreatedAt, run.UpdatedAt = pending.ReceivedAt, pending.ReceivedAt
	key := current.IntentID
	effectArgs, err := backup.CanonicalSnapshotEffectArgs(&run.ExecutionSnapshot.Recipe, runID)
	if err != nil {
		return err
	}
	raw, err := r.Outbox.GetControlRecord(backupRunEffectFamily, key)
	if err != nil {
		return err
	}
	var receipt backupRunEffectReceipt
	if raw != nil {
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return fmt.Errorf("decode backup effect receipt: %w", err)
		}
		if receipt.IntentID != current.IntentID || receipt.Coordinate != current.Coordinate || receipt.RequestEventID != current.RequestEventID ||
			receipt.StepID != backupRunStep || run.ExecutionSnapshot.CredentialRef == nil || receipt.CredentialRef != *run.ExecutionSnapshot.CredentialRef ||
			!slices.Equal(receipt.KopiaArgs, effectArgs) {
			return fmt.Errorf("backup effect receipt conflicts with signed input")
		}
	} else {
		if run.Backend != domain.BackupBackendKopia || run.VerificationMode != domain.BackupVerificationNone || run.ExecutionSnapshot == nil ||
			(run.ExecutionSnapshot.Policy != nil && (run.ExecutionSnapshot.Policy.RequireVerification || run.ExecutionSnapshot.Policy.VerificationMode != domain.BackupVerificationNone)) ||
			run.ExecutionSnapshot.CredentialRef == nil || len(run.ExecutionSnapshot.Recipe.Include) != 0 || len(run.ExecutionSnapshot.Recipe.Exclude) != 0 {
			return fmt.Errorf("bahia-cd0wr: multi-step or non-Kopia backup recipe remains fail-closed")
		}
		receipt = backupRunEffectReceipt{IntentID: current.IntentID, Coordinate: current.Coordinate, RequestEventID: current.RequestEventID,
			CredentialRef: *run.ExecutionSnapshot.CredentialRef, StepID: backupRunStep,
			KopiaArgs: effectArgs}
		if err := r.save(key, &receipt); err != nil {
			return err
		}
	}
	if receipt.TerminalStatus != nil {
		return r.deliver(ctx, *receipt.TerminalStatus, "backup_run.terminal_status")
	}
	if receipt.Failure == "" && receipt.Checkpoint == nil {
		value, err := backup.ResolvePinnedKopiaCredential(ctx, run.ExecutionSnapshot, r.CredentialRecords, r.CredentialValues)
		if errors.Is(err, backup.ErrBackupCredentialMoved) {
			receipt.Failure = "Signet credential version moved; fresh signed run required"
			if err := r.save(key, &receipt); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if value != nil {
			func() {
				defer value.Zeroize()
				var snapshot backup.CanonicalSnapshot
				var found bool
				// Every receipt, including a newly written one, observes the tagged
				// external state. This repairs a kill after effect but before receipt.
				snapshot, found, err = r.Effect.ObserveCanonicalSnapshot(ctx, &run.ExecutionSnapshot.Repository, runID, value)
				if err != nil {
					err = ErrBackupRunOperatorRecovery
					return
				}
				if !found {
					snapshot, err = r.Effect.CreateCanonicalSnapshot(ctx, &run.ExecutionSnapshot.Repository, &run.ExecutionSnapshot.Recipe, runID, value)
					if err != nil {
						// A command failure may follow a committed effect. Reobserve
						// before deciding failure, and never return CLI error text.
						snapshot, found, err = r.Effect.ObserveCanonicalSnapshot(ctx, &run.ExecutionSnapshot.Repository, runID, value)
						if err != nil {
							err = ErrBackupRunOperatorRecovery
							return
						}
						if !found {
							receipt.Failure = "Kopia snapshot create failed after a successful empty tagged listing"
							err = r.save(key, &receipt)
							return
						}
					}
				}
				if snapshot.ID == "" && receipt.Failure == "" {
					err = ErrBackupRunOperatorRecovery
					return
				}
				receipt.SnapshotID = snapshot.ID
				err = r.save(key, &receipt)
			}()
			if err != nil {
				return err
			}
		}
	}
	if receipt.Failure == "" && receipt.Checkpoint == nil {
		floor, err := r.Outbox.BackupRunStatusTimestampFloor(current.ServicePubkey, current.Actor, current.Coordinate)
		if err != nil {
			return err
		}
		stamp := max(nostr.Now(), floor+1)
		if stamp > nostr.Now()+30 {
			return fmt.Errorf("backup step checkpoint waits for prior coordinate timestamp")
		}
		intent.StatusData = map[string]any{"run_id": runID.String(), "step_id": backupRunStep, "snapshot_id": receipt.SnapshotID}
		ev, err := r.Status.buildStatusEventWithExpiryAt(ctx, intent, "pending", "snapshot_created", "", nil, 0, stamp)
		if err != nil {
			return err
		}
		receipt.Checkpoint = &ev
		if err := r.save(key, &receipt); err != nil {
			return err
		}
	}
	if receipt.Checkpoint != nil {
		if err := r.deliver(ctx, *receipt.Checkpoint, "backup_run.step_checkpoint"); err != nil {
			return err
		}
		ready, err := r.proven(*receipt.Checkpoint)
		if err != nil || !ready {
			return err
		}
	}
	if receipt.TerminalState == nil {
		now := time.Now().UTC()
		run.FinishedAt, run.UpdatedAt = &now, now
		if receipt.Failure == "" {
			run.Status, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus = domain.RunStatusSucceeded, true, receipt.SnapshotID, domain.BackupVerificationSkipped
		} else {
			run.Status, run.FailureCategory, run.Error = domain.RunStatusFailed, domain.BackupFailureSnapshot, receipt.Failure
		}
		initial, err := nostr.IDFromHex(current.StateEventID)
		if err != nil {
			return err
		}
		initialProof, found, err := r.Outbox.GetDeliveryProof(initial)
		if err != nil || !found {
			return fmt.Errorf("initial run-state quorum proof unavailable: %w", err)
		}
		stamp := max(nostr.Now(), initialProof.Event.CreatedAt+1)
		if stamp > nostr.Now()+30 {
			return fmt.Errorf("terminal state waits for prior coordinate timestamp")
		}
		ev, err := r.State.SignBackupRunState(ctx, run, stamp)
		if err != nil {
			return err
		}
		receipt.TerminalState = &ev
		if err := r.save(key, &receipt); err != nil {
			return err
		}
	}
	if err := r.deliver(ctx, *receipt.TerminalState, "backup_run.terminal_state"); err != nil {
		return err
	}
	ready, err := r.proven(*receipt.TerminalState)
	if err != nil || !ready {
		return err
	}
	if receipt.TerminalStatus == nil {
		floor, err := r.Outbox.BackupRunStatusTimestampFloor(current.ServicePubkey, current.Actor, current.Coordinate)
		if err != nil {
			return err
		}
		stamp := max(nostr.Now(), floor+1)
		if stamp > nostr.Now()+30 {
			return fmt.Errorf("terminal status waits for prior coordinate timestamp")
		}
		status, result, reason := "success", "succeeded", ""
		if receipt.Failure != "" {
			status, result, reason = "error", "failed", receipt.Failure
		}
		ev, err := r.Status.buildStatusEventWithExpiryAt(ctx, intent, status, result, reason, nil, 0, stamp)
		if err != nil {
			return err
		}
		receipt.TerminalStatus = &ev
		if err := r.save(key, &receipt); err != nil {
			return err
		}
	}
	return r.deliver(ctx, *receipt.TerminalStatus, "backup_run.terminal_status")
}

func (r *BackupRunExecutor) save(key string, receipt *backupRunEffectReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return r.Outbox.PutControlRecord(backupRunEffectFamily, key, raw)
}

func (r *BackupRunExecutor) deliver(ctx context.Context, event nostr.Event, entityType string) error {
	ready, err := r.proven(event)
	if err != nil || ready {
		return err
	}
	return r.Enqueue(ctx, event, entityType)
}

func (r *BackupRunExecutor) proven(event nostr.Event) (bool, error) {
	proof, found, err := r.Outbox.GetDeliveryProof(event.ID)
	if err != nil || !found {
		return false, err
	}
	target := r.ControlTarget
	if event.Kind == 30315 {
		target = r.StatusTarget
	}
	if !proof.ValidFor(event, target) {
		return false, fmt.Errorf("backup run event lacks exact operator-relay quorum")
	}
	return true, nil
}
