package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
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

func (p *SecurityCanonicalPublisher) publishConfidential(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of security record")
	}

	// Security state is fleet-scoped (findings and schedules are operator-wide).
	encrypted, err := p.encryptor.EncryptConfidential(ctx, "fleet", []byte(content), legacyKind, dTag, topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt security state: %w", err)
	}

	return p.projector.publishControlState(ctx, legacyKind, dTag, deleted, extraTags, encrypted, entityType, entityID)
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
	// Details is intentionally omitted from the cp-state record to keep
	// individual events well within NIP-44 limits. The full details are
	// available through the ContextVM findings-list read.

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
