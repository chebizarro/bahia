package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type adoptionReferenceStore struct {
	secretstore.SecretStore
	keys       map[string]string
	version    int
	wrongLabel bool
}

func (s adoptionReferenceStore) Inspect(_ context.Context, id string, scope secretstore.Scope) (secretstore.Metadata, error) {
	key, ok := s.keys[id]
	if !ok {
		return secretstore.Metadata{}, secretstore.ErrNotFound
	}
	label := scope.OrgID.String() + "/" + scope.ServiceID.String() + "/" + key
	if s.wrongLabel {
		label += "-other"
	}
	return secretstore.Metadata{Label: label, Version: s.version, Status: secretstore.StatusActive}, nil
}

type adoptionReferenceRecords struct {
	refs     map[uuid.UUID]domain.SecretRef
	failOnce bool
}

type adoptionReferenceIndex struct {
	refs map[uuid.UUID]domain.SecretRef
}

func (i *adoptionReferenceIndex) MarkStoreBacked(_ context.Context, ref domain.SecretRef) error {
	i.refs[ref.ID] = ref
	return nil
}

func (r *adoptionReferenceRecords) CurrentSecretRef(_ context.Context, id uuid.UUID) (*domain.SecretRef, error) {
	ref, ok := r.refs[id]
	if !ok {
		return nil, nil
	}
	return &ref, nil
}
func (r *adoptionReferenceRecords) PublishSecretRef(_ context.Context, ref domain.SecretRef) error {
	if r.failOnce {
		r.failOnce = false
		return secretstore.ErrUnavailable
	}
	r.refs[ref.ID] = ref
	return nil
}

func sensitiveAdoptionRefs() (map[string]domain.SecretCredentialRef, map[string]string) {
	keys := []string{"AWS_SECRET_ACCESS_KEY", "DATABASE_URL", "DB_PASSWORD", "FLEET_CURATOR_BUNKER_URL", "MINT_LND_REST_MACAROON"}
	refs := make(map[string]domain.SecretCredentialRef, len(keys))
	ids := make(map[string]string, len(keys))
	for _, key := range keys {
		id := fmt.Sprintf("cred_%x", sha256.Sum256([]byte(key)))
		refs[key] = domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: id, Version: 1}
		ids[id] = key
	}
	return refs, ids
}

func TestAdoptionSecretPublishFailureResumesSameCoordinates(t *testing.T) {
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})
	refs, ids := sensitiveAdoptionRefs()
	records := &adoptionReferenceRecords{refs: map[uuid.UUID]domain.SecretRef{}, failOnce: true}
	adoption.ConfigureSecretReferences(AdoptionSecretReferences{Store: adoptionReferenceStore{keys: ids, version: 1}, View: records, Publisher: records})
	req := sensitiveTarget(server)
	req.SecretRefs = refs
	first, err := adoption.Import(context.Background(), req)
	if !errors.Is(err, ErrAdoptionIncomplete) || len(first) != 1 || !first[0].Incomplete || first[0].Step != adoptionStepSecret {
		t.Fatalf("first: %#v, %v", first, err)
	}
	second, err := adoption.Import(context.Background(), req)
	if err != nil || len(second) != 1 || second[0].Incomplete || second[0].Status == adoptionStatusFailed || second[0].ServiceID == nil || *first[0].ServiceID != *second[0].ServiceID {
		t.Fatalf("retry: %#v, %v", second, err)
	}
	if len(records.refs) != len(refs) {
		t.Fatalf("published %d refs", len(records.refs))
	}
}

func TestAdoptionImportsReferenceOnlySecretsAndReplays(t *testing.T) {
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})
	refs, ids := sensitiveAdoptionRefs()
	records := &adoptionReferenceRecords{refs: map[uuid.UUID]domain.SecretRef{}}
	index := &adoptionReferenceIndex{refs: map[uuid.UUID]domain.SecretRef{}}
	adoption.ConfigureSecretReferences(AdoptionSecretReferences{Store: adoptionReferenceStore{keys: ids, version: 1}, View: records, Publisher: records, Index: index})
	req := sensitiveTarget(server)
	req.SecretRefs = refs
	for attempt := 0; attempt < 2; attempt++ {
		results, err := adoption.Import(context.Background(), req)
		if err != nil || len(results) != 1 || results[0].Status == adoptionStatusFailed {
			t.Fatalf("import %d: %#v, %v", attempt, results, err)
		}
		encoded, _ := json.Marshal(results)
		for _, value := range []string{"super-secret", "aws-secret", "postgres://user:pass@db/prod", "hex-secret", "bunker://pub?secret=secret"} {
			if strings.Contains(string(encoded), value) {
				t.Fatalf("result exposed a value: %s", encoded)
			}
		}
		service := canonical.services[*results[0].ServiceID]
		for key := range refs {
			if _, ok := service.RuntimeConfig.Adopted.Environment[key]; ok {
				t.Fatalf("service retained inline %s", key)
			}
			id := adoptionEntityID("secret", service.ID.String(), key)
			got, ok := records.refs[id]
			if !ok || got.Name != key || got.CredentialRef == nil || *got.CredentialRef != refs[key] || got.Version != 1 {
				t.Fatalf("secret %s: %#v", key, got)
			}
		}
	}
	if len(records.refs) != len(refs) {
		t.Fatalf("secret count: %d", len(records.refs))
	}
	if len(index.refs) != len(refs) {
		t.Fatalf("store-backed index markers: %d", len(index.refs))
	}
}

func TestAdoptionSecretReferenceRefusalsNameKeysNotValues(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remove     string
		version    int
		wrongLabel bool
	}{
		{"missing", "DB_PASSWORD", 1, false},
		{"stale version", "", 2, false},
		{"wrong label", "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newSensitiveAdoptionDockerServer(t)
			defer server.Close()
			canonical := newFakeAdoptionCanonical()
			core, logs := observer.New(zap.DebugLevel)
			adoption := NewAdoptionService(canonical, canonical, &events.NoopPublisher{}, zap.New(core))
			refs, ids := sensitiveAdoptionRefs()
			if tc.remove != "" {
				delete(refs, tc.remove)
			}
			records := &adoptionReferenceRecords{refs: map[uuid.UUID]domain.SecretRef{}}
			adoption.ConfigureSecretReferences(AdoptionSecretReferences{Store: adoptionReferenceStore{keys: ids, version: tc.version, wrongLabel: tc.wrongLabel}, View: records, Publisher: records})
			req := sensitiveTarget(server)
			req.SecretRefs = refs
			results, err := adoption.Import(context.Background(), req)
			if err != nil || len(results) != 1 || results[0].Status != adoptionStatusFailed {
				t.Fatalf("result: %#v, %v", results, err)
			}
			if results[0].ServiceID == nil {
				t.Fatal("refusal omitted service id needed for the Signet label")
			}
			if !strings.Contains(results[0].Error, "DB_PASSWORD") && tc.remove != "" {
				t.Fatalf("missing key omitted: %s", results[0].Error)
			}
			encoded, _ := json.Marshal(results)
			for _, value := range []string{"super-secret", "aws-secret", "postgres://user:pass@db/prod", "hex-secret", "bunker://pub?secret=secret"} {
				if strings.Contains(string(encoded), value) {
					t.Fatalf("refusal exposed a value: %s", encoded)
				}
				for _, entry := range logs.All() {
					if strings.Contains(entry.Message+fmt.Sprint(entry.ContextMap()), value) {
						t.Fatalf("log exposed a value: %s", value)
					}
				}
			}
			if len(records.refs) != 0 || canonical.counts()["services"] != 0 {
				t.Fatal("refusal published state")
			}
		})
	}
}
