package nostr

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

// --- bahia-irsry.60 payment/security cp-state publisher tests ----------------
//
// Invariants tested:
//   - Publish-on-mutation: each mutation site publishes exactly one 30900 record.
//   - Legacy path: the original publish path is not disrupted.
//   - Confidentiality: all content passes through EncryptConfidential; no
//     plaintext org data appears in event content; sensitive fields (token_hash,
//     amounts, vulnerability details) are inside the encrypted envelope.
//   - Size bounds: individual finding records stay within NIP-44's 65,535-byte
//     plaintext limit even with large payloads (.39 item 1).

// --- Payment Canonical Publisher ---

func TestPaymentCanonicalPublisher_PublishOnMutation(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewPaymentCanonicalPublisher(p, enc, zap.NewNop())

	recID := uuid.New()
	rec := &domain.PaymentRecord{
		ID:              recID,
		DeploymentRunID: uuid.New(),
		WorkerPubkey:    "abc123",
		MintURL:         "https://mint.example.com",
		AmountSats:      1000,
		TokenHash:       "deadbeef",
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusPending,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	if err := pub.PublishPaymentRecord(ctx, rec); err != nil {
		t.Fatalf("PublishPaymentRecord: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	if len(records) != 1 {
		t.Fatalf("expected 1 event, got %d", len(records))
	}
	ev := records[0]

	// Check d-tag matches payment:<id>
	wantD := "payment:" + recID.String()
	if !hasTag(ev.Tags, "d", wantD) {
		t.Errorf("wrong d-tag: got %v, want d=%s", ev.Tags, wantD)
	}

	// Check legacy_kind
	if !hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindPaymentRecord)) {
		t.Errorf("missing legacy_kind: %v", ev.Tags)
	}

	// Check domain tag
	if !hasTag(ev.Tags, "domain", "payment") {
		t.Errorf("missing domain tag: %v", ev.Tags)
	}

	// Check topic tag
	if !hasTag(ev.Tags, "t", kinds.CPStateTopicPaymentRecord) {
		t.Errorf("missing t topic: %v", ev.Tags)
	}

	// Check encryptor was called with "fleet" scope
	if enc.lastOrgID != "fleet" {
		t.Errorf("encryptor org scope = %q, want %q", enc.lastOrgID, "fleet")
	}
}

func TestPaymentCanonicalPublisher_ContentIsEncrypted(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewPaymentCanonicalPublisher(p, enc, zap.NewNop())

	rec := &domain.PaymentRecord{
		ID:           uuid.New(),
		WorkerPubkey: "worker1",
		MintURL:      "https://mint.example.com",
		AmountSats:   500,
		TokenHash:    "secret_token_hash",
		Direction:    domain.PaymentDirectionChange,
		Status:       domain.PaymentStatusRedeemed,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := pub.PublishPaymentRecord(ctx, rec); err != nil {
		t.Fatalf("PublishPaymentRecord: %v", err)
	}

	ev := sink.byKind(KindCASControlState)[0]

	// The content should be the encrypted envelope, not raw JSON.
	var envelope map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if _, ok := envelope["_test_encrypted"]; !ok {
		t.Fatal("content was not passed through the encryptor")
	}

	// Verify that token_hash does NOT appear in any tag (only in encrypted content)
	for _, tag := range ev.Tags {
		for _, v := range tag {
			if strings.Contains(v, "secret_token_hash") {
				t.Errorf("token_hash leaked into tag %v", tag)
			}
		}
	}

	// Verify that amount does NOT appear in any tag
	for _, tag := range ev.Tags {
		for _, v := range tag {
			if v == "500" || strings.Contains(v, "amount") {
				t.Errorf("amount leaked into tag %v", tag)
			}
		}
	}
}

func TestPaymentCanonicalPublisher_NilEncryptorRefuses(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	pub := NewPaymentCanonicalPublisher(p, nil, zap.NewNop()) // nil encryptor

	rec := &domain.PaymentRecord{
		ID:           uuid.New(),
		WorkerPubkey: "w",
		Direction:    domain.PaymentDirectionPayment,
		Status:       domain.PaymentStatusPending,
	}

	err := pub.PublishPaymentRecord(ctx, rec)
	if err == nil {
		t.Fatal("expected error when encryptor is nil")
	}
	if !strings.Contains(err.Error(), "refusing plaintext") {
		t.Errorf("unexpected error: %v", err)
	}

	if len(sink.byKind(KindCASControlState)) != 0 {
		t.Fatal("should not have published any event without encryptor")
	}
}

func TestPaymentCanonicalPublisher_NilRecordNoOp(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewPaymentCanonicalPublisher(p, enc, zap.NewNop())

	if err := pub.PublishPaymentRecord(ctx, nil); err != nil {
		t.Fatalf("unexpected error for nil record: %v", err)
	}
	if len(sink.byKind(KindCASControlState)) != 0 {
		t.Fatal("should not publish for nil record")
	}
}

// --- Security Finding Canonical Publisher ---

func TestSecurityFindingPublisher_PublishOnMutation(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		RunID:          uuid.New(),
		TargetKeyHash:  "target_abc",
		FindingKeyHash: "hash_xyz",
		FindingKey:     "pkg:npm/lodash@4.17.20/CVE-2021-23337",
		OSVID:          "GHSA-xxx-yyy-zzz",
		CVE:            "CVE-2021-23337",
		Summary:        "Prototype Pollution in lodash",
		Details:        strings.Repeat("x", 10000), // large details omitted from record
		Severity:       domain.SecuritySeverityHigh,
		Package: domain.SecurityPackage{
			Ecosystem: "npm",
			Name:      "lodash",
			Version:   "4.17.20",
			PURL:      "pkg:npm/lodash@4.17.20",
		},
		Aliases: []string{"CVE-2021-23337"},
	}

	if err := pub.PublishFinding(ctx, finding); err != nil {
		t.Fatalf("PublishFinding: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	if len(records) != 1 {
		t.Fatalf("expected 1 event, got %d", len(records))
	}
	ev := records[0]

	// Check d-tag
	wantD := "security:finding:hash_xyz"
	if !hasTag(ev.Tags, "d", wantD) {
		t.Errorf("wrong d-tag: %v, want d=%s", ev.Tags, wantD)
	}

	// Check legacy_kind
	if !hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindSecurityFindingRecord)) {
		t.Errorf("missing legacy_kind: %v", ev.Tags)
	}

	// Check domain tag
	if !hasTag(ev.Tags, "domain", "security") {
		t.Errorf("missing domain tag: %v", ev.Tags)
	}

	// Check topic
	if !hasTag(ev.Tags, "t", kinds.CPStateTopicSecurityFinding) {
		t.Errorf("missing t topic: %v", ev.Tags)
	}

	// Check encryptor was called with "fleet" scope
	if enc.lastOrgID != "fleet" {
		t.Errorf("encryptor org scope = %q, want %q", enc.lastOrgID, "fleet")
	}
}

func TestSecurityFindingPublisher_ContentIsEncrypted(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		RunID:          uuid.New(),
		TargetKeyHash:  "target_secret",
		FindingKeyHash: "finding_hash",
		OSVID:          "GHSA-test-1234-5678",
		Summary:        "Test vulnerability",
		Severity:       domain.SecuritySeverityCritical,
	}

	if err := pub.PublishFinding(ctx, finding); err != nil {
		t.Fatalf("PublishFinding: %v", err)
	}

	ev := sink.byKind(KindCASControlState)[0]

	var envelope map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil {
		t.Fatalf("content not valid JSON: %v", err)
	}
	if _, ok := envelope["_test_encrypted"]; !ok {
		t.Fatal("content was not passed through the encryptor")
	}
}

func TestSecurityFindingPublisher_NilEncryptorRefuses(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	pub := NewSecurityCanonicalPublisher(p, nil, zap.NewNop())

	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		FindingKeyHash: "h",
		OSVID:          "GHSA-test",
		Severity:       domain.SecuritySeverityLow,
	}

	err := pub.PublishFinding(ctx, finding)
	if err == nil {
		t.Fatal("expected error when encryptor is nil")
	}
	if !strings.Contains(err.Error(), "refusing plaintext") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Security Schedule Canonical Publisher ---

func TestSecuritySchedulePublisher_PublishOnMutation(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	schedID := uuid.New()
	schedule := &domain.SecurityScanSchedule{
		ID:              schedID,
		PolicyID:        uuid.New(),
		TargetKeyHash:   "target_hash_1",
		Enabled:         true,
		IntervalSeconds: 86400,
		NextDueAt:       time.Now().Add(24 * time.Hour).UTC(),
	}

	if err := pub.PublishSchedule(ctx, schedule); err != nil {
		t.Fatalf("PublishSchedule: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	if len(records) != 1 {
		t.Fatalf("expected 1 event, got %d", len(records))
	}
	ev := records[0]

	// Check d-tag
	wantD := "security:schedule:" + schedID.String()
	if !hasTag(ev.Tags, "d", wantD) {
		t.Errorf("wrong d-tag: %v, want d=%s", ev.Tags, wantD)
	}

	// Check legacy_kind
	if !hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindSecurityScheduleRecord)) {
		t.Errorf("missing legacy_kind: %v", ev.Tags)
	}

	// Check topic
	if !hasTag(ev.Tags, "t", kinds.CPStateTopicSecuritySchedule) {
		t.Errorf("missing t topic: %v", ev.Tags)
	}

	// Check encryptor
	if enc.lastOrgID != "fleet" {
		t.Errorf("encryptor org scope = %q, want %q", enc.lastOrgID, "fleet")
	}
}

func TestSecuritySchedulePublisher_NilScheduleNoOp(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	if err := pub.PublishSchedule(ctx, nil); err != nil {
		t.Fatalf("unexpected error for nil schedule: %v", err)
	}
	if len(sink.byKind(KindCASControlState)) != 0 {
		t.Fatal("should not publish for nil schedule")
	}
}

// --- Size bounds: NIP-44 limit compliance (.39 item 1) ---

func TestSecurityFindingRecordContent_SizeBound(t *testing.T) {
	// Create a finding with large fields to ensure the serialized content
	// stays within NIP-44's 65,535-byte plaintext limit. The Details field
	// is intentionally omitted from cp-state records for this reason.
	finding := &domain.SecurityOSVFinding{
		ID:             uuid.New(),
		RunID:          uuid.New(),
		TargetKeyHash:  strings.Repeat("a", 64),
		FindingKey:     strings.Repeat("b", 500),
		FindingKeyHash: strings.Repeat("c", 64),
		OSVID:          "GHSA-" + strings.Repeat("x", 100),
		CVE:            "CVE-2024-" + strings.Repeat("9", 50),
		Summary:        strings.Repeat("s", 1000),
		Details:        strings.Repeat("d", 60000), // large details — MUST be omitted
		Severity:       domain.SecuritySeverityCritical,
		Package: domain.SecurityPackage{
			Ecosystem: strings.Repeat("e", 100),
			Name:      strings.Repeat("n", 200),
			Version:   strings.Repeat("v", 100),
			PURL:      strings.Repeat("p", 500),
		},
		Aliases:    make([]string, 50),
		References: make([]string, 50),
	}
	for i := range finding.Aliases {
		finding.Aliases[i] = "CVE-" + strings.Repeat("a", 20)
	}
	for i := range finding.References {
		finding.References[i] = "https://example.com/" + strings.Repeat("r", 100)
	}

	_, content := SecurityFindingRecordContent(finding)
	if len(content) >= 65535 {
		t.Errorf("finding record content size %d exceeds NIP-44 limit 65535", len(content))
	}

	// Verify Details was NOT included in the serialized content
	if strings.Contains(content, strings.Repeat("d", 100)) {
		t.Error("Details field should be omitted from cp-state record content")
	}
}

func TestPaymentRecordContent_SizeBound(t *testing.T) {
	rec := &domain.PaymentRecord{
		ID:              uuid.New(),
		DeploymentRunID: uuid.New(),
		WorkerPubkey:    strings.Repeat("w", 64),
		MintURL:         "https://mint.example.com/" + strings.Repeat("m", 200),
		AmountSats:      999999999,
		TokenHash:       strings.Repeat("t", 256),
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusPending,
		ErrorMessage:    strings.Repeat("e", 1000),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	_, content := PaymentRecordContent(rec)
	if len(content) >= 65535 {
		t.Errorf("payment record content size %d exceeds NIP-44 limit 65535", len(content))
	}
}

func TestSecurityScheduleRecordContent_SizeBound(t *testing.T) {
	now := time.Now().UTC()
	schedule := &domain.SecurityScanSchedule{
		ID:               uuid.New(),
		PolicyID:         uuid.New(),
		TargetKeyHash:    strings.Repeat("h", 64),
		Enabled:          true,
		IntervalSeconds:  86400,
		NextDueAt:        now.Add(24 * time.Hour),
		LastDispatchedAt: &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	_, content := SecurityScheduleRecordContent(schedule)
	if len(content) >= 65535 {
		t.Errorf("schedule record content size %d exceeds NIP-44 limit 65535", len(content))
	}
}

// --- D-tag coordinate tests ---

func TestPaymentDTag(t *testing.T) {
	id := uuid.MustParse("12345678-1234-1234-1234-123456789012")
	got := PaymentDTag(id)
	want := "payment:12345678-1234-1234-1234-123456789012"
	if got != want {
		t.Errorf("PaymentDTag = %q, want %q", got, want)
	}
}

func TestSecurityFindingDTag(t *testing.T) {
	got := SecurityFindingDTag("abc123")
	want := "security:finding:abc123"
	if got != want {
		t.Errorf("SecurityFindingDTag = %q, want %q", got, want)
	}
}

func TestSecurityScheduleDTag(t *testing.T) {
	id := uuid.MustParse("12345678-1234-1234-1234-123456789012")
	got := SecurityScheduleDTag(id)
	want := "security:schedule:12345678-1234-1234-1234-123456789012"
	if got != want {
		t.Errorf("SecurityScheduleDTag = %q, want %q", got, want)
	}
}

// --- Multiple findings published individually ---

func TestSecurityFindingPublisher_OneRecordPerFinding(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	findings := []domain.SecurityOSVFinding{
		{ID: uuid.New(), FindingKeyHash: "h1", OSVID: "GHSA-1", Severity: domain.SecuritySeverityHigh},
		{ID: uuid.New(), FindingKeyHash: "h2", OSVID: "GHSA-2", Severity: domain.SecuritySeverityLow},
		{ID: uuid.New(), FindingKeyHash: "h3", OSVID: "GHSA-3", Severity: domain.SecuritySeverityCritical},
	}

	for _, f := range findings {
		if err := pub.PublishFinding(ctx, f); err != nil {
			t.Fatalf("PublishFinding(%s): %v", f.OSVID, err)
		}
	}

	records := sink.byKind(KindCASControlState)
	if len(records) != 3 {
		t.Fatalf("expected 3 events (one per finding), got %d", len(records))
	}

	// Each should have a unique d-tag
	dtags := make(map[string]bool)
	for _, ev := range records {
		d := tagValue(ev.Tags, "d")
		if dtags[d] {
			t.Errorf("duplicate d-tag: %s", d)
		}
		dtags[d] = true
	}
}

// --- Family tag content verification ---

func TestPaymentRecordContent_Tags(t *testing.T) {
	rec := &domain.PaymentRecord{
		ID:              uuid.New(),
		DeploymentRunID: uuid.New(),
		WorkerPubkey:    "worker_pub_key",
		MintURL:         "https://mint.example.com",
		AmountSats:      42,
		TokenHash:       "secret_token",
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusPending,
	}

	tags, content := PaymentRecordContent(rec)

	// Tags should include worker_pubkey, direction, status
	if !hasTag(tags, "worker_pubkey", "worker_pub_key") {
		t.Error("missing worker_pubkey tag")
	}
	if !hasTag(tags, "direction", "payment") {
		t.Error("missing direction tag")
	}
	if !hasTag(tags, "status", "pending") {
		t.Error("missing status tag")
	}

	// Content should include token_hash
	if !strings.Contains(content, "secret_token") {
		t.Error("token_hash should be in encrypted content")
	}

	// Content should include amount
	if !strings.Contains(content, "42") {
		t.Error("amount_sats should be in encrypted content")
	}
}

func TestSecurityFindingRecordContent_OmitsDetails(t *testing.T) {
	finding := &domain.SecurityOSVFinding{
		ID:             uuid.New(),
		RunID:          uuid.New(),
		TargetKeyHash:  "target_1",
		FindingKeyHash: "fkh_1",
		OSVID:          "GHSA-test",
		Summary:        "A vulnerability",
		Details:        "This is a very long details field that should not appear in the cp-state record",
		Severity:       domain.SecuritySeverityHigh,
	}

	tags, content := SecurityFindingRecordContent(finding)

	// Tags should include osv_id and severity
	if !hasTag(tags, "osv_id", "GHSA-test") {
		t.Error("missing osv_id tag")
	}
	if !hasTag(tags, "severity", string(domain.SecuritySeverityHigh)) {
		t.Error("missing severity tag")
	}

	// Content should include summary but NOT details
	if !strings.Contains(content, "A vulnerability") {
		t.Error("summary should be in content")
	}
	if strings.Contains(content, "This is a very long details") {
		t.Error("Details field must be omitted from cp-state record content")
	}
}

// --- CPStateDomains coverage ---

func TestCPStateDomainsIncludesPaymentAndSecurity(t *testing.T) {
	domains := CPStateDomains()
	found := map[string]bool{}
	for _, d := range domains {
		found[d] = true
	}

	if !found["payment"] {
		t.Error("CPStateDomains() does not include 'payment'")
	}
	if !found["security"] {
		t.Error("CPStateDomains() does not include 'security'")
	}
}

func tagValueFromEvent(ev gonostr.Event, key string) string {
	return tagValue(ev.Tags, key)
}

// --- Security Finding Detail tests ---

func TestSecurityFindingDetailPublisher_SingleRecord(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		FindingKeyHash: "detail_hash_1",
		OSVID:          "GHSA-detail-test",
		Details:        "This is a moderate-length detail about the vulnerability.",
		Severity:       domain.SecuritySeverityHigh,
	}

	if err := pub.PublishFindingDetail(ctx, finding); err != nil {
		t.Fatalf("PublishFindingDetail: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	// Should have 1 detail record
	var detailRecords []gonostr.Event
	for _, ev := range records {
		if hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindSecurityFindingDetailRecord)) {
			detailRecords = append(detailRecords, ev)
		}
	}
	if len(detailRecords) != 1 {
		t.Fatalf("expected 1 detail record, got %d", len(detailRecords))
	}

	ev := detailRecords[0]
	wantD := "security:finding-detail:detail_hash_1"
	if !hasTag(ev.Tags, "d", wantD) {
		t.Errorf("wrong d-tag: %v, want d=%s", ev.Tags, wantD)
	}
	if !hasTag(ev.Tags, "t", kinds.CPStateTopicSecurityFindingDetail) {
		t.Errorf("missing t topic: %v", ev.Tags)
	}
}

func TestSecurityFindingDetailPublisher_EmptyDetailsTombstone(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		FindingKeyHash: "empty_detail_hash",
		OSVID:          "GHSA-empty",
		Details:        "", // empty
		Severity:       domain.SecuritySeverityLow,
	}

	if err := pub.PublishFindingDetail(ctx, finding); err != nil {
		t.Fatalf("PublishFindingDetail: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	var detailRecords []gonostr.Event
	for _, ev := range records {
		if hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindSecurityFindingDetailRecord)) {
			detailRecords = append(detailRecords, ev)
		}
	}
	if len(detailRecords) != 1 {
		t.Fatalf("expected 1 tombstone record, got %d", len(detailRecords))
	}
	if !hasTag(detailRecords[0].Tags, "deleted", "true") {
		t.Error("empty details should produce a tombstone (deleted=true)")
	}
}

func TestSecurityFindingDetailPublisher_LargeDetailsChunked(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	p := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewSecurityCanonicalPublisher(p, enc, zap.NewNop())

	// Create a finding with details > 60,000 bytes (the chunk threshold)
	largeDetails := strings.Repeat("x", 70000)
	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		FindingKeyHash: "large_detail_hash",
		OSVID:          "GHSA-large",
		Details:        largeDetails,
		Severity:       domain.SecuritySeverityCritical,
	}

	if err := pub.PublishFindingDetail(ctx, finding); err != nil {
		t.Fatalf("PublishFindingDetail: %v", err)
	}

	records := sink.byKind(KindCASControlState)
	var detailRecords []gonostr.Event
	for _, ev := range records {
		if hasTag(ev.Tags, "legacy_kind", strconv.Itoa(KindSecurityFindingDetailRecord)) {
			detailRecords = append(detailRecords, ev)
		}
	}
	// 70,000 bytes / 60,000 chunk size = 2 parts plus one base manifest.
	if len(detailRecords) != 3 {
		t.Fatalf("expected 2 chunked detail records and a manifest, got %d", len(detailRecords))
	}

	// Each part should have the part tag
	for _, ev := range detailRecords {
		if !hasTag(ev.Tags, "finding_key_hash", "large_detail_hash") {
			t.Errorf("missing finding_key_hash tag: %v", ev.Tags)
		}
		if !hasTag(ev.Tags, "total_parts", "2") {
			t.Errorf("missing total_parts=2 tag: %v", ev.Tags)
		}
	}

	// d-tags should be ":part:0" and ":part:1"
	dtags := make(map[string]bool)
	for _, ev := range detailRecords {
		dtags[tagValue(ev.Tags, "d")] = true
	}
	if !dtags["security:finding-detail:large_detail_hash:part:0"] {
		t.Error("missing part 0 d-tag")
	}
	if !dtags["security:finding-detail:large_detail_hash:part:1"] {
		t.Error("missing part 1 d-tag")
	}
	if !dtags["security:finding-detail:large_detail_hash"] {
		t.Error("missing detail manifest d-tag")
	}
}

func TestSecurityFindingDetailPublisher_VeryLargeExceedingNIP44(t *testing.T) {
	// Verify that even with a detail >65,535 bytes, each individual chunk's
	// content stays within NIP-44 limits.
	largeDetails := strings.Repeat("A", 200000) // 200KB
	chunks := chunkString(largeDetails, detailChunkSize)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for 200KB, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		_, content := SecurityFindingDetailContent("hash", "GHSA-test", chunk, i, len(chunks))
		if len(content) >= nip44MaxPlaintext {
			t.Errorf("chunk %d content size %d exceeds NIP-44 limit %d", i, len(content), nip44MaxPlaintext)
		}
	}
}

func TestSecurityFindingDetailContent_SizeBound(t *testing.T) {
	// Even a maximally-padded single detail stays under the limit.
	detail := strings.Repeat("d", detailChunkSize)
	_, content := SecurityFindingDetailContent(
		strings.Repeat("h", 64),
		"GHSA-"+strings.Repeat("x", 100),
		detail, 0, 1,
	)
	if len(content) >= nip44MaxPlaintext {
		t.Errorf("detail content size %d exceeds NIP-44 limit %d", len(content), nip44MaxPlaintext)
	}
}

func TestSecurityFindingDetailDTag(t *testing.T) {
	got := SecurityFindingDetailDTag("abc123")
	want := "security:finding-detail:abc123"
	if got != want {
		t.Errorf("SecurityFindingDetailDTag = %q, want %q", got, want)
	}
}

func TestSecurityFindingDetailPartDTag(t *testing.T) {
	got := SecurityFindingDetailPartDTag("abc123", 2)
	want := "security:finding-detail:abc123:part:2"
	if got != want {
		t.Errorf("SecurityFindingDetailPartDTag = %q, want %q", got, want)
	}
}

func TestChunkString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		maxLen  int
		wantLen int
	}{
		{"empty", "", 100, 1},
		{"under limit", "hello", 100, 1},
		{"exact limit", strings.Repeat("a", 100), 100, 1},
		{"over limit", strings.Repeat("a", 250), 100, 3},
		{"way over", strings.Repeat("a", 1000), 100, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := chunkString(tc.input, tc.maxLen)
			if len(chunks) != tc.wantLen {
				t.Errorf("chunkString(%d bytes, %d) = %d chunks, want %d", len(tc.input), tc.maxLen, len(chunks), tc.wantLen)
			}
			// Reassemble and verify
			reassembled := strings.Join(chunks, "")
			if reassembled != tc.input {
				t.Error("reassembled chunks do not match original")
			}
		})
	}
}

func TestChunkStringPreservesUTF8(t *testing.T) {
	input := strings.Repeat("vulnerabilidad-🔥", 5000)
	chunks := chunkString(input, 101)
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
	}
	if got := strings.Join(chunks, ""); got != input {
		t.Fatal("UTF-8 detail did not round-trip")
	}
}
