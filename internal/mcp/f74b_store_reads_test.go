package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// These goldens are the legacy MCP result shapes, served without any repository
// dependency. Real OCK encryption and the local signed-event store are used.
func TestF74bMCPStoreReadParityWithoutDatabase(t *testing.T) {
	ctx := authorizedMCPContext()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	fixture := attachCanonicalMCPFixture(t, server)
	pub := nostrpool.NewF74bCanonicalPublisher(fixture.projector, fixture.confidentialEncryptor(t, server))
	created := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	packageIntent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: "request-event-1", Operation: domain.PackageOperation("repository_apply"), RepositoryName: "libs", RequesterPubkey: "operator", Status: domain.PackageIntentStatusSucceeded, CreatedAt: created, UpdatedAt: created, ResultPayload: map[string]any{"status": "succeeded"}}
	require.NoError(t, pub.PublishPackageIntent(ctx, packageIntent))
	signedIntent := &domain.PackageIntentState{RecordType: "signed-intent", ID: uuid.New().String(), Operation: "repository-apply", RequesterPubkey: "operator", Status: "succeeded", UpdatedAt: created}
	require.NoError(t, pub.PublishSignedPackageIntent(ctx, signedIntent))
	toolIntent := &domain.ToolProvisionIntent{ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), RequestedTools: []domain.ToolRequest{{Name: "jq", Manager: "apt"}}, Status: domain.ToolProvisionStatusPending, CreatedAt: created}
	require.NoError(t, pub.PublishToolIntent(ctx, toolIntent))
	entry := &domain.ToolDenylistEntry{PackageName: "bad-package", Manager: "npm", Reason: "blocked", Source: "operator", BlockedBy: "admin", BlockedAt: created}
	require.NoError(t, pub.PublishToolDenylist(ctx, entry, false))
	profile := &domain.ToolProfileState{ServiceID: toolIntent.ServiceID, EnvironmentID: toolIntent.EnvironmentID, CurrentToolsetHash: "hash", CurrentImageDigest: "sha256:abc", InstalledTools: []domain.ResolvedTool{{Name: "jq", Manager: "apt", Version: "1.7"}}, UpdatedAt: created}
	require.NoError(t, pub.PublishToolProfile(ctx, profile, false))
	channelID := uuid.New()
	log := domain.NotificationLog{ID: uuid.New(), ChannelID: channelID, EventType: "deployment.failed", Payload: map[string]any{"service": "api"}, Status: domain.NotificationStatusRetrying, Attempts: 2, LastError: "rate limited", CreatedAt: created, UpdatedAt: created}
	require.NoError(t, pub.PublishNotificationWindow(ctx, channelID, []domain.NotificationLog{log}, false))
	for _, family := range []int{nostrpool.KindPackageIntentState, nostrpool.KindToolProvisionIntentState, nostrpool.KindToolDenylistState, nostrpool.KindToolProfileState, nostrpool.KindNotificationLogState} {
		records, readErr := server.readStateFamily(ctx, family)
		require.NoError(t, readErr)
		require.NotEmpty(t, records)
		require.NotContains(t, records[0].Event.Content, "bad-package")
		require.NotContains(t, records[0].Event.Content, "rate limited")
	}

	goldens := []struct {
		name string
		args map[string]any
		want any
	}{
		{"bahia_package_status", map[string]any{"intent_id": packageIntent.ID.String()}, packageIntent},
		{"bahia_package_status", map[string]any{"request_event_id": packageIntent.RequestEventID}, packageIntent},
		{"bahia_package_status", map[string]any{"intent_id": signedIntent.ID}, signedIntent},
		{"bahia_tool_provision_status", map[string]any{"intent_id": toolIntent.ID.String()}, toolProvisionIntentToMap(toolIntent)},
		{"bahia_tool_denylist_list", map[string]any{}, map[string]any{"entries": toolDenylistEntriesToMaps([]domain.ToolDenylistEntry{*entry}), "total": 1}},
		{"bahia_tool_profile_get", map[string]any{"service_id": profile.ServiceID.String(), "environment_id": profile.EnvironmentID.String()}, map[string]any{"state": toolProfileStateToMap(profile)}},
		{"bahia_list_notifications", map[string]any{"status": "unread", "event_type": "deployment.failed"}, map[string]any{"notifications": notificationLogsToMaps([]domain.NotificationLog{log}), "total": 1}},
		{"bahia_get_notification", map[string]any{"notification_id": log.ID.String()}, notificationLogsToMaps([]domain.NotificationLog{log})[0]},
	}
	for _, golden := range goldens {
		t.Run(golden.name, func(t *testing.T) {
			result, err := server.CallTool(ctx, golden.name, golden.args)
			require.NoError(t, err)
			require.False(t, result.IsError, "%v", result.Content)
			wantJSON, err := json.Marshal(golden.want)
			require.NoError(t, err)
			require.JSONEq(t, string(wantJSON), result.Content[0].Text)
		})
	}

	// Tombstones remove current state on the same coordinate, not a side path.
	require.NoError(t, pub.PublishToolDenylist(ctx, entry, true))
	result, err := server.CallTool(ctx, "bahia_tool_denylist_list", map[string]any{})
	require.NoError(t, err)
	require.JSONEq(t, `{"entries":[],"total":0}`, result.Content[0].Text)
	require.NoError(t, pub.PublishNotificationWindow(context.Background(), channelID, nil, true))
	result, err = server.CallTool(ctx, "bahia_list_notifications", map[string]any{})
	require.NoError(t, err)
	require.JSONEq(t, `{"notifications":[],"total":0}`, result.Content[0].Text)
}
