package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
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
	copy := *s
	r.secrets[s.ID] = &copy
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

func newTestMCPSecretServer(t *testing.T) (*Server, *testSecretRepo, *secrets.Encryptor) {
	t.Helper()
	repo := newTestSecretRepo()
	encryptor, err := secrets.NewEncryptor("mcp-secret-test-key")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		SecretsRepo: repo,
		Encryptor:   encryptor,
	})
	return server, repo, encryptor
}

func TestGetTools_IncludesSecretCRUD(t *testing.T) {
	server, _, _ := newTestMCPSecretServer(t)
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

func TestCallTool_SecretCRUD(t *testing.T) {
	ctx := context.Background()
	server, repo, encryptor := newTestMCPSecretServer(t)
	serviceID := uuid.New()
	envID := uuid.New()

	createRes, err := server.CallTool(ctx, "bahia_create_secret", map[string]interface{}{
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"name":           "DATABASE_URL",
		"value":          "postgres://user:pass@example/db",
	})
	if err != nil {
		t.Fatalf("create err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	secretID := uuid.MustParse(createPayload["secret_id"].(string))
	if strings.Contains(createRes.Content[0].Text, "postgres://user:pass") {
		t.Fatalf("create response leaked plaintext secret: %s", createRes.Content[0].Text)
	}
	stored := repo.secrets[secretID]
	if stored == nil {
		t.Fatalf("secret was not persisted")
	}
	plaintext, err := encryptor.Decrypt(stored.EncryptedValue, stored.EncryptionMethod)
	if err != nil {
		t.Fatalf("decrypt created secret: %v", err)
	}
	if plaintext != "postgres://user:pass@example/db" || stored.Version != 1 || stored.CreatedBy != "mcp-agent" {
		t.Fatalf("unexpected stored secret metadata or decrypted value mismatch: version=%d created_by=%q", stored.Version, stored.CreatedBy)
	}

	listRes, err := server.CallTool(ctx, "bahia_list_secrets", map[string]interface{}{"service_id": serviceID.String()})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected one secret, got %#v", listPayload)
	}
	secretsList := listPayload["secrets"].([]interface{})
	secretMeta := secretsList[0].(map[string]interface{})
	if _, ok := secretMeta["encrypted_value"]; ok {
		t.Fatalf("list response leaked encrypted_value: %#v", secretMeta)
	}
	if secretMeta["environment_id"] != envID.String() || secretMeta["name"] != "DATABASE_URL" {
		t.Fatalf("unexpected secret metadata: %#v", secretMeta)
	}

	updateRes, err := server.CallTool(ctx, "bahia_update_secret", map[string]interface{}{
		"secret_id": secretID.String(),
		"value":     "postgres://user:new-pass@example/db",
	})
	if err != nil {
		t.Fatalf("update err: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("update returned error: %s", updateRes.Content[0].Text)
	}
	updatePayload := decodeResultMap(t, updateRes)
	if int(updatePayload["version"].(float64)) != 2 {
		t.Fatalf("expected version 2, got %#v", updatePayload)
	}
	updated := repo.secrets[secretID]
	plaintext, err = encryptor.Decrypt(updated.EncryptedValue, updated.EncryptionMethod)
	if err != nil {
		t.Fatalf("decrypt updated secret: %v", err)
	}
	if plaintext != "postgres://user:new-pass@example/db" {
		t.Fatalf("secret decrypted value was not updated")
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_secret", map[string]interface{}{"secret_id": secretID.String()})
	if err != nil {
		t.Fatalf("delete err: %v", err)
	}
	if deleteRes.IsError {
		t.Fatalf("delete returned error: %s", deleteRes.Content[0].Text)
	}
	if _, ok := repo.secrets[secretID]; ok {
		t.Fatalf("secret was not deleted")
	}
}

func TestCallTool_SecretValidationAndConfiguration(t *testing.T) {
	ctx := context.Background()
	configured, _, _ := newTestMCPSecretServer(t)

	result, err := configured.CallTool(ctx, "bahia_create_secret", map[string]interface{}{
		"service_id": uuid.New().String(),
		"value":      "missing-name",
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "name is required") {
		t.Fatalf("expected name validation error, got %#v", result)
	}

	result, err = configured.CallTool(ctx, "bahia_update_secret", map[string]interface{}{
		"secret_id": "not-a-uuid",
		"value":     "new-value",
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "invalid secret_id") {
		t.Fatalf("expected invalid secret_id error, got %#v", result)
	}

	unconfigured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err = unconfigured.CallTool(ctx, "bahia_list_secrets", map[string]interface{}{"service_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not configured") {
		t.Fatalf("expected configuration error, got %#v", result)
	}
}

func newTestMCPWorkerServer() (*Server, *testPaymentWorkerRepo) {
	repo := newTestPaymentWorkerRepo()
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{Workers: repo})
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
	ctx := context.Background()
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
	ctx := context.Background()
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

	unconfigured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err = unconfigured.CallTool(ctx, "bahia_list_workers", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not configured") {
		t.Fatalf("expected configuration error, got %#v", result)
	}
}
