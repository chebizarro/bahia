package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

type refusingResultPublisher struct {
	err   error
	calls int
}

func (p *refusingResultPublisher) Publish(context.Context, nostr.Event) (int, error) {
	p.calls++
	return 0, p.err
}

func TestContextVMResultRetryStopsOnKillSwitch(t *testing.T) {
	publisher := &refusingResultPublisher{err: nostrout.ErrKillSwitch}
	failure := newContextVMResultPublishFailure(1, nil, publisher.err)
	cfg := contextVMResultRetryConfig{timeout: time.Minute, initialBackoff: time.Millisecond, maxBackoff: time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	err := continueContextVMResultRetry(ctx, publisher, nostr.Event{}, cfg, failure)
	if !errors.Is(err, nostrout.ErrKillSwitch) {
		t.Fatalf("retry error = %v, want kill switch cause reachable", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("kill switch must stop retries after the refused attempt, got %d attempts", publisher.calls)
	}
}
