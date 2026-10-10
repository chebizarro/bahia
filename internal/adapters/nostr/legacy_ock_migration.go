package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// legacyOCKTopicMap maps each confidential cp-state topic to the metadata
// needed to detect, decrypt, and re-publish old-format records under the OCK
// scheme (docs/architecture/confidential-state.md steps 1–8). The keys match the cpStateFamilies topic
// values for the five confidential families.
var legacyOCKTopicMap = map[string]legacyOCKTopic{
	kinds.CPStateTopicOrgRegistry:                 {legacyKind: KindOrgRegistry, decryptKind: legacyDecryptO1},
	kinds.CPStateTopicOrgMemberRegistry:           {legacyKind: KindOrgMemberRegistry, decryptKind: legacyDecryptO1},
	kinds.CPStateTopicOrgInviteRegistry:           {legacyKind: KindOrgInviteRegistry, decryptKind: legacyDecryptO1},
	kinds.CPStateTopicSecretRegistry:              {legacyKind: KindSecretRegistry, decryptKind: legacyDecryptN1},
	kinds.CPStateTopicNotificationChannelRegistry: {legacyKind: KindNotificationChannelRegistry, decryptKind: legacyDecryptN1},
}

// confidentialAEADV1Schema is the schema identifier for the new per-org
// content key format. Duplicated here to avoid an import cycle with the
// controlplane package (which imports this package).
const confidentialAEADV1Schema = "bahia.confidential.aead.v1"

type legacyDecryptMethod int

const (
	legacyDecryptO1 legacyDecryptMethod = iota // sha256-of-private-key XChaCha AEAD (org_state_crypto.go)
	legacyDecryptN1                            // NIP-44 self-encryption to service pubkey
)

type legacyOCKTopic struct {
	legacyKind  int
	decryptKind legacyDecryptMethod
}

// LegacyOCKMigrator re-publishes the retained, service-authored records of
// five confidential cp-state families under the per-org content key. It is
// registered at warm start, after EOSE. RunChecked accounts for every returned
// record and refuses to certify skipped failures or a full bounded page.
// It does not certify remote relay inventory or authorize raw-key removal.
type LegacyOCKMigrator struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	legacyO1  LegacyOrgStateDecryptor
	legacyN1  LegacyN1Decryptor
	logger    *zap.Logger

	mu              sync.Mutex
	completed       bool
	lastReport      LegacyOCKMigrationReport
	migratedRecords map[string]struct{}
}

// LegacyN1Decryptor is the existing OCK manager service NIP-44 decrypt path.
// It uses the configured Keyer, so no raw service key is required here.
type LegacyN1Decryptor interface {
	ServiceDecrypt(ctx context.Context, ciphertext string) (string, error)
}

// NewLegacyOCKMigrator creates a migrator. Missing dependencies are reported
// as an incomplete attempt by RunChecked.
func NewLegacyOCKMigrator(
	projector *Projector,
	encryptor ConfidentialStateEncryptor,
	legacyO1 LegacyOrgStateDecryptor,
	legacyN1 LegacyN1Decryptor,
	logger *zap.Logger,
) *LegacyOCKMigrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LegacyOCKMigrator{
		projector: projector,
		encryptor: encryptor,
		legacyO1:  legacyO1,
		legacyN1:  legacyN1,
		logger:    logger.Named("legacy-ock-migration"),
	}
}

// LegacyOCKMigrationFailure identifies a record or topic that could not be
// certified. Content and plaintext are deliberately absent.
type LegacyOCKMigrationFailure struct {
	Topic   string
	EventID string
	Reason  string
}

// LegacyOCKMigrationTopicReport accounts for every returned record in a topic.
// ForeignAuthor records are not part of the service identity's migration.
type LegacyOCKMigrationTopicReport struct {
	Scanned        int
	ForeignAuthor  int
	AlreadyCurrent int
	Migrated       int
	Failed         int
}

// LegacyOCKMigrationReport is only a bounded local-history result. Complete
// never proves that relays, older stores, or unpublished outboxes lack legacy
// records, nor that a durably queued publish has reached relay quorum. It must
// not be used alone to authorize service-key removal.
type LegacyOCKMigrationReport struct {
	Topics   map[string]LegacyOCKMigrationTopicReport
	Failures []LegacyOCKMigrationFailure
	Complete bool
}

const legacyOCKScanLimit = 10000

// Run preserves the existing post-warm-start hook. Unlike the old log-only
// runner it surfaces an incomplete local scan as an error-level log.
func (m *LegacyOCKMigrator) Run(ctx context.Context) {
	report, err := m.RunChecked(ctx)
	if m == nil {
		return
	}
	if err != nil {
		m.logger.Error("legacy OCK migration incomplete", zap.Error(err),
			zap.Any("topics", report.Topics), zap.Any("failures", report.Failures))
		return
	}
	m.logger.Info("legacy OCK migration local scan complete", zap.Any("topics", report.Topics))
}

// RunChecked retries failed or canceled attempts. Only a successful local
// scan is cached; a second caller does not inherit the first caller's failure.
// A successful result certifies only the bounded, author-scoped local history
// presented by ProjectionHistory after warm-start, not remote relay history.
func (m *LegacyOCKMigrator) RunChecked(ctx context.Context) (LegacyOCKMigrationReport, error) {
	if m == nil {
		return LegacyOCKMigrationReport{}, fmt.Errorf("legacy OCK migrator is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return LegacyOCKMigrationReport{}, fmt.Errorf("legacy OCK migration interrupted: %w", err)
	}
	if m.completed {
		return cloneLegacyOCKMigrationReport(m.lastReport), nil
	}
	report, err := m.runChecked(ctx)
	if err == nil {
		m.completed = true
		m.lastReport = cloneLegacyOCKMigrationReport(report)
	}
	return report, err
}

func cloneLegacyOCKMigrationReport(report LegacyOCKMigrationReport) LegacyOCKMigrationReport {
	copy := LegacyOCKMigrationReport{Complete: report.Complete,
		Topics:   make(map[string]LegacyOCKMigrationTopicReport, len(report.Topics)),
		Failures: append([]LegacyOCKMigrationFailure(nil), report.Failures...)}
	for topic, stats := range report.Topics {
		copy.Topics[topic] = stats
	}
	return copy
}

func (m *LegacyOCKMigrator) runChecked(ctx context.Context) (LegacyOCKMigrationReport, error) {
	report := LegacyOCKMigrationReport{Topics: make(map[string]LegacyOCKMigrationTopicReport, len(legacyOCKTopicMap))}
	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("legacy OCK migration interrupted: %w", err)
	}
	if m.projector == nil || !m.projector.Enabled() || m.encryptor == nil || m.projector.history == nil {
		return report, fmt.Errorf("legacy OCK migration requires enabled projector, encryptor, and local history")
	}
	servicePubkey := m.projector.servicePubkey
	if servicePubkey == "" && m.projector.privateKey != "" {
		var err error
		servicePubkey, err = publicKeyHexFromPrivateKeyHex(m.projector.privateKey)
		if err != nil {
			return report, fmt.Errorf("resolve service pubkey: %w", err)
		}
	}
	if servicePubkey == "" {
		return report, fmt.Errorf("legacy OCK migration requires pinned service pubkey")
	}
	topics := legacyOCKMigrationTopics()
	sort.Strings(topics)
	for _, topic := range topics {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("legacy OCK migration interrupted: %w", err)
		}
		stats, failures := m.migrateTopic(ctx, topic, legacyOCKTopicMap[topic], servicePubkey)
		report.Topics[topic] = stats
		report.Failures = append(report.Failures, failures...)
	}
	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("legacy OCK migration interrupted: %w", err)
	}
	if len(report.Failures) != 0 {
		return report, fmt.Errorf("legacy OCK migration incomplete: %d failures", len(report.Failures))
	}
	report.Complete = true
	return report, nil
}

// migrateTopic scans one confidential family. A full page is ambiguous and
// therefore cannot be certified; it is not processed or treated as complete.
func (m *LegacyOCKMigrator) migrateTopic(ctx context.Context, topic string, meta legacyOCKTopic, servicePubkey string) (LegacyOCKMigrationTopicReport, []LegacyOCKMigrationFailure) {
	stats := LegacyOCKMigrationTopicReport{}
	failures := []LegacyOCKMigrationFailure{}
	fail := func(eventID, reason string) {
		stats.Failed++
		failures = append(failures, LegacyOCKMigrationFailure{Topic: topic, EventID: eventID, Reason: reason})
	}
	records, err := m.projector.history.FindByTag(ctx, "t", topic, []int{KindCASControlState}, legacyOCKScanLimit)
	if err != nil {
		fail("", fmt.Sprintf("history query: %v", err))
		return stats, failures
	}
	stats.Scanned = len(records)
	if len(records) >= legacyOCKScanLimit {
		fail("", "history scan reached bounded limit; older records are unproven")
		return stats, failures
	}
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			fail(rec.ID, fmt.Sprintf("interrupted: %v", err))
			break
		}
		if rec.PubKey != servicePubkey {
			stats.ForeignAuthor++
			continue
		}
		if _, migrated := m.migratedRecords[topic+":"+rec.ID]; migrated {
			stats.Migrated++
			continue
		}
		if isConfidentialAEADV1(rec.Content) {
			stats.AlreadyCurrent++
			continue
		}
		plaintext, err := m.tryLegacyDecrypt(ctx, rec.Content, meta)
		if err != nil {
			fail(rec.ID, fmt.Sprintf("legacy decrypt or format: %v", err))
			continue
		}
		if err := ctx.Err(); err != nil {
			fail(rec.ID, fmt.Sprintf("interrupted after legacy decrypt: %v", err))
			break
		}
		dTag := extractDTagFromRecord(rec)
		if dTag == "" {
			fail(rec.ID, "missing d-tag")
			continue
		}
		orgID := extractOrgIDFromPlaintext(plaintext)
		if orgID == "" {
			fail(rec.ID, "missing org_id in decrypted content")
			continue
		}
		encrypted, err := m.encryptor.EncryptConfidential(ctx, orgID, []byte(plaintext), meta.legacyKind, dTag, topic, nil)
		if err != nil {
			fail(rec.ID, fmt.Sprintf("re-encrypt: %v", err))
			continue
		}
		if err := ctx.Err(); err != nil {
			fail(rec.ID, fmt.Sprintf("interrupted before re-publish: %v", err))
			break
		}
		if err := m.projector.publishControlState(ctx, meta.legacyKind, dTag, false, nil, encrypted, "legacy_ock_migration", nil); err != nil {
			fail(rec.ID, fmt.Sprintf("re-publish: %v", err))
			continue
		}
		if m.migratedRecords == nil {
			m.migratedRecords = make(map[string]struct{})
		}
		m.migratedRecords[topic+":"+rec.ID] = struct{}{}
		stats.Migrated++
	}
	return stats, failures
}

// tryLegacyDecrypt attempts old-format decryption based on the decrypt method.
func (m *LegacyOCKMigrator) tryLegacyDecrypt(ctx context.Context, content string, meta legacyOCKTopic) (string, error) {
	switch meta.decryptKind {
	case legacyDecryptO1:
		if m.legacyO1 == nil {
			return "", fmt.Errorf("legacy O1 decryptor not configured")
		}
		plaintext, err := m.legacyO1.DecryptOrgState(content)
		if err != nil {
			return "", fmt.Errorf("legacy O1 decrypt: %w", err)
		}
		return string(plaintext), nil
	case legacyDecryptN1:
		if m.legacyN1 == nil {
			return "", fmt.Errorf("legacy N1 service decryptor not configured")
		}
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("legacy N1 decrypt interrupted: %w", err)
		}
		plaintext, err := m.legacyN1.ServiceDecrypt(ctx, content)
		if err != nil {
			return "", fmt.Errorf("legacy N1 decrypt: %w", err)
		}
		return plaintext, nil
	default:
		return "", fmt.Errorf("unknown legacy decrypt method %d", meta.decryptKind)
	}
}

// isConfidentialAEADV1 recognizes a structurally complete OCK envelope.
// This is format classification, not an AEAD authentication or relay proof.
func isConfidentialAEADV1(content string) bool {
	var envelope struct {
		Schema         string            `json:"schema"`
		Algorithm      string            `json:"algorithm"`
		KeyOrg         string            `json:"key_org"`
		KeyRef         string            `json:"key_ref"`
		KeyVersion     string            `json:"key_version"`
		Nonce          string            `json:"nonce"`
		Ciphertext     string            `json:"ciphertext"`
		AssociatedData map[string]string `json:"associated_data"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	return envelope.Schema == confidentialAEADV1Schema &&
		envelope.Algorithm == "xchacha20-poly1305" &&
		envelope.KeyOrg != "" && envelope.KeyRef == "ock:"+envelope.KeyOrg &&
		envelope.KeyVersion != "" && envelope.Nonce != "" && envelope.Ciphertext != "" &&
		len(envelope.AssociatedData) != 0
}

// extractOrgIDFromPlaintext extracts the org_id field from decrypted
// JSON content. Handles both direct org records (id field = org_id) and
// records with an explicit org_id field (members, invites, secrets,
// notification channels).
func extractOrgIDFromPlaintext(plaintext string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(plaintext), &parsed); err != nil {
		return ""
	}
	// Most records carry org_id explicitly.
	if orgID, ok := parsed["org_id"].(string); ok && orgID != "" {
		return orgID
	}
	// Org records use id as the org_id.
	if id, ok := parsed["id"].(string); ok && id != "" {
		return id
	}
	return ""
}

// legacyOCKMigrationTopics returns the topic tags of the five confidential
// cp-state families that the migration processes. Exported for testing.
func legacyOCKMigrationTopics() []string {
	out := make([]string, 0, len(legacyOCKTopicMap))
	for topic := range legacyOCKTopicMap {
		out = append(out, topic)
	}
	return out
}
