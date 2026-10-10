package repository

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func f74aSignedLiveGrant(t *testing.T, private ed25519.PrivateKey, grant f74aLiveGrantPayload) []byte {
	t.Helper()
	payload, err := json.Marshal(grant)
	require.NoError(t, err)
	envelope, err := json.Marshal(struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}{payload, hex.EncodeToString(ed25519.Sign(private, append([]byte(f74aLiveGrantDomain), payload...)))})
	require.NoError(t, err)
	return envelope
}

func TestF74aLiveGrantRequiresFreshScopedFencedSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	request := f74aLiveGrantRequest{
		Nonce: strings.Repeat("a", 64), ReceiptID: uuid.New(), ReceiptSHA256: strings.Repeat("1", 64), RunID: uuid.New(),
		SourceDatabase: F74aDatabaseIdentity{Name: "source", OID: "42", SystemIdentifier: "12345"},
		Cutoff:         now.Add(-48 * time.Hour), SourceInventorySHA256: strings.Repeat("2", 64),
		BackupObjectRef: "s3://custodian/object/version", BackupObjectSHA256: strings.Repeat("3", 64),
	}
	grant := f74aLiveGrantPayload{
		Version: f74aLiveGrantVersion, Nonce: request.Nonce, ReceiptID: request.ReceiptID,
		ReceiptSHA256: request.ReceiptSHA256, RunID: request.RunID, SourceDatabase: request.SourceDatabase,
		Cutoff: request.Cutoff, SourceInventorySHA256: request.SourceInventorySHA256,
		BackupObjectRef: request.BackupObjectRef, BackupObjectSHA256: request.BackupObjectSHA256,
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), RetainedUntil: now.Add(48 * time.Hour),
		CredentialRecoveryVerifiedAt: now, CustodyHoldID: "hold-123", HoldUntilExplicitRelease: true,
	}
	pin := hex.EncodeToString(public)
	check := func(g f74aLiveGrantPayload) error {
		return verifyF74aLiveGrant(f74aSignedLiveGrant(t, private, g), pin, request, now, now)
	}
	require.NoError(t, check(grant))
	for _, tc := range []struct {
		name string
		edit func(*f74aLiveGrantPayload)
	}{
		{"stale", func(g *f74aLiveGrantPayload) { g.IssuedAt = now.Add(-time.Minute) }},
		{"expired", func(g *f74aLiveGrantPayload) { g.ExpiresAt = now.Add(5 * time.Second) }},
		{"revoked", func(g *f74aLiveGrantPayload) { g.Revoked = true }},
		{"no hold", func(g *f74aLiveGrantPayload) { g.HoldUntilExplicitRelease = false }},
		{"short retention", func(g *f74aLiveGrantPayload) { g.RetainedUntil = now.Add(time.Hour) }},
		{"stale credential recovery", func(g *f74aLiveGrantPayload) { g.CredentialRecoveryVerifiedAt = now.Add(-time.Hour) }},
		{"wrong nonce", func(g *f74aLiveGrantPayload) { g.Nonce = strings.Repeat("b", 64) }},
		{"wrong run", func(g *f74aLiveGrantPayload) { g.RunID = uuid.New() }},
		{"wrong object", func(g *f74aLiveGrantPayload) { g.BackupObjectSHA256 = strings.Repeat("4", 64) }},
		{"wrong receipt", func(g *f74aLiveGrantPayload) { g.ReceiptSHA256 = strings.Repeat("5", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := grant
			tc.edit(&changed)
			require.Error(t, check(changed))
		})
	}
	wrongPublic, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.Error(t, verifyF74aLiveGrant(f74aSignedLiveGrant(t, private, grant), hex.EncodeToString(wrongPublic), request, now, now))
	signed := f74aSignedLiveGrant(t, private, grant)
	require.Error(t, verifyF74aLiveGrant([]byte(strings.Replace(string(signed), `"nonce":`, `"Nonce":`, 1)), pin, request, now, now))
	require.Error(t, verifyF74aLiveGrant([]byte(strings.Replace(string(signed), `"revoked":false`, `"revoked":null`, 1)), pin, request, now, now))
	require.Error(t, verifyF74aLiveGrant([]byte(strings.Replace(string(signed), `"revoked":false,`, ``, 1)), pin, request, now, now))
}

func TestF74aLiveAttestorRequiresHTTPSAndIndependentPin(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pool := &pgxpool.Pool{}
	for _, endpoint := range []string{"http://authority.example/grant", "https://user:secret@authority.example/grant", "https://authority.example/grant?token=secret"} {
		_, err := newF74aLiveAttestorProof(pool, hex.EncodeToString(public), []byte("receipt"), endpoint, nil)
		require.Error(t, err)
	}
	_, err = newF74aLiveAttestorProof(pool, "", []byte("receipt"), "https://authority.example/grant", nil)
	require.Error(t, err)
}
