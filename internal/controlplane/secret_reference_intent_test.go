package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// memCanonicalSecrets is the daemon's canonical secret state as the local
// event store holds it: a published record is what CurrentSecretRef returns.
type memCanonicalSecrets struct {
	mu         sync.Mutex
	records    map[uuid.UUID]domain.SecretRef
	serviceOrg map[uuid.UUID]string
	publishErr error
	tombstones []uuid.UUID
}

func (c *memCanonicalSecrets) CurrentSecretRef(_ context.Context, id uuid.UUID) (*domain.SecretRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref, ok := c.records[id]
	if !ok {
		return nil, nil
	}
	return &ref, nil
}

func (c *memCanonicalSecrets) ServiceOrgID(_ context.Context, serviceID uuid.UUID) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	org, ok := c.serviceOrg[serviceID]
	if !ok {
		return "", errors.New("service has no canonical record")
	}
	return org, nil
}

func (c *memCanonicalSecrets) PublishSecretRef(_ context.Context, ref domain.SecretRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.publishErr != nil {
		return c.publishErr
	}
	c.records[ref.ID] = ref
	return nil
}

func (c *memCanonicalSecrets) PublishSecretDeleted(_ context.Context, id uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.publishErr != nil {
		return c.publishErr
	}
	delete(c.records, id)
	c.tombstones = append(c.tombstones, id)
	return nil
}

// memSecretStore stands in for the external store behind the SecretStore
// seam; the Signet implementation has its own tests against a Signet fake.
type memSecretStore struct {
	mu        sync.Mutex
	creds     map[string]secretstore.Metadata
	err       error
	inspected []string
}

func (s *memSecretStore) Inspect(_ context.Context, id string) (secretstore.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inspected = append(s.inspected, id)
	if s.err != nil {
		return secretstore.Metadata{}, s.err
	}
	meta, ok := s.creds[id]
	if !ok {
		return secretstore.Metadata{}, secretstore.ErrNotFound
	}
	return meta, nil
}

func (s *memSecretStore) Resolve(context.Context, domain.SecretCredentialRef) (*secretstore.Value, error) {
	return nil, errors.New("the intent handler never resolves values")
}

func (s *memSecretStore) Health(context.Context) error { return s.err }

type referenceFixture struct {
	processor *IntentProcessor
	canonical *memCanonicalSecrets
	store     *memSecretStore
	repo      *memSecretRepo
	ownerPub  string
	serviceID uuid.UUID
	credID    string
}

// newReferenceFixture wires the handler with a secret store and no
// PostgreSQL registry unless withRegistry is set.
func newReferenceFixture(t *testing.T, withRegistry bool) *referenceFixture {
	t.Helper()
	_, ownerPub := testNostrKeypair()
	serviceID := domain.NewEntityID()
	credID := "cred_" + strings.Repeat("ab", 32)
	f := &referenceFixture{
		canonical: &memCanonicalSecrets{records: map[uuid.UUID]domain.SecretRef{}, serviceOrg: map[uuid.UUID]string{serviceID: testOrgID().String()}},
		store:     &memSecretStore{creds: map[string]secretstore.Metadata{credID: {CredentialID: credID, Owner: "bahia-vault", Version: 2, Status: secretstore.StatusActive}}},
		ownerPub:  ownerPub,
		serviceID: serviceID,
		credID:    credID,
	}
	trustSet := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): ownerPub}))
	f.processor = NewIntentProcessor(trustSet, nil, nil, IntentProcessorConfig{EnabledDomains: map[string]bool{"secret": true}}, zap.NewNop())
	cfg := SecretIntentHandlerConfig{Store: f.store, Records: f.canonical, Publisher: f.canonical, Logger: zap.NewNop()}
	if withRegistry {
		f.repo = newMemSecretRepo()
		cfg.Registry = f.repo
		cfg.Encryptor = &testEncryptor{}
	}
	f.processor.RegisterHandler("secret", NewSecretIntentHandler(cfg))
	return f
}

func (f *referenceFixture) content(secretID uuid.UUID, version int) map[string]interface{} {
	return map[string]interface{}{
		"id": secretID.String(), "service_id": f.serviceID.String(), "name": "DB_PASSWORD",
		"credential_ref": map[string]interface{}{"store": "signet", "credential_id": f.credID, "version": version},
	}
}

func (f *referenceFixture) process(t *testing.T, op string, content map[string]interface{}) error {
	t.Helper()
	return f.processor.ProcessRelayIntent(context.Background(), makeTestSecretIntent(t, op, content, uuid.NewString(), f.ownerPub))
}

func TestSecretReferenceIntentPublishesReferenceWithoutPostgres(t *testing.T) {
	f := newReferenceFixture(t, false)
	secretID := domain.NewEntityID()
	if err := f.process(t, "create", f.content(secretID, 2)); err != nil {
		t.Fatalf("create: %v", err)
	}
	ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID)
	if ref == nil || ref.CredentialRef == nil {
		t.Fatalf("canonical record = %+v", ref)
	}
	want := domain.SecretCredentialRef{Store: "signet", CredentialID: f.credID, Version: 2}
	if *ref.CredentialRef != want || ref.Version != 1 || ref.Name != "DB_PASSWORD" || ref.ServiceID != f.serviceID || ref.EncryptionMethod != "" || ref.CreatedBy != f.ownerPub {
		t.Fatalf("canonical record = %+v (credential %+v)", ref, *ref.CredentialRef)
	}

	// Rotation at the source: the operator re-points the secret to the new
	// version, guarded by the record's revision.
	f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Version: 3, Status: secretstore.StatusActive}
	update := f.content(secretID, 3)
	update["expected_updated_at"] = ref.UpdatedAt.Format(time.RFC3339Nano)
	if err := f.process(t, "update", update); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, _ := f.canonical.CurrentSecretRef(context.Background(), secretID)
	if updated.CredentialRef.Version != 3 || updated.Version != 2 || !updated.CreatedAt.Equal(ref.CreatedAt) {
		t.Fatalf("updated record = %+v", updated)
	}

	if err := f.process(t, "delete", map[string]interface{}{"id": secretID.String(), "deleted": true}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gone, _ := f.canonical.CurrentSecretRef(context.Background(), secretID); gone != nil || len(f.canonical.tombstones) != 1 {
		t.Fatalf("after delete: record %+v tombstones %v", gone, f.canonical.tombstones)
	}
}

func TestSecretReferenceIntentRejections(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*referenceFixture)
		mutate func(*referenceFixture, map[string]interface{})
		want   string
	}{
		{"value with the store configured", nil, func(f *referenceFixture, c map[string]interface{}) {
			delete(c, "credential_ref")
			c["encrypted_value"] = "nip44-ciphertext"
		}, "not accepted in intents while the secret store is configured"},
		{"plaintext with the store configured", nil, func(f *referenceFixture, c map[string]interface{}) {
			delete(c, "credential_ref")
			c["value"] = "hunter2"
		}, "not accepted in intents while the secret store is configured"},
		{"reference and value", nil, func(f *referenceFixture, c map[string]interface{}) { c["encrypted_value"] = "x" }, "either credential_ref or a value"},
		{"unknown backend", nil, func(f *referenceFixture, c map[string]interface{}) {
			c["credential_ref"] = map[string]interface{}{"store": "vault", "credential_id": f.credID, "version": 2}
		}, "invalid secret store reference"},
		{"unknown reference field", nil, func(f *referenceFixture, c map[string]interface{}) {
			c["credential_ref"] = map[string]interface{}{"store": "signet", "credential_id": f.credID, "version": 2, "value": "x"}
		}, "credential_ref must be"},
		{"stale version", nil, func(f *referenceFixture, c map[string]interface{}) {
			c["credential_ref"].(map[string]interface{})["version"] = 1
		}, "active version is 2"},
		{"revoked", func(f *referenceFixture) {
			f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Version: 2, Status: secretstore.StatusRevoked}
		}, nil, "revoked"},
		{"unknown credential", func(f *referenceFixture) { delete(f.store.creds, f.credID) }, nil, "not found"},
		{"store unavailable", func(f *referenceFixture) { f.store.err = secretstore.ErrUnavailable }, nil, "unavailable"},
		{"service of another org", func(f *referenceFixture) { f.canonical.serviceOrg[f.serviceID] = uuid.NewString() }, nil, "does not belong to org"},
		{"canonical publish fails", func(f *referenceFixture) { f.canonical.publishErr = errors.New("outbox full") }, nil, "outbox full"},
		{"new secret without service", nil, func(f *referenceFixture, c map[string]interface{}) { delete(c, "service_id") }, "needs service_id and name"},
		{"create with a revision", nil, func(f *referenceFixture, c map[string]interface{}) {
			c["expected_updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		}, "conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReferenceFixture(t, false)
			if tc.setup != nil {
				tc.setup(f)
			}
			secretID := domain.NewEntityID()
			content := f.content(secretID, 2)
			if tc.mutate != nil {
				tc.mutate(f, content)
			}
			err := f.process(t, "create", content)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID); ref != nil {
				t.Fatalf("a rejected intent published %+v", ref)
			}
		})
	}
}

func TestSecretReferenceIntentServiceCannotMove(t *testing.T) {
	f := newReferenceFixture(t, false)
	secretID := domain.NewEntityID()
	if err := f.process(t, "create", f.content(secretID, 2)); err != nil {
		t.Fatal(err)
	}
	moved := f.content(secretID, 2)
	other := domain.NewEntityID()
	f.canonical.serviceOrg[other] = testOrgID().String()
	moved["service_id"] = other.String()
	if err := f.process(t, "update", moved); err == nil || !strings.Contains(err.Error(), "service_id cannot change") {
		t.Fatalf("moving a secret to another service = %v", err)
	}
}

func TestSecretReferenceIntentRetiresLegacySealedValue(t *testing.T) {
	f := newReferenceFixture(t, true)
	secretID := domain.NewEntityID()
	// A legacy secret: canonical metadata plus a sealed value in PostgreSQL.
	f.canonical.records[secretID] = domain.SecretRef{ID: secretID, ServiceID: f.serviceID, Name: "DB_PASSWORD", EncryptionMethod: domain.EncryptionNIP44, Version: 4, CreatedAt: time.Now().UTC()}
	_ = f.repo.Create(context.Background(), &domain.ServiceSecret{ID: secretID, ServiceID: f.serviceID, Name: "DB_PASSWORD", EncryptedValue: []byte("sealed"), Version: 4})

	if err := f.process(t, "update", f.content(secretID, 2)); err != nil {
		t.Fatalf("re-point to the store: %v", err)
	}
	ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID)
	if ref.CredentialRef == nil || ref.EncryptionMethod != "" || ref.Version != 5 {
		t.Fatalf("record after re-point = %+v", ref)
	}
	if row, _ := f.repo.GetByID(context.Background(), secretID); row != nil {
		t.Fatal("the legacy sealed value must not stay readable after the secret moved to the store")
	}
}

func TestSecretValueIntentWithoutStoreOrPostgresIsRefused(t *testing.T) {
	_, ownerPub := testNostrKeypair()
	trustSet := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): ownerPub}))
	processor := NewIntentProcessor(trustSet, nil, nil, IntentProcessorConfig{EnabledDomains: map[string]bool{"secret": true}}, zap.NewNop())
	canonical := &memCanonicalSecrets{records: map[uuid.UUID]domain.SecretRef{}, serviceOrg: map[uuid.UUID]string{}}
	processor.RegisterHandler("secret", NewSecretIntentHandler(SecretIntentHandlerConfig{Records: canonical, Publisher: canonical, Logger: zap.NewNop()}))
	secretID := domain.NewEntityID()
	content := map[string]interface{}{"id": secretID.String(), "service_id": domain.NewEntityID().String(), "name": "X",
		"credential_ref": map[string]interface{}{"store": "signet", "credential_id": "cred_" + strings.Repeat("0", 64), "version": 1}}
	err := processor.ProcessRelayIntent(context.Background(), makeTestSecretIntent(t, "create", content, uuid.NewString(), ownerPub))
	if !errors.Is(err, secretstore.ErrNotConfigured) {
		t.Fatalf("reference intent without a store = %v, want ErrNotConfigured", err)
	}
	delete(content, "credential_ref")
	content["encrypted_value"] = "x"
	err = processor.ProcessRelayIntent(context.Background(), makeTestSecretIntent(t, "create", content, uuid.NewString(), ownerPub))
	if err == nil || !strings.Contains(err.Error(), "need the PostgreSQL secret store") {
		t.Fatalf("value intent without PostgreSQL = %v", err)
	}
}

// What MCP builds for a reference is exactly what the handler accepts.
func TestBuildSecretReferenceIntentContentIsAccepted(t *testing.T) {
	f := newReferenceFixture(t, false)
	secretID := domain.NewEntityID()
	ref := domain.SecretCredentialRef{Store: "signet", CredentialID: f.credID, Version: 2}
	content := BuildSecretReferenceIntentContent(f.serviceID, secretID, "API_TOKEN", nil, ref)
	if _, has := content["value"]; has {
		t.Fatal("reference content carries a value")
	}
	if err := f.process(t, "create", content); err != nil {
		t.Fatalf("handler refused the built reference intent: %v", err)
	}
	got, _ := f.canonical.CurrentSecretRef(context.Background(), secretID)
	if got == nil || *got.CredentialRef != ref || got.Name != "API_TOKEN" {
		t.Fatalf("record = %+v", got)
	}
}
