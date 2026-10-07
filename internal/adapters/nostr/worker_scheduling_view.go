package nostr

import (
	"context"
	"fmt"
	"strconv"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// WorkerSchedulingView reads a worker's operator scheduling state (active,
// cordoned, draining,...) from the daemon's retained worker-state record in
// the local event store. It lets worker admission honour an operator's cordon
// without a SQL worker row.
type WorkerSchedulingView struct {
	history ProjectionHistory
}

// NewWorkerSchedulingView returns a view over the daemon's own records.
func NewWorkerSchedulingView(history ProjectionHistory) *WorkerSchedulingView {
	return &WorkerSchedulingView{history: history}
}

// WorkerSchedulingState returns the scheduling state the daemon last published
// for the worker. A worker with no retained record has no operator override
// and is active.
func (v *WorkerSchedulingView) WorkerSchedulingState(ctx context.Context, pubkey string) (domain.WorkerSchedulingState, error) {
	if v == nil || v.history == nil {
		return "", fmt.Errorf("worker scheduling view is unavailable")
	}
	d, ok := kinds.CPStateFamilyWorkerState.WorkerDTag(pubkey)
	if !ok {
		return "", fmt.Errorf("worker state coordinate is unavailable")
	}
	records, err := v.history.FindByTag(ctx, "d", d, []int{KindCASControlState}, 1)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindWorkerState) || isTombstoneTags(tags) {
			continue
		}
		if state := tagValue(tags, "scheduling_state"); state != "" {
			return domain.WorkerSchedulingState(state), nil
		}
	}
	return domain.WorkerSchedulingActive, nil
}
