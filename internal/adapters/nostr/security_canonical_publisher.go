package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

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
// It is the canonical security store (audit B-32): targets, runs, schedules
// and findings are published here before any SQL index is written, and the
// List methods read them back from the daemon's own retained records in the
// local event store. A run record on "security:run:<run-id>" is the signed
// claim of one scan; the scheduler derives that id from the schedule and its
// due time, so concurrent wakeups address one coordinate.
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
		return fmt.Errorf("security canonical projector is unavailable")
	}
	dTag := SecurityFindingDTag(finding.FindingKeyHash)
	tags, content := SecurityFindingRecordContent(&finding)
	return p.publishConfidential(ctx, KindSecurityFindingRecord, dTag, false, tags, content, "security_finding.projection", &finding.ID)
}

// PublishSchedule publishes a single security scan schedule as a confidential
// 30900 cp-state record.
func (p *SecurityCanonicalPublisher) PublishSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if schedule == nil {
		return nil
	}
	if p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("security canonical projector is unavailable")
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
		return fmt.Errorf("security canonical projector is unavailable")
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
	// Publish a base-coordinate manifest last. Readers use it to choose the
	// current fixed part set and ignore stale extra parts from an older, larger
	// detail. Parts are replaced in place, so a part publish that fails leaves
	// a mixed set the manifest does not describe; readers then report no
	// detail for the finding until a retry completes the set.
	tags, content := SecurityFindingDetailManifestContent(finding.FindingKeyHash, finding.OSVID, len(chunks))
	return p.publishConfidential(ctx, KindSecurityFindingDetailRecord, SecurityFindingDetailDTag(finding.FindingKeyHash), false, tags, content, "security_finding_detail.projection", &finding.ID)
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

	return p.projector.publishCanonicalFirst(ctx, legacyKind, dTag, deleted, extraTags, content, encrypted, entityType, entityID)
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

// SecurityFindingDetailManifestContent describes the fixed part coordinates
// that make up the current detail. The manifest is published only after every
// part has been admitted, so a failed update leaves the previous complete set
// readable instead of exposing a mixture of generations.
func SecurityFindingDetailManifestContent(findingKeyHash, osvID string, totalParts int) (gonostr.Tags, string) {
	tags := gonostr.Tags{{"finding_key_hash", findingKeyHash}, {"manifest", "true"}, {"total_parts", strconv.Itoa(totalParts)}}
	if osvID != "" {
		tags = append(tags, gonostr.Tag{"osv_id", osvID})
	}
	content, _ := json.Marshal(map[string]any{"finding_key_hash": findingKeyHash, "total_parts": totalParts})
	return tags, string(content)
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
		for end > 0 && !utf8.ValidString(s[:end]) {
			end--
		}
		if end == 0 {
			_, size := utf8.DecodeRuneInString(s)
			end = size
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
		"created_at":       finding.CreatedAt, "updated_at": finding.UpdatedAt,
		"withdrawn_at": finding.WithdrawnAt, "raw_modified": finding.RawModified,
		"metadata": finding.Metadata,
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
		"id": schedule.ID.String(), "policy_id": schedule.PolicyID.String(),
		"target_id": schedule.TargetID.String(), "target_key_hash": schedule.TargetKeyHash,
		"enabled": schedule.Enabled, "metadata": schedule.Metadata, "last_run_id": schedule.LastRunID,
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

// PublishTarget and PublishRun carry the complete execution inputs and progress
// on stable, replaceable coordinates; neither SQL nor a process-local claim is
// needed to resume a scan after restart.
func (p *SecurityCanonicalPublisher) PublishTarget(ctx context.Context, target *domain.SecurityTarget) error {
	if target == nil || p == nil || p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("security target canonical publisher is unavailable")
	}
	content, err := json.Marshal(target)
	if err != nil {
		return err
	}
	d := "security:target:" + target.TargetKeyHash
	tags := gonostr.Tags{{"target_key_hash", target.TargetKeyHash}, {"target_type", string(target.Type)}}
	return p.publishConfidential(ctx, KindSecurityTargetRecord, d, false, tags, string(content), "security_target.projection", &target.ID)
}
func (p *SecurityCanonicalPublisher) PublishRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if run == nil || p == nil || p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("security run canonical publisher is unavailable")
	}
	content, err := json.Marshal(run)
	if err != nil {
		return err
	}
	d := "security:run:" + run.ID.String()
	tags := gonostr.Tags{{"run_id", run.ID.String()}, {"target_key_hash", run.TargetKeyHash}, {"status", string(run.Status)}}
	return p.publishConfidential(ctx, KindSecurityRunRecord, d, false, tags, string(content), "security_run.projection", &run.ID)
}

// listState returns the decrypted content of the daemon's retained records of
// one security family. match, when non-nil, selects records by their public
// tags before anything is decrypted. A record that cannot be decrypted fails
// the read: scheduling and claims must not act on a partial view.
func (p *SecurityCanonicalPublisher) listState(ctx context.Context, kind int, match func(gonostr.Tags) bool) ([][]byte, error) {
	if p == nil || p.projector == nil || p.projector.history == nil || p.encryptor == nil {
		return nil, fmt.Errorf("security canonical local view is unavailable")
	}
	family := cpStateFamilies[kind]
	records, err := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("security %s view reached history limit", family.entity)
	}
	out := make([][]byte, 0, len(records))
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(kind) || isTombstoneTags(tags) {
			continue
		}
		if match != nil && !match(tags) {
			continue
		}
		d := tagValue(tags, "d")
		if d == "" {
			return nil, fmt.Errorf("security %s record %s lacks coordinate", family.entity, record.ID)
		}
		plaintext, err := p.encryptor.DecryptConfidential(ctx, record.Content, kind, d, family.topic)
		if err != nil {
			return nil, fmt.Errorf("decrypt security %s/%s: %w", family.entity, record.ID, err)
		}
		out = append(out, plaintext)
	}
	return out, nil
}

func decodeSecurityState[T any](raw [][]byte) ([]T, error) {
	out := make([]T, 0, len(raw))
	for _, b := range raw {
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// matchSecurityTags selects records whose public tags carry every non-empty
// wanted value.
func matchSecurityTags(want map[string]string) func(gonostr.Tags) bool {
	return func(tags gonostr.Tags) bool {
		for name, value := range want {
			if value != "" && tagValue(tags, name) != value {
				return false
			}
		}
		return true
	}
}

// ListSecurityTargets returns the retained scan targets; targetKeyHash, when
// set, selects one.
func (p *SecurityCanonicalPublisher) ListSecurityTargets(ctx context.Context, targetKeyHash string) ([]domain.SecurityTarget, error) {
	raw, err := p.listState(ctx, KindSecurityTargetRecord, matchSecurityTags(map[string]string{"target_key_hash": targetKeyHash}))
	if err != nil {
		return nil, err
	}
	return decodeSecurityState[domain.SecurityTarget](raw)
}

// ListSecurityRuns returns the retained scan runs; runID (when not nil) and
// targetKeyHash (when set) narrow the result.
func (p *SecurityCanonicalPublisher) ListSecurityRuns(ctx context.Context, runID uuid.UUID, targetKeyHash string) ([]domain.SecurityScanRun, error) {
	want := map[string]string{"target_key_hash": targetKeyHash}
	if runID != uuid.Nil {
		want["run_id"] = runID.String()
	}
	raw, err := p.listState(ctx, KindSecurityRunRecord, matchSecurityTags(want))
	if err != nil {
		return nil, err
	}
	return decodeSecurityState[domain.SecurityScanRun](raw)
}

// ListSecuritySchedules returns every retained scan schedule.
func (p *SecurityCanonicalPublisher) ListSecuritySchedules(ctx context.Context) ([]domain.SecurityScanSchedule, error) {
	raw, err := p.listState(ctx, KindSecurityScheduleRecord, nil)
	if err != nil {
		return nil, err
	}
	return decodeSecurityState[domain.SecurityScanSchedule](raw)
}

// ListSecurityFindings returns the retained findings with their detail text
// reassembled; runID (when not nil) and targetKeyHash (when set) narrow the
// result. A finding coordinate holds the finding of the latest run that
// reported it.
func (p *SecurityCanonicalPublisher) ListSecurityFindings(ctx context.Context, runID uuid.UUID, targetKeyHash string) ([]domain.SecurityOSVFinding, error) {
	want := map[string]string{"target_key_hash": targetKeyHash}
	if runID != uuid.Nil {
		want["run_id"] = runID.String()
	}
	raw, err := p.listState(ctx, KindSecurityFindingRecord, matchSecurityTags(want))
	if err != nil {
		return nil, err
	}
	out, err := decodeSecurityState[domain.SecurityOSVFinding](raw)
	if err != nil || len(out) == 0 {
		return out, err
	}
	wanted := make(map[string]struct{}, len(out))
	for _, finding := range out {
		wanted[finding.FindingKeyHash] = struct{}{}
	}
	details, err := p.findingDetails(ctx, wanted)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Details = details[out[i].FindingKeyHash]
	}
	return out, nil
}

// findingDetailHash returns the finding key hash a detail coordinate belongs
// to and whether the coordinate is the base record (single detail, manifest
// or tombstone) rather than a numbered part.
func findingDetailHash(d string) (hash string, base, ok bool) {
	rest, found := strings.CutPrefix(d, "security:finding-detail:")
	if !found || rest == "" {
		return "", false, false
	}
	if hash, _, isPart := strings.Cut(rest, ":part:"); isPart {
		return hash, false, true
	}
	return rest, true, true
}

// findingDetails reassembles the detail text of the wanted findings from
// their detail records. The base coordinate decides: a tombstone means no
// detail, a single record carries the text, and a manifest names how many
// parts make up the current text, so stale parts of an older, longer detail
// are ignored. A part set the manifest does not describe (a multi-part
// publish that stopped half way) yields no detail rather than a mixture.
func (p *SecurityCanonicalPublisher) findingDetails(ctx context.Context, wanted map[string]struct{}) (map[string]string, error) {
	family := cpStateFamilies[KindSecurityFindingDetailRecord]
	records, err := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("security finding detail view reached history limit")
	}
	type detailPart struct {
		index, total int
		text         string
	}
	type detailBase struct {
		text  string
		total int
	}
	parts := map[string][]detailPart{}
	bases := map[string]detailBase{}
	deleted := map[string]bool{}
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindSecurityFindingDetailRecord) {
			continue
		}
		d := tagValue(tags, "d")
		hash, isBase, ok := findingDetailHash(d)
		if !ok {
			continue
		}
		if _, want := wanted[hash]; !want {
			continue
		}
		if isTombstoneTags(tags) {
			if isBase {
				deleted[hash] = true
			}
			continue
		}
		plaintext, err := p.encryptor.DecryptConfidential(ctx, record.Content, KindSecurityFindingDetailRecord, d, family.topic)
		if err != nil {
			return nil, fmt.Errorf("decrypt security finding detail %s: %w", record.ID, err)
		}
		var detail struct {
			Details    string `json:"details"`
			PartIndex  int    `json:"part_index"`
			TotalParts int    `json:"total_parts"`
		}
		if err := json.Unmarshal(plaintext, &detail); err != nil {
			return nil, fmt.Errorf("decode security finding detail %s: %w", record.ID, err)
		}
		if detail.TotalParts == 0 {
			detail.TotalParts = 1
		}
		if isBase {
			bases[hash] = detailBase{text: detail.Details, total: detail.TotalParts}
			continue
		}
		parts[hash] = append(parts[hash], detailPart{detail.PartIndex, detail.TotalParts, detail.Details})
	}
	out := make(map[string]string, len(wanted))
	for hash := range wanted {
		if deleted[hash] {
			continue
		}
		base, hasBase := bases[hash]
		if hasBase && base.total <= 1 {
			out[hash] = base.text
			continue
		}
		group := parts[hash]
		if len(group) == 0 {
			continue
		}
		// Parts published before manifests existed describe themselves.
		total := group[0].total
		if hasBase {
			total = base.total
		}
		ordered := make([]string, total)
		seen := 0
		for _, part := range group {
			if part.total != total || part.index < 0 || part.index >= total {
				continue
			}
			ordered[part.index] = part.text
			seen++
		}
		if seen != total {
			p.logger.Warn("security finding detail is incomplete; reporting no detail", zap.String("finding_key_hash", hash), zap.Int("parts", seen), zap.Int("total_parts", total))
			continue
		}
		out[hash] = strings.Join(ordered, "")
	}
	return out, nil
}
