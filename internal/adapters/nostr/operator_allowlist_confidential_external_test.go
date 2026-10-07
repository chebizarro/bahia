package nostr_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type allowlistSink struct{ events []gonostr.Event }

func (s *allowlistSink) PublishProjection(_ context.Context, event gonostr.Event, _ string, _ *uuid.UUID) error {
	s.events = append(s.events, event)
	return nil
}

// fleetOCKEncryptor is the real confidential scheme keyed by one fleet OCK:
// what ConfidentialEncryptor does once OCKManager has resolved the key.
type fleetOCKEncryptor struct{ key controlplane.OrgContentKey }

func (e fleetOCKEncryptor) EncryptConfidential(ctx context.Context, orgID string, plaintext []byte, legacyKind int, dTag, topic string, serviceOnly []byte) (string, error) {
	if orgID != e.key.OrgID {
		return "", context.Canceled
	}
	return controlplane.EncryptConfidentialContent(ctx, e.key, plaintext, controlplane.ConfidentialRecordContext{LegacyKind: legacyKind, DTag: dTag, Topic: topic}, serviceOnly, nil)
}
func (e fleetOCKEncryptor) DecryptConfidential(_ context.Context, content string, legacyKind int, dTag, topic string) ([]byte, error) {
	return controlplane.DecryptConfidentialContent(e.key, content, controlplane.ConfidentialRecordContext{LegacyKind: legacyKind, DTag: dTag, Topic: topic})
}
func (fleetOCKEncryptor) DecryptServiceInner(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (fleetOCKEncryptor) RotateKey(context.Context, string) error                  { return nil }
func (fleetOCKEncryptor) RotateKeyExcluding(context.Context, string, string) error { return nil }
func (fleetOCKEncryptor) WrapKeyForMember(context.Context, string, string) error   { return nil }

// AC2: a fleet-OCK holder decrypts the published allowlist; without the key
// (or with another org's key) the content is opaque and the operator pubkeys
// appear nowhere in the signed event.
func TestOperatorAllowlistRecordReadableOnlyWithFleetOCK(t *testing.T) {
	var fleetKey controlplane.OrgContentKey
	fleetKey.OrgID, fleetKey.Version = kinds.FleetOCKScope, 1
	for i := range fleetKey.Key {
		fleetKey.Key[i] = byte(i + 1)
	}
	otherKey := fleetKey
	otherKey.Key[0] ^= 0xff

	sink := &allowlistSink{}
	projector := nostradapter.NewProjector(config.NostrConfig{PrivateKey: strings.Repeat("1", 64), PublishEnabled: true}, &paritySource{}, sink, nil, zap.NewNop())
	require.True(t, projector.Enabled())
	publisher := nostradapter.NewOperatorAllowlistPublisher(projector, fleetOCKEncryptor{key: fleetKey}, zap.NewNop())

	alice, bob := strings.Repeat("a", 64), strings.Repeat("b", 64)
	require.NoError(t, publisher.PublishOperatorAllowlist(context.Background(), kinds.OperatorAllowlistScopeContinuity, []string{alice, bob}))
	require.Len(t, sink.events, 1)
	event := sink.events[0]
	require.Equal(t, gonostr.Kind(kinds.CASControlState), event.Kind)

	// Opaque to relays and non-holders: the serialized event never contains a
	// pubkey of the allowlist (the content is AEAD ciphertext, the tags only
	// name the scope).
	serialized, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, strings.ToLower(string(serialized)), alice)
	require.NotContains(t, strings.ToLower(string(serialized)), bob)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(event.Content), &envelope))
	require.Equal(t, controlplane.ConfidentialSchema, envelope["schema"])
	require.Equal(t, kinds.FleetOCKScope, envelope["key_org"])

	recordCtx := controlplane.ConfidentialRecordContext{LegacyKind: int(kinds.CPStateFamilyOperatorAllowlist), DTag: "operators:continuity", Topic: kinds.CPStateTopicOperatorAllowlist}
	plaintext, err := controlplane.DecryptConfidentialContent(fleetKey, event.Content, recordCtx)
	require.NoError(t, err, "a fleet-OCK holder reads the allowlist")
	var record nostradapter.OperatorAllowlistRecord
	require.NoError(t, json.Unmarshal(plaintext, &record))
	require.Equal(t, []string{alice, bob}, record.Pubkeys)
	require.Equal(t, kinds.OperatorAllowlistScopeContinuity, record.Scope)

	_, err = controlplane.DecryptConfidentialContent(otherKey, event.Content, recordCtx)
	require.Error(t, err, "a non-holder cannot read the allowlist")
	// The AEAD binds the coordinate: the ciphertext cannot be replayed as
	// another scope's allowlist even by a key holder.
	_, err = controlplane.DecryptConfidentialContent(fleetKey, event.Content, controlplane.ConfidentialRecordContext{LegacyKind: recordCtx.LegacyKind, DTag: "operators:soul-factory", Topic: recordCtx.Topic})
	require.Error(t, err)
}
