package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

// Inspect enforces the scope on the label like the Signet store does.
func (s *memSecretStore) Inspect(_ context.Context, id string, scope secretstore.Scope) (secretstore.Metadata, error) {
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
	if !scope.Admits(meta.Label) {
		return secretstore.Metadata{}, secretstore.ErrDenied
	}
	return meta, nil
}

func (s *memSecretStore) Resolve(context.Context, domain.SecretCredentialRef, secretstore.Scope) (*secretstore.Value, error) {
	return nil, errors.New("the intent handler never resolves values")
}

func (s *memSecretStore) ResolveAll(context.Context, []secretstore.Request) ([]*secretstore.Value, error) {
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
		store:     &memSecretStore{creds: map[string]secretstore.Metadata{credID: {CredentialID: credID, Owner: "bahia-vault", Label: testOrgID().String() + "/" + serviceID.String() + "/DB_PASSWORD", Version: 2, Status: secretstore.StatusActive}}},
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
		cfg.StoreIndex = f.repo
	}
	f.processor.RegisterHandler("secret", NewSecretIntentHandler(cfg))
	return f
}

func (f *referenceFixture) label() string {
	return testOrgID().String() + "/" + f.serviceID.String() + "/DB_PASSWORD"
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

func TestSecretIntentRejectionPreservesPrivateDiagnosticOnlyInDaemonLog(t *testing.T) {
	f := newReferenceFixture(t, false)
	secretID := domain.NewEntityID()
	f.store.err = fmt.Errorf("%w: signet transport detail for service %s", secretstore.ErrUnavailable, f.serviceID)
	logs, observed := observer.New(zap.WarnLevel)
	f.processor.logger = zap.New(logs)
	published := &statusCollector{}
	f.processor.status = NewIntentStatusPublisher(published.publish, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	request := makeTestSecretIntent(t, "create", f.content(secretID, 2), "request-correlation", f.ownerPub)
	err := f.processor.ProcessRelayIntent(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "signet transport detail") {
		t.Fatalf("handler error = %v, want private diagnostic", err)
	}
	if observed.FilterMessage("intent handler failed").Len() != 1 {
		t.Fatalf("private diagnostic was not logged for operators: %+v", observed.All())
	}
	if !strings.Contains(fmt.Sprint(observed.All()[0].ContextMap()["error"]), "signet transport detail") {
		t.Fatal("operator diagnostic was lost")
	}
	if len(published.events) != 1 {
		t.Fatalf("status count = %d, want 1", len(published.events))
	}
	status := published.events[0]
	if !status.CheckID() || !status.VerifySignature() {
		t.Fatal("intent status is not signed")
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{f.serviceID.String(), secretID.String(), "signet transport detail"} {
		if strings.Contains(string(wire), private) {
			t.Fatalf("public status exposes %q: %s", private, wire)
		}
	}
	if extractTag(status, "intent_id") != "request-correlation" || extractTag(status, "e") != request.ID.Hex() {
		t.Fatalf("request correlation lost: %+v", status.Tags)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(status.Content), &content); err != nil {
		t.Fatal(err)
	}
	if content["reason"] != "store_unavailable" {
		t.Fatalf("public reason = %v, want store_unavailable", content["reason"])
	}
}

func TestSecretIntentRevisionConflictUsesNonIdentifyingReason(t *testing.T) {
	f := newReferenceFixture(t, false)
	secretID := domain.NewEntityID()
	published := &statusCollector{}
	f.processor.status = NewIntentStatusPublisher(published.publish, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	content := f.content(secretID, 2)
	content["expected_updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	request := makeTestSecretIntent(t, "create", content, "conflict-correlation", f.ownerPub)
	if err := f.processor.ProcessRelayIntent(t.Context(), request); !IsRevisionConflict(err) {
		t.Fatalf("rejection = %v, want revision conflict", err)
	}
	if len(published.events) != 1 {
		t.Fatalf("status count = %d, want 1", len(published.events))
	}
	status := published.events[0]
	if !status.CheckID() || !status.VerifySignature() || strings.Contains(status.Content, secretID.String()) || extractTag(status, "d") == secretID.String() {
		t.Fatalf("unsafe conflict status: %+v", status)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(status.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reason"] != "revision_conflict" || extractTag(status, "e") != request.ID.Hex() {
		t.Fatalf("conflict reason or correlation lost: %+v %+v", payload, status.Tags)
	}
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
	f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Label: f.label(), Version: 3, Status: secretstore.StatusActive}
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
		{"encrypted value", nil, func(f *referenceFixture, c map[string]interface{}) {
			delete(c, "credential_ref")
			c["encrypted_value"] = "nip44-ciphertext"
		}, "never carry a value"},
		{"plaintext value", nil, func(f *referenceFixture, c map[string]interface{}) {
			delete(c, "credential_ref")
			c["value"] = "hunter2"
		}, "never carry a value"},
		{"reference and value", nil, func(f *referenceFixture, c map[string]interface{}) { c["encrypted_value"] = "x" }, "never carry a value"},
		{"no reference", nil, func(f *referenceFixture, c map[string]interface{}) { delete(c, "credential_ref") }, "needs credential_ref"},
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
			f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Label: f.label(), Version: 2, Status: secretstore.StatusRevoked}
		}, nil, "revoked"},
		{"unknown credential", func(f *referenceFixture) { delete(f.store.creds, f.credID) }, nil, "not available to this org and service"},
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
	f.canonical.records[secretID] = domain.SecretRef{ID: secretID, ServiceID: f.serviceID, Name: "DB_PASSWORD", EncryptionMethod: domain.EncryptionMethod("nip44"), Version: 4, CreatedAt: time.Now().UTC()}
	_ = f.repo.Create(context.Background(), &domain.ServiceSecret{ID: secretID, ServiceID: f.serviceID, Name: "DB_PASSWORD", EncryptedValue: []byte("sealed"), Version: 4})

	if err := f.process(t, "update", f.content(secretID, 2)); err != nil {
		t.Fatalf("re-point to the store: %v", err)
	}
	ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID)
	if ref.CredentialRef == nil || ref.EncryptionMethod != "" || ref.Version != 5 {
		t.Fatalf("record after re-point = %+v", ref)
	}
	requireStoreMarker(t, f.repo, secretID)

	// A re-point whose publish fails still retires the compromised value:
	// the index row is a marker before the publish is attempted.
	other := domain.NewEntityID()
	f.canonical.records[other] = domain.SecretRef{ID: other, ServiceID: f.serviceID, Name: "API_KEY", EncryptionMethod: domain.EncryptionMethod("nip44"), Version: 1, CreatedAt: time.Now().UTC()}
	_ = f.repo.Create(context.Background(), &domain.ServiceSecret{ID: other, ServiceID: f.serviceID, Name: "API_KEY", EncryptedValue: []byte("sealed"), Version: 1})
	f.canonical.publishErr = errors.New("outbox full")
	otherContent := f.content(other, 2)
	otherContent["name"] = "API_KEY"
	if err := f.process(t, "update", otherContent); err == nil {
		t.Fatal("a failed publish must fail the intent")
	}
	requireStoreMarker(t, f.repo, other)
	f.canonical.publishErr = nil

	// A sealed row with no canonical record (an index the canonical history
	// lost) is scrubbed by id all the same.
	orphan := domain.NewEntityID()
	_ = f.repo.Create(context.Background(), &domain.ServiceSecret{ID: orphan, ServiceID: f.serviceID, Name: "ORPHAN", EncryptedValue: []byte("sealed"), Version: 1})
	orphanContent := f.content(orphan, 2)
	orphanContent["name"] = "ORPHAN"
	if err := f.process(t, "create", orphanContent); err != nil {
		t.Fatalf("reference intent for an unindexed id: %v", err)
	}
	requireStoreMarker(t, f.repo, orphan)
}

// requireStoreMarker asserts PostgreSQL holds a value-less marker, so its
// consumers list the secret and resolve it through the store.
func requireStoreMarker(t *testing.T, repo *memSecretRepo, id uuid.UUID) {
	t.Helper()
	row, _ := repo.GetByID(context.Background(), id)
	if row == nil || row.EncryptionMethod != domain.EncryptionStoreRef || len(row.EncryptedValue) != 0 {
		t.Fatalf("index row = %+v, want a value-less store-ref marker", row)
	}
}

// A credential of another org or another service of the same org is
// refused even though the store holds it under the same owner.
func TestSecretReferenceIntentRefusesAnotherTenantsCredential(t *testing.T) {
	for name, label := range map[string]func(*referenceFixture) string{
		"another org":               func(*referenceFixture) string { return uuid.NewString() + "/" + uuid.NewString() + "/DB_PASSWORD" },
		"another service, same org": func(*referenceFixture) string { return testOrgID().String() + "/" + uuid.NewString() + "/DB_PASSWORD" },
		"another org, same service": func(f *referenceFixture) string {
			return uuid.NewString() + "/" + f.serviceID.String() + "/DB_PASSWORD"
		},
		"unscoped label": func(*referenceFixture) string { return "DB_PASSWORD" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newReferenceFixture(t, false)
			f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Label: label(f), Version: 2, Status: secretstore.StatusActive}
			secretID := domain.NewEntityID()
			err := f.process(t, "create", f.content(secretID, 2))
			if !errors.Is(err, errCredentialNotAvailable) {
				t.Fatalf("err = %v, want the public not-available reason", err)
			}
			if strings.Contains(err.Error(), f.credID) {
				t.Fatalf("the refusal (a public status) names the credential id: %v", err)
			}
			if ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID); ref != nil {
				t.Fatal("a cross-tenant reference was published")
			}
		})
	}
}

func TestSecretIntentWithoutStoreIsRefused(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "never carry a value") {
		t.Fatalf("value intent without a store = %v", err)
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

// Intent status is a relay record: a missing credential and another
// tenant's credential get the same public reason, so one tenant cannot probe
// another's credential ids.
func TestSecretReferenceIntentDoesNotRevealCredentialExistence(t *testing.T) {
	reasons := map[string]string{}
	for name, setup := range map[string]func(*referenceFixture){
		"missing": func(f *referenceFixture) { delete(f.store.creds, f.credID) },
		"another tenant's": func(f *referenceFixture) {
			f.store.creds[f.credID] = secretstore.Metadata{CredentialID: f.credID, Label: uuid.NewString() + "/" + uuid.NewString() + "/X", Version: 2, Status: secretstore.StatusActive}
		},
	} {
		f := newReferenceFixture(t, false)
		setup(f)
		err := f.process(t, "create", f.content(domain.NewEntityID(), 2))
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		reasons[name] = err.Error()
	}
	if reasons["missing"] != reasons["another tenant's"] {
		t.Fatalf("public reasons differ: %q vs %q", reasons["missing"], reasons["another tenant's"])
	}
}

// With PostgreSQL, a reference intent whose name another secret already
// holds for the service and environment is refused before anything is
// published or changed, with the remedy; the other secret keeps its value.
func TestSecretReferenceIntentRefusesNameHeldByAnotherSecret(t *testing.T) {
	f := newReferenceFixture(t, true)
	legacy := domain.NewEntityID()
	_ = f.repo.Create(context.Background(), &domain.ServiceSecret{ID: legacy, ServiceID: f.serviceID, Name: "DB_PASSWORD", EncryptedValue: []byte("sealed"), EncryptionMethod: domain.EncryptionMethod("nip44"), Version: 1})
	secretID := domain.NewEntityID()
	err := f.process(t, "create", f.content(secretID, 2))
	if !errors.Is(err, repository.ErrSecretNameTaken) || !strings.Contains(err.Error(), "re-point that secret") {
		t.Fatalf("err = %v", err)
	}
	if ref, _ := f.canonical.CurrentSecretRef(context.Background(), secretID); ref != nil {
		t.Fatal("published despite the collision")
	}
	if row, _ := f.repo.GetByID(context.Background(), legacy); row == nil || string(row.EncryptedValue) != "sealed" {
		t.Fatal("a refused intent changed another secret's row")
	}
}
