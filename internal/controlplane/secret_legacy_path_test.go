package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type secretLegacyFixture struct {
	handlers  *EncryptedRouteHandlers
	repo      *memSecretRepo
	publisher *memSecretPublisher
	serviceID uuid.UUID
	orgID     uuid.UUID
	pubkey    string
}

func newSecretLegacyFixture(t *testing.T) *secretLegacyFixture {
	t.Helper()
	_, pubkey := testNostrKeypair()
	orgID := testOrgID()
	serviceID := uuid.New()
	repo := newMemSecretRepo()
	publisher := &memSecretPublisher{}

	// Intent processor with NO secret handler → legacy path.
	processor := NewIntentProcessor(
		NewTrustSet(nil, zap.NewNop()), nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{}},
		zap.NewNop(),
	)

	handlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		IntentProcessor: processor,
		Secrets:         &fullSecretRepo{repo},
		Encryptor:       testLegacyEncryptor(),
		SecretPublisher: publisher,
		Services:        &testSecretFixtureService{orgID: orgID},
		RBAC:            testSecretLegacyRBAC(orgID, pubkey),
		Logger:          zap.NewNop(),
	})
	return &secretLegacyFixture{
		handlers:  handlers,
		repo:      repo,
		publisher: publisher,
		serviceID: serviceID,
		orgID:     orgID,
		pubkey:    pubkey,
	}
}

func (f *secretLegacyFixture) encryptedRequest(t *testing.T, payload map[string]any) EncryptedRequest {
	t.Helper()
	raw, _ := json.Marshal(payload)
	return EncryptedRequest{
		Event: &nostr.Event{PubKey: testNostrPubKeyFromHex(t, f.pubkey)},
		Envelope: EncryptedRequestEnvelope{
			Version:   ContextVMWireVersion,
			Operation: "services.secrets.create",
			Payload:   raw,
		},
	}
}

// TestSecretLegacyCreatePublishesCanonical verifies that the legacy path
// (secret domain disabled) publishes a canonical record after the DB write.
func TestSecretLegacyCreatePublishesCanonical(t *testing.T) {
	f := newSecretLegacyFixture(t)
	ctx := context.Background()

	req := f.encryptedRequest(t, map[string]any{
		"service_id": f.serviceID.String(),
		"name":       "API_KEY",
		"value":      "secret-value-123",
	})
	result, err := f.handlers.CreateSecret(ctx, req)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	m := result.(map[string]any)
	if m["status"] != "created" {
		t.Fatalf("expected created, got %v", m["status"])
	}

	// Canonical publish happened.
	if len(f.publisher.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(f.publisher.published))
	}
	if f.publisher.published[0].Name != "API_KEY" {
		t.Fatalf("published ref name mismatch: %s", f.publisher.published[0].Name)
	}
}

// TestSecretLegacyDeletePublishesTombstone verifies that the legacy delete
// path publishes a tombstone canonical record.
func TestSecretLegacyDeletePublishesTombstone(t *testing.T) {
	f := newSecretLegacyFixture(t)
	ctx := context.Background()

	// Seed a secret.
	secretID := uuid.New()
	_ = f.repo.Create(ctx, &domain.ServiceSecret{
		ID:               secretID,
		ServiceID:        f.serviceID,
		Name:             "TO_DELETE",
		EncryptedValue:   []byte("encrypted"),
		EncryptionMethod: domain.EncryptionAES256,
		Version:          1,
	})

	req := f.encryptedRequest(t, map[string]any{
		"service_id": f.serviceID.String(),
		"secret_id":  secretID.String(),
	})
	req.Envelope.Operation = "services.secrets.delete"
	result, err := f.handlers.DeleteSecret(ctx, req)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "deleted" {
		t.Fatalf("expected deleted, got %v", m["status"])
	}

	// Tombstone published.
	if len(f.publisher.tombstones) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(f.publisher.tombstones))
	}
	if f.publisher.tombstones[0] != secretID {
		t.Fatal("tombstone ID mismatch")
	}
}
