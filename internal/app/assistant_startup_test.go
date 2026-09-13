package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type blockingAssistantEventRepository struct {
	repository.NostrEventRepository
	started chan struct{}
}

func (r *blockingAssistantEventRepository) ListByKind(ctx context.Context, _ int, _ int) ([]repository.NostrEventRecord, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLoadAssistantSessionsWithTimeoutDoesNotBlockStartup(t *testing.T) {
	repo := &blockingAssistantEventRepository{started: make(chan struct{})}
	startedAt := time.Now()
	sessions := loadAssistantSessionsWithTimeout(context.Background(), repo, zap.NewNop(), 20*time.Millisecond)
	if sessions != nil {
		t.Fatalf("expected no sessions after timeout, got %#v", sessions)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("assistant session load blocked startup for %s", elapsed)
	}
	select {
	case <-repo.started:
	default:
		t.Fatal("assistant session repository was not queried")
	}
}

type contextInspectingAssistantEventRepository struct {
	repository.NostrEventRepository
	deadline time.Time
}

func (r *contextInspectingAssistantEventRepository) ListByKind(ctx context.Context, _ int, _ int) ([]repository.NostrEventRecord, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("assistant session load context has no deadline")
	}
	r.deadline = deadline
	return nil, nil
}

func TestLoadAssistantSessionsWithTimeoutAddsDeadline(t *testing.T) {
	repo := &contextInspectingAssistantEventRepository{}
	before := time.Now()
	loadAssistantSessionsWithTimeout(context.Background(), repo, zap.NewNop(), time.Second)
	if repo.deadline.Before(before) || repo.deadline.After(before.Add(2*time.Second)) {
		t.Fatalf("unexpected assistant session load deadline: %s", repo.deadline)
	}
}
