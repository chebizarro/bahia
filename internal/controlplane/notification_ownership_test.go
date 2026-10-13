package controlplane

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNotificationOwnershipUsesSignedEncryptedCanonicalScope(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	key, err := nostr.SecretKeyFromHex(serviceKeyHex)
	require.NoError(t, err)
	manager, _, _ := newTestOCKManager(t, nil)
	encryptor := NewConfidentialEncryptor(manager, zap.NewNop())
	reader, err := NewLocalNotificationOwnershipReader(store, encryptor, key.Public().Hex())
	require.NoError(t, err)
	org := uuid.New()
	id := uuid.New()
	d := id.String()
	topic := kinds.CPStateTopicNotificationChannelRegistry
	for i, tc := range []struct {
		name       string
		contentOrg uuid.UUID
		valid      bool
	}{
		{"owner", org, true},
		{"mismatched-inner-owner", uuid.New(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, err := json.Marshal(map[string]any{"id": id.String(), "org_id": tc.contentOrg.String(), "name": "private"})
			require.NoError(t, err)
			encrypted, err := encryptor.EncryptConfidential(t.Context(), org.String(), []byte(`{"id":"`+d+`"}`), kinds.NotificationChannelRegistry, d, topic, inner)
			require.NoError(t, err)
			ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now() + nostr.Timestamp(i), Tags: nostr.Tags{{"d", d}, {"t", topic}, {"domain", "notification"}, {"schema", kinds.CASControlStateSchema}, {"legacy_kind", strconv.Itoa(kinds.NotificationChannelRegistry)}, {"deleted", "false"}}, Content: encrypted}
			require.NoError(t, ev.Sign(key))
			_, err = store.SaveEvent(ev)
			require.NoError(t, err)
			owner, found, err := reader.OwnerOf(t.Context(), id)
			require.True(t, found)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, org, owner)
			} else {
				require.ErrorContains(t, err, "inconsistent")
			}
		})
	}
}

func TestConfidentialEnvelopeMalformedVersionFailsClosed(t *testing.T) {
	for _, version := range []string{"", "x1", "v0"} {
		raw, err := json.Marshal(ConfidentialEnvelope{Schema: ConfidentialSchema, KeyOrg: uuid.NewString(), KeyVersion: version})
		require.NoError(t, err)
		_, _, err = VersionFromEnvelope(string(raw))
		require.Error(t, err)
	}
}
