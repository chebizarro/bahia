package mcp

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (s *Server) storePackageStatus(ctx context.Context, args map[string]any) (*ToolResult, error) {
	id := optionalUUIDArg(args, "intent_id")
	requestID := stringArg(args, "request_event_id")
	if id == uuid.Nil && requestID == "" {
		return errorResult("intent_id or request_event_id is required"), nil
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindPackageIntentState)
	if err != nil {
		return nil, err
	}
	var claim map[string]any
	for _, rec := range records {
		if rec.Fields["record_type"] == "intent" {
			if (id != uuid.Nil && rec.Fields["id"] == id.String()) || (requestID != "" && rec.Fields["request_event_id"] == requestID) {
				var intent domain.PackageIntent
				if err := json.Unmarshal(rec.Content, &intent); err != nil {
					return nil, err
				}
				return jsonResult(&intent)
			}
		} else if rec.Fields["record_type"] == "signed-intent" {
			if (id != uuid.Nil && rec.Fields["id"] == id.String()) || (requestID != "" && rec.Fields["request_event_id"] == requestID) {
				return jsonResult(rec.Fields)
			}
		} else if id == uuid.Nil && rec.Fields["record_type"] == "claim" && rec.Fields["request_event_id"] == requestID {
			claim = rec.Fields
		}
	}
	if claim != nil {
		return jsonResult(claim)
	}
	return errorResult("package intent not found"), nil
}

func (s *Server) storeToolProvisionStatus(ctx context.Context, args map[string]any) (*ToolResult, error) {
	id, err := parseRequiredUUIDArg(args, "intent_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	rec, err := s.readStateOne(ctx, nostrpool.KindToolProvisionIntentState, "id", id.String())
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return errorResult("intent not found"), nil
	}
	var intent domain.ToolProvisionIntent
	if err := json.Unmarshal(rec.Content, &intent); err != nil {
		return nil, err
	}
	return jsonResult(toolProvisionIntentToMap(&intent))
}

func (s *Server) storeToolDenylist(ctx context.Context) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindToolDenylistState)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.ToolDenylistEntry, 0, len(records))
	for _, rec := range records {
		var entry domain.ToolDenylistEntry
		if err := json.Unmarshal(rec.Content, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].BlockedAt.Equal(entries[j].BlockedAt) {
			return entries[i].BlockedAt.After(entries[j].BlockedAt)
		}
		if entries[i].Manager != entries[j].Manager {
			return entries[i].Manager < entries[j].Manager
		}
		return entries[i].PackageName < entries[j].PackageName
	})
	return jsonResult(map[string]any{"entries": toolDenylistEntriesToMaps(entries), "total": len(entries)})
}

func (s *Server) storeToolProfile(ctx context.Context, args map[string]any) (*ToolResult, error) {
	serviceID, err := parseRequiredUUIDArg(args, "service_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	envID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindToolProfileState)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		if rec.Fields["service_id"] == serviceID.String() && rec.Fields["environment_id"] == envID.String() {
			var profile domain.ToolProfileState
			if err := json.Unmarshal(rec.Content, &profile); err != nil {
				return nil, err
			}
			return jsonResult(map[string]any{"state": toolProfileStateToMap(&profile)})
		}
	}
	return jsonResult(map[string]any{"state": nil})
}

func (s *Server) notificationWindow(ctx context.Context) ([]domain.NotificationLog, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindNotificationLogState)
	if err != nil {
		return nil, err
	}
	logs := make([]domain.NotificationLog, 0)
	for _, rec := range records {
		var window struct {
			Logs []domain.NotificationLog `json:"logs"`
		}
		if err := json.Unmarshal(rec.Content, &window); err != nil {
			return nil, err
		}
		logs = append(logs, window.Logs...)
	}
	sort.SliceStable(logs, func(i, j int) bool {
		if !logs[i].CreatedAt.Equal(logs[j].CreatedAt) {
			return logs[i].CreatedAt.After(logs[j].CreatedAt)
		}
		return logs[i].ID.String() > logs[j].ID.String()
	})
	return logs, nil
}

func (s *Server) storeNotifications(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	if name == "bahia_get_notification" && stringArg(args, "notification_id") == "" {
		return errorResult("notification_id is required"), nil
	}
	var notificationID uuid.UUID
	if name == "bahia_get_notification" {
		parsed, parseErr := uuid.Parse(stringArg(args, "notification_id"))
		if parseErr != nil {
			return errorResult("invalid notification_id: " + parseErr.Error()), nil
		}
		notificationID = parsed
	}
	logs, err := s.notificationWindow(ctx)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_notification" {
		for _, log := range logs {
			if log.ID == notificationID {
				return jsonResult(notificationLogsToMaps([]domain.NotificationLog{log})[0])
			}
		}
		return errorResult("notification not found"), nil
	}
	limit := optionalIntArg(args, "limit", nostrpool.NotificationLogWindowLimit)
	if limit <= 0 {
		limit = nostrpool.NotificationLogWindowLimit
	}
	if limit > nostrpool.NotificationLogWindowLimit {
		limit = nostrpool.NotificationLogWindowLimit
	}
	if len(logs) > limit {
		logs = logs[:limit]
	}
	status, eventType := stringArg(args, "status"), stringArg(args, "event_type")
	filtered := make([]domain.NotificationLog, 0, len(logs))
	for _, log := range logs {
		if status == "read" && log.Status != domain.NotificationStatusSent {
			continue
		}
		if status == "unread" && log.Status != domain.NotificationStatusPending && log.Status != domain.NotificationStatusRetrying {
			continue
		}
		if eventType != "" && log.EventType != eventType {
			continue
		}
		filtered = append(filtered, log)
	}
	return jsonResult(map[string]any{"notifications": notificationLogsToMaps(filtered), "total": len(filtered)})
}
