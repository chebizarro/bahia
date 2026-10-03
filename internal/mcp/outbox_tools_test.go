package mcp

import (
	"context"
	"encoding/json"
	"testing"

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
