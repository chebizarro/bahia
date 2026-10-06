package mcp

import (
	"context"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// OutboxReader is the subset of *localstore.Outbox needed by the MCP tool.
// It avoids a hard import-time dependency on bbolt when the outbox is nil.
type OutboxReader interface {
	Counts() (localstore.OutboxCounts, error)
	ListFailed(limit int) ([]localstore.OutboxEntry, error)
}

// OutboxRetrier is the optional subset of *localstore.Outbox behind the
// operator retry tool (bahia-u5whr, §3.7): a failed entry goes back to
// pending and the daemon's outbox runner delivers it on its next pass, which
// clears the coordinate's undelivered marker once the quorum accepts it.
type OutboxRetrier interface {
	Retry(id nostr.ID) (localstore.OutboxEntry, error)
	RetryAllFailed() (int, error)
}

func outboxToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "bahia_outbox_retry",
			Description: "Operator action: re-queue failed Nostr publish outbox entries (one event id, or all) so the daemon's outbox runner delivers them again; a delivered entry clears its coordinate's undelivered marker on the readiness endpoint",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"event_id": map[string]interface{}{
						"type":        "string",
						"description": "Hex id of the failed outbox entry to retry. Mutually exclusive with all.",
					},
					"all": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, retry every failed entry. Default: false.",
					},
				},
			},
		},
		{
			Name:        "bahia_outbox_status",
			Description: "Inspect the Nostr publish outbox: pending and failed event counts, with optional failed-entry details for authorized callers",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"include_details": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, include individual failed entries (authorized callers only). Default: false (counts only).",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of failed entries to return when include_details is true. Default: 20.",
					},
				},
			},
		},
	}
}

func (s *Server) handleOutboxStatus(_ context.Context, arguments map[string]interface{}) (*ToolResult, error) {
	if s.outbox == nil {
		return errorResult("outbox not available: the daemon's outbox is not wired to the MCP server"), nil
	}

	counts, err := s.outbox.Counts()
	if err != nil {
		return errorResult("failed to read outbox counts: " + err.Error()), nil
	}

	result := map[string]interface{}{
		"pending": counts.Pending,
		"failed":  counts.Failed,
	}

	includeDetails, _ := arguments["include_details"].(bool)
	if includeDetails {
		limit := 20
		if l, ok := arguments["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		failed, err := s.outbox.ListFailed(limit)
		if err != nil {
			return errorResult("failed to list failed entries: " + err.Error()), nil
		}
		details := make([]map[string]interface{}, 0, len(failed))
		for _, e := range failed {
			settled := ""
			if !e.SettledAt.IsZero() {
				settled = e.SettledAt.UTC().Format(time.RFC3339)
			}
			detail := map[string]interface{}{
				"event_id":   e.Event.ID.Hex(),
				"kind":       int(e.Event.Kind),
				"target":     e.Target,
				"rounds":     e.Rounds,
				"last_error": e.LastError,
				"settled_at": settled,
			}
			if e.EntityType != "" {
				detail["entity_type"] = e.EntityType
			}
			if e.EntityID != "" {
				detail["entity_id"] = e.EntityID
			}
			details = append(details, detail)
		}
		result["failed_entries"] = details
	}

	return jsonResult(result)
}

func (s *Server) handleOutboxRetry(_ context.Context, arguments map[string]interface{}) (*ToolResult, error) {
	if s.outbox == nil {
		return errorResult("outbox not available: the daemon's outbox is not wired to the MCP server"), nil
	}
	retrier, ok := s.outbox.(OutboxRetrier)
	if !ok {
		return errorResult("outbox retry not available: the wired outbox is read-only"), nil
	}
	all, _ := arguments["all"].(bool)
	eventID, _ := arguments["event_id"].(string)
	eventID = strings.TrimSpace(eventID)
	switch {
	case all && eventID != "":
		return errorResult("all and event_id are mutually exclusive"), nil
	case all:
		retried, err := retrier.RetryAllFailed()
		if err != nil {
			return errorResult("failed to retry outbox entries: " + err.Error()), nil
		}
		return jsonResult(map[string]interface{}{"retried": retried})
	case eventID == "":
		return errorResult("event_id is required unless all is true"), nil
	}
	id, err := nostr.IDFromHex(eventID)
	if err != nil {
		return errorResult("invalid event_id: " + err.Error()), nil
	}
	entry, err := retrier.Retry(id)
	if err != nil {
		return errorResult("failed to retry outbox entry: " + err.Error()), nil
	}
	return jsonResult(map[string]interface{}{
		"retried":  1,
		"event_id": entry.Event.ID.Hex(),
		"kind":     int(entry.Event.Kind),
		"target":   entry.Target,
		"state":    entry.State,
	})
}
