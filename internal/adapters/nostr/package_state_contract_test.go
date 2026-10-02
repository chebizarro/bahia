package nostr

import (
	"context"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// TestPackageRegistryRecordCarriesControlStateEnvelope verifies that a
// package record published through the shared record builder carries
// exactly the same envelope tags (d, domain, schema, legacy_kind, deleted, t)
// that controlStateEnvelope produces for that family. This is the contract
// that consumers (web catalog, relay-first reads) rely on.
func TestPackageRegistryRecordCarriesControlStateEnvelope(t *testing.T) {
	cases := []struct {
		name      string
		kind      int
		id        uuid.UUID
		wantTopic string
	}{
		{"repository", KindPackageRepositoryRegistry, uuid.New(), kinds.CPStateTopicPackageRepository},
		{"artifact", KindPackageArtifactRegistry, uuid.New(), kinds.CPStateTopicPackageArtifact},
		{"promotion", KindPackagePromotionRegistry, uuid.New(), kinds.CPStateTopicPackagePromotion},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, deleted := range []bool{false, true} {
				wireKind, envelope := controlStateEnvelope(tc.kind, tc.id.String(), deleted)
				if wireKind != KindCASControlState {
					t.Fatalf("wire kind = %d, want %d", wireKind, KindCASControlState)
				}
				if got := pkgTagValue(envelope, "d"); got != tc.id.String() {
					t.Errorf("d = %q, want %q", got, tc.id.String())
				}
				if got := pkgTagValue(envelope, "domain"); got != "package" {
					t.Errorf("domain = %q, want %q", got, "package")
				}
				if got := pkgTagValue(envelope, "schema"); got != kinds.CASControlStateSchema {
					t.Errorf("schema = %q, want %q", got, kinds.CASControlStateSchema)
				}
				if got := pkgTagValue(envelope, "legacy_kind"); got != strconv.Itoa(tc.kind) {
					t.Errorf("legacy_kind = %q, want %q", got, strconv.Itoa(tc.kind))
				}
				wantDeleted := strconv.FormatBool(deleted)
				if got := pkgTagValue(envelope, "deleted"); got != wantDeleted {
					t.Errorf("deleted = %q, want %q", got, wantDeleted)
				}
				if got := pkgTagValue(envelope, "t"); got != tc.wantTopic {
					t.Errorf("t = %q, want %q", got, tc.wantTopic)
				}
			}
		})
	}
}

// TestPackageRegistryRecordBuilderMatchesEnvelope verifies that the exported
// record builder produces tags that are additive to the envelope (no conflicts).
func TestPackageRegistryRecordBuilderMatchesEnvelope(t *testing.T) {
	now := time.Now().UTC()
	repoID := uuid.New()
	repo := &domain.PackageRepository{
		ID:          repoID,
		Name:        "test-repo",
		Format:      domain.PackageRepositoryFormatNPM,
		BackendRef:  "mock",
		BackendType: domain.PackageBackendFilesystemMock,
		Status:      domain.PackageRepositoryStatusReady,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, envelope := controlStateEnvelope(KindPackageRepositoryRegistry, repoID.String(), false)
	recordTags, _ := packageRepositoryRegistryRecord(repo, false)

	envelopeKeys := map[string]bool{}
	for _, tag := range envelope {
		if len(tag) >= 1 {
			envelopeKeys[tag[0]] = true
		}
	}
	for _, tag := range recordTags {
		if len(tag) >= 1 && envelopeKeys[tag[0]] {
			t.Errorf("record tag %q conflicts with envelope tag", tag[0])
		}
	}

	if got := pkgTagValue(recordTags, "repository"); got != repoID.String() {
		t.Errorf("repository tag = %q, want %q", got, repoID.String())
	}
	if got := pkgTagValue(recordTags, "name"); got != "test-repo" {
		t.Errorf("name tag = %q, want %q", got, "test-repo")
	}
}

// TestPackageMonotonicCreatedAt verifies that two updates to the same package
// record in the same second produce strictly increasing created_at values
// through the projector's dedup pipeline.
func TestPackageMonotonicCreatedAt(t *testing.T) {
	sink := &pkgCaptureSink{}
	projector := newRelayFirstTestProjector(nil, sink)
	ctx := context.Background()

	repoID := uuid.New()
	now := time.Now().UTC()
	repo := &domain.PackageRepository{
		ID:                     repoID,
		Name:                   "mono-repo",
		Format:                 domain.PackageRepositoryFormatNPM,
		BackendRef:             "mock",
		BackendType:            domain.PackageBackendFilesystemMock,
		Status:                 domain.PackageRepositoryStatusReady,
		ExternalRepositoryName: "mono-repo",
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	tags1, content1 := packageRepositoryRegistryRecord(repo, false)
	wireKind, baseTags := controlStateEnvelope(KindPackageRepositoryRegistry, repoID.String(), false)
	if err := projector.publishAuthoritative(ctx, wireKind, append(baseTags, tags1...), content1, "package_repository.projection", &repoID); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(sink.events))
	}
	first := sink.events[0]

	// Change status so fingerprint differs.
	repo.Status = domain.PackageRepositoryStatusFailed
	repo.UpdatedAt = now.Add(time.Millisecond) // same second
	tags2, content2 := packageRepositoryRegistryRecord(repo, false)
	if err := projector.publishAuthoritative(ctx, wireKind, append(baseTags, tags2...), content2, "package_repository.projection", &repoID); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(sink.events))
	}
	second := sink.events[1]

	if second.CreatedAt <= first.CreatedAt {
		t.Errorf("second created_at (%d) must be strictly greater than first (%d)", second.CreatedAt, first.CreatedAt)
	}
}

// TestPackageFingerprintDedup verifies that publishing the same package state
// twice (identical fingerprint) does not produce a second event.
func TestPackageFingerprintDedup(t *testing.T) {
	sink := &pkgCaptureSink{}
	projector := newRelayFirstTestProjector(nil, sink)
	ctx := context.Background()

	repoID := uuid.New()
	now := time.Now().UTC()
	repo := &domain.PackageRepository{
		ID:         repoID,
		Name:       "dedup-repo",
		Format:     domain.PackageRepositoryFormatNPM,
		BackendRef: "mock",
		Status:     domain.PackageRepositoryStatusReady,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	tags, content := packageRepositoryRegistryRecord(repo, false)
	wireKind, baseTags := controlStateEnvelope(KindPackageRepositoryRegistry, repoID.String(), false)
	allTags := append(baseTags, tags...)
	if err := projector.publishAuthoritative(ctx, wireKind, allTags, content, "package_repository.projection", &repoID); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Same state again — should be deduped.
	if err := projector.publishAuthoritative(ctx, wireKind, allTags, content, "package_repository.projection", &repoID); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected 1 event (dedupe), got %d", len(sink.events))
	}
}

func pkgTagValue(tags gonostr.Tags, key string) string {
	if tag := tags.Find(key); len(tag) >= 2 {
		return tag[1]
	}
	return ""
}

// pkgCaptureSink captures events published through the projector for test assertions.
type pkgCaptureSink struct {
	events []gonostr.Event
}

func (s *pkgCaptureSink) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	s.events = append(s.events, ev)
	return 1, nil
}
