package secretstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

type fakeRecords struct {
	secrets map[uuid.UUID]*domain.SecretRef
	orgs    map[uuid.UUID]string
}

func (f fakeRecords) CurrentSecretRef(_ context.Context, id uuid.UUID) (*domain.SecretRef, error) {
	return f.secrets[id], nil
}

func (f fakeRecords) ServiceOrgID(_ context.Context, id uuid.UUID) (string, error) {
	org, ok := f.orgs[id]
	if !ok {
		return "", fmt.Errorf("service %s has no canonical record", id)
	}
	return org, nil
}

// fakeStore delivers "<credential id>@<version>" for credentials labelled in
// scope and records every batch it was asked for.
type fakeStore struct {
	values  map[string]string // credential id -> owning "<org>/<service>/"
	batches [][]secretstore.Request
	fail    map[string]error
}

func (s *fakeStore) Resolve(ctx context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error) {
	if err := s.fail[ref.CredentialID]; err != nil {
		return nil, err
	}
	if s.values[ref.CredentialID] != scope.LabelPrefix() {
		return nil, secretstore.ErrDenied
	}
	return secretstore.NewValue([]byte(fmt.Sprintf("%s@%d", ref.CredentialID, ref.Version)), nowPlusMinute()), nil
}

func (s *fakeStore) ResolveAll(ctx context.Context, reqs []secretstore.Request) ([]*secretstore.Value, error) {
	s.batches = append(s.batches, reqs)
	var out []*secretstore.Value
	for i, r := range reqs {
		v, err := s.Resolve(ctx, r.Ref, r.Scope)
		if err != nil {
			secretstore.ZeroizeAll(out)
			return nil, &secretstore.BatchError{Index: i, Err: err}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *fakeStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, errors.New("unused")
}
func (s *fakeStore) Health(context.Context) error { return nil }

func TestResolverResolvesCanonicalReferencesInTheServicesScope(t *testing.T) {
	org, svc := uuid.New(), uuid.New()
	storeBacked := &domain.SecretRef{ID: uuid.New(), ServiceID: svc, Name: "DATABASE_URL",
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_a", Version: 3}}
	second := &domain.SecretRef{ID: uuid.New(), ServiceID: svc, Name: "API_KEY",
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_b", Version: 1}}
	legacy := &domain.SecretRef{ID: uuid.New(), ServiceID: svc, Name: "OLD", EncryptionMethod: domain.EncryptionAES256}
	scope := secretstore.Scope{OrgID: org, ServiceID: svc}
	store := &fakeStore{values: map[string]string{"cred_a": scope.LabelPrefix(), "cred_b": scope.LabelPrefix()}}
	records := fakeRecords{
		secrets: map[uuid.UUID]*domain.SecretRef{storeBacked.ID: storeBacked, second.ID: second, legacy.ID: legacy},
		orgs:    map[uuid.UUID]string{svc: org.String()},
	}
	r := secretstore.NewResolver(store)

	if _, err := r.Resolve(t.Context(), storeBacked.ID); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("resolve before records are attached = %v, want ErrUnavailable", err)
	}
	r.UseRecords(records)

	got, err := r.Resolve(t.Context(), storeBacked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Value.Bytes()) != "cred_a@3" || got.Secret.Name != "DATABASE_URL" {
		t.Fatalf("resolved %q for %+v", got.Value.Bytes(), got.Secret)
	}
	got.Value.Zeroize()

	all, err := r.ResolveAll(t.Context(), []uuid.UUID{storeBacked.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.batches) != 1 || len(store.batches[0]) != 2 {
		t.Fatalf("ResolveAll made batches %v, want one of two", store.batches)
	}
	if string(all[1].Value.Bytes()) != "cred_b@1" || all[1].Secret.ID != second.ID {
		t.Fatalf("second value %q for %s", all[1].Value.Bytes(), all[1].Secret.ID)
	}
	secretstore.ZeroizeResolved(all)

	// A legacy value is refused before the store is asked.
	before := len(store.batches)
	if _, err := r.ResolveAll(t.Context(), []uuid.UUID{storeBacked.ID, legacy.ID}); !errors.Is(err, domain.ErrLegacySecretValue) {
		t.Fatalf("legacy secret = %v, want ErrLegacySecretValue", err)
	}
	if len(store.batches) != before {
		t.Fatal("the store was asked for a batch containing a legacy secret")
	}
	if _, err := r.Resolve(t.Context(), uuid.New()); !errors.Is(err, secretstore.ErrNoRecord) {
		t.Fatalf("missing record = %v, want ErrNoRecord", err)
	}
	if _, err := r.ResolveAll(t.Context(), []uuid.UUID{second.ID, second.ID}); err == nil {
		t.Fatal("duplicate ids were accepted")
	}

	// Store failures name the failing secret.
	store.fail = map[string]error{"cred_b": secretstore.ErrRevoked}
	_, err = r.ResolveAll(t.Context(), []uuid.UUID{storeBacked.ID, second.ID})
	if !errors.Is(err, secretstore.ErrRevoked) || !containsAll(err.Error(), second.ID.String(), "API_KEY") {
		t.Fatalf("batch failure = %v", err)
	}
}

func TestResolverWithoutStoreFailsClosed(t *testing.T) {
	r := secretstore.NewResolver(nil)
	if _, err := r.Resolve(t.Context(), uuid.New()); !errors.Is(err, secretstore.ErrNotConfigured) {
		t.Fatalf("no store = %v, want ErrNotConfigured", err)
	}
	var nilResolver *secretstore.Resolver
	if _, err := nilResolver.ResolveAll(t.Context(), []uuid.UUID{uuid.New()}); !errors.Is(err, secretstore.ErrNotConfigured) {
		t.Fatalf("nil resolver = %v, want ErrNotConfigured", err)
	}
}

func nowPlusMinute() time.Time { return time.Now().Add(time.Minute) }

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
