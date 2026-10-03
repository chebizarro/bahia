package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// SecurityCanonicalPublisher publishes authoritative security cp-state records
// through the shared signing/outbox pipeline. Content is OCK-encrypted under
// the "fleet" scope: security findings contain per-org vulnerability data and
// scan schedules contain policy configuration that must not appear in plaintext.
//
// Each finding is one 30900 record (d="security:finding:<hash>") so no single
// event exceeds NIP-44's 65,535-byte plaintext limit. This replaces the
// scanner's chunked 30078 findings path for cp-state consumers — the legacy
// chunked path continues to publish for backward compatibility.
//
// Schedules are one record per schedule (d="security:schedule:<id>").
//
// Warm-start covers the "security" domain automatically via CPStateDomains()
// since the security cp-state families share it with the scanner's existing
// summary/status families in the cpStateFamilies table.
//
// bahia-irsry.60: confidential cp-state for security findings and schedules.
type SecurityCanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	logger    *zap.Logger
}

// NewSecurityCanonicalPublisher creates a publisher backed by the given
// projector. encryptor is required; publishes fail closed without it.
func NewSecurityCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *SecurityCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecurityCanonicalPublisher{
		projector: projector,
		encryptor: encryptor,
		logger:    logger.Named("security-canonical"),
	}
}

// PublishFinding publishes a single security finding as a confidential 30900
// cp-state record. One record per finding ensures no single event exceeds
// NIP-44 limits (bahia-irsry.39 item 1).
func (p *SecurityCanonicalPublisher) PublishFinding(ctx context.Context, finding domain.SecurityOSVFinding) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	dTag := SecurityFindingDTag(finding.FindingKeyHash)
	tags, content := SecurityFindingRecordContent(&finding)
	return p.publishConfidential(ctx, KindSecurityFindingRecord, dTag, false, tags, content, "security_finding.projection", &finding.ID)
}

// PublishSchedule publishes a single security scan schedule as a confidential
// 30900 cp-state record.
func (p *SecurityCanonicalPublisher) PublishSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if p.projector == nil || !p.projector.Enabled() || schedule == nil {
		return nil
	}
	dTag := SecurityScheduleDTag(schedule.ID)
	tags, content := SecurityScheduleRecordContent(schedule)
	return p.publishConfidential(ctx, KindSecurityScheduleRecord, dTag, false, tags, content, "security_schedule.projection", &schedule.ID)
}

// nip44MaxPlaintext is the maximum plaintext size for a single NIP-44
// encrypted event. Chunks must stay below this after JSON envelope overhead.
const nip44MaxPlaintext = 65535

// detailChunkSize is the maximum detail text per chunk, leaving room for
// the JSON envelope (finding_key_hash, chunk index, total, etc.).
const detailChunkSize = 60000

// PublishFindingDetail publishes the Details field of a finding as one or
// more separate 30900 records (family 32014, d="security:finding-detail:<hash>"
// or "security:finding-detail:<hash>:part:<n>"). If the detail fits in a
// single record it is published as-is. If it exceeds detailChunkSize it is
// split into numbered parts with a "total_parts" field so consumers can
// reassemble. Empty details publish a tombstone (deleted=true) so any
// prior detail record is superseded.
func (p *SecurityCanonicalPublisher) PublishFindingDetail(ctx context.Context, finding domain.SecurityOSVFinding) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if finding.Details == "" {
		// Tombstone: no details to publish.
		dTag := SecurityFindingDetailDTag(finding.FindingKeyHash)
		return p.publishConfidential(ctx, KindSecurityFindingDetailRecord, dTag, true, nil, "{}", "security_finding_detail.projection", &finding.ID)
	}

	chunks := chunkString(finding.Details, detailChunkSize)
	if len(chunks) == 1 {
		// Single record — no chunking needed.
		dTag := SecurityFindingDetailDTag(finding.FindingKeyHash)
		tags, content := SecurityFindingDetailContent(finding.FindingKeyHash, finding.OSVID, finding.Details, 0, 1)
		return p.publishConfidential(ctx, KindSecurityFindingDetailRecord, dTag, false, tags, content, "security_finding_detail.projection", &finding.ID)
	}

	// Multi-part: publish each chunk as a separate addressable record.
	for i, chunk := range chunks {
		dTag := SecurityFindingDetailPartDTag(finding.FindingKeyHash, i)
		tags, content := SecurityFindingDetailContent(finding.FindingKeyHash, finding.OSVID, chunk, i, len(chunks))
		if err := p.publishConfidential(ctx, KindSecurityFindingDetailRecord, dTag, false, tags, content, "security_finding_detail.projection", &finding.ID); err != nil {
			return fmt.Errorf("publish finding detail part %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

func (p *SecurityCanonicalPublisher) publishConfidential(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of security record")
	}

	// Security state is fleet-scoped (findings and schedules are operator-wide).
	encrypted, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, []byte(content), legacyKind, dTag, topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt security state: %w", err)
	}

	return p.projector.publishControlState(ctx, legacyKind, dTag, deleted, extraTags, encrypted, entityType, entityID)
}

// SecurityFindingDetailDTag returns the d-tag for a finding's detail record.
func SecurityFindingDetailDTag(findingKeyHash string) string {
	return "security:finding-detail:" + findingKeyHash
}

// SecurityFindingDetailPartDTag returns the d-tag for a chunked finding
// detail part: "security:finding-detail:<hash>:part:<n>".
func SecurityFindingDetailPartDTag(findingKeyHash string, partIndex int) string {
	return "security:finding-detail:" + findingKeyHash + ":part:" + strconv.Itoa(partIndex)
}

// SecurityFindingDetailContent builds the tags and JSON content for a
// security finding detail cp-state record. partIndex and totalParts are
// used for chunked details; for single-record details pass 0 and 1.
func SecurityFindingDetailContent(findingKeyHash, osvID, detail string, partIndex, totalParts int) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"finding_key_hash", findingKeyHash},
	}
	if osvID != "" {
		tags = append(tags, gonostr.Tag{"osv_id", osvID})
	}
	if totalParts > 1 {
		tags = append(tags, gonostr.Tag{"part", strconv.Itoa(partIndex)})
		tags = append(tags, gonostr.Tag{"total_parts", strconv.Itoa(totalParts)})
	}

	payload := map[string]any{
		"finding_key_hash": findingKeyHash,
		"details":          detail,
	}
	if totalParts > 1 {
		payload["part_index"] = partIndex
		payload["total_parts"] = totalParts
	}

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}

// chunkString splits s into chunks of at most maxLen bytes.
func chunkString(s string, maxLen int) []string {
	if len(s) <= maxLen {
		return []string{s}
	}
	var chunks []string
	for len(s) > 0 {
		end := maxLen
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}

// SecurityFindingDTag returns the d-tag for a security finding:
// "security:finding:<hash>". The hash is the canonical finding key hash,
// so each unique finding has exactly one relay coordinate.
func SecurityFindingDTag(findingKeyHash string) string {
	return "security:finding:" + findingKeyHash
}

// SecurityScheduleDTag returns the d-tag for a security scan schedule.
func SecurityScheduleDTag(id uuid.UUID) string {
	return "security:schedule:" + id.String()
}

// SecurityFindingRecordContent builds the tags and JSON content for a
// security finding cp-state record. Each finding is one record — no chunking.
func SecurityFindingRecordContent(finding *domain.SecurityOSVFinding) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"osv_id", finding.OSVID},
		{"severity", string(finding.Severity)},
		{"target_key_hash", finding.TargetKeyHash},
	}
	if finding.RunID != uuid.Nil {
		tags = append(tags, gonostr.Tag{"run_id", finding.RunID.String()})
	}
	if finding.CVE != "" {
		tags = append(tags, gonostr.Tag{"cve", finding.CVE})
	}

	payload := map[string]any{
		"id":               finding.ID.String(),
		"run_id":           finding.RunID.String(),
		"target_key_hash":  finding.TargetKeyHash,
		"finding_key":      finding.FindingKey,
		"finding_key_hash": finding.FindingKeyHash,
		"osv_id":           finding.OSVID,
		"cve":              finding.CVE,
		"summary":          finding.Summary,
		"severity":         string(finding.Severity),
	}
	if finding.Package.Name != "" {
		payload["package"] = map[string]any{
			"ecosystem": finding.Package.Ecosystem,
			"name":      finding.Package.Name,
			"version":   finding.Package.Version,
			"purl":      finding.Package.PURL,
		}
	}
	if len(finding.Aliases) > 0 {
		payload["aliases"] = finding.Aliases
	}
	if len(finding.References) > 0 {
		payload["references"] = finding.References
	}
	// Details is published as a separate finding-detail record so that the
	// main finding record stays small and the relay copy is the source of
	// truth. See PublishFindingDetail.

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}

// SecurityScheduleRecordContent builds the tags and JSON content for a
// security scan schedule cp-state record.
func SecurityScheduleRecordContent(schedule *domain.SecurityScanSchedule) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"target_key_hash", schedule.TargetKeyHash},
	}
	if schedule.PolicyID != uuid.Nil {
		tags = append(tags, gonostr.Tag{"policy_id", schedule.PolicyID.String()})
	}
	if schedule.Enabled {
		tags = append(tags, gonostr.Tag{"enabled", "true"})
	} else {
		tags = append(tags, gonostr.Tag{"enabled", "false"})
	}

	payload := map[string]any{
		"id":              schedule.ID.String(),
		"policy_id":       schedule.PolicyID.String(),
		"target_key_hash": schedule.TargetKeyHash,
		"enabled":         schedule.Enabled,
	}
	if schedule.IntervalSeconds > 0 {
		payload["interval_seconds"] = schedule.IntervalSeconds
	}
	if !schedule.NextDueAt.IsZero() {
		payload["next_due_at"] = schedule.NextDueAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if schedule.LastDispatchedAt != nil && !schedule.LastDispatchedAt.IsZero() {
		payload["last_dispatched_at"] = schedule.LastDispatchedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !schedule.CreatedAt.IsZero() {
		payload["created_at"] = schedule.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !schedule.UpdatedAt.IsZero() {
		payload["updated_at"] = schedule.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}
