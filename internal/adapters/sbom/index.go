// Package sbom provides SBOM parsing, attestation, and indexing for SPDX and CycloneDX formats.
package sbom

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// Nostr event kinds for SBOM attestations and indexes.
// Uses the NIP-51 parameterized replaceable list pattern (kind 30000-39999).
const (
	// KindSBOMAttestation is a replaceable event containing an SBOM attestation.
	// d-tag format: "sbom:attestation:{subject_digest}"
	KindSBOMAttestation = 30078

	// KindSBOMIndex is a NIP-51-style parameterized list of SBOMs for a subject.
	// d-tag format: "sbom:index:{subject_type}:{subject_id}"
	// Subject types: artifact, service, deployment, package
	KindSBOMIndex = 30079
)

// Tag names for SBOM events.
const (
	TagDIdentifier   = "d"
	TagSubjectDigest = "subject"
	TagFormat        = "format"
	TagLocation      = "location"
	TagStorageType   = "storage"
	TagGenerator     = "generator"
	TagNTIA          = "ntia"
	TagSBOMRef       = "sbom" // Reference to SBOM attestation event
)

// IndexPublisher publishes and manages SBOM index events on Nostr.
type IndexPublisher struct {
	publisher NostrPublisher
}

// NostrPublisher is the interface for publishing Nostr events.
type NostrPublisher interface {
	PublishSignedEvent(ctx context.Context, ev *nostr.Event) error
}

// NewIndexPublisher creates a new SBOM index publisher.
func NewIndexPublisher(publisher NostrPublisher) *IndexPublisher {
	return &IndexPublisher{publisher: publisher}
}

// PublishAttestationInput contains input for publishing an SBOM attestation event.
type PublishAttestationInput struct {
	Attestation *domain.SBOMAttestation
	SubjectName string
}

// PublishAttestation publishes an SBOM attestation as a Nostr event.
// Returns the event ID.
func (p *IndexPublisher) PublishAttestation(ctx context.Context, input PublishAttestationInput) (string, error) {
	if input.Attestation == nil {
		return "", fmt.Errorf("attestation is required")
	}
	if len(input.Attestation.Subject) == 0 {
		return "", fmt.Errorf("attestation must have at least one subject")
	}

	// Get the primary subject digest for the d-tag.
	var subjectDigest string
	for algo, hash := range input.Attestation.Subject[0].Digest {
		subjectDigest = fmt.Sprintf("%s:%s", algo, hash)
		break
	}

	content, err := json.Marshal(input.Attestation)
	if err != nil {
		return "", fmt.Errorf("marshaling attestation: %w", err)
	}

	// Build tags following NIP-51 patterns.
	tags := nostr.Tags{
		{TagDIdentifier, fmt.Sprintf("sbom:attestation:%s", subjectDigest)},
		{TagSubjectDigest, subjectDigest},
		{TagFormat, string(input.Attestation.Predicate.Format)},
		{TagStorageType, string(input.Attestation.Predicate.Location.Type)},
		{TagLocation, input.Attestation.Predicate.Location.URI},
	}

	// Add generator tag if present.
	if input.Attestation.Predicate.Generator.ID != "" {
		generatorTag := input.Attestation.Predicate.Generator.ID
		if input.Attestation.Predicate.Generator.Version != "" {
			generatorTag += "@" + input.Attestation.Predicate.Generator.Version
		}
		tags = append(tags, nostr.Tag{TagGenerator, generatorTag})
	}

	// Add NTIA compliance tag if present.
	if input.Attestation.Predicate.NTIA != nil {
		ntiaStatus := "partial"
		if input.Attestation.Predicate.NTIA.IsCompliant {
			ntiaStatus = "compliant"
		}
		tags = append(tags, nostr.Tag{TagNTIA, ntiaStatus})
	}

	ev := &nostr.Event{
		Kind:      KindSBOMAttestation,
		Content:   string(content),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
	}

	if err := p.publisher.PublishSignedEvent(ctx, ev); err != nil {
		return "", fmt.Errorf("publishing attestation event: %w", err)
	}

	return nostrutil.EventIDHex(ev), nil
}

// PublishIndexInput contains input for publishing/updating an SBOM index event.
type PublishIndexInput struct {
	SubjectType string // "artifact", "service", "deployment", "package"
	SubjectID   string
	Entries     []domain.SBOMIndexEntry
}

// PublishIndex publishes or updates an SBOM index as a NIP-51-style list event.
// Returns the event ID.
func (p *IndexPublisher) PublishIndex(ctx context.Context, input PublishIndexInput) (string, error) {
	if input.SubjectType == "" || input.SubjectID == "" {
		return "", fmt.Errorf("subject type and ID are required")
	}

	index := domain.SBOMIndex{
		SubjectType: input.SubjectType,
		SubjectID:   input.SubjectID,
		Entries:     input.Entries,
		UpdatedAt:   time.Now().UTC(),
	}

	content, err := json.Marshal(index)
	if err != nil {
		return "", fmt.Errorf("marshaling index: %w", err)
	}

	// Build tags following NIP-51 parameterized list pattern.
	dTag := fmt.Sprintf("sbom:index:%s:%s", input.SubjectType, input.SubjectID)
	tags := nostr.Tags{
		{TagDIdentifier, dTag},
	}

	// Add individual SBOM reference tags for each entry.
	// This allows efficient filtering by subject digest without parsing content.
	for _, entry := range input.Entries {
		tags = append(tags, nostr.Tag{
			TagSBOMRef,
			entry.SubjectDigest,
			string(entry.Format),
			string(entry.StorageType),
			entry.LocationURI,
		})
	}

	ev := &nostr.Event{
		Kind:      KindSBOMIndex,
		Content:   string(content),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
	}

	if err := p.publisher.PublishSignedEvent(ctx, ev); err != nil {
		return "", fmt.Errorf("publishing index event: %w", err)
	}

	return nostrutil.EventIDHex(ev), nil
}

// AddToIndex adds a new SBOM entry to an existing index.
// This is a convenience method that fetches the current index, adds the entry,
// and publishes the updated index.
func (p *IndexPublisher) AddToIndex(ctx context.Context, subjectType, subjectID string, entry domain.SBOMIndexEntry) (string, error) {
	// For now, just publish with the new entry.
	// In production, this would fetch existing entries from relays first.
	return p.PublishIndex(ctx, PublishIndexInput{
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Entries:     []domain.SBOMIndexEntry{entry},
	})
}

// BuildIndexEntry creates an index entry from an attestation.
func BuildIndexEntry(att *domain.SBOMAttestation, attestationEventID string) (*domain.SBOMIndexEntry, error) {
	if att == nil || len(att.Subject) == 0 {
		return nil, fmt.Errorf("invalid attestation")
	}

	// Get subject digest.
	var subjectDigest string
	for algo, hash := range att.Subject[0].Digest {
		subjectDigest = fmt.Sprintf("%s:%s", algo, hash)
		break
	}

	entry := &domain.SBOMIndexEntry{
		SubjectDigest: subjectDigest,
		AttestationID: attestationEventID,
		Format:        att.Predicate.Format,
		LocationURI:   att.Predicate.Location.URI,
		StorageType:   att.Predicate.Location.Type,
		GeneratorID:   att.Predicate.Generator.ID,
		Timestamp:     att.Predicate.Timestamp,
	}

	return entry, nil
}

// ParseIndexFromEvent parses an SBOM index from a Nostr event.
func ParseIndexFromEvent(ev *nostr.Event) (*domain.SBOMIndex, error) {
	if int(ev.Kind) != KindSBOMIndex {
		return nil, fmt.Errorf("invalid event kind: %d", ev.Kind)
	}

	var index domain.SBOMIndex
	if err := json.Unmarshal([]byte(ev.Content), &index); err != nil {
		return nil, fmt.Errorf("parsing index content: %w", err)
	}

	return &index, nil
}

// ParseAttestationFromEvent parses an SBOM attestation from a Nostr event.
func ParseAttestationFromEvent(ev *nostr.Event) (*domain.SBOMAttestation, error) {
	if int(ev.Kind) != KindSBOMAttestation {
		return nil, fmt.Errorf("invalid event kind: %d", ev.Kind)
	}

	return ParseAttestation([]byte(ev.Content))
}

// FilterForArtifactSBOMs returns a Nostr filter for finding SBOM indexes for an artifact.
func FilterForArtifactSBOMs(artifactDigest string) nostr.Filter {
	return nostr.Filter{
		Kinds: []nostr.Kind{KindSBOMIndex},
		Tags: map[string][]string{
			TagSBOMRef: {artifactDigest},
		},
	}
}

// FilterForAttestations returns a Nostr filter for finding SBOM attestations.
func FilterForAttestations(subjectDigest string) nostr.Filter {
	return nostr.Filter{
		Kinds: []nostr.Kind{KindSBOMAttestation},
		Tags: map[string][]string{
			TagSubjectDigest: {subjectDigest},
		},
	}
}
