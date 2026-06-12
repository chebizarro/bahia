package sbom

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

const testSBOMPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

// mockNostrPublisher captures published events for testing.
type mockNostrPublisher struct {
	events []*nostr.Event
}

func (m *mockNostrPublisher) PublishSignedEvent(ctx context.Context, ev *nostr.Event) error {
	if err := nostrutil.SignEventWithHexKey(ev, testSBOMPrivateKey); err != nil {
		return err
	}
	m.events = append(m.events, ev)
	return nil
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

func TestIndexPublisher_PublishAttestation(t *testing.T) {
	mock := &mockNostrPublisher{}
	pub := NewIndexPublisher(mock)

	att := &domain.SBOMAttestation{
		Type: InTotoStatementType,
		Subject: []domain.AttestationSubject{
			{Name: "test-image:v1", Digest: map[string]string{"sha256": "abc123def456"}},
		},
		PredicateType: domain.AttestationTypeSPDX,
		Predicate: domain.SBOMPredicate{
			Format: domain.SBOMFormatSPDX,
			Location: domain.SBOMLocation{
				Type: domain.SBOMStorageBlossom,
				URI:  "https://blossom.example.com/abc123",
			},
			Digest: map[string]string{"sha256": "payload-hash"},
			Generator: domain.SBOMGenerator{
				ID:      "syft",
				Version: "0.95.0",
			},
			NTIA: &domain.NTIACompliance{IsCompliant: true},
		},
	}

	eventID, err := pub.PublishAttestation(context.Background(), PublishAttestationInput{
		Attestation: att,
		SubjectName: "test-image:v1",
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

	ev := mock.events[0]

	// Check event kind.
	if int(ev.Kind) != KindSBOMAttestation {
		t.Errorf("Event kind = %d, want %d", ev.Kind, KindSBOMAttestation)
	}

	// Check d-tag format.
	dTag := testTagValue(ev.Tags, TagDIdentifier)
	if dTag == "" {
		t.Fatal("Missing d-tag")
	}
	expectedDTag := "sbom:attestation:sha256:abc123def456"
	if dTag != expectedDTag {
		t.Errorf("d-tag = %q, want %q", dTag, expectedDTag)
	}

	// Check format tag.
	formatTag := testTagValue(ev.Tags, TagFormat)
	if formatTag != "spdx" {
		t.Errorf("format tag = %v, want 'spdx'", formatTag)
	}

	// Check storage type tag.
	storageTag := testTagValue(ev.Tags, TagStorageType)
	if storageTag != "blossom" {
		t.Errorf("storage tag = %v, want 'blossom'", storageTag)
	}

	// Check generator tag.
	genTag := testTagValue(ev.Tags, TagGenerator)
	if genTag != "syft@0.95.0" {
		t.Errorf("generator tag = %v, want 'syft@0.95.0'", genTag)
	}

	// Check NTIA tag.
	ntiaTag := testTagValue(ev.Tags, TagNTIA)
	if ntiaTag != "compliant" {
		t.Errorf("ntia tag = %v, want 'compliant'", ntiaTag)
	}

	// Verify content is valid JSON.
	var parsedAtt domain.SBOMAttestation
	if err := json.Unmarshal([]byte(ev.Content), &parsedAtt); err != nil {
		t.Fatalf("Failed to parse event content: %v", err)
	}
}

func TestIndexPublisher_PublishIndex(t *testing.T) {
	mock := &mockNostrPublisher{}
	pub := NewIndexPublisher(mock)

	entries := []domain.SBOMIndexEntry{
		{
			SubjectDigest: "sha256:abc123",
			AttestationID: "event-id-1",
			Format:        domain.SBOMFormatSPDX,
			LocationURI:   "https://blossom.example.com/abc123",
			StorageType:   domain.SBOMStorageBlossom,
			GeneratorID:   "syft",
			Timestamp:     time.Now(),
		},
		{
			SubjectDigest: "sha256:def456",
			AttestationID: "event-id-2",
			Format:        domain.SBOMFormatCycloneDX,
			LocationURI:   "sha256:oci-ref",
			StorageType:   domain.SBOMStorageOCI,
			GeneratorID:   "trivy",
			Timestamp:     time.Now(),
		},
	}

	eventID, err := pub.PublishIndex(context.Background(), PublishIndexInput{
		SubjectType: "artifact",
		SubjectID:   "myapp-v1.0.0",
		Entries:     entries,
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

	ev := mock.events[0]

	// Check event kind.
	if int(ev.Kind) != KindSBOMIndex {
		t.Errorf("Event kind = %d, want %d", ev.Kind, KindSBOMIndex)
	}

	// Check d-tag format.
	dTag := testTagValue(ev.Tags, TagDIdentifier)
	if dTag == "" {
		t.Fatal("Missing d-tag")
	}
	expectedDTag := "sbom:index:artifact:myapp-v1.0.0"
	if dTag != expectedDTag {
		t.Errorf("d-tag = %q, want %q", dTag, expectedDTag)
	}

	// Check SBOM reference tags.
	sbomTags := testTagsByName(ev.Tags, TagSBOMRef)
	if len(sbomTags) != 2 {
		t.Errorf("Expected 2 sbom tags, got %d", len(sbomTags))
	}

	// Verify content is valid JSON.
	var parsedIndex domain.SBOMIndex
	if err := json.Unmarshal([]byte(ev.Content), &parsedIndex); err != nil {
		t.Fatalf("Failed to parse event content: %v", err)
	}

	if parsedIndex.SubjectType != "artifact" {
		t.Errorf("SubjectType = %q, want %q", parsedIndex.SubjectType, "artifact")
	}
	if len(parsedIndex.Entries) != 2 {
		t.Errorf("Entries count = %d, want 2", len(parsedIndex.Entries))
	}
}

func TestBuildIndexEntry(t *testing.T) {
	att := &domain.SBOMAttestation{
		Subject: []domain.AttestationSubject{
			{Name: "test", Digest: map[string]string{"sha256": "abc123"}},
		},
		Predicate: domain.SBOMPredicate{
			Format: domain.SBOMFormatSPDX,
			Location: domain.SBOMLocation{
				Type: domain.SBOMStorageBlossom,
				URI:  "https://example.com/abc123",
			},
			Generator: domain.SBOMGenerator{ID: "syft"},
			Timestamp: time.Now(),
		},
	}

	entry, err := BuildIndexEntry(att, "event-123")
	if err != nil {
		t.Fatalf("BuildIndexEntry failed: %v", err)
	}

	if entry.SubjectDigest != "sha256:abc123" {
		t.Errorf("SubjectDigest = %q, want %q", entry.SubjectDigest, "sha256:abc123")
	}
	if entry.AttestationID != "event-123" {
		t.Errorf("AttestationID = %q, want %q", entry.AttestationID, "event-123")
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
		SubjectType: "service",
		SubjectID:   "my-service",
		Entries: []domain.SBOMIndexEntry{
			{SubjectDigest: "sha256:abc123", Format: domain.SBOMFormatSPDX},
		},
		UpdatedAt: time.Now().UTC(),
	}

	content, _ := json.Marshal(index)

	ev := &nostr.Event{
		Kind:    KindSBOMIndex,
		Content: string(content),
	}

	parsed, err := ParseIndexFromEvent(ev)
	if err != nil {
		t.Fatalf("ParseIndexFromEvent failed: %v", err)
	}

	if parsed.SubjectType != "service" {
		t.Errorf("SubjectType = %q, want %q", parsed.SubjectType, "service")
	}
	if len(parsed.Entries) != 1 {
		t.Errorf("Entries count = %d, want 1", len(parsed.Entries))
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
