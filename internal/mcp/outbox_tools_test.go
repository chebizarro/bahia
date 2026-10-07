package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
)

// stubOutbox implements OutboxReader for unit tests.
type stubOutbox struct {
	counts localstore.OutboxCounts
	failed []localstore.OutboxEntry
}

func (s *stubOutbox) Counts() (localstore.OutboxCounts, error) { return s.counts, nil }
func (s *stubOutbox) ListFailed(limit int) ([]localstore.OutboxEntry, error) {
	if limit < len(s.failed) {
		return s.failed[:limit], nil
	}
	return s.failed, nil
}

func TestOutboxStatusCountsOnly(t *testing.T) {
	srv := &Server{
		outbox: &stubOutbox{counts: localstore.OutboxCounts{Pending: 3, Failed: 1}},
	}
	result, err := srv.handleOutboxStatus(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	var data map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &data))
	require.Equal(t, float64(3), data["pending"])
	require.Equal(t, float64(1), data["failed"])
	require.Nil(t, data["failed_entries"], "counts-only should not include details")
}

func TestOutboxStatusWithDetails(t *testing.T) {
	srv := &Server{
		outbox: &stubOutbox{
			counts: localstore.OutboxCounts{Pending: 0, Failed: 1},
			failed: []localstore.OutboxEntry{
				{
					Target:     "control-plane",
					Rounds:     3,
					LastError:  "budget exhausted",
					EntityType: "service",
				},
			},
		},
	}
	result, err := srv.handleOutboxStatus(context.Background(), map[string]interface{}{
		"include_details": true,
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	var data map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &data))
	entries, ok := data["failed_entries"].([]interface{})
	require.True(t, ok)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]interface{})
	require.Equal(t, "budget exhausted", entry["last_error"])
	require.Equal(t, "service", entry["entity_type"])
}

func TestOutboxStatusNilOutbox(t *testing.T) {
	srv := &Server{outbox: nil}
	result, err := srv.handleOutboxStatus(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].Text, "not available")
}

// A real local outbox file: a failed entry retried through the tool goes
// back to pending for the daemon's runner (docs/architecture/outbox-delivery.md).
func TestOutboxRetryRequeuesFailedEntry(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	sk := nostr.Generate()
	ev := nostr.Event{Kind: 30900, CreatedAt: 100, Tags: nostr.Tags{{"d", "x"}}, Content: "{}"}
	require.NoError(t, ev.Sign(sk))
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "control-plane", EnqueuedAt: time.Now()})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, localstore.OutboxRound{Rounds: 5, State: localstore.OutboxFailed, Detail: "abandoned after 5 publish attempts", At: time.Now()})
	require.NoError(t, err)

	srv := &Server{outbox: outbox}
	result, err := srv.handleOutboxRetry(context.Background(), map[string]interface{}{"event_id": ev.ID.Hex()})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content[0].Text)
	var data map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &data))
	require.Equal(t, localstore.OutboxPending, data["state"])
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)

	// Retrying a pending entry is refused; a read-only outbox is reported.
	result, err = srv.handleOutboxRetry(context.Background(), map[string]interface{}{"event_id": ev.ID.Hex()})
	require.NoError(t, err)
	require.True(t, result.IsError)
	result, err = (&Server{outbox: &stubOutbox{}}).handleOutboxRetry(context.Background(), map[string]interface{}{"all": true})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].Text, "read-only")
	result, err = srv.handleOutboxRetry(context.Background(), map[string]interface{}{"all": true, "event_id": "x"})
	require.NoError(t, err)
	require.True(t, result.IsError)
}
