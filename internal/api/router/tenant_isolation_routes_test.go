package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type tenantIsolationSBOMRepo struct{}

func (tenantIsolationSBOMRepo) CreateSBOM(context.Context, *domain.ArtifactSBOM) error { return nil }
func (tenantIsolationSBOMRepo) GetSBOMByID(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) GetSBOMByArtifact(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) GetSBOMByHash(context.Context, string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) CreatePackages(context.Context, []domain.SBOMPackage) error {
	return nil
}
func (tenantIsolationSBOMRepo) ListPackagesBySBOM(context.Context, uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (tenantIsolationSBOMRepo) SearchPackagesByName(context.Context, string, int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

type tenantIsolationRuntimeResolver struct{}

func (tenantIsolationRuntimeResolver) Resolve(*domain.Service, *domain.Environment) (runtimeadapter.Runtime, error) {
	return nil, errors.New("runtime resolution should not run for a cross-tenant request")
}

type tenantIsolationNotificationRepo struct {
	channels map[uuid.UUID]*domain.NotificationChannel
	logs     []domain.NotificationLog
}

func (r *tenantIsolationNotificationRepo) CreateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	copyChannel := *ch
	r.channels[ch.ID] = &copyChannel
	return nil
}
func (r *tenantIsolationNotificationRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	ch := r.channels[id]
	if ch == nil {
		return nil, nil
	}
	copyChannel := *ch
	return &copyChannel, nil
}
func (r *tenantIsolationNotificationRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	var out []domain.NotificationChannel
	for _, ch := range r.channels {
		if !enabledOnly || ch.Enabled {
			out = append(out, *ch)
		}
	}
	return out, nil
}
func (r *tenantIsolationNotificationRepo) UpdateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	if r.channels[ch.ID] == nil {
		return repository.ErrNotFound
	}
	copyChannel := *ch
	r.channels[ch.ID] = &copyChannel
	return nil
}
func (r *tenantIsolationNotificationRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	if r.channels[id] == nil {
		return repository.ErrNotFound
	}
	delete(r.channels, id)
	return nil
}
func (r *tenantIsolationNotificationRepo) CreateLog(_ context.Context, log *domain.NotificationLog) error {
	r.logs = append(r.logs, *log)
	return nil
}
func (r *tenantIsolationNotificationRepo) UpdateLog(context.Context, *domain.NotificationLog) error {
	return nil
}
func (r *tenantIsolationNotificationRepo) ListLogsByChannel(_ context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	var out []domain.NotificationLog
	for _, log := range r.logs {
		if log.ChannelID == channelID && len(out) < limit {
			out = append(out, log)
		}
	}
	return out, nil
}
func (r *tenantIsolationNotificationRepo) ListRecentLogs(_ context.Context, limit int) ([]domain.NotificationLog, error) {
	if limit > len(r.logs) {
		limit = len(r.logs)
	}
	return append([]domain.NotificationLog(nil), r.logs[:limit]...), nil
}
func (r *tenantIsolationNotificationRepo) ListRetryable(context.Context, int) ([]domain.NotificationLog, error) {
	return nil, nil
}
func (r *tenantIsolationNotificationRepo) GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error) {
	ch, err := r.GetChannelByID(ctx, id)
	if err != nil || ch == nil || ch.OrgID != orgID {
		return nil, err
	}
	return ch, nil
}
func (r *tenantIsolationNotificationRepo) ListChannelsByOrg(_ context.Context, orgID uuid.UUID, enabledOnly bool) ([]domain.NotificationChannel, error) {
	var out []domain.NotificationChannel
	for _, ch := range r.channels {
		if ch.OrgID == orgID && (!enabledOnly || ch.Enabled) {
			out = append(out, *ch)
		}
	}
	return out, nil
}
func (r *tenantIsolationNotificationRepo) UpdateChannelForOrg(ctx context.Context, ch *domain.NotificationChannel, orgID uuid.UUID) error {
	existing, err := r.GetChannelByIDForOrg(ctx, ch.ID, orgID)
	if err != nil {
		return err
	}
	if existing == nil {
		return repository.ErrNotFound
	}
	return r.UpdateChannel(ctx, ch)
}
func (r *tenantIsolationNotificationRepo) DeleteChannelForOrg(ctx context.Context, id, orgID uuid.UUID) error {
	existing, err := r.GetChannelByIDForOrg(ctx, id, orgID)
	if err != nil {
		return err
	}
	if existing == nil {
		return repository.ErrNotFound
	}
	return r.DeleteChannel(ctx, id)
}
func (r *tenantIsolationNotificationRepo) ListRecentLogsByOrg(_ context.Context, orgID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	var out []domain.NotificationLog
	for _, log := range r.logs {
		if ch := r.channels[log.ChannelID]; ch != nil && ch.OrgID == orgID && len(out) < limit {
			out = append(out, log)
		}
	}
	return out, nil
}

type tenantIsolationFixture struct {
	server       *httptest.Server
	aliceKey     string
	orgA         uuid.UUID
	orgB         uuid.UUID
	serviceA     uuid.UUID
	serviceB     uuid.UUID
	environmentA uuid.UUID
	environmentB uuid.UUID
	runB         uuid.UUID
	artifactB    uuid.UUID
	channelA     uuid.UUID
	channelB     uuid.UUID
}

func newTenantIsolationFixture(t *testing.T) tenantIsolationFixture {
	t.Helper()
	const aliceKey = "0000000000000000000000000000000000000000000000000000000000000001"
	aliceSecret, err := nostr.SecretKeyFromHex(aliceKey)
	if err != nil {
		t.Fatal(err)
	}
	alicePubkey := aliceSecret.Public().Hex()

	orgA, orgB := uuid.New(), uuid.New()
	serviceA := &domain.Service{ID: uuid.New(), OrgID: orgA, Name: "service-a"}
	serviceB := &domain.Service{ID: uuid.New(), OrgID: orgB, Name: "service-b"}
	environmentA := &domain.Environment{ID: uuid.New(), OrgID: orgA, Name: "environment-a"}
	environmentB := &domain.Environment{ID: uuid.New(), OrgID: orgB, Name: "environment-b"}

	services := newMockServiceRepo()
	services.services[serviceA.ID] = serviceA
	services.services[serviceB.ID] = serviceB
	environments := newMockEnvRepo()
	environments.envs[environmentA.ID] = environmentA
	environments.envs[environmentB.ID] = environmentB
	builds := newMockBuildRepo()
	artifacts := newMockArtifactRepo()
	artifactB := &domain.Artifact{ID: uuid.New(), ServiceID: serviceB.ID}
	artifacts.artifacts[artifactB.ID] = artifactB
	intents := newMockIntentRepo()
	intentB := &domain.DeploymentIntent{ID: uuid.New(), ServiceID: serviceB.ID, EnvironmentID: environmentB.ID}
	intents.intents[intentB.ID] = intentB
	runs := newMockRunRepo()
	runB := &domain.DeploymentRun{ID: uuid.New(), DeploymentIntentID: intentB.ID}
	runs.runs[runB.ID] = runB
	states := newMockStateRepo()
	states.states[sk(serviceA.ID, environmentA.ID)] = &domain.EnvironmentServiceState{ServiceID: serviceA.ID, EnvironmentID: environmentA.ID}
	states.states[sk(serviceB.ID, environmentB.ID)] = &domain.EnvironmentServiceState{ServiceID: serviceB.ID, EnvironmentID: environmentB.ID}

	registry := service.NewRegistryService(
		services, environments, builds, artifacts, intents, runs, newMockObsRepo(), states,
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)

	channelA := &domain.NotificationChannel{ID: uuid.New(), OrgID: orgA, Name: "channel-a", ChannelType: domain.ChannelTypeWebhook, Enabled: true}
	channelB := &domain.NotificationChannel{ID: uuid.New(), OrgID: orgB, Name: "channel-b", ChannelType: domain.ChannelTypeWebhook, Enabled: true}
	notificationRepo := &tenantIsolationNotificationRepo{
		channels: map[uuid.UUID]*domain.NotificationChannel{channelA.ID: channelA, channelB.ID: channelB},
		logs: []domain.NotificationLog{
			{ID: uuid.New(), ChannelID: channelA.ID, EventType: "org-a"},
			{ID: uuid.New(), ChannelID: channelB.ID, EventType: "org-b"},
		},
	}
	dispatcher := notifications.NewDispatcher(notificationRepo, zap.NewNop())
	lookup := &rbacMemberLookup{members: map[uuid.UUID]map[string]domain.Role{
		orgA: {alicePubkey: domain.RoleViewer},
	}}

	handler := router.NewWithDeps(registry, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		AuthMiddleware:  auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config())},
		Runs:            runs,
		Services:        services,
		Environments:    environments,
		RuntimeResolver: tenantIsolationRuntimeResolver{},
		Artifacts:       artifacts,
		SBOMs:           tenantIsolationSBOMRepo{},
		SBOMImporter:    &service.SBOMOrchestrator{},
		Blossom:         &blossom.Client{},
		Notifications:   notificationRepo,
		Dispatcher:      dispatcher,
		RBAC:            auth.NewRBAC(lookup),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return tenantIsolationFixture{
		server:       server,
		aliceKey:     aliceKey,
		orgA:         orgA,
		orgB:         orgB,
		serviceA:     serviceA.ID,
		serviceB:     serviceB.ID,
		environmentA: environmentA.ID,
		environmentB: environmentB.ID,
		runB:         runB.ID,
		artifactB:    artifactB.ID,
		channelA:     channelA.ID,
		channelB:     channelB.ID,
	}
}

func TestSensitiveRoutesRejectCrossTenantRequests(t *testing.T) {
	fixture := newTenantIsolationFixture(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		orgID  uuid.UUID
	}{
		{name: "service environment state", method: http.MethodGet, path: "/api/v1/services/" + fixture.serviceB.String() + "/environments/" + fixture.environmentB.String() + "/state"},
		{name: "environment state", method: http.MethodGet, path: "/api/v1/environments/" + fixture.environmentB.String() + "/state"},
		{name: "deployment run logs", method: http.MethodGet, path: "/api/v1/deployments/runs/" + fixture.runB.String() + "/logs"},
		{name: "live logs", method: http.MethodGet, path: "/api/v1/services/" + fixture.serviceB.String() + "/environments/" + fixture.environmentB.String() + "/logs"},
		{name: "read SBOM", method: http.MethodGet, path: "/api/v1/artifacts/" + fixture.artifactB.String() + "/sbom"},
		{name: "read SBOM packages", method: http.MethodGet, path: "/api/v1/artifacts/" + fixture.artifactB.String() + "/sbom/packages"},
		{name: "ingest SBOM", method: http.MethodPost, path: "/api/v1/artifacts/" + fixture.artifactB.String() + "/sbom", body: `{}`},
		{name: "list channels for foreign org", method: http.MethodGet, path: "/api/v1/notifications/channels", orgID: fixture.orgB},
		{name: "create channel for foreign org", method: http.MethodPost, path: "/api/v1/notifications/channels", body: `{"name":"foreign","channel_type":"webhook","config":{}}`, orgID: fixture.orgB},
		{name: "get foreign channel", method: http.MethodGet, path: "/api/v1/notifications/channels/" + fixture.channelB.String(), orgID: fixture.orgA},
		{name: "update foreign channel", method: http.MethodPut, path: "/api/v1/notifications/channels/" + fixture.channelB.String(), body: `{"name":"changed"}`, orgID: fixture.orgA},
		{name: "delete foreign channel", method: http.MethodDelete, path: "/api/v1/notifications/channels/" + fixture.channelB.String(), orgID: fixture.orgA},
		{name: "test foreign channel", method: http.MethodPost, path: "/api/v1/notifications/channels/" + fixture.channelB.String() + "/test", orgID: fixture.orgA},
		{name: "list logs for foreign org", method: http.MethodGet, path: "/api/v1/notifications/log", orgID: fixture.orgB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := fixture.server.URL + tt.path
			req, err := http.NewRequest(tt.method, url, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, fixture.aliceKey, tt.method, url))
			if tt.orgID != uuid.Nil {
				req.Header.Set("X-Bahia-Org-ID", tt.orgID.String())
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

func TestTenantListRoutesFilterGlobalStateChannelsAndLogs(t *testing.T) {
	fixture := newTenantIsolationFixture(t)

	t.Run("state", func(t *testing.T) {
		body := tenantIsolationGET(t, fixture, "/api/v1/state")
		data := body["data"].([]any)
		if len(data) != 1 || data[0].(map[string]any)["service_id"] != fixture.serviceA.String() {
			t.Fatalf("unexpected tenant state data: %#v", data)
		}
	})

	t.Run("notification channels", func(t *testing.T) {
		body := tenantIsolationGET(t, fixture, "/api/v1/notifications/channels")
		data := body["data"].([]any)
		if len(data) != 1 || data[0].(map[string]any)["id"] != fixture.channelA.String() {
			t.Fatalf("unexpected tenant channel data: %#v", data)
		}
	})

	t.Run("notification logs", func(t *testing.T) {
		body := tenantIsolationGET(t, fixture, "/api/v1/notifications/log")
		data := body["data"].([]any)
		if len(data) != 1 || data[0].(map[string]any)["channel_id"] != fixture.channelA.String() {
			t.Fatalf("unexpected tenant notification logs: %#v", data)
		}
	})
}

func tenantIsolationGET(t *testing.T, fixture tenantIsolationFixture, path string) map[string]any {
	t.Helper()
	url := fixture.server.URL + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, fixture.aliceKey, http.MethodGet, url))
	req.Header.Set("X-Bahia-Org-ID", fixture.orgA.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}
