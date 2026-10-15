package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// BackupRunStatusReconciler turns a durable run-state ACK or pending-run
// refusal into one signed final intent status. Outbox and history callbacks
// wake it outside publisher locks; startup scans repair interrupted staging.
type BackupRunStatusReconciler struct {
	outbox     *localstore.Outbox
	status     *IntentStatusPublisher
	target     string
	statusWake func()
	events     *localstore.Store
	logger     *zap.Logger
	wake       chan struct{}
	lastError  atomic.Value
}

func NewBackupRunStatusReconciler(outbox *localstore.Outbox, status *IntentStatusPublisher, statusTarget string, statusWake func(), events *localstore.Store, logger *zap.Logger) (*BackupRunStatusReconciler, error) {
	if outbox == nil || status == nil || status.signer == nil || statusWake == nil {
		return nil, fmt.Errorf("backup run status reconciliation requires durable outbox, signer, and status publisher")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	r := &BackupRunStatusReconciler{outbox: outbox, status: status, target: statusTarget, statusWake: statusWake, events: events, logger: logger.Named("backup-run-status"), wake: make(chan struct{}, 1)}
	r.lastError.Store("")
	return r, nil
}

func (r *BackupRunStatusReconciler) Name() string { return "backup-run-status" }

func (r *BackupRunStatusReconciler) LastError() string {
	if r == nil {
		return "backup run status reconciler is unavailable"
	}
	value, _ := r.lastError.Load().(string)
	return value
}

func (r *BackupRunStatusReconciler) Notify(ev nostr.Event) {
	if ev.Kind != nostr.Kind(kinds.CASControlState) || backupReceiptTag(ev.Tags, "t") != kinds.CPStateTopicBackupRun {
		return
	}
	r.Wake()
}

func (r *BackupRunStatusReconciler) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run reconciles once at startup, then after a delivery or history wake.
// Local signing/storage errors retry with backoff, not a protocol
// completion timer; relay delivery remains the publisher runner's job.
func (r *BackupRunStatusReconciler) Run(ctx context.Context) error {
	for {
		err := r.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.logger.Warn("backup run status reconciliation delayed", zap.Error(err))
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

func (r *BackupRunStatusReconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("backup run status reconciler is unavailable")
	}
	var after string
	var failures []error
	for {
		if err := ctx.Err(); err != nil {
			r.lastError.Store(err.Error())
			return err
		}
		records, next, err := r.outbox.ListBackupRunAdmissionsNeedingStatus(after, 100)
		if err != nil {
			failures = append(failures, err)
		}
		for _, record := range records {
			if err := r.reconcileRecord(ctx, record); err != nil {
				failures = append(failures, fmt.Errorf("backup run %s final status: %w", record.IntentID, err))
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	after = ""
	for {
		if err := ctx.Err(); err != nil {
			r.lastError.Store(err.Error())
			return err
		}
		records, next, err := r.outbox.ListBackupRunRefusalsNeedingStatus(after, 100)
		if err != nil {
			failures = append(failures, err)
		}
		for _, record := range records {
			if err := r.reconcileRefusal(ctx, record); err != nil {
				failures = append(failures, fmt.Errorf("backup run %s refusal status: %w", record.IntentID, err))
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	result := errors.Join(failures...)
	if result != nil {
		r.lastError.Store(result.Error())
	} else {
		r.lastError.Store("")
	}
	return result
}

func (r *BackupRunStatusReconciler) reconcileRefusal(ctx context.Context, record localstore.BackupRunPending) error {
	current, err := r.outbox.GetBackupRunPending(record.IntentID, record.Coordinate, record.RequestEvent.ID.Hex())
	if err != nil {
		return fmt.Errorf("read exact pending request: %w", err)
	}
	if current == nil {
		return fmt.Errorf("backup run pending request disappeared before refusal status staging")
	}
	if current.State != localstore.BackupRunRefusedState || current.StatusEventID != "" {
		return nil
	}
	intent := &Intent{IntentID: current.IntentID, Domain: "backup", Actor: current.Actor, Coordinate: current.Coordinate, Event: &current.RequestEvent}
	floor, err := r.outbox.BackupRunStatusTimestampFloor(current.ServicePubkey, current.Actor, current.Coordinate)
	if err != nil {
		return err
	}
	now := nostr.Now()
	createdAt := max(now, floor+1)
	if createdAt > now+30 {
		return fmt.Errorf("backup run refusal status waits for prior coordinate timestamp %d", floor)
	}
	event, err := r.status.buildStatusEventWithExpiryAt(ctx, intent, "rejected", "rejected", current.LastFailure, nil, 0, createdAt)
	if err != nil {
		return err
	}
	_, inserted, err := r.outbox.StageBackupRunRefusedStatus(*current, event, r.target)
	if err != nil {
		return err
	}
	if inserted {
		if r.events != nil {
			if _, err := r.events.SaveEvent(event); err != nil {
				r.logger.Warn("backup refusal status local cache write failed", zap.String("event_id", event.ID.Hex()), zap.Error(err))
			}
		}
		r.statusWake()
	}
	return nil
}

func (r *BackupRunStatusReconciler) reconcileRecord(ctx context.Context, record localstore.BackupRunAdmission) error {
	current, err := r.outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	if err != nil || current == nil {
		return fmt.Errorf("read exact admission: %w", err)
	}
	if current.StatusEventID != "" || !current.Delivered {
		return nil
	}
	requestID, err := nostr.IDFromHex(current.RequestEventID)
	if err != nil {
		return err
	}
	intent := &Intent{IntentID: current.IntentID, Domain: "backup", Actor: current.Actor, Coordinate: current.Coordinate,
		Event: &nostr.Event{ID: requestID}, StatusData: map[string]any{
			"run_id": strings.TrimPrefix(current.Coordinate, "backup-run:"), "state_event_id": current.StateEventID, "execution": "queued",
		}}
	floor, err := r.outbox.BackupRunStatusTimestampFloor(current.ServicePubkey, current.Actor, current.Coordinate)
	if err != nil {
		return err
	}
	now := nostr.Now()
	createdAt := max(now, floor+1)
	if createdAt > now+30 {
		return fmt.Errorf("backup run accepted status waits for prior coordinate timestamp %d", floor)
	}
	event, err := r.status.buildStatusEventWithExpiryAt(ctx, intent, "accepted", "applied", "", nil, 0, createdAt)
	if err != nil {
		return err
	}
	_, inserted, err := r.outbox.StageBackupRunAcceptedStatus(*current, event, r.target)
	if err != nil {
		return err
	}
	if inserted {
		if r.events != nil {
			if _, err := r.events.SaveEvent(event); err != nil {
				r.logger.Warn("backup final status local cache write failed", zap.String("event_id", event.ID.Hex()), zap.Error(err))
			}
		}
		r.statusWake()
	}
	return nil
}
