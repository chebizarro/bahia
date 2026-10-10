package repository

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const f74aLiveGrantVersion = "bahia-f74a-live-custody-v1"
const f74aLiveGrantDomain = "bahia-f74a-live-custody-v1\x00"
const f74aLiveGrantMaxBytes = 65536

// f74aLiveAttestorProof is private until an independently operated authority
// implements the documented non-revocable custody hold. Bahia never signs a
// live grant and never treats a signed restore receipt as a live grant.
type f74aLiveAttestorProof struct {
	pool     *pgxpool.Pool
	pin      string
	receipt  []byte
	endpoint string
	client   *http.Client
}

type f74aLiveGrantRequest struct {
	Nonce                 string               `json:"nonce"`
	ReceiptID             uuid.UUID            `json:"receipt_id"`
	ReceiptSHA256         string               `json:"receipt_sha256"`
	RunID                 uuid.UUID            `json:"run_id"`
	SourceDatabase        F74aDatabaseIdentity `json:"source_database"`
	Cutoff                time.Time            `json:"cutoff"`
	SourceInventorySHA256 string               `json:"source_inventory_sha256"`
	BackupObjectRef       string               `json:"backup_object_ref"`
	BackupObjectSHA256    string               `json:"backup_object_sha256"`
}

type f74aLiveGrantPayload struct {
	Version                      string               `json:"version"`
	Nonce                        string               `json:"nonce"`
	ReceiptID                    uuid.UUID            `json:"receipt_id"`
	ReceiptSHA256                string               `json:"receipt_sha256"`
	RunID                        uuid.UUID            `json:"run_id"`
	SourceDatabase               F74aDatabaseIdentity `json:"source_database"`
	Cutoff                       time.Time            `json:"cutoff"`
	SourceInventorySHA256        string               `json:"source_inventory_sha256"`
	BackupObjectRef              string               `json:"backup_object_ref"`
	BackupObjectSHA256           string               `json:"backup_object_sha256"`
	IssuedAt                     time.Time            `json:"issued_at"`
	ExpiresAt                    time.Time            `json:"expires_at"`
	RetainedUntil                time.Time            `json:"retained_until"`
	CredentialRecoveryVerifiedAt time.Time            `json:"credential_recovery_verified_at"`
	Revoked                      bool                 `json:"revoked"`
	CustodyHoldID                string               `json:"custody_hold_id"`
	HoldUntilExplicitRelease     bool                 `json:"hold_until_explicit_release"`
}

func newF74aLiveAttestorProof(pool *pgxpool.Pool, pin string, receipt []byte, endpoint string, client *http.Client) (*f74aLiveAttestorProof, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, fmt.Errorf("F74a live attestor endpoint must be an HTTPS URL without credentials or query")
	}
	pub, err := hex.DecodeString(strings.TrimSpace(pin))
	if err != nil || len(pub) != ed25519.PublicKeySize || pool == nil || len(receipt) == 0 || len(receipt) > F74aReceiptMaxBytes {
		return nil, fmt.Errorf("F74a live attestor configuration is incomplete")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &f74aLiveAttestorProof{pool: pool, pin: pin, receipt: bytes.Clone(receipt), endpoint: endpoint, client: client}, nil
}

func (p *f74aLiveAttestorProof) proveCurrentF74aBackup(ctx context.Context, runID uuid.UUID) (F74aReceiptVerification, error) {
	var proof F74aReceiptVerification
	var err error
	if runID == uuid.Nil {
		proof, err = VerifyF74aAttestedReceipt(ctx, p.pool, p.pin, p.receipt)
	} else {
		proof, err = verifyF74aAttestedReceiptForDeletionRun(ctx, p.pool, p.pin, p.receipt, runID)
	}
	if err != nil {
		return F74aReceiptVerification{}, fmt.Errorf("F74a signed restore receipt is not current: %w", err)
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return F74aReceiptVerification{}, fmt.Errorf("F74a live attestor nonce unavailable")
	}
	request := f74aLiveGrantRequest{
		Nonce: hex.EncodeToString(nonce[:]), ReceiptID: proof.ReceiptID, ReceiptSHA256: proof.ReceiptSHA256,
		RunID: runID, SourceDatabase: proof.SourceDatabase, Cutoff: proof.Cutoff,
		SourceInventorySHA256: proof.InventorySHA256, BackupObjectRef: proof.BackupObjectRef,
		BackupObjectSHA256: proof.BackupObjectSHA256,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return F74aReceiptVerification{}, fmt.Errorf("encoding F74a live attestor request: %w", err)
	}
	// Bound network time even when the caller has no deadline. Redirects are
	// refused so request scope cannot leak to another endpoint.
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(checkCtx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return F74aReceiptVerification{}, fmt.Errorf("creating F74a live attestor request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	requestedAt := time.Now().UTC()
	response, err := client.Do(httpRequest)
	if err != nil {
		return F74aReceiptVerification{}, fmt.Errorf("F74a live attestor unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return F74aReceiptVerification{}, fmt.Errorf("F74a live attestor refused admission")
	}
	responseJSON, err := io.ReadAll(io.LimitReader(response.Body, f74aLiveGrantMaxBytes+1))
	if err != nil || len(responseJSON) == 0 || len(responseJSON) > f74aLiveGrantMaxBytes {
		return F74aReceiptVerification{}, fmt.Errorf("F74a live attestor response is invalid")
	}
	if err := verifyF74aLiveGrant(responseJSON, p.pin, request, requestedAt, time.Now().UTC()); err != nil {
		return F74aReceiptVerification{}, err
	}
	return proof, nil
}

func verifyF74aLiveGrant(raw []byte, pin string, request f74aLiveGrantRequest, requestedAt, now time.Time) error {
	if err := rejectF74aLiveGrantJSON(raw); err != nil {
		return err
	}
	var envelope struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("F74a live grant envelope is invalid")
	}
	publicKey, err := hex.DecodeString(strings.TrimSpace(pin))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("F74a live attestor pin is invalid")
	}
	signature, err := hex.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, append([]byte(f74aLiveGrantDomain), envelope.Payload...), signature) {
		return fmt.Errorf("F74a live grant signature is invalid")
	}
	var grant f74aLiveGrantPayload
	if err := json.Unmarshal(envelope.Payload, &grant); err != nil {
		return fmt.Errorf("F74a live grant payload is invalid")
	}
	if grant.Version != f74aLiveGrantVersion || grant.Nonce != request.Nonce ||
		grant.ReceiptID != request.ReceiptID || grant.ReceiptSHA256 != request.ReceiptSHA256 ||
		grant.RunID != request.RunID || grant.SourceDatabase != request.SourceDatabase ||
		!grant.Cutoff.Equal(request.Cutoff) || grant.SourceInventorySHA256 != request.SourceInventorySHA256 ||
		grant.BackupObjectRef != request.BackupObjectRef || grant.BackupObjectSHA256 != request.BackupObjectSHA256 ||
		grant.Revoked || !grant.HoldUntilExplicitRelease || strings.TrimSpace(grant.CustodyHoldID) == "" ||
		strings.TrimSpace(grant.CustodyHoldID) != grant.CustodyHoldID ||
		grant.IssuedAt.Before(requestedAt.Add(-5*time.Second)) || grant.IssuedAt.After(now.Add(5*time.Second)) ||
		!now.Add(10*time.Second).Before(grant.ExpiresAt) || grant.ExpiresAt.After(grant.IssuedAt.Add(2*time.Minute)) ||
		grant.CredentialRecoveryVerifiedAt.Before(grant.IssuedAt.Add(-5*time.Minute)) ||
		grant.CredentialRecoveryVerifiedAt.After(grant.IssuedAt) ||
		!grant.RetainedUntil.After(grant.ExpiresAt.Add(24*time.Hour)) {
		return fmt.Errorf("F74a live grant is stale, revoked, unfenced, or outside receipt scope")
	}
	return nil
}

func rejectF74aLiveGrantJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkF74aLiveJSONObject(decoder, "envelope"); err != nil {
		return fmt.Errorf("F74a live grant JSON is invalid: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("F74a live grant JSON has trailing content")
	}
	return nil
}

func walkF74aLiveJSONObject(decoder *json.Decoder, schema string) error {
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return fmt.Errorf("expected object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate or invalid key")
		}
		seen[key] = true
		nested, allowed := f74aLiveField(schema, key)
		if !allowed {
			return fmt.Errorf("unexpected key")
		}
		if nested != "" {
			if err := walkF74aLiveJSONObject(decoder, nested); err != nil {
				return err
			}
		} else {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if value == nil {
				return fmt.Errorf("null value")
			}
			if _, container := value.(json.Delim); container {
				return fmt.Errorf("unexpected container")
			}
		}
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') {
		return fmt.Errorf("invalid object")
	}
	expected := 0
	switch schema {
	case "envelope":
		expected = 2
	case "payload":
		expected = 17
	case "database":
		expected = 3
	}
	if len(seen) != expected {
		return fmt.Errorf("missing required key")
	}
	return nil
}

func f74aLiveField(schema, key string) (string, bool) {
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
		case "source_database":
			return "database", true
		case "version", "nonce", "receipt_id", "receipt_sha256", "run_id", "cutoff", "source_inventory_sha256",
			"backup_object_ref", "backup_object_sha256", "issued_at", "expires_at", "retained_until",
			"credential_recovery_verified_at", "revoked", "custody_hold_id", "hold_until_explicit_release":
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
