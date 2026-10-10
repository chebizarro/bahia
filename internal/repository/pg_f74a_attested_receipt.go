package repository

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const f74aReceiptVersion = "bahia-f74a-backup-restore-v1"
const f74aReceiptDomain = "bahia-f74a-backup-restore-v1\x00"
const F74aReceiptMaxBytes = 65536

// F74aDatabaseIdentity names one physical PostgreSQL database, not a DSN or
// an operator-supplied label. Reading the cluster system identifier requires
// pg_monitor or equivalent privileges; lack of that privilege fails closed.
type F74aDatabaseIdentity struct {
	Name             string `json:"name"`
	OID              string `json:"oid"`
	SystemIdentifier string `json:"system_identifier"`
}

func ReadF74aDatabaseIdentity(ctx context.Context, pool *pgxpool.Pool) (F74aDatabaseIdentity, error) {
	var id F74aDatabaseIdentity
	err := pool.QueryRow(ctx, `SELECT current_database(),d.oid::text,c.system_identifier::text
		FROM pg_database d CROSS JOIN pg_control_system() c
		WHERE d.datname=current_database()`).Scan(&id.Name, &id.OID, &id.SystemIdentifier)
	if err != nil {
		return id, fmt.Errorf("reading PostgreSQL system/database identity (pg_monitor privilege required): %w", err)
	}
	if id.Name == "" || id.OID == "" || id.SystemIdentifier == "" {
		return id, fmt.Errorf("PostgreSQL system/database identity is incomplete")
	}
	return id, nil
}

// F74aAttestedBackupPayload is signed by a separately pinned backup attestor.
// Its signed claims are necessary evidence, not proof that the snapshot still
// exists or that credentials remain usable. Verification never authorizes
// confirmed deletion.
type F74aAttestedBackupPayload struct {
	Version                string               `json:"version"`
	ReceiptID              uuid.UUID            `json:"receipt_id"`
	SourceDatabase         F74aDatabaseIdentity `json:"source_database"`
	RestoreDatabase        F74aDatabaseIdentity `json:"restore_database"`
	Cutoff                 time.Time            `json:"cutoff"`
	SnapshotID             string               `json:"snapshot_id"`
	BackupObjectRef        string               `json:"backup_object_ref"`
	SnapshotCreatedAt      time.Time            `json:"snapshot_created_at"`
	BackupObjectSHA256     string               `json:"backup_object_sha256"`
	SourceInventorySHA256  string               `json:"source_inventory_sha256"`
	RestoreInventorySHA256 string               `json:"restore_inventory_sha256"`
	RestoreVerifiedAt      time.Time            `json:"restore_verified_at"`
	IssuedAt               time.Time            `json:"issued_at"`
	ExpiresAt              time.Time            `json:"expires_at"`
}

// SigningBytes emits the fixed Go wire form for an attestor implemented in Go.
// The verifier authenticates the exact raw JSON payload bytes in the envelope,
// so other issuers may use their own serialization without ambiguity.
func (p F74aAttestedBackupPayload) SigningBytes() ([]byte, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return append([]byte(f74aReceiptDomain), payload...), nil
}

type F74aAttestedBackupReceipt struct {
	Payload   F74aAttestedBackupPayload `json:"payload"`
	Signature string                    `json:"signature"`
}

type F74aReceiptVerification struct {
	ReceiptID          uuid.UUID
	ReceiptSHA256      string
	SourceDatabase     F74aDatabaseIdentity
	Cutoff             time.Time
	InventorySHA256    string
	BackupObjectRef    string
	BackupObjectSHA256 string
	ExpiresAt          time.Time
}

// VerifyF74aAttestedReceipt checks one bounded, signed receipt against the
// connected source database and a fresh read-only inventory. It does not
// accept a caller-provided database identity or inventory as local proof.
func VerifyF74aAttestedReceipt(ctx context.Context, pool *pgxpool.Pool, pinnedAttestorHex string, receiptJSON []byte) (F74aReceiptVerification, error) {
	return verifyF74aAttestedReceipt(ctx, pool, pinnedAttestorHex, receiptJSON, nil)
}

// A future independent live backup provider may use this private verifier on
// restart. It authenticates the same receipt against the run's immutable
// per-row deletion provenance, not against today's physical hot flags.
func verifyF74aAttestedReceiptForDeletionRun(ctx context.Context, pool *pgxpool.Pool, pinnedAttestorHex string, receiptJSON []byte, runID uuid.UUID) (F74aReceiptVerification, error) {
	if runID == uuid.Nil {
		return F74aReceiptVerification{}, fmt.Errorf("F74a deletion run ID is required")
	}
	return verifyF74aAttestedReceipt(ctx, pool, pinnedAttestorHex, receiptJSON, &runID)
}

func verifyF74aAttestedReceipt(ctx context.Context, pool *pgxpool.Pool, pinnedAttestorHex string, receiptJSON []byte, runID *uuid.UUID) (F74aReceiptVerification, error) {
	var out F74aReceiptVerification
	pub, err := hex.DecodeString(strings.TrimSpace(pinnedAttestorHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return out, fmt.Errorf("independent F74a backup attestor public key is not configured as 32-byte Ed25519 hex")
	}
	if len(receiptJSON) == 0 || len(receiptJSON) > F74aReceiptMaxBytes {
		return out, fmt.Errorf("F74a attested receipt size is invalid")
	}
	if err := rejectF74aDuplicateJSONKeys(receiptJSON); err != nil {
		return out, err
	}
	var receipt struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}
	decoder := json.NewDecoder(bytes.NewReader(receiptJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return out, fmt.Errorf("decoding F74a attested receipt: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return out, fmt.Errorf("F74a attested receipt has trailing content")
	}
	sig, err := hex.DecodeString(receipt.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return out, fmt.Errorf("F74a attested receipt signature is malformed")
	}
	message := append([]byte(f74aReceiptDomain), receipt.Payload...)
	if !ed25519.Verify(pub, message, sig) {
		return out, fmt.Errorf("F74a attested receipt signature is not from pinned attestor")
	}
	var p F74aAttestedBackupPayload
	payloadDecoder := json.NewDecoder(bytes.NewReader(receipt.Payload))
	payloadDecoder.DisallowUnknownFields()
	if err := payloadDecoder.Decode(&p); err != nil {
		return out, fmt.Errorf("decoding signed F74a receipt payload: %w", err)
	}
	if err := payloadDecoder.Decode(&extra); err != io.EOF {
		return out, fmt.Errorf("signed F74a receipt payload has trailing content")
	}
	now := time.Now().UTC()
	if p.Version != f74aReceiptVersion || p.ReceiptID == uuid.Nil ||
		p.Cutoff.IsZero() || !p.Cutoff.Before(p.SnapshotCreatedAt) ||
		p.SnapshotCreatedAt.After(p.RestoreVerifiedAt) || p.RestoreVerifiedAt.After(p.IssuedAt) ||
		p.IssuedAt.After(now) || !now.Before(p.ExpiresAt) ||
		strings.TrimSpace(p.SnapshotID) == "" || strings.TrimSpace(p.SnapshotID) != p.SnapshotID ||
		strings.TrimSpace(p.BackupObjectRef) == "" || strings.TrimSpace(p.BackupObjectRef) != p.BackupObjectRef || sameF74aPhysicalDatabase(p.SourceDatabase, p.RestoreDatabase) ||
		!validF74aIdentity(p.SourceDatabase) || !validF74aIdentity(p.RestoreDatabase) ||
		!validF74aDigest(p.BackupObjectSHA256) || !validF74aDigest(p.SourceInventorySHA256) ||
		!validF74aDigest(p.RestoreInventorySHA256) || p.SourceInventorySHA256 != p.RestoreInventorySHA256 {
		return out, fmt.Errorf("F74a attested receipt has invalid scope, chronology, identity, or inventory")
	}
	identity, err := ReadF74aDatabaseIdentity(ctx, pool)
	if err != nil {
		return out, err
	}
	if identity != p.SourceDatabase {
		return out, fmt.Errorf("F74a attested receipt is for a different PostgreSQL database")
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	receiptSHA256 := hex.EncodeToString(receiptDigest[:])
	var inventory F74aRestorePreflight
	if runID == nil {
		inventory, err = PreflightF74aRestore(ctx, pool, p.Cutoff)
	} else {
		var runReceiptID uuid.UUID
		var runReceiptSHA256, runInventorySHA256, runBackupSHA256 string
		var runDatabase F74aDatabaseIdentity
		var runCutoff time.Time
		err = pool.QueryRow(ctx, `SELECT receipt_id,receipt_sha256,source_inventory_sha256,
			source_database_name,source_database_oid,source_system_identifier,backup_object_sha256,cutoff
			FROM f74a_confirmed_deletion_runs WHERE id=$1`, *runID).Scan(&runReceiptID,
			&runReceiptSHA256, &runInventorySHA256, &runDatabase.Name, &runDatabase.OID,
			&runDatabase.SystemIdentifier, &runBackupSHA256, &runCutoff)
		if err != nil || runReceiptID != p.ReceiptID || runReceiptSHA256 != receiptSHA256 ||
			runInventorySHA256 != p.SourceInventorySHA256 || runDatabase != p.SourceDatabase ||
			runBackupSHA256 != p.BackupObjectSHA256 || !runCutoff.Equal(p.Cutoff) {
			return out, fmt.Errorf("F74a signed receipt does not match deletion run provenance")
		}
		inventory, err = preflightF74aRestoreForRun(ctx, pool, p.Cutoff, *runID)
	}
	if err != nil {
		return out, fmt.Errorf("F74a attested receipt local inventory: %w", err)
	}
	if inventory.InventorySHA256 != p.SourceInventorySHA256 {
		return out, fmt.Errorf("F74a attested receipt inventory differs from connected database")
	}
	return F74aReceiptVerification{
		ReceiptID: p.ReceiptID, ReceiptSHA256: receiptSHA256, SourceDatabase: identity, Cutoff: p.Cutoff,
		InventorySHA256: inventory.InventorySHA256,
		BackupObjectRef: p.BackupObjectRef, BackupObjectSHA256: p.BackupObjectSHA256, ExpiresAt: p.ExpiresAt,
	}, nil
}

func sameF74aPhysicalDatabase(a, b F74aDatabaseIdentity) bool {
	return a.SystemIdentifier == b.SystemIdentifier && a.OID == b.OID
}

func validF74aIdentity(id F74aDatabaseIdentity) bool {
	if strings.TrimSpace(id.Name) != id.Name || id.Name == "" {
		return false
	}
	oid, oidErr := strconv.ParseUint(id.OID, 10, 32)
	systemID, systemErr := strconv.ParseUint(id.SystemIdentifier, 10, 64)
	return oidErr == nil && oid > 0 && strconv.FormatUint(oid, 10) == id.OID &&
		systemErr == nil && systemID > 0 && strconv.FormatUint(systemID, 10) == id.SystemIdentifier
}

func validF74aDigest(text string) bool {
	decoded, err := hex.DecodeString(text)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == text
}

// Match the signed wire schema exactly. encoding/json accepts case-insensitive
// struct-field aliases, which could give the same signed bytes different
// meanings in another implementation. No unknown, duplicate, or aliased key
// may enter either envelope or payload, including nested database identities.
func rejectF74aDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkF74aJSONObject(decoder, "envelope"); err != nil {
		return fmt.Errorf("F74a receipt JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("F74a receipt JSON has trailing content")
	}
	return nil
}

func walkF74aJSONObject(decoder *json.Decoder, schema string) error {
	open, err := decoder.Token()
	if err != nil {
		return err
	}
	if open != json.Delim('{') {
		return fmt.Errorf("expected JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("object key is not a string")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate object key")
		}
		seen[key] = struct{}{}
		nested, allowed := f74aJSONField(schema, key)
		if !allowed {
			return fmt.Errorf("unexpected object key")
		}
		if nested != "" {
			if err := walkF74aJSONObject(decoder, nested); err != nil {
				return err
			}
			continue
		}
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		if _, container := value.(json.Delim); container {
			return fmt.Errorf("unexpected JSON container")
		}
	}
	close, err := decoder.Token()
	if err != nil {
		return err
	}
	if close != json.Delim('}') {
		return fmt.Errorf("invalid JSON object")
	}
	return nil
}

func f74aJSONField(schema, key string) (nested string, allowed bool) {
	switch schema {
	case "envelope":
		switch key {
		case "payload":
			return "payload", true
		case "signature":
			return "", true
		}
	case "payload":
		switch key {
		case "source_database", "restore_database":
			return "database", true
		case "version", "receipt_id", "cutoff", "snapshot_id", "backup_object_ref",
			"snapshot_created_at", "backup_object_sha256", "source_inventory_sha256",
			"restore_inventory_sha256", "restore_verified_at", "issued_at", "expires_at":
			return "", true
		}
	case "database":
		switch key {
		case "name", "oid", "system_identifier":
			return "", true
		}
	}
	return "", false
}
