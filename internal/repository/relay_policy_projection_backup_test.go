package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestRelayPolicyBackupRequiresSignedFullEvent(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"schema":"bahia.relay-settings.v1","service_relays":["wss://relay.example"]}`)
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"d", "relay-settings:operator"}, {"domain", "relay-settings"}, {"schema", "bahia.relay-settings.v1"}}, Content: string(payload)}
	require.NoError(t, event.Sign(nostr.Generate()))
	rawEvent, err := json.Marshal(event)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	projection := RelayPolicyProjection{AuthorPubkey: event.PubKey.Hex(), EventID: event.ID.Hex(), EventCreatedAt: now, EventAcceptedAt: now, Schema: relayPolicySchema, CanonicalPayload: payload, SignedEvent: rawEvent, PayloadHash: hex.EncodeToString(sum[:]), LastSyncAt: now}
	backup, err := NewRelayPolicyProjectionBackup(projection, now)
	require.NoError(t, err)
	require.NoError(t, ValidateRelayPolicyProjectionBackup(backup))
	decoded, err := DecodeRelayPolicyProjectionBackup(backup)
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), decoded.EventID)

	stale := backup
	stale.BackupSchema = "bahia.relay-policy-projection-backup.v1"
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(stale), "unsupported")
	missing := backup
	missing.SignedEvent = nil
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(missing), "signed_event is required")
	wrongAuthor := backup
	wrongAuthor.AuthorPubkey = nostr.Generate().Public().Hex()
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(wrongAuthor), "provenance mismatch")
	wrongID := backup
	wrongID.EventID = wrongAuthor.AuthorPubkey
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(wrongID), "provenance mismatch")
	tampered := event
	tampered.Content = `{"schema":"bahia.relay-settings.v1","service_relays":["wss://attacker.example"]}`
	badEvent, err := json.Marshal(tampered)
	require.NoError(t, err)
	badSignature := backup
	badSignature.SignedEvent = badEvent
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(badSignature), "invalid id or Schnorr signature")
	wrongPayload := backup
	wrongPayload.CanonicalPayload = []byte(`{"schema":"bahia.relay-settings.v1","service_relays":["wss://attacker.example"]}`)
	wrongSum := sha256.Sum256(wrongPayload.CanonicalPayload)
	wrongPayload.PayloadHash = hex.EncodeToString(wrongSum[:])
	require.ErrorContains(t, ValidateRelayPolicyProjectionBackup(wrongPayload), "content mismatch")
}
