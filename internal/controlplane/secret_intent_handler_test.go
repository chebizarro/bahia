package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// --- test doubles ---

type memSecretRepo struct {
	mu      sync.RWMutex
	secrets map[uuid.UUID]*domain.ServiceSecret
}

type rejectingSecretUpdateRepo struct{ *memSecretRepo }

func (*rejectingSecretUpdateRepo) Update(context.Context, *domain.ServiceSecret) error {
	return errors.New("simulated transaction conflict")
}

func newMemSecretRepo() *memSecretRepo {
	return &memSecretRepo{secrets: make(map[uuid.UUID]*domain.ServiceSecret)}
}

func (r *memSecretRepo) Create(_ context.Context, s *domain.ServiceSecret) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.secrets[s.ID] = &cp
	return nil
}

func (r *memSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.secrets[id]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (r *memSecretRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.ServiceSecret
	for _, s := range r.secrets {
		if s.ServiceID == serviceID {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (r *memSecretRepo) Update(_ context.Context, s *domain.ServiceSecret) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	cp.Version++ // Match PgSecretRepository's version-on-update contract.
	s.Version = cp.Version
	r.secrets[s.ID] = &cp
	return nil
}

type versionedIntentKeyer struct {
	key       nostr.SecretKey
	fail      bool
	failAfter int
	calls     int
}

func (k *versionedIntentKeyer) GetPublicKey(context.Context) (nostr.PubKey, error) {
	k.calls++
	if k.fail || k.failAfter > 0 && k.calls >= k.failAfter {
		return nostr.ZeroPK, errors.New("fenced key unavailable")
	}
	return k.key.Public(), nil
}
func (k *versionedIntentKeyer) Encrypt(_ context.Context, plaintext string, peer nostr.PubKey) (string, error) {
	conversation, err := nip44.GenerateConversationKey(peer, k.key)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plaintext, conversation)
}
func (k *versionedIntentKeyer) Decrypt(_ context.Context, ciphertext string, peer nostr.PubKey) (string, error) {
	if k.fail {
		return "", errors.New("fenced key unavailable")
	}
	conversation, err := nip44.GenerateConversationKey(peer, k.key)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(ciphertext, conversation)
}
func (*versionedIntentKeyer) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	panic("unused")
}
func (*versionedIntentKeyer) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	panic("unused")
}
func (*versionedIntentKeyer) SignEvent(context.Context, *nostr.Event) error { panic("unused") }

func (r *memSecretRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.secrets, id)
	return nil
}

// memSecretPublisher records publish calls.
type memSecretPublisher struct {
	mu         sync.Mutex
	published  []domain.SecretRef
	tombstones []uuid.UUID
}

func (p *memSecretPublisher) PublishSecretRef(_ context.Context, ref domain.SecretRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, ref)
	return nil
}

func (p *memSecretPublisher) PublishSecretDeleted(_ context.Context, id uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tombstones = append(p.tombstones, id)
	return nil
}

// testEncryptor encrypts by prefixing "enc:" to the value.
type testEncryptor struct{}

func (e *testEncryptor) Encrypt(plaintext string, _ domain.EncryptionMethod) ([]byte, error) {
	return []byte("enc:" + plaintext), nil
}

// --- fixture ---

type secretIntentFixture struct {
	processor *IntentProcessor
	handler   *SecretIntentHandler
	repo      *memSecretRepo
	publisher *memSecretPublisher
	trustSet  *TrustSet
	ownerPub  string
	logger    *zap.Logger
}

func newSecretIntentFixture(t *testing.T) *secretIntentFixture {
	t.Helper()
	logger := zap.NewNop()
	_, ownerPub := testNostrKeypair()
	orgID := testOrgID()

	repo := newMemSecretRepo()
	publisher := &memSecretPublisher{}

	trustSet := NewTrustSet(nil, logger,
		WithBootstrapOwners(map[string]string{
			orgID.String(): ownerPub,
		}),
	)

	processor := NewIntentProcessor(
		trustSet, nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"secret": true}},
		logger,
	)

	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{
		Registry:  repo,
		Encryptor: &testEncryptor{},
		Publisher: publisher,
		Logger:    logger,
	})
	processor.RegisterHandler("secret", handler)

	return &secretIntentFixture{
		processor: processor,
		handler:   handler,
		repo:      repo,
		publisher: publisher,
		trustSet:  trustSet,
		ownerPub:  ownerPub,
		logger:    logger,
	}
}

func makeTestSecretIntent(t *testing.T, op string, content map[string]interface{}, intentID, pubkeyHex string) *nostr.Event {
	t.Helper()
	contentJSON, _ := json.Marshal(content)
	coordinate := ""
	if id, ok := content["id"].(string); ok {
		coordinate = id
	}

	ev := &nostr.Event{
		Kind:      30900,
		PubKey:    testNostrPubKeyFromHex(t, pubkeyHex),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", coordinate},
			{"domain", "secret"},
			{"schema", "bahia.intent.secret.v1"},
			{"t", "bahia-intent"},
			{"t", "secret-registry"},
			{"op", op},
			{"org", testOrgID().String()},
			{"intent_id", intentID},
		},
		Content: string(contentJSON),
	}
	ev.ID = ev.GetID()
	return ev
}

// --- tests ---

func TestSecretIntentHandler_CreateViaRelayIntent(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "DB_PASSWORD",
		"service_id":      serviceID.String(),
		"encrypted_value": "nip44:encrypted:data",
	}
	ev := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("ProcessRelayIntent failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored == nil {
		t.Fatal("secret not created after relay intent")
	}
	if stored.Name != "DB_PASSWORD" {
		t.Errorf("expected name 'DB_PASSWORD', got %q", stored.Name)
	}
	if stored.ServiceID != serviceID {
		t.Errorf("expected service_id %s, got %s", serviceID, stored.ServiceID)
	}
	if string(stored.EncryptedValue) != "nip44:encrypted:data" {
		t.Errorf("expected encrypted value, got %q", string(stored.EncryptedValue))
	}
	if stored.Version != 1 {
		t.Errorf("expected version 1, got %d", stored.Version)
	}

	// Verify publish was called with SecretRef, not full secret.
	if len(f.publisher.published) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(f.publisher.published))
	}
	ref := f.publisher.published[0]
	if ref.ID != secretID {
		t.Errorf("published ref ID %s != secret ID %s", ref.ID, secretID)
	}
}

func TestSecretIntentHandler_CreateWithPlaintext_DualDispatch(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()

	intent := &Intent{
		Domain:     "secret",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   uuid.New().String(),
		Coordinate: secretID.String(),
		Actor:      f.ownerPub,
		Content: map[string]interface{}{
			"id":         secretID.String(),
			"name":       "API_KEY",
			"service_id": serviceID.String(),
			"value":      "my-secret-api-key",
		},
	}

	if err := f.processor.ProcessInProcess(ctx, intent); err != nil {
		t.Fatalf("ProcessInProcess failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored == nil {
		t.Fatal("secret not created via in-process intent")
	}

	// Plaintext should have been encrypted by testEncryptor.
	if string(stored.EncryptedValue) != "enc:my-secret-api-key" {
		t.Errorf("expected encrypted value 'enc:my-secret-api-key', got %q", string(stored.EncryptedValue))
	}
}

func TestSecretIntentHandler_Update(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()
	statuses := &statusCollector{}
	f.processor.status = NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop())

	// Create first.
	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "DB_HOST",
		"service_id":      serviceID.String(),
		"encrypted_value": "nip44:v1",
	}
	ev := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	current, _ := f.repo.GetByID(ctx, secretID)
	revision := current.UpdatedAt.Format(time.RFC3339Nano)

	// Update.
	content["encrypted_value"] = "nip44:v2"
	content["name"] = "DB_HOST_UPDATED"
	content["expected_updated_at"] = revision
	ev2 := makeTestSecretIntent(t, "update", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev2); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored == nil {
		t.Fatal("secret not found after update")
	}
	if string(stored.EncryptedValue) != "nip44:v2" {
		t.Errorf("expected encrypted value 'nip44:v2', got %q", string(stored.EncryptedValue))
	}
	if stored.Name != "DB_HOST_UPDATED" {
		t.Errorf("expected name 'DB_HOST_UPDATED', got %q", stored.Name)
	}
	if len(f.publisher.published) != 2 {
		t.Fatalf("expected 2 publish calls (create+update), got %d", len(f.publisher.published))
	}
	stale := makeTestSecretIntent(t, "update", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, stale); !IsRevisionConflict(err) {
		t.Fatalf("stale revision must conflict: %v", err)
	}
	if len(f.publisher.published) != 2 || len(statuses.events) != 3 || tagValueNostr(statuses.events[2].Tags, "status") != "conflict" {
		t.Fatal("stale secret update mutated state or missed conflict status")
	}
}

func TestSecretIntentHandler_Delete(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	// Create first.
	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "TEMP_KEY",
		"service_id":      serviceID.String(),
		"encrypted_value": "nip44:temp",
	}
	ev := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Delete.
	delContent := map[string]interface{}{
		"id": secretID.String(),
	}
	evDel := makeTestSecretIntent(t, "delete", delContent, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, evDel); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored != nil {
		t.Fatal("secret should be deleted")
	}
	if len(f.publisher.tombstones) != 1 {
		t.Fatalf("expected 1 tombstone publish, got %d", len(f.publisher.tombstones))
	}
	if f.publisher.tombstones[0] != secretID {
		t.Errorf("tombstone ID %s != secret ID %s", f.publisher.tombstones[0], secretID)
	}
}

func TestSecretIntentHandler_Unauthorized(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	// Use an untrusted key — untrusted authors are dropped silently (no error).
	_, untrustedPub := testNostrKeypair()

	secretID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "STOLEN_SECRET",
		"service_id":      domain.NewEntityID().String(),
		"encrypted_value": "nip44:stolen",
	}
	ev := makeTestSecretIntent(t, "create", content, uuid.New().String(), untrustedPub)

	// Untrusted authors are dropped silently — no error returned.
	err := f.processor.ProcessRelayIntent(ctx, ev)
	if err != nil {
		t.Fatalf("expected silent drop for untrusted author, got error: %v", err)
	}
	// Secret should not have been created.
	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored != nil {
		t.Fatal("untrusted key should not have been able to create a secret")
	}
}

func TestSecretIntentHandler_Idempotent(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "IDEMPOTENT",
		"service_id":      serviceID.String(),
		"encrypted_value": "nip44:data",
	}

	// Process the same intent content with different intent IDs.
	ev1 := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	ev2 := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev1); err != nil {
		t.Fatalf("first process failed: %v", err)
	}
	if err := f.processor.ProcessRelayIntent(ctx, ev2); err != nil {
		t.Fatalf("second process failed: %v", err)
	}

	// Level-triggered: both intents run, but only one entity exists
	// (second intent updates the existing entity).
	stored, _ := f.repo.GetByID(ctx, secretID)
	if stored == nil {
		t.Fatal("secret should exist after two intents")
	}
	if stored.Name != "IDEMPOTENT" {
		t.Errorf("expected name 'IDEMPOTENT', got %q", stored.Name)
	}
	// 2 publishes (create + update), both converge to same state.
	if len(f.publisher.published) != 2 {
		t.Errorf("expected 2 publish calls (level-triggered), got %d", len(f.publisher.published))
	}
}

func TestSecretIntentHandler_NoPlaintextInPublish(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()

	secretID := domain.NewEntityID()
	serviceID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              secretID.String(),
		"name":            "SECRET_VALUE_MUST_NOT_LEAK",
		"service_id":      serviceID.String(),
		"encrypted_value": "nip44:encrypted:data",
	}
	ev := makeTestSecretIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Verify the published ref does not contain the encrypted value.
	if len(f.publisher.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(f.publisher.published))
	}
	ref := f.publisher.published[0]

	// SecretRef should not have EncryptedValue field at all.
	refJSON, _ := json.Marshal(ref)
	refStr := string(refJSON)
	if contains(refStr, "nip44:encrypted:data") {
		t.Error("published ref contains secret value — must never appear in plaintext on relay events")
	}
	if contains(refStr, "encrypted_value") {
		t.Error("published ref contains encrypted_value field — must be stripped from relay events")
	}
}

func TestSecretIntentHandler_PermissionIsWriteSecrets(t *testing.T) {
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{Logger: zap.NewNop()})
	perm := handler.PermissionFor("create")
	if perm != domain.PermWriteSecrets {
		t.Errorf("expected PermWriteSecrets, got %v", perm)
	}
	perm2 := handler.PermissionFor("delete")
	if perm2 != domain.PermWriteSecrets {
		t.Errorf("expected PermWriteSecrets for delete, got %v", perm2)
	}
}

func TestSecretIntentVersionedWriterBindsCreateAndUpdateVersion(t *testing.T) {
	ctx := context.Background()
	key, err := secrets.NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	keyer := &versionedIntentKeyer{key: nostr.Generate()}
	repo := newMemSecretRepo()
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{
		Registry: repo, VersionedKey: key, ServiceKeyer: keyer, ServicePubkey: keyer.key.Public(),
	})
	id, serviceID := uuid.New(), uuid.New()
	create := &Intent{Content: map[string]any{"id": id.String(), "service_id": serviceID.String(), "name": "TOKEN", "value": "first"}}
	if err := handler.handleCreateOrUpdate(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, id)
	if err != nil || stored == nil || stored.EncryptionMethod != domain.EncryptionAES256V2 || stored.Version != 1 {
		t.Fatalf("versioned create state invalid: %v, %#v", err, stored)
	}
	plain, err := key.Open(id, stored.Version, stored.EncryptedValue)
	if err != nil || string(plain) != "first" {
		t.Fatalf("versioned create unreadable: %v", err)
	}
	clientCipher, err := keyer.Encrypt(ctx, "second", keyer.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	update := &Intent{Content: map[string]any{"id": id.String(), "service_id": serviceID.String(), "encrypted_value": clientCipher, "encryption_method": "nip44"}}
	if err := handler.handleCreateOrUpdate(ctx, update); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetByID(ctx, id)
	if err != nil || stored == nil || stored.EncryptionMethod != domain.EncryptionAES256V2 || stored.Version != 2 {
		t.Fatalf("versioned update state invalid: %v, %#v", err, stored)
	}
	plain, err = key.Open(id, stored.Version, stored.EncryptedValue)
	if err != nil || string(plain) != "second" {
		t.Fatalf("versioned update unreadable: %v", err)
	}
	if _, err := key.Open(id, 1, stored.EncryptedValue); err == nil {
		t.Fatal("updated ciphertext was not bound to the target version")
	}
}

func TestSecretIntentVersionedWriterNeverFallsBackWhenFenceFails(t *testing.T) {
	ctx := context.Background()
	key, err := secrets.NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	keyer := &versionedIntentKeyer{key: nostr.Generate(), fail: true}
	repo := newMemSecretRepo()
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{
		Registry: repo, Encryptor: &testEncryptor{}, VersionedKey: key,
		ServiceKeyer: keyer, ServicePubkey: keyer.key.Public(),
	})
	id := uuid.New()
	intent := &Intent{Content: map[string]any{"id": id.String(), "service_id": uuid.New().String(), "value": "private"}}
	if err := handler.handleCreateOrUpdate(ctx, intent); err == nil {
		t.Fatal("versioned writer fell back after fenced key failure")
	}
	stored, err := repo.GetByID(ctx, id)
	if err != nil || stored != nil {
		t.Fatal("failed fenced write mutated repository")
	}
	keyer.fail = false
	handler.servicePubkey = nostr.Generate().Public()
	if err := handler.handleCreateOrUpdate(ctx, intent); err == nil {
		t.Fatal("versioned writer accepted a changed service pubkey")
	}
	handler.servicePubkey = keyer.key.Public()
	keyer.calls = 0
	keyer.failAfter = 2
	if err := handler.handleCreateOrUpdate(ctx, intent); err == nil {
		t.Fatal("versioned writer accepted a lost post-seal fence")
	}
	stored, err = repo.GetByID(ctx, id)
	if err != nil || stored != nil {
		t.Fatal("lost post-seal fence mutated repository")
	}
}

func TestSecretIntentVersionedWriterRejectsAmbiguousAndUntrustedInput(t *testing.T) {
	ctx := context.Background()
	key, err := secrets.NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	keyer := &versionedIntentKeyer{key: nostr.Generate()}
	repo := newMemSecretRepo()
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{
		Registry: repo, VersionedKey: key, ServiceKeyer: keyer, ServicePubkey: keyer.key.Public(),
	})
	cipher, err := keyer.Encrypt(ctx, "secret", keyer.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []map[string]any{
		{"encrypted_value": cipher, "value": "also-plaintext"},
		{"encrypted_value": cipher, "encryption_method": "aes256gcm"},
		{"encrypted_value": "not-nip44"},
		{"value": "secret", "encryption_method": "aes256gcm-v2"},
	} {
		id := uuid.New()
		content := map[string]any{"id": id.String(), "service_id": uuid.New().String()}
		for k, v := range extra {
			content[k] = v
		}
		if err := handler.handleCreateOrUpdate(ctx, &Intent{Content: content}); err == nil {
			t.Fatal("untrusted input was accepted")
		}
		stored, err := repo.GetByID(ctx, id)
		if err != nil || stored != nil {
			t.Fatal("rejected input mutated repository")
		}
	}
}

func TestSecretIntentVersionedUpdateConflictKeepsPriorCiphertext(t *testing.T) {
	ctx := context.Background()
	key, err := secrets.NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	keyer := &versionedIntentKeyer{key: nostr.Generate()}
	id := uuid.New()
	prior, err := key.Seal(id, 1, []byte("previous"))
	if err != nil {
		t.Fatal(err)
	}
	base := newMemSecretRepo()
	if err := base.Create(ctx, &domain.ServiceSecret{ID: id, ServiceID: uuid.New(), Version: 1, EncryptedValue: prior, EncryptionMethod: domain.EncryptionAES256V2}); err != nil {
		t.Fatal(err)
	}
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{
		Registry: &rejectingSecretUpdateRepo{base}, VersionedKey: key, ServiceKeyer: keyer, ServicePubkey: keyer.key.Public(),
	})
	intent := &Intent{Content: map[string]any{"id": id.String(), "value": "replacement"}}
	if err := handler.handleCreateOrUpdate(ctx, intent); err == nil {
		t.Fatal("simulated update conflict was accepted")
	}
	stored, err := base.GetByID(ctx, id)
	if err != nil || stored == nil || stored.Version != 1 || !bytes.Equal(stored.EncryptedValue, prior) {
		t.Fatal("rejected update changed persisted secret")
	}
}
