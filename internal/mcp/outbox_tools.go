package mcp

import (
	"context"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// OutboxReader is the subset of *localstore.Outbox needed by the MCP tool.
// It avoids a hard import-time dependency on bbolt when the outbox is nil.
type OutboxReader interface {
	Counts() (localstore.OutboxCounts, error)
	ListFailed(limit int) ([]localstore.OutboxEntry, error)
}

func outboxToolDefinitions() []Tool {
	return []Tool{
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
