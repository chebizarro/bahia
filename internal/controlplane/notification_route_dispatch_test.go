package controlplane

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// TestNotificationChannelCreate_DispatchThroughTransport verifies that a
// gift-wrapped encrypted request for "notifications.channels.create" is
// correctly dispatched through the handler registry and reaches the
// CreateNotificationChannel handler. It tests both the legacy (repo) and
// intent-enabled (ProcessInProcess) branches.
func TestNotificationChannelCreate_DispatchThroughTransport(t *testing.T) {
	// testRequesterKey's pubkey — makeRouteRequest signs with this key.
	pubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)

	t.Run("legacy path", func(t *testing.T) {
		orgID := testOrgID()
		repo := newMemNotificationRepo()
		publisher := &memNotificationPublisher{}
		notifier := &memChannelChangeNotifier{}

		processor := NewIntentProcessor(
			NewTrustSet(nil, zap.NewNop()), nil, nil,
			IntentProcessorConfig{EnabledDomains: map[string]bool{}},
			zap.NewNop(),
		)
		rbac := auth.NewRBAC(&encryptedMemberRepo{
			members: []domain.OrgMember{{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner}},
		})

		handlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
			IntentProcessor: processor,
			NotifRepo:       &fullNotifRepo{repo},
			NotifPublisher:  publisher,
			NotifNotifier:   notifier,
			RBAC:            rbac,
			Logger:          zap.NewNop(),
		})

		transport, mockPub := encryptedRouteTransport(t, handlers)

		// Build a ContextVM JSON-RPC request with the method the web sends
		// (contextVMMethod("notifications.channels.create") → "notifications/channels-create").
		channelPayload := map[string]any{
			"name":         "PagerDuty Webhook",
			"channel_type": "webhook",
			"org_id":       orgID.String(),
			"config":       map[string]any{"url": "https://hooks.pagerduty.com/test"},
			"enabled":      true,
		}
		event := makeRouteRequest(t, ContextVMMethodNotificationChannelsCreate, channelPayload)

		transport.HandleEvent(context.Background(), event)

		// Verify a response was published.
		if len(mockPub.events) == 0 {
			t.Fatal("no response published; handler was not dispatched")
		}
		payload := routeResultPayload(t, mockPub.events[len(mockPub.events)-1])
		if payload["status"] != "created" {
			t.Fatalf("expected status=created, got %v; full payload: %#v", payload["status"], payload)
		}

		// Channel was persisted in the repo.
		channels, _ := repo.ListChannels(context.Background(), false)
		if len(channels) != 1 {
			t.Fatalf("expected 1 channel in repo, got %d", len(channels))
		}
		if channels[0].Name != "PagerDuty Webhook" {
			t.Fatalf("channel name = %q, want PagerDuty Webhook", channels[0].Name)
		}
	})

	t.Run("intent-enabled path", func(t *testing.T) {
		orgID := testOrgID()
		repo := newMemNotificationRepo()
		notifPublisher := &memNotificationPublisher{}
		notifier := &memChannelChangeNotifier{}

		trustSet := NewTrustSet(nil, zap.NewNop(),
			WithBootstrapOwners(map[string]string{orgID.String(): pubkey}),
		)
		processor := NewIntentProcessor(
			trustSet, nil, nil,
			IntentProcessorConfig{EnabledDomains: map[string]bool{"notification": true}},
			zap.NewNop(),
		)
		handler := NewNotificationIntentHandler(NotificationIntentHandlerConfig{
			Registry:  repo,
			Publisher: notifPublisher,
			Notifier:  notifier,
			Logger:    zap.NewNop(),
		})
		processor.RegisterHandler("notification", handler)

		rbac := auth.NewRBAC(&encryptedMemberRepo{
			members: []domain.OrgMember{{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner}},
		})

		handlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
			IntentProcessor: processor,
			NotifRepo:       &fullNotifRepo{repo},
			NotifPublisher:  notifPublisher,
			NotifNotifier:   notifier,
			RBAC:            rbac,
			Logger:          zap.NewNop(),
		})

		transport, mockPub := encryptedRouteTransport(t, handlers)

		channelPayload := map[string]any{
			"name":         "Slack Alert",
			"channel_type": "webhook",
			"org_id":       orgID.String(),
			"config":       map[string]any{"url": "https://hooks.slack.com/alert"},
			"enabled":      true,
		}
		event := makeRouteRequest(t, ContextVMMethodNotificationChannelsCreate, channelPayload)

		transport.HandleEvent(context.Background(), event)

		if len(mockPub.events) == 0 {
			t.Fatal("no response published; intent-enabled handler was not dispatched")
		}
		payload := routeResultPayload(t, mockPub.events[len(mockPub.events)-1])
		if payload["status"] != "created" {
			t.Fatalf("expected status=created, got %v", payload["status"])
		}

		// Intent handler persisted the channel in the repo.
		channels, _ := repo.ListChannels(context.Background(), false)
		if len(channels) != 1 {
			t.Fatalf("expected 1 channel in repo, got %d", len(channels))
		}
	})
}

// TestNotificationOperationNames_AllWebOperationsRegistered asserts that every
// operation name in the web's NOTIFICATION_ENCRYPTED_OPERATIONS map is
// registered on the transport's contextVMHandlers map under the method name
// the web actually sends (the contextVMMethod-transformed form).
//
// This catches drift between the web operation list and the daemon's handler
// registrations at compile time.
func TestNotificationOperationNames_AllWebOperationsRegistered(t *testing.T) {
	// The web's NOTIFICATION_ENCRYPTED_OPERATIONS mapped through contextVMMethod():
	//   notifications.channels.list   → notifications/channels-list
	//   notifications.channels.get    → notifications/channels-get
	//   notifications.channels.create → notifications/channels-create
	//   notifications.channels.update → notifications/channels-update
	//   notifications.channels.delete → notifications/channels-delete
	//   notifications.channels.test   → notifications/channels-test
	//   notifications.logs.list       → notifications/logs-list
	webOperations := []struct {
		dotForm   string // encrypted operation name
		slashForm string // contextVMMethod() output — what the web sends
	}{
		{"notifications.channels.list", "notifications/channels-list"},
		{"notifications.channels.get", "notifications/channels-get"},
		{"notifications.channels.create", "notifications/channels-create"},
		{"notifications.channels.update", "notifications/channels-update"},
		{"notifications.channels.delete", "notifications/channels-delete"},
		{"notifications.channels.test", "notifications/channels-test"},
		{"notifications.logs.list", "notifications/logs-list"},
	}

	// Build a full transport with all notification handlers registered.
	orgID := testOrgID()
	repo := newFakeNotificationRepo()
	processor := NewIntentProcessor(
		NewTrustSet(nil, zap.NewNop()), nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{}},
		zap.NewNop(),
	)
	rbac := newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgID: domain.RoleOwner})

	routeHandlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		IntentProcessor: processor,
		NotifRepo:       repo,
		RBAC:            rbac,
		Logger:          zap.NewNop(),
	})

	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), contextVMTestAuthorizedPubkeys(t), zap.NewNop())
	routeHandlers.Register(transport)
	RegisterNotificationEncryptedHandlers(transport, repo, nil, rbac)

	for _, op := range webOperations {
		t.Run(op.dotForm, func(t *testing.T) {
			// The web sends the slashForm as the JSON-RPC method. Verify it's registered.
			if transport.contextVMHandlers[op.slashForm] == nil {
				// Also check if the dot-form itself is registered (some handlers register under both).
				if transport.contextVMHandlers[op.dotForm] == nil {
					t.Errorf("neither %q nor %q is registered in contextVMHandlers", op.slashForm, op.dotForm)
				} else {
					t.Errorf("handler registered under dot-form %q but NOT under slash-form %q which the web sends", op.dotForm, op.slashForm)
				}
			}
		})
	}
}

// TestNotificationChannel_ContextVMAliasRoutesToHandler verifies that all
// three notification mutation ContextVM method aliases are reachable through
// transport dispatch.
func TestNotificationChannel_ContextVMAliasRoutesToHandler(t *testing.T) {
	pubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	methods := []struct {
		method string
		name   string
	}{
		{ContextVMMethodNotificationChannelsCreate, "notifications/channels-create"},
		{ContextVMMethodNotificationChannelCreate, "notification/create"},
		{ContextVMMethodNotificationChannelsUpdate, "notifications/channels-update"},
		{ContextVMMethodNotificationChannelUpdate, "notification/update"},
		{ContextVMMethodNotificationChannelsDelete, "notifications/channels-delete"},
		{ContextVMMethodNotificationChannelDelete, "notification/delete"},
	}

	orgID := testOrgID()
	repo := newMemNotificationRepo()
	processor := NewIntentProcessor(
		NewTrustSet(nil, zap.NewNop()), nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{}},
		zap.NewNop(),
	)
	rbac := auth.NewRBAC(&encryptedMemberRepo{
		members: []domain.OrgMember{{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner}},
	})
	handlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		IntentProcessor: processor,
		NotifRepo:       &fullNotifRepo{repo},
		RBAC:            rbac,
		Logger:          zap.NewNop(),
	})
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), contextVMTestAuthorizedPubkeys(t), zap.NewNop())
	handlers.Register(transport)

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			publisher.events = nil // reset
			channelID := uuid.New()
			var payload map[string]any
			if m.method == ContextVMMethodNotificationChannelsDelete || m.method == ContextVMMethodNotificationChannelDelete {
				// Seed a channel for delete to find.
				repo.channels[channelID] = &domain.NotificationChannel{
					ID: channelID, OrgID: orgID, Name: "del-test", ChannelType: domain.ChannelTypeWebhook, Enabled: true,
				}
				payload = map[string]any{"id": channelID.String()}
			} else if m.method == ContextVMMethodNotificationChannelsUpdate || m.method == ContextVMMethodNotificationChannelUpdate {
				repo.channels[channelID] = &domain.NotificationChannel{
					ID: channelID, OrgID: orgID, Name: "upd-test", ChannelType: domain.ChannelTypeWebhook, Enabled: true,
				}
				payload = map[string]any{"id": channelID.String(), "name": "updated", "channel_type": "webhook", "org_id": orgID.String()}
			} else {
				payload = map[string]any{
					"name":         "test-create",
					"channel_type": "webhook",
					"org_id":       orgID.String(),
					"config":       map[string]any{"url": "https://example.com"},
					"enabled":      true,
				}
			}

			event := makeRouteRequest(t, m.method, payload)
			transport.HandleEvent(context.Background(), event)

			if len(publisher.events) == 0 {
				t.Fatalf("no response for method %q — handler not dispatched", m.method)
			}

			// Parse the ContextVM response to verify no error.
			resp := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if resp.Error != nil {
				t.Fatalf("ContextVM error for %q: %+v", m.method, resp.Error)
			}
		})
	}
}
