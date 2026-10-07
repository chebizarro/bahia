package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

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
	legacyDecryptN1                            // NIP-44 self-encryption to service pubkey (confidential_state.go)
)

type legacyOCKTopic struct {
	legacyKind  int
	decryptKind legacyDecryptMethod
}

// LegacyOCKMigrator re-publishes confidential records that are still in an
// old encryption format under the per-org content key (OCK) scheme at daemon
// startup. It implements
// docs/architecture/confidential-state.md "Migration" steps 1–8 exactly:
//
//  1. Scan projectionHistory.FindByTag for each confidential topic.
//  2. Try parsing as bahia.confidential.aead.v1 — skip if already migrated.
//  3. Try O1 decryptOrgState (org/member/invite) or N1
//     selfDecryptNIP44Legacy (secret/notification).
//  4. Re-encrypt the plaintext with ConfidentialEncryptor.EncryptConfidential.
//  5. Re-publish through publishControlState (same coordinate, monotonic created_at).
//  6. Log progress: "migrated N/M records for topic T".
//  7. Run at most once per daemon lifetime (in-memory "migration-done" flag).
//  8. (The old-format decrypt code is required until every deployment has
//     run this migration.)
//
// The migration is idempotent: records already in the new format are
// skipped, and re-running publishes the same content on the same
// coordinate, which the relay's addressable-event replacement deduplicates.
type LegacyOCKMigrator struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	legacyO1  LegacyOrgStateDecryptor
	logger    *zap.Logger

	done atomic.Bool
}

// NewLegacyOCKMigrator creates a migrator. All parameters are required;
// if any are nil, Run returns early without error (the daemon may not
// have confidential crypto configured).
func NewLegacyOCKMigrator(
	projector *Projector,
	encryptor ConfidentialStateEncryptor,
	legacyO1 LegacyOrgStateDecryptor,
	logger *zap.Logger,
) *LegacyOCKMigrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LegacyOCKMigrator{
		projector: projector,
		encryptor: encryptor,
		legacyO1:  legacyO1,
		logger:    logger.Named("legacy-ock-migration"),
	}
}

// Run executes the migration at most once per daemon lifetime.
// It should be called after warm-start readiness (EOSE received) so that
// the history contains the latest records from all relays.
//
// The HydrateTrustSetFromHistory call runs before this and already has
// dual-read support (OCK format first, O1 fallback), so it can read
// records in either format.
func (m *LegacyOCKMigrator) Run(ctx context.Context) {
	if m == nil {
		return
	}
	// Step 7: at most once per daemon lifetime.
	if !m.done.CompareAndSwap(false, true) {
		return
	}
	if m.projector == nil || !m.projector.Enabled() || m.encryptor == nil {
		m.logger.Info("legacy OCK migration skipped: projector or encryptor not configured")
		return
	}
	if m.projector.history == nil {
		m.logger.Info("legacy OCK migration skipped: no projection history available")
		return
	}

	totalMigrated := 0
	totalSkipped := 0
	totalErrors := 0
	for topic, meta := range legacyOCKTopicMap {
		if ctx.Err() != nil {
			m.logger.Warn("legacy OCK migration interrupted", zap.Error(ctx.Err()))
			return
		}
		migrated, skipped, errors := m.migrateTopic(ctx, topic, meta)
		totalMigrated += migrated
		totalSkipped += skipped
		totalErrors += errors
	}
	m.logger.Info("legacy OCK migration complete",
		zap.Int("migrated", totalMigrated),
		zap.Int("skipped", totalSkipped),
		zap.Int("errors", totalErrors))
}

// migrateTopic processes one confidential topic. Returns counts of
// migrated, skipped, and errored records.
func (m *LegacyOCKMigrator) migrateTopic(ctx context.Context, topic string, meta legacyOCKTopic) (migrated, skipped, errors int) {
	// Step 1: scan history for records with this topic tag.
	records, err := m.projector.history.FindByTag(ctx, "t", topic, nil, 10000)
	if err != nil {
		m.logger.Warn("legacy OCK migration: failed to query history",
			zap.String("topic", topic), zap.Error(err))
		return 0, 0, 1
	}
	if len(records) == 0 {
		return 0, 0, 0
	}

	servicePubkey := ""
	if m.projector.privateKey != "" {
		if derived, derivErr := publicKeyHexFromPrivateKeyHex(m.projector.privateKey); derivErr == nil {
			servicePubkey = derived
		}
	}

	for _, rec := range records {
		if ctx.Err() != nil {
			return migrated, skipped, errors
		}
		// Only process the daemon's own records.
		if servicePubkey != "" && rec.PubKey != servicePubkey {
			continue
		}

		// Step 2: try parsing as new format. If it succeeds, skip.
		if isConfidentialAEADV1(rec.Content) {
			skipped++
			continue
		}

		// Step 3: try old-format decrypt.
		plaintext, decryptErr := m.tryLegacyDecrypt(rec.Content, meta)
		if decryptErr != nil {
			m.logger.Debug("legacy OCK migration: record not in legacy format or decrypt failed",
				zap.String("topic", topic), zap.String("event_id", rec.ID),
				zap.Error(decryptErr))
			skipped++
			continue
		}

		// Extract the d-tag from the record's tags.
		dTag := extractDTagFromRecord(rec)
		if dTag == "" {
			m.logger.Warn("legacy OCK migration: record missing d-tag",
				zap.String("topic", topic), zap.String("event_id", rec.ID))
			errors++
			continue
		}

		// Extract orgID from the decrypted plaintext for OCK scoping.
		orgID := extractOrgIDFromPlaintext(plaintext)
		if orgID == "" {
			m.logger.Warn("legacy OCK migration: cannot determine org_id from decrypted content",
				zap.String("topic", topic), zap.String("event_id", rec.ID))
			errors++
			continue
		}

		// Step 4: re-encrypt with ConfidentialEncryptor.
		encrypted, encryptErr := m.encryptor.EncryptConfidential(
			ctx, orgID, []byte(plaintext), meta.legacyKind, dTag, topic, nil)
		if encryptErr != nil {
			m.logger.Warn("legacy OCK migration: re-encrypt failed",
				zap.String("topic", topic), zap.String("event_id", rec.ID),
				zap.Error(encryptErr))
			errors++
			continue
		}

		// Step 5: re-publish through publishControlState (same coordinate).
		// publishControlState takes the raw id and creates the d-tag via
		// controlStateEnvelope → canonicalStateDTag, which for non-worker
		// kinds returns the id unchanged. The dTag from history IS the id.
		if publishErr := m.projector.publishControlState(
			ctx, meta.legacyKind, dTag, false, nil, encrypted,
			"legacy_ock_migration", nil,
		); publishErr != nil {
			m.logger.Warn("legacy OCK migration: re-publish failed",
				zap.String("topic", topic), zap.String("event_id", rec.ID),
				zap.Error(publishErr))
			errors++
			continue
		}
		migrated++
	}

	// Step 6: log progress.
	m.logger.Info(fmt.Sprintf("legacy OCK migration: migrated %d/%d records for topic %s",
		migrated, len(records), topic),
		zap.String("topic", topic),
		zap.Int("total", len(records)),
		zap.Int("migrated", migrated),
		zap.Int("skipped", skipped),
		zap.Int("errors", errors))
	return migrated, skipped, errors
}

// tryLegacyDecrypt attempts old-format decryption based on the decrypt method.
func (m *LegacyOCKMigrator) tryLegacyDecrypt(content string, meta legacyOCKTopic) (string, error) {
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
		plaintext, err := m.projector.selfDecryptNIP44Legacy(content)
		if err != nil {
			return "", fmt.Errorf("legacy N1 decrypt: %w", err)
		}
		return plaintext, nil
	default:
		return "", fmt.Errorf("unknown legacy decrypt method %d", meta.decryptKind)
	}
}

// isConfidentialAEADV1 checks whether content is already in the new
// bahia.confidential.aead.v1 format by parsing just the schema field.
func isConfidentialAEADV1(content string) bool {
	var probe struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal([]byte(content), &probe); err != nil {
		return false
	}
	return probe.Schema == confidentialAEADV1Schema
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
