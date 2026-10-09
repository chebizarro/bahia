package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// BackupRunStatusReconciler turns a durable run-state relay outcome into one
// signed final intent status. Outbox delivery callbacks wake it but never
// publish while the publisher holds its per-event lock. Startup scans cover a
// crash between the run-state ACK and status staging.
type BackupRunStatusReconciler struct {
	outbox     *localstore.Outbox
	status     *IntentStatusPublisher
	target     string
	statusWake func()
	events     *localstore.Store
	logger     *zap.Logger
	wake       chan struct{}
}

func NewBackupRunStatusReconciler(outbox *localstore.Outbox, status *IntentStatusPublisher, statusTarget string, statusWake func(), events *localstore.Store, logger *zap.Logger) (*BackupRunStatusReconciler, error) {
	if outbox == nil || status == nil || status.signer == nil || statusWake == nil {
		return nil, fmt.Errorf("backup run status reconciliation requires durable outbox, signer, and status publisher")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRunStatusReconciler{outbox: outbox, status: status, target: statusTarget, statusWake: statusWake, events: events, logger: logger.Named("backup-run-status"), wake: make(chan struct{}, 1)}, nil
}

func (r *BackupRunStatusReconciler) Name() string { return "backup-run-status" }

func (r *BackupRunStatusReconciler) Notify(ev nostr.Event) {
	if ev.Kind != nostr.Kind(kinds.CASControlState) || backupReceiptTag(ev.Tags, "t") != kinds.CPStateTopicBackupRun {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run reconciles once at startup, then only after an outbox settlement
// callback. Local signing/storage errors retry with backoff, not a protocol
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
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, next, err := r.outbox.ListBackupRunAdmissionsNeedingStatus(after, 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := r.reconcileRecord(ctx, record); err != nil {
				return fmt.Errorf("backup run %s final status: %w", record.IntentID, err)
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
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
	intent := &Intent{IntentID: current.IntentID, Actor: current.Actor, Coordinate: current.Coordinate,
		Event: &nostr.Event{ID: requestID}, StatusData: map[string]any{
			"run_id": strings.TrimPrefix(current.Coordinate, "backup-run:"), "state_event_id": current.StateEventID, "execution": "paused",
		}}
	event, err := r.status.buildStatusEvent(ctx, intent, "accepted", "applied", "", nil)
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
