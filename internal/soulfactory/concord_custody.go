package soulfactory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fiatjaf.com/nostr"
)

// concordCustodyMaxBytes bounds every custody read. The sealed form is a NIP-44
// payload wrapping a document at least as large as the bundle, so the ceiling is
// generous relative to the 65,535-byte NIP-44 plaintext cap.
const concordCustodyMaxBytes = 262144

// concordCustodyVersion is the current sealed custody document version.
const concordCustodyVersion = 1

// concordCustodyRecord is the material Soul Factory holds for one community.
// Bundle is the CORD-05 §1 CommunityInvite handed to invitees; ControlRoot is
// the CORD-02 §2 staff write secret, which is minted by a Refounding and MUST
// NOT travel in an invite. Neither field is ever logged or returned to callers.
type concordCustodyRecord struct {
	Bundle      json.RawMessage
	ControlRoot string
}

// concordCustodyDocument is the sealed on-disk representation of a record.
type concordCustodyDocument struct {
	Version      int             `json:"version"`
	InviteBundle json.RawMessage `json:"invite_bundle"`
	ControlRoot  string          `json:"control_root,omitempty"`
}

// concordBundleCustody is the storage boundary for Concord invite material.
// Plaintext key material exists only between a Load and the operation that
// consumes it; at rest it is either operator-managed (read-only) or sealed
// under the Signet-held staff key.
type concordBundleCustody interface {
	Load(ctx context.Context) (concordCustodyRecord, error)
	Store(ctx context.Context, record concordCustodyRecord) error
	// Source is a redacted description safe for logs and errors.
	Source() string
	Writable() bool
}

// concordCustodySigner is the Signet surface custody needs: the staff pubkey
// plus NIP-44 seal and unseal. The staff secret never leaves the bunker.
type concordCustodySigner interface {
	GetPublicKey(context.Context) (string, error)
	NIP44Encrypt(context.Context, nostr.PubKey, string) (string, error)
	NIP44Decrypt(context.Context, nostr.PubKey, string) (string, error)
}

// staticConcordCustody holds invite material supplied by the operator through
// an environment variable or a mounted secret file. It is read-only: rotation
// would have nowhere durable to put the fresh material.
type staticConcordCustody struct {
	bundle json.RawMessage
}

func (c *staticConcordCustody) Load(context.Context) (concordCustodyRecord, error) {
	return concordCustodyRecord{Bundle: append(json.RawMessage(nil), c.bundle...)}, nil
}

func (c *staticConcordCustody) Store(context.Context, concordCustodyRecord) error {
	return fmt.Errorf("invite material from %s is read-only; CORD-06 rotation requires Signet-sealed custody (invite_bundle_sealed_file)", c.Source())
}

func (c *staticConcordCustody) Source() string { return "operator-supplied secret" }

func (c *staticConcordCustody) Writable() bool { return false }

// sealedConcordCustody keeps invite material in a file that only ever contains
// a NIP-44 payload sealed to the Signet-held staff key. Reading it requires a
// bunker round trip, so a leaked config, backup, or container image yields
// ciphertext instead of a community's access keys.
type sealedConcordCustody struct {
	path   string
	signer concordCustodySigner
}

func newSealedConcordCustody(path string, signer concordCustodySigner) (*sealedConcordCustody, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sealed custody path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("sealed custody path %s must be absolute", path)
	}
	if signer == nil {
		return nil, fmt.Errorf("sealed custody requires a Signet signer with NIP-44 encryption and decryption")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("sealed custody file: %w", err)
	}
	if info.IsDir() || info.Size() == 0 {
		return nil, fmt.Errorf("sealed custody file %s must be a non-empty regular file", path)
	}
	return &sealedConcordCustody{path: path, signer: signer}, nil
}

func (c *sealedConcordCustody) Source() string { return "Signet-sealed custody " + c.path }

func (c *sealedConcordCustody) Writable() bool { return true }

func (c *sealedConcordCustody) Load(ctx context.Context) (concordCustodyRecord, error) {
	sealed, err := readConcordCustodyFile(c.path)
	if err != nil {
		return concordCustodyRecord{}, err
	}
	staffPK, err := c.staffPubKey(ctx)
	if err != nil {
		return concordCustodyRecord{}, err
	}
	plaintext, err := c.signer.NIP44Decrypt(ctx, staffPK, sealed)
	if err != nil {
		return concordCustodyRecord{}, fmt.Errorf("Signet unseal custody %s: %w", c.path, err)
	}
	record, err := decodeConcordCustodyDocument([]byte(plaintext))
	if err != nil {
		return concordCustodyRecord{}, fmt.Errorf("custody %s: %w", c.path, err)
	}
	return record, nil
}

func (c *sealedConcordCustody) Store(ctx context.Context, record concordCustodyRecord) error {
	plaintext, err := encodeConcordCustodyDocument(record)
	if err != nil {
		return err
	}
	staffPK, err := c.staffPubKey(ctx)
	if err != nil {
		return err
	}
	sealed, err := c.signer.NIP44Encrypt(ctx, staffPK, string(plaintext))
	if err != nil {
		return fmt.Errorf("Signet seal custody %s: %w", c.path, err)
	}
	if strings.TrimSpace(sealed) == "" {
		return fmt.Errorf("Signet returned an empty sealed payload for custody %s", c.path)
	}
	// Never replace custody with material Signet cannot reopen: unseal the
	// fresh payload and require it to match before it becomes the only copy.
	verified, err := c.signer.NIP44Decrypt(ctx, staffPK, sealed)
	if err != nil {
		return fmt.Errorf("verify sealed custody %s: %w", c.path, err)
	}
	if verified != string(plaintext) {
		return fmt.Errorf("verify sealed custody %s: unsealed payload does not match", c.path)
	}
	return writeConcordCustodyFile(c.path, sealed)
}

func (c *sealedConcordCustody) staffPubKey(ctx context.Context) (nostr.PubKey, error) {
	staffHex, err := c.signer.GetPublicKey(ctx)
	if err != nil {
		return nostr.PubKey{}, fmt.Errorf("resolve Concord staff pubkey from Signet: %w", err)
	}
	staffPK, err := nostr.PubKeyFromHex(strings.ToLower(strings.TrimSpace(staffHex)))
	if err != nil {
		return nostr.PubKey{}, fmt.Errorf("invalid Concord staff pubkey from Signet: %w", err)
	}
	return staffPK, nil
}

func readConcordCustodyFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open custody file: %w", err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, concordCustodyMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read custody file %s: %w", path, err)
	}
	if len(contents) > concordCustodyMaxBytes {
		return "", fmt.Errorf("custody file %s exceeds %d bytes", path, concordCustodyMaxBytes)
	}
	sealed := strings.TrimSpace(string(contents))
	if sealed == "" {
		return "", fmt.Errorf("custody file %s is empty", path)
	}
	return sealed, nil
}

// writeConcordCustodyFile replaces the custody file atomically so a crash mid
// rotation can never leave truncated ciphertext as the only copy.
func writeConcordCustodyFile(path, sealed string) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".concord-custody-*")
	if err != nil {
		return fmt.Errorf("create custody temp file in %s: %w", dir, err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("restrict custody temp file permissions: %w", err)
	}
	if _, err := temp.WriteString(sealed + "\n"); err != nil {
		cleanup()
		return fmt.Errorf("write custody temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync custody temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close custody temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace custody file %s: %w", path, err)
	}
	return nil
}

// decodeConcordCustodyDocument accepts either the versioned custody document or
// a bare CommunityInvite, so an operator can seal an existing bundle as-is and
// have the first Refounding upgrade it to the document form.
func decodeConcordCustodyDocument(plaintext []byte) (concordCustodyRecord, error) {
	plaintext = bytes.TrimSpace(plaintext)
	if len(plaintext) == 0 {
		return concordCustodyRecord{}, fmt.Errorf("sealed payload is empty")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &fields); err != nil {
		return concordCustodyRecord{}, fmt.Errorf("sealed payload is not a JSON object")
	}
	if _, ok := fields["invite_bundle"]; !ok {
		// A bare CommunityInvite: the caller validates it as usual.
		return concordCustodyRecord{Bundle: append(json.RawMessage(nil), plaintext...)}, nil
	}
	var document concordCustodyDocument
	if err := json.Unmarshal(plaintext, &document); err != nil {
		return concordCustodyRecord{}, fmt.Errorf("decode custody document: %w", err)
	}
	if document.Version != concordCustodyVersion {
		return concordCustodyRecord{}, fmt.Errorf("custody document version %d is unsupported", document.Version)
	}
	bundle := bytes.TrimSpace(document.InviteBundle)
	if !validConcordBundleObject(bundle) {
		return concordCustodyRecord{}, fmt.Errorf("custody document has no invite bundle")
	}
	if document.ControlRoot != "" && !validConcordHex32(document.ControlRoot) {
		return concordCustodyRecord{}, fmt.Errorf("custody document control_root must be 32-byte lowercase hex")
	}
	return concordCustodyRecord{
		Bundle:      append(json.RawMessage(nil), bundle...),
		ControlRoot: document.ControlRoot,
	}, nil
}

func encodeConcordCustodyDocument(record concordCustodyRecord) ([]byte, error) {
	bundle := json.RawMessage(bytes.TrimSpace(record.Bundle))
	if !validConcordBundleObject(bundle) {
		return nil, fmt.Errorf("custody record has no valid invite bundle")
	}
	if record.ControlRoot != "" && !validConcordHex32(record.ControlRoot) {
		return nil, fmt.Errorf("custody record control_root must be 32-byte lowercase hex")
	}
	plaintext, err := json.Marshal(concordCustodyDocument{
		Version:      concordCustodyVersion,
		InviteBundle: bundle,
		ControlRoot:  record.ControlRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("encode custody document: %w", err)
	}
	if len(plaintext) > concordInviteMaxBytes {
		return nil, fmt.Errorf("custody document exceeds the NIP-44 plaintext limit")
	}
	return plaintext, nil
}

// validConcordBundleObject reports whether raw is a JSON object, the only shape
// a CommunityInvite ever takes. A JSON null or scalar in custody means the
// document is corrupt, and custody fails closed rather than passing it on.
func validConcordBundleObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{' && json.Valid(raw)
}
