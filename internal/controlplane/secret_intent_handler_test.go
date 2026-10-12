package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
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

func (r *memSecretRepo) MarkStoreBacked(_ context.Context, ref domain.SecretRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.secrets[ref.ID]; ok && existing.ServiceID != ref.ServiceID {
		return errors.New("index row belongs to another service")
	}
	for id, s := range r.secrets {
		sameEnv := (s.EnvironmentID == nil && ref.EnvironmentID == nil) || (s.EnvironmentID != nil && ref.EnvironmentID != nil && *s.EnvironmentID == *ref.EnvironmentID)
		if id != ref.ID && s.ServiceID == ref.ServiceID && sameEnv && s.Name == ref.Name {
			return fmt.Errorf("marking %s: %w: [%s]", ref.ID, repository.ErrSecretNameTaken, id)
		}
	}
	r.secrets[ref.ID] = &domain.ServiceSecret{ID: ref.ID, ServiceID: ref.ServiceID, EnvironmentID: ref.EnvironmentID, Name: ref.Name,
		EncryptionMethod: domain.EncryptionStoreRef, Version: ref.Version, CreatedBy: ref.CreatedBy}
	return nil
}

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

// Secret intents never carry a value: encrypted_value (NIP-44 to the service
// key) and value (in-process) are refused whether or not a store is
// configured, and nothing is written or published.
func TestSecretIntentHandler_RefusesValues(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()
	for name, extra := range map[string]map[string]interface{}{
		"encrypted_value": {"encrypted_value": "nip44:ciphertext"},
		"value":           {"value": "hunter2"},
	} {
		for _, op := range []string{"create", "update"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				secretID := domain.NewEntityID()
				content := map[string]interface{}{"id": secretID.String(), "name": "API_KEY", "service_id": domain.NewEntityID().String()}
				for k, v := range extra {
					content[k] = v
				}
				err := f.processor.ProcessRelayIntent(ctx, makeTestSecretIntent(t, op, content, uuid.New().String(), f.ownerPub))
				if err == nil || !strings.Contains(err.Error(), "never carry a value") {
					t.Fatalf("err = %v, want the value refused", err)
				}
				if stored, _ := f.repo.GetByID(ctx, secretID); stored != nil {
					t.Fatal("a refused intent wrote PostgreSQL")
				}
			})
		}
	}
	if len(f.publisher.published) != 0 {
		t.Fatalf("refused intents published %d records", len(f.publisher.published))
	}
}

func TestSecretIntentHandler_Delete(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()
	secretID := domain.NewEntityID()
	if err := f.repo.Create(ctx, &domain.ServiceSecret{ID: secretID, ServiceID: domain.NewEntityID(), Name: "TEMP_KEY", EncryptionMethod: domain.EncryptionStoreRef}); err != nil {
		t.Fatal(err)
	}
	evDel := makeTestSecretIntent(t, "delete", map[string]interface{}{"id": secretID.String()}, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, evDel); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if stored, _ := f.repo.GetByID(ctx, secretID); stored != nil {
		t.Fatal("secret should be deleted")
	}
	if len(f.publisher.tombstones) != 1 || f.publisher.tombstones[0] != secretID {
		t.Fatalf("tombstones = %v, want %s", f.publisher.tombstones, secretID)
	}
}

func TestSecretIntentHandler_Unauthorized(t *testing.T) {
	f := newSecretIntentFixture(t)
	ctx := context.Background()
	_, untrustedPub := testNostrKeypair()
	secretID := domain.NewEntityID()
	content := map[string]interface{}{
		"id": secretID.String(), "name": "STOLEN_SECRET", "service_id": domain.NewEntityID().String(),
		"credential_ref": map[string]interface{}{"store": "signet", "credential_id": "cred_" + strings.Repeat("0", 64), "version": 1},
	}
	// Untrusted authors are dropped silently — no error returned.
	if err := f.processor.ProcessRelayIntent(ctx, makeTestSecretIntent(t, "create", content, uuid.New().String(), untrustedPub)); err != nil {
		t.Fatalf("expected silent drop for untrusted author, got error: %v", err)
	}
	if stored, _ := f.repo.GetByID(ctx, secretID); stored != nil || len(f.publisher.published) != 0 {
		t.Fatal("untrusted key should not have been able to create a secret")
	}
}

func TestSecretIntentHandler_PermissionIsWriteSecrets(t *testing.T) {
	handler := NewSecretIntentHandler(SecretIntentHandlerConfig{Logger: zap.NewNop()})
	if perm := handler.PermissionFor("create"); perm != domain.PermWriteSecrets {
		t.Errorf("expected PermWriteSecrets, got %v", perm)
	}
	if perm := handler.PermissionFor("delete"); perm != domain.PermWriteSecrets {
		t.Errorf("expected PermWriteSecrets for delete, got %v", perm)
	}
}
