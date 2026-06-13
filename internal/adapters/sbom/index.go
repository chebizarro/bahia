// Package sbom provides SBOM parsing, attestation, and indexing for SPDX and CycloneDX formats.
package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// Nostr event kinds for SBOM references and availability lists.
const (
	// KindSBOMReference is a NIP-78 app-data event containing an SBOM reference attestation.
	// d-tag format: "sbom:ref:{subject_key}:{format}:{payload_sha256}"
	KindSBOMReference = 30078

	// KindSBOMAvailabilityList is a NIP-51 Curation Set listing available SBOM references for a subject.
	// d-tag format: "sbom:available:{subject_type}:{subject_key}"
	KindSBOMAvailabilityList = 30004

	// KindLegacySBOMIndex is read-only compatibility for historical SBOM index events.
	KindLegacySBOMIndex = 30079

	// KindSBOMAttestation is a compatibility alias for the canonical SBOM reference kind.
	KindSBOMAttestation = KindSBOMReference

	// KindSBOMIndex is a compatibility alias for the canonical SBOM availability-list kind.
	KindSBOMIndex = KindSBOMAvailabilityList
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
	TagDomain        = "domain"
	TagSchema        = "schema"
	TagSubjectType   = "subject_type"
	TagMediaType     = "media_type"
	TagPayloadHash   = "x"
	TagTitle         = "title"
	TagAReference    = "a"
	TagSBOMRef       = "sbom" // SBOM availability-list entry
)

// IndexPublisher publishes and manages SBOM index events on Nostr.
type IndexPublisher struct {
	publisher NostrPublisher
}

// NostrPublisher is the interface for publishing Nostr events.
type NostrPublisher interface {
	PublishSignedEvent(ctx context.Context, ev *nostr.Event) error
}

// PublishOKResult contains a relay OK outcome for one publish attempt.
type PublishOKResult struct {
	RelayURL string
	Accepted bool
	Reason   string
	Error    error
}

// OKVerifiedNostrPublisher publishes Nostr events and returns relay OK outcomes.
type OKVerifiedNostrPublisher interface {
	PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]PublishOKResult, error)
}

// NewIndexPublisher creates a new SBOM index publisher.
func NewIndexPublisher(publisher NostrPublisher) *IndexPublisher {
	return &IndexPublisher{publisher: publisher}
}

// BuildSBOMReferenceEventInput contains input for building a canonical 30078 SBOM reference event.
type BuildSBOMReferenceEventInput struct {
	Subject     domain.SBOMSubject
	Attestation *domain.SBOMAttestation
	CreatedAt   *time.Time
}

// BuildSBOMAvailabilityListEventInput contains input for building a canonical 30004 SBOM availability list.
type BuildSBOMAvailabilityListEventInput struct {
	Subject         domain.SBOMSubject
	Entries         []domain.SBOMIndexEntry
	PublisherPubkey string
	CreatedAt       *time.Time
}

// PublishAttestationInput contains input for publishing an SBOM attestation event.
type PublishAttestationInput struct {
	Subject     domain.SBOMSubject
	Attestation *domain.SBOMAttestation
	SubjectName string
	CreatedAt   *time.Time
}

// PublishIndexInput contains input for publishing/updating an SBOM availability list event.
type PublishIndexInput struct {
	Subject         domain.SBOMSubject
	SubjectType     string // compatibility: "artifact", "deployment", "package", "repository"
	SubjectID       string
	SubjectDigest   string
	SubjectName     string
	PublisherPubkey string
	Entries         []domain.SBOMIndexEntry
	CreatedAt       *time.Time
}

// BuildSBOMReferenceEvent builds a NIP-78 app-data SBOM reference event.
func BuildSBOMReferenceEvent(input BuildSBOMReferenceEventInput) (*nostr.Event, string, error) {
	if input.Attestation == nil {
		return nil, "", fmt.Errorf("attestation is required")
	}
	if err := validateSBOMSubject(input.Subject); err != nil {
		return nil, "", err
	}
	if len(input.Attestation.Subject) == 0 {
		return nil, "", fmt.Errorf("attestation must have at least one subject")
	}

	subjectDigest, err := attestationSubjectDigest(input.Attestation)
	if err != nil {
		return nil, "", err
	}
	if !strings.EqualFold(subjectDigest, input.Subject.Digest) {
		return nil, "", fmt.Errorf("attestation subject digest %q does not match subject digest %q", subjectDigest, input.Subject.Digest)
	}

	payloadSHA := input.Attestation.Predicate.Digest["sha256"]
	if err := validatePayloadSHA256(payloadSHA, "attestation predicate digest sha256"); err != nil {
		return nil, "", err
	}
	if input.Attestation.Predicate.Location.Type != domain.SBOMStorageBlossom {
		return nil, "", fmt.Errorf("SBOM reference storage must be blossom, got %q", input.Attestation.Predicate.Location.Type)
	}
	if input.Attestation.Predicate.Location.URI == "" {
		return nil, "", fmt.Errorf("SBOM reference location is required")
	}
	if input.Attestation.Predicate.Format != domain.SBOMFormatSPDX && input.Attestation.Predicate.Format != domain.SBOMFormatCycloneDX {
		return nil, "", fmt.Errorf("unsupported SBOM format: %s", input.Attestation.Predicate.Format)
	}
	generatorTag := generatorTag(input.Attestation.Predicate.Generator)
	if generatorTag == "" {
		return nil, "", fmt.Errorf("SBOM reference generator is required")
	}

	content, err := json.Marshal(input.Attestation)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling attestation: %w", err)
	}

	subjectKey := SubjectKey(input.Subject)
	dTag := fmt.Sprintf("sbom:ref:%s:%s:%s", subjectKey, input.Attestation.Predicate.Format, payloadSHA)
	mediaType := input.Attestation.Predicate.Location.MediaType
	if mediaType == "" {
		mediaType = MediaTypeForFormat(input.Attestation.Predicate.Format)
	}
	tags := nostr.Tags{
		{TagDIdentifier, dTag},
		{TagDomain, "sbom"},
		{TagSchema, "bahia.sbom.ref.v1"},
		{TagSubjectType, string(input.Subject.Type)},
		{TagSubjectDigest, input.Subject.Digest},
		{TagFormat, string(input.Attestation.Predicate.Format)},
		{TagStorageType, string(input.Attestation.Predicate.Location.Type)},
		{TagLocation, input.Attestation.Predicate.Location.URI},
		{TagPayloadHash, payloadSHA},
		{TagMediaType, mediaType},
		{TagGenerator, generatorTag},
		{TagNTIA, ntiaStatus(input.Attestation.Predicate.NTIA)},
	}
	tags = append(tags, resourceTag(input.Subject))

	ev := &nostr.Event{
		Kind:      KindSBOMReference,
		Content:   string(content),
		CreatedAt: eventTimestamp(input.CreatedAt),
		Tags:      tags,
	}
	return ev, dTag, nil
}

// BuildSBOMAvailabilityListEvent builds a NIP-51 30004 complete availability list for one subject version.
func BuildSBOMAvailabilityListEvent(input BuildSBOMAvailabilityListEventInput) (*nostr.Event, string, error) {
	if err := validateSBOMSubject(input.Subject); err != nil {
		return nil, "", err
	}
	if input.PublisherPubkey == "" {
		return nil, "", fmt.Errorf("publisher pubkey is required")
	}

	subjectKey := SubjectKey(input.Subject)
	dTag := fmt.Sprintf("sbom:available:%s:%s", input.Subject.Type, subjectKey)
	titleSubject := input.Subject.DisplayName
	if titleSubject == "" {
		titleSubject = input.Subject.ID
	}

	entries := dedupeSBOMIndexEntries(input.Entries)
	index := domain.SBOMIndex{
		SubjectType: string(input.Subject.Type),
		SubjectID:   input.Subject.ID,
		Entries:     entries,
		UpdatedAt:   time.Unix(int64(eventTimestamp(input.CreatedAt)), 0).UTC(),
	}
	content, err := json.Marshal(index)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling index: %w", err)
	}

	tags := nostr.Tags{
		{TagDIdentifier, dTag},
		{TagTitle, fmt.Sprintf("SBOMs for %s", titleSubject)},
		{TagDomain, "sbom"},
		{TagSchema, "bahia.sbom.available-list.v1"},
		{TagSubjectType, string(input.Subject.Type)},
		{TagSubjectDigest, input.Subject.Digest},
	}
	for _, entry := range entries {
		if err := validateSBOMIndexEntry(input.Subject, entry); err != nil {
			return nil, "", err
		}
		refDTag := entry.ReferenceDTag
		if refDTag == "" && strings.HasPrefix(entry.AttestationID, fmt.Sprintf("%d:%s:", KindSBOMReference, input.PublisherPubkey)) {
			refDTag = strings.TrimPrefix(entry.AttestationID, fmt.Sprintf("%d:%s:", KindSBOMReference, input.PublisherPubkey))
		}
		if refDTag == "" {
			return nil, "", fmt.Errorf("SBOM availability entry reference d tag is required")
		}
		coordinate := fmt.Sprintf("%d:%s:%s", KindSBOMReference, input.PublisherPubkey, refDTag)
		tags = append(tags, nostr.Tag{TagAReference, coordinate})
		tags = append(tags, nostr.Tag{
			TagSBOMRef,
			entry.SubjectDigest,
			string(entry.Format),
			string(entry.StorageType),
			entry.LocationURI,
			entry.PayloadSHA256,
			entry.GeneratorID,
			refDTag,
		})
	}

	ev := &nostr.Event{
		Kind:      KindSBOMAvailabilityList,
		Content:   string(content),
		CreatedAt: eventTimestamp(input.CreatedAt),
		Tags:      tags,
	}
	return ev, dTag, nil
}

// PublishAttestation publishes an SBOM reference as a NIP-78 app-data event after relay OK acceptance.
// Returns the event ID.
func (p *IndexPublisher) PublishAttestation(ctx context.Context, input PublishAttestationInput) (string, error) {
	if input.Subject.ID == "" && input.SubjectName != "" {
		input.Subject.ID = input.SubjectName
	}
	ev, _, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{
		Subject:     input.Subject,
		Attestation: input.Attestation,
		CreatedAt:   input.CreatedAt,
	})
	if err != nil {
		return "", err
	}
	if err := p.publishSignedEventVerified(ctx, ev, "SBOM reference"); err != nil {
		return "", err
	}
	return nostrutil.EventIDHex(ev), nil
}

// PublishIndex publishes or updates the complete SBOM availability list as a NIP-51 Curation Set after relay OK acceptance.
// Returns the event ID.
func (p *IndexPublisher) PublishIndex(ctx context.Context, input PublishIndexInput) (string, error) {
	subject := input.Subject
	if subject.Type == "" {
		subject.Type = domain.SBOMSubjectType(input.SubjectType)
	}
	if subject.ID == "" {
		subject.ID = input.SubjectID
	}
	if subject.Digest == "" {
		subject.Digest = input.SubjectDigest
	}
	if subject.DisplayName == "" {
		subject.DisplayName = input.SubjectName
	}

	ev, _, err := BuildSBOMAvailabilityListEvent(BuildSBOMAvailabilityListEventInput{
		Subject:         subject,
		Entries:         input.Entries,
		PublisherPubkey: input.PublisherPubkey,
		CreatedAt:       input.CreatedAt,
	})
	if err != nil {
		return "", err
	}
	if err := p.publishSignedEventVerified(ctx, ev, "SBOM availability list"); err != nil {
		return "", err
	}
	return nostrutil.EventIDHex(ev), nil
}

func (p *IndexPublisher) publishSignedEventVerified(ctx context.Context, ev *nostr.Event, label string) error {
	if p == nil || p.publisher == nil {
		return fmt.Errorf("%s publisher is required", label)
	}
	results, err := publishSignedEventWithOKResults(ctx, p.publisher, ev)
	if err != nil {
		return fmt.Errorf("publishing %s event: %w", label, err)
	}
	if err := requireAcceptedOK(label, results); err != nil {
		return err
	}
	return nil
}

func publishSignedEventWithOKResults(ctx context.Context, publisher NostrPublisher, ev *nostr.Event) ([]PublishOKResult, error) {
	if verified, ok := publisher.(OKVerifiedNostrPublisher); ok {
		return verified.PublishSignedEventWithResults(ctx, ev)
	}
	method := reflect.ValueOf(publisher).MethodByName("PublishSignedEventWithResults")
	if !method.IsValid() {
		return nil, fmt.Errorf("publisher does not expose relay OK results")
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(ev)})
	if len(results) != 2 {
		return nil, fmt.Errorf("publisher returned invalid relay OK result arity")
	}
	if !results[1].IsNil() {
		if err, ok := results[1].Interface().(error); ok {
			return nil, err
		}
		return nil, fmt.Errorf("publisher returned non-error failure value")
	}
	return convertPublishOKResults(results[0])
}

func convertPublishOKResults(value reflect.Value) ([]PublishOKResult, error) {
	if !value.IsValid() || value.IsNil() || value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("publisher returned invalid relay OK results")
	}
	out := make([]PublishOKResult, 0, value.Len())
	for i := 0; i < value.Len(); i++ {
		item := value.Index(i)
		if item.Kind() == reflect.Pointer {
			if item.IsNil() {
				return nil, fmt.Errorf("publisher returned nil relay OK result at index %d", i)
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.Struct {
			return nil, fmt.Errorf("publisher returned non-struct relay OK result at index %d", i)
		}
		converted, err := convertPublishOKResult(item)
		if err != nil {
			return nil, fmt.Errorf("publisher relay OK result %d: %w", i, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

func convertPublishOKResult(item reflect.Value) (PublishOKResult, error) {
	relayURL, err := stringField(item, "RelayURL")
	if err != nil {
		return PublishOKResult{}, err
	}
	accepted, err := boolField(item, "Accepted")
	if err != nil {
		return PublishOKResult{}, err
	}
	reason, err := stringField(item, "Reason")
	if err != nil {
		return PublishOKResult{}, err
	}
	errorValue, err := errorField(item, "Error")
	if err != nil {
		return PublishOKResult{}, err
	}
	return PublishOKResult{RelayURL: relayURL, Accepted: accepted, Reason: reason, Error: errorValue}, nil
}

func stringField(item reflect.Value, name string) (string, error) {
	field := item.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", fmt.Errorf("missing string field %s", name)
	}
	return field.String(), nil
}

func boolField(item reflect.Value, name string) (bool, error) {
	field := item.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Bool {
		return false, fmt.Errorf("missing bool field %s", name)
	}
	return field.Bool(), nil
}

func errorField(item reflect.Value, name string) (error, error) {
	field := item.FieldByName(name)
	if !field.IsValid() {
		return nil, fmt.Errorf("missing error field %s", name)
	}
	if field.IsNil() {
		return nil, nil
	}
	err, ok := field.Interface().(error)
	if !ok {
		return nil, fmt.Errorf("field %s is not an error", name)
	}
	return err, nil
}

func requireAcceptedOK(label string, results []PublishOKResult) error {
	if len(results) == 0 {
		return fmt.Errorf("publishing %s event: no relay OK results", label)
	}
	rejections := make([]string, 0)
	for _, result := range results {
		if result.Accepted {
			return nil
		}
		relay := result.RelayURL
		if relay == "" {
			relay = "unknown relay"
		}
		switch {
		case result.Reason != "":
			rejections = append(rejections, fmt.Sprintf("%s rejected event: %s", relay, result.Reason))
		case result.Error != nil:
			rejections = append(rejections, fmt.Sprintf("%s publish error: %v", relay, result.Error))
		default:
			rejections = append(rejections, fmt.Sprintf("%s returned OK accepted=false without reason", relay))
		}
	}
	return fmt.Errorf("publishing %s event: no relay accepted event: %s", label, strings.Join(rejections, "; "))
}

// BuildIndexEntry creates an index entry from an attestation.
func BuildIndexEntry(att *domain.SBOMAttestation, attestationEventID string) (*domain.SBOMIndexEntry, error) {
	if att == nil || len(att.Subject) == 0 {
		return nil, fmt.Errorf("invalid attestation")
	}

	subjectDigest, err := attestationSubjectDigest(att)
	if err != nil {
		return nil, err
	}
	payloadSHA := att.Predicate.Digest["sha256"]

	entry := &domain.SBOMIndexEntry{
		SubjectDigest: subjectDigest,
		AttestationID: attestationEventID,
		Format:        att.Predicate.Format,
		LocationURI:   att.Predicate.Location.URI,
		StorageType:   att.Predicate.Location.Type,
		GeneratorID:   att.Predicate.Generator.ID,
		PayloadSHA256: payloadSHA,
		Timestamp:     att.Predicate.Timestamp,
	}

	return entry, nil
}

// ParseIndexFromEvent parses an SBOM availability list from a canonical event or historical read-only index event.
func ParseIndexFromEvent(ev *nostr.Event) (*domain.SBOMIndex, error) {
	if int(ev.Kind) != KindSBOMAvailabilityList && int(ev.Kind) != KindLegacySBOMIndex {
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

// FilterForArtifactSBOMs returns a Nostr filter for finding canonical SBOM availability lists for an artifact.
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

// SubjectKey returns the deterministic subject key used in SBOM d tags.
func SubjectKey(subject domain.SBOMSubject) string {
	parts := []string{string(subject.Type), subject.ID, subject.Digest}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func validateSBOMSubject(subject domain.SBOMSubject) error {
	switch subject.Type {
	case domain.SBOMSubjectArtifact, domain.SBOMSubjectDeployment, domain.SBOMSubjectPackage, domain.SBOMSubjectRepository:
	default:
		return fmt.Errorf("invalid SBOM subject type: %q", subject.Type)
	}
	if subject.ID == "" {
		return fmt.Errorf("SBOM subject ID is required")
	}
	if subject.Digest == "" {
		return fmt.Errorf("SBOM subject digest is required")
	}
	if _, _, err := parseDigest(subject.Digest); err != nil {
		return fmt.Errorf("invalid SBOM subject digest: %w", err)
	}
	return nil
}

func validateSBOMIndexEntry(subject domain.SBOMSubject, entry domain.SBOMIndexEntry) error {
	if !strings.EqualFold(entry.SubjectDigest, subject.Digest) {
		return fmt.Errorf("SBOM availability entry subject digest %q does not match subject digest %q", entry.SubjectDigest, subject.Digest)
	}
	if entry.Format != domain.SBOMFormatSPDX && entry.Format != domain.SBOMFormatCycloneDX {
		return fmt.Errorf("unsupported SBOM availability entry format: %s", entry.Format)
	}
	if entry.StorageType != domain.SBOMStorageBlossom {
		return fmt.Errorf("SBOM availability entry storage must be blossom, got %q", entry.StorageType)
	}
	if entry.LocationURI == "" {
		return fmt.Errorf("SBOM availability entry location is required")
	}
	if err := validatePayloadSHA256(entry.PayloadSHA256, "SBOM availability entry payload SHA-256"); err != nil {
		return err
	}
	if entry.GeneratorID == "" {
		return fmt.Errorf("SBOM availability entry generator ID is required")
	}
	return nil
}

func validatePayloadSHA256(value, field string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) != 64 {
		return fmt.Errorf("%s must be a 64-character hex SHA-256", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be hex: %w", field, err)
	}
	return nil
}

func dedupeSBOMIndexEntries(entries []domain.SBOMIndexEntry) []domain.SBOMIndexEntry {
	seen := make(map[string]struct{}, len(entries))
	out := make([]domain.SBOMIndexEntry, 0, len(entries))
	for _, entry := range entries {
		key := strings.Join([]string{
			entry.SubjectDigest,
			string(entry.Format),
			string(entry.StorageType),
			entry.LocationURI,
			entry.PayloadSHA256,
			entry.GeneratorID,
		}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.Join([]string{string(out[i].Format), out[i].GeneratorID, out[i].PayloadSHA256}, "\x00")
		right := strings.Join([]string{string(out[j].Format), out[j].GeneratorID, out[j].PayloadSHA256}, "\x00")
		return left < right
	})
	return out
}

func attestationSubjectDigest(att *domain.SBOMAttestation) (string, error) {
	if att == nil || len(att.Subject) == 0 {
		return "", fmt.Errorf("attestation must have at least one subject")
	}
	keys := make([]string, 0, len(att.Subject[0].Digest))
	for algo := range att.Subject[0].Digest {
		keys = append(keys, algo)
	}
	sort.Strings(keys)
	for _, algo := range keys {
		if hash := att.Subject[0].Digest[algo]; hash != "" {
			return fmt.Sprintf("%s:%s", algo, hash), nil
		}
	}
	return "", fmt.Errorf("attestation subject digest is required")
}

func generatorTag(generator domain.SBOMGenerator) string {
	if generator.ID == "" {
		return ""
	}
	if generator.Version == "" {
		return generator.ID
	}
	return generator.ID + "@" + generator.Version
}

func ntiaStatus(ntia *domain.NTIACompliance) string {
	if ntia == nil {
		return "unknown"
	}
	if ntia.IsCompliant {
		return "compliant"
	}
	return "partial"
}

func resourceTag(subject domain.SBOMSubject) nostr.Tag {
	switch subject.Type {
	case domain.SBOMSubjectArtifact:
		return nostr.Tag{"artifact", subject.ID}
	case domain.SBOMSubjectDeployment:
		return nostr.Tag{"deployment", subject.ID}
	case domain.SBOMSubjectPackage:
		return nostr.Tag{"package", subject.ID}
	case domain.SBOMSubjectRepository:
		return nostr.Tag{"repo", subject.ID}
	default:
		return nostr.Tag{"subject_id", subject.ID}
	}
}

func eventTimestamp(createdAt *time.Time) nostr.Timestamp {
	if createdAt == nil {
		return nostr.Timestamp(time.Now().Unix())
	}
	return nostr.Timestamp(createdAt.UTC().Unix())
}
