package mcp

import (
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestMCPCanonicalBackupAndNotificationReadsDenyCrossTenant(t *testing.T) {
	serviceKey := nostr.Generate()
	actor := nostr.Generate().Public().Hex()
	orgA, orgB := uuid.New(), uuid.New()
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: serviceKey.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop(), mcpProjectorSigner(t, serviceKey))
	backup := nostrpool.NewBackupCanonicalPublisher(projector, zap.NewNop())
	now := time.Now().UTC()
	ownerRepo := domain.BackupRepository{ID: uuid.New(), OrgID: orgA, Name: "org-a-backup", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/a", CreatedAt: now, UpdatedAt: now}
	foreignRepo := domain.BackupRepository{ID: uuid.New(), OrgID: orgB, Name: "org-b-private-backup", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/b", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, backup.PublishRepository(ctx, &ownerRepo))
	require.NoError(t, backup.PublishRepository(ctx, &foreignRepo))
	signer, err := keyer.New(ctx, nil, serviceKey.Hex(), nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: serviceKey.Public().Hex(), Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	channels := nostrpool.NewNotificationCanonicalPublisher(projector, encryptor, nil, zap.NewNop())
	ownerChannel := domain.NotificationChannel{ID: uuid.New(), OrgID: orgA, Name: "org-a-alerts", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"secret": "a-secret"}}
	foreignChannel := domain.NotificationChannel{ID: uuid.New(), OrgID: orgB, Name: "org-b-private-alerts", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"secret": "b-secret"}}
	require.NoError(t, channels.PublishChannel(ctx, &ownerChannel))
	require.NoError(t, channels.PublishChannel(ctx, &foreignChannel))
	mismatchedID := uuid.New()
	mismatchedName := "org-b-mismatched-private-channel"
	inner := []byte(`{"id":"` + mismatchedID.String() + `","org_id":"` + orgB.String() + `","name":"` + mismatchedName + `"}`)
	ciphertext, err := encryptor.EncryptConfidential(ctx, orgA.String(), []byte(`{"id":"`+mismatchedID.String()+`"}`), nostrpool.KindNotificationChannelRegistry, mismatchedID.String(), kinds.CPStateTopicNotificationChannelRegistry, inner)
	require.NoError(t, err)
	mismatched := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", mismatchedID.String()}, {"t", kinds.CPStateTopicNotificationChannelRegistry}, {"domain", "notification"}, {"schema", kinds.CASControlStateSchema}, {"legacy_kind", strconv.Itoa(nostrpool.KindNotificationChannelRegistry)}, {"deleted", "false"}}, Content: ciphertext}
	require.NoError(t, mismatched.Sign(serviceKey))
	_, err = store.SaveEvent(mismatched)
	require.NoError(t, err)
	logs := nostrpool.NewF74bCanonicalPublisher(projector, encryptor)
	require.NoError(t, logs.PublishNotificationWindow(ctx, ownerChannel.ID, []domain.NotificationLog{{ID: uuid.New(), ChannelID: ownerChannel.ID, EventType: "org-a-event", Payload: map[string]any{"message": "org-a-log"}}}, false))
	require.NoError(t, logs.PublishNotificationWindow(ctx, foreignChannel.ID, []domain.NotificationLog{{ID: uuid.New(), ChannelID: foreignChannel.ID, EventType: "org-b-event", Payload: map[string]any{"message": "org-b-private-log"}}}, false))
	trust := controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgA.String(): actor}))
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: serviceKey.Public().Hex(), ConfidentialReader: encryptor, AuthorizedPubkeys: []string{actor}, TrustSet: trust})
	derived := newTestNotificationRepo()
	foreignLogID := uuid.New()
	derived.logs[foreignLogID] = &domain.NotificationLog{ID: foreignLogID, ChannelID: foreignChannel.ID, Status: domain.NotificationStatusPending}
	server.notificationRepo = derived
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_backup_repositories", map[string]any{}},
		{"bahia_inspect_backup_repository", map[string]any{"repository_id": foreignRepo.ID.String()}},
		{"bahia_list_notification_channels", map[string]any{}},
		{"bahia_get_notification_channel", map[string]any{"channel_id": foreignChannel.ID.String()}},
		{"bahia_list_notifications", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := server.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.NotEmpty(t, result.Content)
			wire := result.Content[0].Text
			require.NotContains(t, wire, foreignRepo.Name)
			require.NotContains(t, wire, foreignChannel.Name)
			require.NotContains(t, wire, mismatchedName)
			require.NotContains(t, wire, "b-secret")
			require.NotContains(t, wire, "org-b-private-log")
		})
	}
	listBackup, err := server.CallTool(ctx, "bahia_list_backup_repositories", map[string]any{})
	require.NoError(t, err)
	require.Contains(t, listBackup.Content[0].Text, ownerRepo.Name)
	listChannels, err := server.CallTool(ctx, "bahia_list_notification_channels", map[string]any{})
	require.NoError(t, err)
	require.Contains(t, listChannels.Content[0].Text, ownerChannel.Name)
	listLogs, err := server.CallTool(ctx, "bahia_list_notifications", map[string]any{})
	require.NoError(t, err)
	require.Contains(t, listLogs.Content[0].Text, "org-a-log")
	mark, err := server.CallTool(ctx, "bahia_mark_notification_read", map[string]any{"notification_id": foreignLogID.String()})
	require.NoError(t, err)
	require.True(t, mark.IsError)
	require.Contains(t, mark.Content[0].Text, "access denied")
	require.Equal(t, domain.NotificationStatusPending, derived.logs[foreignLogID].Status)
}

func TestMCPFleetReadPathsDenyOrgOnlyRequester(t *testing.T) {
	actor := nostr.Generate().Public().Hex()
	org := uuid.New()
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
	trust := controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{org.String(): actor}))
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{actor}, TrustSet: trust})
	for _, tool := range []string{"bahia_dns_list_endpoints", "bahia_dns_list_drift", "bahia_list_workers", "bahia_get_worker", "bahia_worker_get_assignments", "bahia_worker_preview_eligibility", "bahia_fips_list_mesh_nodes"} {
		t.Run(tool, func(t *testing.T) {
			result, err := server.CallTool(ctx, tool, map[string]any{"worker_pubkey": "org-b-private-worker"})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, result.Content[0].Text, "access denied")
			require.NotContains(t, result.Content[0].Text, "org-b-private-worker")
		})
	}
	resources, err := server.GetResources(ctx)
	require.NoError(t, err)
	for _, resource := range resources {
		require.NotContains(t, resource.URI, "bahia://dns/")
		require.NotContains(t, resource.URI, "bahia://fips/")
	}
}
