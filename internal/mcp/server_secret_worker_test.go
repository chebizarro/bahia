package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type testSecretRepo struct {
	secrets map[uuid.UUID]*domain.ServiceSecret
}

func newTestSecretRepo() *testSecretRepo {
	return &testSecretRepo{secrets: make(map[uuid.UUID]*domain.ServiceSecret)}
}

func (r *testSecretRepo) Create(_ context.Context, s *domain.ServiceSecret) error {
	copy := *s
	r.secrets[s.ID] = &copy
	return nil
}

func (r *testSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	secret, ok := r.secrets[id]
	if !ok {
		return nil, nil
	}
	copy := *secret
	return &copy, nil
}

func (r *testSecretRepo) GetCurrentVersion(_ context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	secret, ok := r.secrets[secretID]
	if !ok {
		return nil, nil
	}
	return &domain.SecretVersion{ID: uuid.New(), SecretID: secretID, Version: secret.Version, EncryptedValue: secret.EncryptedValue, EncryptionMethod: secret.EncryptionMethod, CreatedBy: secret.CreatedBy, CreatedAt: secret.UpdatedAt}, nil
}

func (r *testSecretRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := make([]domain.ServiceSecret, 0)
	for _, secret := range r.secrets {
		if secret.ServiceID == serviceID {
			out = append(out, *secret)
		}
	}
	return out, nil
}

func (r *testSecretRepo) ListByServiceAndEnv(_ context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := make([]domain.ServiceSecret, 0)
	for _, secret := range r.secrets {
		if secret.ServiceID == serviceID && secret.EnvironmentID != nil && *secret.EnvironmentID == envID {
			out = append(out, *secret)
		}
	}
	return out, nil
}

func (r *testSecretRepo) ListEffective(_ context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := make([]domain.ServiceSecret, 0)
	for _, secret := range r.secrets {
		if secret.ServiceID != serviceID {
			continue
		}
		if secret.EnvironmentID == nil || *secret.EnvironmentID == envID {
			out = append(out, *secret)
		}
	}
	return out, nil
}

func (r *testSecretRepo) Update(_ context.Context, s *domain.ServiceSecret) error {
	current, ok := r.secrets[s.ID]
	if !ok {
		return repository.ErrNotFound
	}
	if s.Version != current.Version {
		return repository.ErrConflict
	}
	copy := *s
	copy.Version++ // PgSecretRepository increments only after matching current version.
	s.Version = copy.Version
	r.secrets[s.ID] = &copy
	return nil
}

func (r *testSecretRepo) RecordSecretAccessAudit(context.Context, *domain.SecretAccessAudit) error {
	return nil
}

func (r *testSecretRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.secrets, id)
	return nil
}

func (r *testSecretRepo) DeleteByName(_ context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error {
	for id, secret := range r.secrets {
		if secret.ServiceID != serviceID || secret.Name != name {
			continue
		}
		if (envID == nil && secret.EnvironmentID == nil) || (envID != nil && secret.EnvironmentID != nil && *envID == *secret.EnvironmentID) {
			delete(r.secrets, id)
			return nil
		}
	}
	return nil
}

func TestGetTools_IncludesSecretCRUD(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{
		"bahia_list_secrets":  false,
		"bahia_create_secret": false,
		"bahia_update_secret": false,
		"bahia_delete_secret": false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
			if tool.InputSchema["required"] == nil {
				t.Fatalf("%s missing required schema", tool.Name)
			}
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

// Secret tools only publish reference intents: without an intent processor
// they fail closed, and a value argument is refused before any intent is
// built.
func TestCallTool_SecretToolsAreReferenceOnlyIntents(t *testing.T) {
	ctx := authorizedMCPContext()
	unconfigured := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	for _, tool := range []string{"bahia_create_secret", "bahia_update_secret", "bahia_delete_secret"} {
		result, err := unconfigured.CallTool(ctx, tool, map[string]interface{}{"secret_id": uuid.New().String()})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if !result.IsError || !strings.Contains(result.Content[0].Text, "intent processor is not configured") {
			t.Fatalf("%s without an intent processor = %#v", tool, result)
		}
	}
	for _, tool := range []string{"bahia_create_secret", "bahia_update_secret"} {
		for _, schema := range unconfigured.GetTools() {
			if schema.Name == tool {
				if _, ok := schema.InputSchema["properties"].(map[string]interface{})["value"]; ok {
					t.Fatalf("%s still accepts a value", tool)
				}
			}
		}
		_, err := unconfigured.intentWriteForTool(ctx, tool, map[string]interface{}{
			"service_id": uuid.New().String(), "secret_id": uuid.New().String(), "name": "TOKEN", "value": "hunter2",
		}, uuid.NewString())
		if err == nil || !strings.Contains(err.Error(), "never sent to Bahia") || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("%s with a value = %v", tool, err)
		}
	}
}

func newTestMCPWorkerServer() (*Server, *testPaymentWorkerRepo) {
	repo := newTestPaymentWorkerRepo()
	server := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{Workers: repo})
	return server, repo
}

func TestGetTools_IncludesWorkerQueriesAndPricing(t *testing.T) {
	server, _ := newTestMCPWorkerServer()
	required := map[string]bool{
		"bahia_list_workers":       false,
		"bahia_get_worker":         false,
		"bahia_get_worker_pricing": false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_WorkerQueriesAndPricing(t *testing.T) {
	ctx := authorizedMCPContext()
	server, repo := newTestMCPWorkerServer()
	now := time.Now().UTC()

	repo.workers["worker-online"] = &domain.Worker{
		PubKey:              "worker-online",
		Name:                "online-builder",
		Description:         "fast linux worker",
		Architecture:        "linux/amd64",
		MaxConcurrentJobs:   4,
		CurrentQueueDepth:   1,
		Software:            []domain.WorkerSoftware{{Name: "docker", Version: "26.1.0"}, {Name: "go", Version: "1.22"}},
		Pricing:             []domain.WorkerPricing{{MintURL: "https://mint.example", PricePerSecond: 7, Unit: "sat"}},
		MinDurationSecs:     15,
		MaxDurationSecs:     600,
		Geohash:             "9q8yy",
		PreferredRelays:     []string{"wss://relay.example"},
		LastAdvertisementAt: now,
		Status:              domain.WorkerStatusOnline,
		CreatedAt:           now.Add(-time.Hour),
		UpdatedAt:           now,
	}
	repo.workers["worker-offline"] = &domain.Worker{
		PubKey:              "worker-offline",
		Name:                "offline-builder",
		Software:            []domain.WorkerSoftware{{Name: "docker", Version: "25.0.0"}},
		LastAdvertisementAt: now.Add(-time.Hour),
		Status:              domain.WorkerStatusOffline,
		CreatedAt:           now.Add(-2 * time.Hour),
		UpdatedAt:           now.Add(-time.Hour),
	}
	fixture := attachCanonicalMCPFixture(t, server)
	fixture.publishWorker(t, repo.workers["worker-online"])
	fixture.publishWorker(t, repo.workers["worker-offline"])

	listRes, err := server.CallTool(ctx, "bahia_list_workers", map[string]interface{}{
		"capability": "docker",
		"available":  true,
		"limit":      float64(10),
	})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected one available docker worker, got %#v", listPayload)
	}
	workers := listPayload["workers"].([]interface{})
	workerMeta := workers[0].(map[string]interface{})
	if workerMeta["pubkey"] != "worker-online" || workerMeta["architecture"] != "linux/amd64" {
		t.Fatalf("unexpected worker metadata: %#v", workerMeta)
	}

	getRes, err := server.CallTool(ctx, "bahia_get_worker", map[string]interface{}{"pubkey": "worker-online"})
	if err != nil {
		t.Fatalf("get err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["description"] != "fast linux worker" || int(getPayload["max_duration_secs"].(float64)) != 600 {
		t.Fatalf("unexpected get payload: %#v", getPayload)
	}

	pricingRes, err := server.CallTool(ctx, "bahia_get_worker_pricing", map[string]interface{}{"pubkey": "worker-online"})
	if err != nil {
		t.Fatalf("pricing err: %v", err)
	}
	if pricingRes.IsError {
		t.Fatalf("pricing returned error: %s", pricingRes.Content[0].Text)
	}
	pricingPayload := decodeResultMap(t, pricingRes)
	prices := pricingPayload["pricing"].([]interface{})
	price := prices[0].(map[string]interface{})
	if int(price["price_per_second"].(float64)) != 7 || price["mint_url"] != "https://mint.example" {
		t.Fatalf("unexpected pricing payload: %#v", pricingPayload)
	}
}

func TestCallTool_WorkerValidationAndConfiguration(t *testing.T) {
	ctx := authorizedMCPContext()
	server, _ := newTestMCPWorkerServer()

	result, err := server.CallTool(ctx, "bahia_get_worker", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "pubkey is required") {
		t.Fatalf("expected pubkey validation error, got %#v", result)
	}

	result, err = server.CallTool(ctx, "bahia_get_worker_pricing", map[string]interface{}{"pubkey": "missing"})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "worker not found") {
		t.Fatalf("expected not found error, got %#v", result)
	}

	unconfigured := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err = unconfigured.CallTool(ctx, "bahia_list_workers", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if result.IsError || int(decodeResultMap(t, result)["total"].(float64)) != 0 {
		t.Fatalf("expected empty store result, got %#v", result)
	}
}
