package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const (
	RelayPolicyProjectionBackupSchema = "bahia.relay-policy-projection-backup.v2"
	relayPolicySchema                 = "bahia.relay-settings.v1"
	maxRelayPolicyBackupPayload       = 1 << 20
	maxRelayPolicyBackupEvent         = 2 << 20
)

var relayPolicyBackupHex64 = regexp.MustCompile("^[0-9a-f]{64}$")

// RelayPolicyProjectionBackup contains only public signed policy content and
// provenance. Credentials, private keys, runtime config, and browser overrides
// are deliberately excluded.
type RelayPolicyProjectionBackup struct {
	BackupSchema     string          `json:"backup_schema"`
	AuthorPubkey     string          `json:"author_pubkey"`
	EventID          string          `json:"event_id"`
	EventCreatedAt   time.Time       `json:"event_created_at"`
	EventAcceptedAt  time.Time       `json:"event_accepted_at"`
	PolicySchema     string          `json:"policy_schema"`
	CanonicalPayload json.RawMessage `json:"canonical_payload"`
	SignedEvent      json.RawMessage `json:"signed_event"`
	PayloadHash      string          `json:"payload_hash"`
	SourceRelay      string          `json:"source_relay,omitempty"`
	LastSyncAt       time.Time       `json:"last_sync_at"`
	ExportedAt       time.Time       `json:"exported_at"`
}

// RelayPolicyProjectionBackupRepository is the provenance-safe backup extension.
type RelayPolicyProjectionBackupRepository interface {
	Export(ctx context.Context, authorPubkey string, exportedAt time.Time) (*RelayPolicyProjectionBackup, error)
	RestoreCached(ctx context.Context, backup RelayPolicyProjectionBackup) (bool, error)
}

func NewRelayPolicyProjectionBackup(projection RelayPolicyProjection, exportedAt time.Time) (RelayPolicyProjectionBackup, error) {
	backup := RelayPolicyProjectionBackup{
		BackupSchema:     RelayPolicyProjectionBackupSchema,
		AuthorPubkey:     strings.ToLower(strings.TrimSpace(projection.AuthorPubkey)),
		EventID:          strings.ToLower(strings.TrimSpace(projection.EventID)),
		EventCreatedAt:   projection.EventCreatedAt.UTC(),
		EventAcceptedAt:  projection.EventAcceptedAt.UTC(),
		PolicySchema:     strings.TrimSpace(projection.Schema),
		CanonicalPayload: append(json.RawMessage(nil), projection.CanonicalPayload...),
		SignedEvent:      append(json.RawMessage(nil), projection.SignedEvent...),
		PayloadHash:      strings.ToLower(strings.TrimSpace(projection.PayloadHash)),
		SourceRelay:      strings.TrimSpace(projection.SourceRelay),
		LastSyncAt:       projection.LastSyncAt.UTC(),
		ExportedAt:       exportedAt.UTC(),
	}
	return backup, ValidateRelayPolicyProjectionBackup(backup)
}

func ValidateRelayPolicyProjectionBackup(backup RelayPolicyProjectionBackup) error {
	if backup.BackupSchema != RelayPolicyProjectionBackupSchema {
		return fmt.Errorf("unsupported relay policy projection backup schema %q", backup.BackupSchema)
	}
	for name, value := range map[string]string{"author_pubkey": backup.AuthorPubkey, "event_id": backup.EventID, "payload_hash": backup.PayloadHash} {
		if !relayPolicyBackupHex64.MatchString(strings.ToLower(strings.TrimSpace(value))) {
			return fmt.Errorf("relay policy projection backup %s must be 64 hex characters", name)
		}
	}
	if backup.EventCreatedAt.IsZero() || backup.EventAcceptedAt.IsZero() || backup.LastSyncAt.IsZero() || backup.ExportedAt.IsZero() {
		return fmt.Errorf("relay policy projection backup provenance timestamps are required")
	}
	if strings.TrimSpace(backup.PolicySchema) != relayPolicySchema {
		return fmt.Errorf("relay policy projection backup policy_schema %q is not supported", backup.PolicySchema)
	}
	if len(backup.CanonicalPayload) == 0 || len(backup.CanonicalPayload) > maxRelayPolicyBackupPayload || !json.Valid(backup.CanonicalPayload) {
		return fmt.Errorf("relay policy projection backup canonical_payload must be valid JSON")
	}
	if len(backup.SignedEvent) == 0 || len(backup.SignedEvent) > maxRelayPolicyBackupEvent {
		return fmt.Errorf("relay policy projection backup signed_event is required")
	}
	var event nostr.Event
	if err := json.Unmarshal(backup.SignedEvent, &event); err != nil || !event.CheckID() || !event.VerifySignature() {
		return fmt.Errorf("relay policy projection backup signed_event has invalid id or Schnorr signature")
	}
	if event.Kind != 30900 || !strings.EqualFold(event.PubKey.Hex(), backup.AuthorPubkey) ||
		!strings.EqualFold(event.ID.Hex(), backup.EventID) || !event.CreatedAt.Time().UTC().Equal(backup.EventCreatedAt.UTC()) {
		return fmt.Errorf("relay policy projection backup signed_event provenance mismatch")
	}
	if !backupEventHasTag(event.Tags, "d", "relay-settings:operator") ||
		!backupEventHasTag(event.Tags, "domain", "relay-settings") ||
		!backupEventHasTag(event.Tags, "schema", relayPolicySchema) {
		return fmt.Errorf("relay policy projection backup signed_event is not relay settings state")
	}
	var signedPayload, canonicalPayload any
	if json.Unmarshal([]byte(event.Content), &signedPayload) != nil || json.Unmarshal(backup.CanonicalPayload, &canonicalPayload) != nil || !reflect.DeepEqual(signedPayload, canonicalPayload) {
		return fmt.Errorf("relay policy projection backup signed_event content mismatch")
	}
	var payloadHeader struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(backup.CanonicalPayload, &payloadHeader); err != nil || payloadHeader.Schema != backup.PolicySchema {
		return fmt.Errorf("relay policy projection backup payload schema mismatch")
	}
	if source := strings.TrimSpace(backup.SourceRelay); source != "" {
		parsed, err := url.Parse(source)
		if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("relay policy projection backup source_relay is unsafe")
		}
	}
	sum := sha256.Sum256(backup.CanonicalPayload)
	if hex.EncodeToString(sum[:]) != strings.ToLower(strings.TrimSpace(backup.PayloadHash)) {
		return fmt.Errorf("relay policy projection backup payload hash mismatch")
	}
	return nil
}

func backupEventHasTag(tags nostr.Tags, key, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func DecodeRelayPolicyProjectionBackup(value any) (RelayPolicyProjectionBackup, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return RelayPolicyProjectionBackup{}, fmt.Errorf("encode relay policy projection backup metadata: %w", err)
	}
	var backup RelayPolicyProjectionBackup
	if err := json.Unmarshal(raw, &backup); err != nil {
		return RelayPolicyProjectionBackup{}, fmt.Errorf("decode relay policy projection backup metadata: %w", err)
	}
	return backup, ValidateRelayPolicyProjectionBackup(backup)
}
