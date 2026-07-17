package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

const testSBOMPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

// mockNostrPublisher captures published events and deterministic relay OK outcomes for testing.
type mockNostrPublisher struct {
	events  []*nostr.Event
	results []PublishOKResult
	err     error
}

type adapterLikePublishResult struct {
	RelayURL string
	Accepted bool
	Reason   string
	Error    error
}

type adapterLikeNostrPublisher struct {
	events  []*nostr.Event
	results []adapterLikePublishResult
	err     error
}

func (m *mockNostrPublisher) PublishSignedEvent(ctx context.Context, ev *nostr.Event) error {
	_, err := m.PublishSignedEventWithResults(ctx, ev)
	return err
}

func (m *mockNostrPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]PublishOKResult, error) {
	if err := nostrutil.SignEventWithHexKey(ev, testSBOMPrivateKey); err != nil {
		return nil, err
	}
	m.events = append(m.events, ev)
	return m.results, m.err
}

func (m *adapterLikeNostrPublisher) PublishSignedEvent(ctx context.Context, ev *nostr.Event) error {
	_, err := m.PublishSignedEventWithResults(ctx, ev)
	return err
}

func (m *adapterLikeNostrPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]adapterLikePublishResult, error) {
	if err := nostrutil.SignEventWithHexKey(ev, testSBOMPrivateKey); err != nil {
		return nil, err
	}
	m.events = append(m.events, ev)
	return m.results, m.err
}

func testTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func testTagsByName(tags nostr.Tags, key string) []nostr.Tag {
	matched := make([]nostr.Tag, 0)
	for _, tag := range tags {
		if len(tag) >= 1 && tag[0] == key {
			matched = append(matched, tag)
		}
	}
	return matched
}

func testSubject() domain.SBOMSubject {
	return domain.SBOMSubject{
		Type:        domain.SBOMSubjectArtifact,
		ID:          "artifact-123",
		DisplayName: "test-image:v1",
		Digest:      "sha256:" + testSHA256A,
	}
}

func testAttestation() *domain.SBOMAttestation {
	att := &domain.SBOMAttestation{
		Type: InTotoStatementType,
		Subject: []domain.AttestationSubject{
			{Name: "test-image:v1", Digest: map[string]string{"sha256": testSHA256A}},
		},
		PredicateType: domain.AttestationTypeSPDX,
		Predicate: domain.SBOMPredicate{
			Format: domain.SBOMFormatSPDX,
			Location: domain.SBOMLocation{
				Type:      domain.SBOMStorageBlossom,
				URI:       "https://blossom.example.com/" + testSHA256B,
				MediaType: MediaTypeSPDX,
			},
			Digest: map[string]string{"sha256": testSHA256B},
			Generator: domain.SBOMGenerator{
				ID:      "syft",
				Version: "0.95.0",
			},
			NTIA: &domain.NTIACompliance{IsCompliant: true},
		},
	}
	signer, err := NewNostrDSSESigner(testSBOMPrivateKey)
	if err != nil {
		panic(err)
	}
	if err := SignAttestation(context.Background(), att, signer); err != nil {
		panic(err)
	}
	return att
}

func TestBuildSBOMReferenceEvent(t *testing.T) {
	createdAt := time.Unix(1710000000, 0).UTC()
	subject := testSubject()
	ev, dTag, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{
		Subject:     subject,
		Attestation: testAttestation(),
		CreatedAt:   &createdAt,
	})
	if err != nil {
		t.Fatalf("BuildSBOMReferenceEvent failed: %v", err)
	}

	if int(ev.Kind) != KindSBOMReference {
		t.Errorf("Event kind = %d, want %d", ev.Kind, KindSBOMReference)
	}
	expectedDTag := "sbom:ref:" + SubjectKey(subject) + ":spdx:" + testSHA256B
	if dTag != expectedDTag {
		t.Errorf("d-tag = %q, want %q", dTag, expectedDTag)
	}
	if tagD := testTagValue(ev.Tags, TagDIdentifier); tagD != expectedDTag {
		t.Errorf("event d-tag = %q, want %q", tagD, expectedDTag)
	}
	if subjectTypeTag := testTagValue(ev.Tags, TagSubjectType); subjectTypeTag != "artifact" {
		t.Errorf("subject_type tag = %q, want %q", subjectTypeTag, "artifact")
	}
	if subjectTag := testTagValue(ev.Tags, TagSubjectDigest); subjectTag != subject.Digest {
		t.Errorf("subject tag = %q, want %q", subjectTag, subject.Digest)
	}
	if mediaTypeTag := testTagValue(ev.Tags, TagMediaType); mediaTypeTag != MediaTypeSPDX {
		t.Errorf("media_type tag = %q, want %q", mediaTypeTag, MediaTypeSPDX)
	}
	if artifactTag := testTagValue(ev.Tags, "artifact"); artifactTag != subject.ID {
		t.Errorf("artifact tag = %q, want %q", artifactTag, subject.ID)
	}
	if genTag := testTagValue(ev.Tags, TagGenerator); genTag != "syft@0.95.0" {
		t.Errorf("generator tag = %q, want %q", genTag, "syft@0.95.0")
	}
	if ntiaTag := testTagValue(ev.Tags, TagNTIA); ntiaTag != "compliant" {
		t.Errorf("ntia tag = %q, want %q", ntiaTag, "compliant")
	}
	if ev.CreatedAt != nostr.Timestamp(createdAt.Unix()) {
		t.Errorf("CreatedAt = %d, want %d", ev.CreatedAt, createdAt.Unix())
	}

	var parsedAtt domain.SBOMAttestation
	if err := json.Unmarshal([]byte(ev.Content), &parsedAtt); err != nil {
		t.Fatalf("Failed to parse event content: %v", err)
	}
}

func TestBuildSBOMReferenceEventRejectsUnsignedAttestation(t *testing.T) {
	att := testAttestation()
	att.Envelope = nil
	_, _, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{Subject: testSubject(), Attestation: att})
	if err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("BuildSBOMReferenceEvent error = %v, want unsigned rejection", err)
	}
}

func TestParseAttestationFromEventBindsDSSESignerToPublisher(t *testing.T) {
	ev, _, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{Subject: testSubject(), Attestation: testAttestation()})
	if err != nil {
		t.Fatal(err)
	}
	secret := nostr.MustSecretKeyFromHex(testSBOMPrivateKey)
	if err := ev.Sign(secret); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAttestationFromEvent(ev); err != nil {
		t.Fatalf("ParseAttestationFromEvent error = %v", err)
	}

	otherSecret := nostr.Generate()
	if err := ev.Sign(otherSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAttestationFromEvent(ev); err == nil || !strings.Contains(err.Error(), "does not match event publisher") {
		t.Fatalf("ParseAttestationFromEvent error = %v, want signer binding rejection", err)
	}
}

func TestBuildSBOMReferenceEventRejectsDigestMismatch(t *testing.T) {
	subject := testSubject()
	subject.Digest = "sha256:" + testSHA256B
	_, _, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{Subject: subject, Attestation: testAttestation()})
	if err == nil || !strings.Contains(err.Error(), "does not match subject digest") {
		t.Fatalf("BuildSBOMReferenceEvent error = %v, want digest mismatch", err)
	}
}

func TestIndexPublisher_PublishAttestationRequiresAcceptedOK(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true}}}
	pub := NewIndexPublisher(mock)

	eventID, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{
		Subject:     testSubject(),
		Attestation: testAttestation(),
	})
	if err != nil {
		t.Fatalf("PublishAttestation failed: %v", err)
	}
	if eventID == "" {
		t.Error("Expected non-empty event ID")
	}
	if len(mock.events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(mock.events))
	}
}

func TestIndexPublisher_PublishAttestationRejectsRelayOKFalse(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "blocked: policy denied event"}}}
	pub := NewIndexPublisher(mock)

	_, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{Subject: testSubject(), Attestation: testAttestation()})
	if err == nil || !strings.Contains(err.Error(), "no relay accepted") || !strings.Contains(err.Error(), "blocked: policy denied event") {
		t.Fatalf("PublishAttestation error = %v, want relay rejection", err)
	}
}

func TestIndexPublisher_PublishAttestationRejectsAuthRequiredOKFalse(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "auth-required: sign event"}}}
	pub := NewIndexPublisher(mock)

	_, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{Subject: testSubject(), Attestation: testAttestation()})
	if err == nil || !strings.Contains(err.Error(), "auth-required") {
		t.Fatalf("PublishAttestation error = %v, want auth-required rejection", err)
	}
}

func TestIndexPublisher_PublishAttestationRejectsClosedPublishPath(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Error: errors.New("closed: relay closed connection")}}}
	pub := NewIndexPublisher(mock)

	_, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{Subject: testSubject(), Attestation: testAttestation()})
	if err == nil || !strings.Contains(err.Error(), "closed: relay closed connection") {
		t.Fatalf("PublishAttestation error = %v, want closed publish path", err)
	}
}

func TestIndexPublisher_PublishAttestationAcceptsAdapterCompatibleOKResults(t *testing.T) {
	mock := &adapterLikeNostrPublisher{results: []adapterLikePublishResult{{RelayURL: "wss://relay.example", Accepted: true}}}
	pub := NewIndexPublisher(mock)

	eventID, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{Subject: testSubject(), Attestation: testAttestation()})
	if err != nil {
		t.Fatalf("PublishAttestation failed: %v", err)
	}
	if eventID == "" {
		t.Error("Expected non-empty event ID")
	}
	if len(mock.events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(mock.events))
	}
}

func TestBuildSBOMAvailabilityListEvent(t *testing.T) {
	createdAt := time.Unix(1710000000, 0).UTC()
	subject := testSubject()
	refDTag := "sbom:ref:" + SubjectKey(subject) + ":spdx:" + testSHA256B
	entries := []domain.SBOMIndexEntry{
		{
			SubjectDigest: subject.Digest,
			ReferenceDTag: refDTag,
			Format:        domain.SBOMFormatSPDX,
			LocationURI:   "https://blossom.example.com/" + testSHA256B,
			StorageType:   domain.SBOMStorageBlossom,
			PayloadSHA256: testSHA256B,
			GeneratorID:   "syft",
			Timestamp:     createdAt,
		},
		{
			SubjectDigest: subject.Digest,
			ReferenceDTag: refDTag,
			Format:        domain.SBOMFormatSPDX,
			LocationURI:   "https://blossom.example.com/" + testSHA256B,
			StorageType:   domain.SBOMStorageBlossom,
			PayloadSHA256: testSHA256B,
			GeneratorID:   "syft",
			Timestamp:     createdAt,
		},
	}

	ev, dTag, err := BuildSBOMAvailabilityListEvent(BuildSBOMAvailabilityListEventInput{
		Subject:         subject,
		Entries:         entries,
		PublisherPubkey: "publisher-pubkey",
		CreatedAt:       &createdAt,
	})
	if err != nil {
		t.Fatalf("BuildSBOMAvailabilityListEvent failed: %v", err)
	}

	expectedDTag := "sbom:available:artifact:" + SubjectKey(subject)
	if dTag != expectedDTag {
		t.Errorf("d-tag = %q, want %q", dTag, expectedDTag)
	}
	if int(ev.Kind) != KindSBOMAvailabilityList {
		t.Errorf("Event kind = %d, want %d", ev.Kind, KindSBOMAvailabilityList)
	}
	if subjectTag := testTagValue(ev.Tags, TagSubjectDigest); subjectTag != subject.Digest {
		t.Errorf("subject tag = %q, want %q", subjectTag, subject.Digest)
	}

	// Verify the resource tag is present so browser #artifact filters match.
	if artifactTag := testTagValue(ev.Tags, "artifact"); artifactTag != subject.ID {
		t.Errorf("artifact resource tag = %q, want %q", artifactTag, subject.ID)
	}

	aTags := testTagsByName(ev.Tags, TagAReference)
	if len(aTags) != 1 {
		t.Fatalf("Expected 1 deduped a tag, got %d", len(aTags))
	}
	if got := aTags[0][1]; got != "30078:publisher-pubkey:"+refDTag {
		t.Errorf("a tag = %q, want %q", got, "30078:publisher-pubkey:"+refDTag)
	}

	sbomTags := testTagsByName(ev.Tags, TagSBOMRef)
	if len(sbomTags) != 1 {
		t.Fatalf("Expected 1 deduped sbom tag, got %d", len(sbomTags))
	}
	expectedSBOMTag := nostr.Tag{TagSBOMRef, subject.Digest, "spdx", "blossom", "https://blossom.example.com/" + testSHA256B, testSHA256B, "syft", refDTag}
	if strings.Join(sbomTags[0], "|") != strings.Join(expectedSBOMTag, "|") {
		t.Errorf("sbom tag = %#v, want %#v", sbomTags[0], expectedSBOMTag)
	}

	var parsedIndex domain.SBOMIndex
	if err := json.Unmarshal([]byte(ev.Content), &parsedIndex); err != nil {
		t.Fatalf("Failed to parse event content: %v", err)
	}
	if len(parsedIndex.Entries) != 1 {
		t.Errorf("Entries count = %d, want 1", len(parsedIndex.Entries))
	}
}

func TestIndexPublisher_PublishIndexRequiresAcceptedOK(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true}}}
	pub := NewIndexPublisher(mock)
	subject := testSubject()
	refDTag := "sbom:ref:" + SubjectKey(subject) + ":spdx:" + testSHA256B

	eventID, err := pub.PublishIndex(context.Background(), PublishIndexInput{
		Subject:         subject,
		PublisherPubkey: "publisher-pubkey",
		Entries: []domain.SBOMIndexEntry{{
			SubjectDigest: subject.Digest,
			ReferenceDTag: refDTag,
			Format:        domain.SBOMFormatSPDX,
			LocationURI:   "https://blossom.example.com/" + testSHA256B,
			StorageType:   domain.SBOMStorageBlossom,
			PayloadSHA256: testSHA256B,
			GeneratorID:   "syft",
		}},
	})
	if err != nil {
		t.Fatalf("PublishIndex failed: %v", err)
	}
	if eventID == "" {
		t.Error("Expected non-empty event ID")
	}
	if len(mock.events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(mock.events))
	}
}

func TestIndexPublisher_PublishIndexRejectsRelayOKFalse(t *testing.T) {
	mock := &mockNostrPublisher{results: []PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "rate-limited: slow down"}}}
	pub := NewIndexPublisher(mock)
	subject := testSubject()
	refDTag := "sbom:ref:" + SubjectKey(subject) + ":spdx:" + testSHA256B

	_, err := pub.PublishIndex(context.Background(), PublishIndexInput{
		Subject:         subject,
		PublisherPubkey: "publisher-pubkey",
		Entries: []domain.SBOMIndexEntry{{
			SubjectDigest: subject.Digest,
			ReferenceDTag: refDTag,
			Format:        domain.SBOMFormatSPDX,
			LocationURI:   "https://blossom.example.com/" + testSHA256B,
			StorageType:   domain.SBOMStorageBlossom,
			PayloadSHA256: testSHA256B,
			GeneratorID:   "syft",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("PublishIndex error = %v, want rate-limited rejection", err)
	}
}

func TestBuildIndexEntry(t *testing.T) {
	att := &domain.SBOMAttestation{
		Subject: []domain.AttestationSubject{
			{Name: "test", Digest: map[string]string{"sha256": testSHA256A}},
		},
		Predicate: domain.SBOMPredicate{
			Format: domain.SBOMFormatSPDX,
			Location: domain.SBOMLocation{
				Type: domain.SBOMStorageBlossom,
				URI:  "https://example.com/abc123",
			},
			Digest:    map[string]string{"sha256": testSHA256B},
			Generator: domain.SBOMGenerator{ID: "syft"},
			Timestamp: time.Now(),
		},
	}

	entry, err := BuildIndexEntry(att, "event-123")
	if err != nil {
		t.Fatalf("BuildIndexEntry failed: %v", err)
	}

	if entry.SubjectDigest != "sha256:"+testSHA256A {
		t.Errorf("SubjectDigest = %q, want %q", entry.SubjectDigest, "sha256:"+testSHA256A)
	}
	if entry.AttestationID != "event-123" {
		t.Errorf("AttestationID = %q, want %q", entry.AttestationID, "event-123")
	}
	if entry.PayloadSHA256 != testSHA256B {
		t.Errorf("PayloadSHA256 = %q, want %q", entry.PayloadSHA256, testSHA256B)
	}
	if entry.Format != domain.SBOMFormatSPDX {
		t.Errorf("Format = %q, want %q", entry.Format, domain.SBOMFormatSPDX)
	}
	if entry.StorageType != domain.SBOMStorageBlossom {
		t.Errorf("StorageType = %q, want %q", entry.StorageType, domain.SBOMStorageBlossom)
	}
}

func TestParseIndexFromEvent(t *testing.T) {
	index := domain.SBOMIndex{
		SubjectType: "repository",
		SubjectID:   "github.com/openagentsinc/bahia",
		Entries: []domain.SBOMIndexEntry{
			{SubjectDigest: "git:abc123", Format: domain.SBOMFormatSPDX},
		},
		UpdatedAt: time.Now().UTC(),
	}

	content, _ := json.Marshal(index)

	ev := &nostr.Event{
		Kind:    KindSBOMAvailabilityList,
		Content: string(content),
	}

	parsed, err := ParseIndexFromEvent(ev)
	if err != nil {
		t.Fatalf("ParseIndexFromEvent failed: %v", err)
	}

	if parsed.SubjectType != "repository" {
		t.Errorf("SubjectType = %q, want %q", parsed.SubjectType, "repository")
	}
	if len(parsed.Entries) != 1 {
		t.Errorf("Entries count = %d, want 1", len(parsed.Entries))
	}
}

func TestParseIndexFromEvent_ReadsLegacyKind(t *testing.T) {
	index := domain.SBOMIndex{
		SubjectType: "artifact",
		SubjectID:   "legacy-artifact",
		Entries: []domain.SBOMIndexEntry{
			{SubjectDigest: "sha256:legacy", Format: domain.SBOMFormatSPDX},
		},
		UpdatedAt: time.Now().UTC(),
	}
	content, _ := json.Marshal(index)

	ev := &nostr.Event{
		Kind:    KindLegacySBOMIndex,
		Content: string(content),
	}

	parsed, err := ParseIndexFromEvent(ev)
	if err != nil {
		t.Fatalf("ParseIndexFromEvent failed for legacy kind: %v", err)
	}
	if parsed.SubjectID != "legacy-artifact" {
		t.Errorf("SubjectID = %q, want %q", parsed.SubjectID, "legacy-artifact")
	}
}

func TestFilterForArtifactSBOMs(t *testing.T) {
	filter := FilterForArtifactSBOMs("sha256:abc123")

	if len(filter.Kinds) != 1 || filter.Kinds[0] != KindSBOMIndex {
		t.Errorf("Expected kinds [%d], got %v", KindSBOMIndex, filter.Kinds)
	}

	sbomTags := filter.Tags[TagSBOMRef]
	if len(sbomTags) != 1 || sbomTags[0] != "sha256:abc123" {
		t.Errorf("Expected sbom tag filter, got %v", sbomTags)
	}
}

func TestFilterForAttestations(t *testing.T) {
	filter := FilterForAttestations("sha256:def456")

	if len(filter.Kinds) != 1 || filter.Kinds[0] != KindSBOMAttestation {
		t.Errorf("Expected kinds [%d], got %v", KindSBOMAttestation, filter.Kinds)
	}

	subjectTags := filter.Tags[TagSubjectDigest]
	if len(subjectTags) != 1 || subjectTags[0] != "sha256:def456" {
		t.Errorf("Expected subject tag filter, got %v", subjectTags)
	}
}
