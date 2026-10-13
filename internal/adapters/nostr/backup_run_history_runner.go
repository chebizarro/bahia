package nostr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// BackupRunHistoryRunner inspects cold-cache run coordinates outside the
// ProcessSync path. It deliberately has no service-key signer: complete relay
// history is only one prerequisite for future admission.
type BackupRunHistoryRunner struct {
	outbox     *localstore.Outbox
	pool       *RelayPool
	store      BackupRunHistoryStore
	logger     *zap.Logger
	wake       chan struct{}
	now        func() time.Time
	inspect    func(context.Context, BackupRunHistoryStore, nostr.PubKey, string) (*BackupRunHistoryProof, error)
	statusWake func()
	mu         sync.Mutex
	lastError  string
}

func NewBackupRunHistoryRunner(outbox *localstore.Outbox, pool *RelayPool, store BackupRunHistoryStore, logger *zap.Logger) (*BackupRunHistoryRunner, error) {
	if outbox == nil || pool == nil || store == nil {
		return nil, fmt.Errorf("backup run history runner requires durable inbox, canonical relays and local cache")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRunHistoryRunner{outbox: outbox, pool: pool, store: store, logger: logger.Named("backup-run-history"),
		wake: make(chan struct{}, 1), now: time.Now, inspect: pool.InspectBackupRunHistory}, nil
}

func (r *BackupRunHistoryRunner) Name() string { return "backup-run-history" }

func (r *BackupRunHistoryRunner) SetStatusWake(wake func()) { r.statusWake = wake }

func (r *BackupRunHistoryRunner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *BackupRunHistoryRunner) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastError
}
func (r *BackupRunHistoryRunner) setError(err error) {
	r.mu.Lock()
	if err == nil {
		r.lastError = ""
	} else {
		r.lastError = err.Error()
	}
	r.mu.Unlock()
}

// ReconcileOnce makes one bounded pass over the durable inbox. A failed or
// stalled relay does not hold the sync goroutine, and one bad request does not
// starve later records. Complete history alone cannot enable paused admission.
func (r *BackupRunHistoryRunner) ReconcileOnce(ctx context.Context) (time.Time, error) {
	if r == nil {
		return time.Time{}, fmt.Errorf("backup run history runner unavailable")
	}
	var earliest time.Time
	var firstErr error
	var after string
	for {
		records, next, err := r.outbox.ListBackupRunPending(after, 100)
		if err != nil {
			r.setError(err)
			return time.Time{}, err
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return time.Time{}, err
			}
			now := r.now().UTC()
			if !now.Before(record.ExpiresAt) {
				if err := r.outbox.RecordBackupRunPendingAttempt(record.IntentID, record.RequestEvent.ID.Hex(), now, time.Time{}, "signed request expired before backup-run admission"); err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else if r.statusWake != nil {
					r.statusWake()
				}
				continue
			}
			if !record.NextAttemptAt.IsZero() && now.Before(record.NextAttemptAt) {
				earliest = earlierBackupRunHistoryWake(earliest, record.NextAttemptAt)
				continue
			}
			service, err := nostr.PubKeyFromHex(record.ServicePubkey)
			var proof *BackupRunHistoryProof
			if err == nil {
				deadline := record.ExpiresAt
				if bound := now.Add(DefaultStoredEventsTimeout); deadline.After(bound) {
					deadline = bound
				}
				attemptCtx, cancel := context.WithDeadline(ctx, deadline)
				proof, err = r.inspect(attemptCtx, r.store, service, record.Coordinate)
				if proof != nil {
					if currentErr := proof.StillCurrent(); currentErr != nil {
						err = currentErr
					}
					proof.Close()
				}
				cancel()
			}
			if err == nil {
				err = fmt.Errorf("backup run admission is paused")
			}
			if errors.Is(err, ErrBackupRunCoordinateExists) {
				if writeErr := r.outbox.RefuseBackupRunPending(record.IntentID, record.RequestEvent.ID.Hex(), err.Error()); writeErr != nil {
					if firstErr == nil {
						firstErr = writeErr
					}
				} else if r.statusWake != nil {
					r.statusWake()
				}
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			r.logger.Warn("backup run admission remains pending", zap.String("intent_id", record.IntentID), zap.Error(err))
			now = r.now().UTC()
			// Retry after a bounded backoff, with expiration as the hard stop.
			delay := time.Second << min(record.Attempts, 5)
			attempt := now.Add(delay)
			if attempt.After(record.ExpiresAt) {
				attempt = record.ExpiresAt
			}
			if writeErr := r.outbox.RecordBackupRunPendingAttempt(record.IntentID, record.RequestEvent.ID.Hex(), now, attempt, err.Error()); writeErr != nil {
				if firstErr == nil {
					firstErr = writeErr
				}
				continue
			}
			if now.Before(record.ExpiresAt) {
				earliest = earlierBackupRunHistoryWake(earliest, attempt)
			} else if r.statusWake != nil {
				r.statusWake()
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	r.setError(firstErr)
	return earliest, firstErr
}

func earlierBackupRunHistoryWake(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func (r *BackupRunHistoryRunner) Run(ctx context.Context) error {
	for {
		next, err := r.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.logger.Warn("backup run history pass incomplete", zap.Error(err))
			if next.IsZero() {
				next = r.now().Add(5 * time.Second)
			}
		}
		if next.IsZero() {
			select {
			case <-ctx.Done():
				return nil
			case <-r.wake:
				continue
			}
		}
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-r.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}
