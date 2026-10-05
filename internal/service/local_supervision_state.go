package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// LocalSupervisionHistory is the author-scoped LocalEventRepository view used
// by supervision. That repository returns newest-first events and collapses
// addressable records to the latest version of each coordinate.
type LocalSupervisionHistory interface {
	FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error)
}

// LocalSupervisionState reads the daemon's subscribed, verified cp-state
// history. The store is populated by relay catch-up and live subscriptions;
// SQL projections are deliberately not consulted by supervisors.
type LocalSupervisionState struct {
	History LocalSupervisionHistory
}

func (s LocalSupervisionState) records(ctx context.Context, topic string) ([]repository.NostrEventRecord, error) {
	if s.History == nil {
		return nil, fmt.Errorf("local supervision history is required")
	}
	// The local repository is a bounded latest-coordinate store. MaxInt means
	// "all retained coordinates", not unbounded relay history.
	return s.History.FindByTag(ctx, "t", topic, []int{kinds.CASControlState}, int(^uint(0)>>1))
}

func localStateContent(event repository.NostrEventRecord, out any) bool {
	if localTag(event, "deleted") == "true" {
		return false
	}
	return json.Unmarshal([]byte(event.Content), out) == nil
}

func localTag(event repository.NostrEventRecord, name string) string {
	var tags [][]string
	if json.Unmarshal(event.Tags, &tags) != nil {
		return ""
	}
	for _, tag := range tags {
		if len(tag) > 1 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// SupervisionReadiness gates autonomous sweeps until relay catch-up has made
// the local history authoritative. It is intentionally channel-based.
type SupervisionReadiness interface {
	ReadySignal() <-chan struct{}
}

func waitForSupervisionReadiness(ctx context.Context, readiness SupervisionReadiness) error {
	if readiness == nil {
		return nil
	}
	select {
	case <-readiness.ReadySignal():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
