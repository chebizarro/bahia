package nostr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newAllowlistPublisherForTest(t *testing.T) (*OperatorAllowlistPublisher, *captureProjectionPublisher, *mockConfidentialEncryptor) {
	t.Helper()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	encryptor := &mockConfidentialEncryptor{}
	publisher := NewOperatorAllowlistPublisher(projector, encryptor, zap.NewNop())
	publisher.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	return publisher, sink, encryptor
}

// AC1: the record is published on operators:<scope> under the family envelope
// with the configured set (normalised) only in the encrypted content. No tag,
// including the coordinate, carries an operator pubkey.
func TestOperatorAllowlistPublisherPublishesConfiguredSetWithoutPubkeyTags(t *testing.T) {
	publisher, sink, encryptor := newAllowlistPublisherForTest(t)
	alice, bob := strings.Repeat("a", 64), strings.Repeat("b", 64)
	ctx := context.Background()
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, []string{" " + strings.ToUpper(bob) + " ", alice, bob, "not-a-pubkey"}))

	records := sink.byKind(kinds.CASControlState)
	require.Len(t, records, 1)
	record := records[0]
	require.True(t, hasTag(record.Tags, "t", kinds.CPStateTopicOperatorAllowlist))
	require.True(t, hasTag(record.Tags, "legacy_kind", "32029"))
	require.True(t, hasTag(record.Tags, "d", "operators:continuity"))
	require.True(t, hasTag(record.Tags, "domain", "operator"))
	require.True(t, hasTag(record.Tags, "deleted", "false"))
	for _, tag := range record.Tags {
		require.NotEqual(t, "p", tag[0], "allowlist records must not name operators in p tags")
		for _, value := range tag {
			require.NotContains(t, strings.ToLower(value), alice)
			require.NotContains(t, strings.ToLower(value), bob)
		}
	}
	require.Equal(t, kinds.FleetOCKScope, encryptor.lastOrgID, "allowlists are fleet-scoped")
	require.Equal(t, KindOperatorAllowlistRecord, encryptor.lastLegacyKind)
	require.Equal(t, "operators:continuity", encryptor.lastDTag)
	require.Equal(t, kinds.CPStateTopicOperatorAllowlist, encryptor.lastTopic)
	require.Nil(t, encryptor.lastServiceOnlyPlain)

	plaintext, err := encryptor.DecryptConfidential(ctx, record.Content, KindOperatorAllowlistRecord, "operators:continuity", kinds.CPStateTopicOperatorAllowlist)
	require.NoError(t, err)
	var decoded OperatorAllowlistRecord
	require.NoError(t, json.Unmarshal(plaintext, &decoded))
	require.Equal(t, kinds.OperatorAllowlistScopeContinuity, decoded.Scope)
	require.Equal(t, []string{alice, bob}, decoded.Pubkeys, "lower-cased, deduplicated, sorted")
	require.Equal(t, time.Unix(1_700_000_000, 0).UTC(), decoded.UpdatedAt)
}

// AC3: an unchanged set is not republished; a changed set replaces the record
// on the same coordinate; the two scopes are independent coordinates.
func TestOperatorAllowlistPublisherReplacesOnChangeOnly(t *testing.T) {
	publisher, sink, encryptor := newAllowlistPublisherForTest(t)
	alice, bob := strings.Repeat("a", 64), strings.Repeat("b", 64)
	ctx := context.Background()
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, []string{alice}))
	require.Len(t, sink.byKind(kinds.CASControlState), 1)

	// Same set, different order and case, later clock: nothing new.
	publisher.now = func() time.Time { return time.Unix(1_700_000_999, 0) }
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, []string{strings.ToUpper(alice)}))
	require.Len(t, sink.byKind(kinds.CASControlState), 1, "unchanged allowlist must not republish")

	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, []string{alice, bob}))
	records := sink.byKind(kinds.CASControlState)
	require.Len(t, records, 2)
	require.True(t, hasTag(records[1].Tags, "d", "operators:continuity"))
	require.Greater(t, records[1].CreatedAt, records[0].CreatedAt, "replacement must win on the relay")
	plaintext, err := encryptor.DecryptConfidential(ctx, records[1].Content, KindOperatorAllowlistRecord, "operators:continuity", kinds.CPStateTopicOperatorAllowlist)
	require.NoError(t, err)
	var decoded OperatorAllowlistRecord
	require.NoError(t, json.Unmarshal(plaintext, &decoded))
	require.Equal(t, []string{alice, bob}, decoded.Pubkeys)

	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeSoulFactory, []string{bob}))
	records = sink.byKind(kinds.CASControlState)
	require.Len(t, records, 3)
	require.True(t, hasTag(records[2].Tags, "d", "operators:soul-factory"))
}

// AC4: an emptied (or disabled) scope replaces its record with a tombstone on
// the same coordinate, with no pubkey anywhere.
func TestOperatorAllowlistPublisherTombstonesEmptiedScope(t *testing.T) {
	publisher, sink, _ := newAllowlistPublisherForTest(t)
	alice := strings.Repeat("a", 64)
	ctx := context.Background()
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeSoulFactory, []string{alice}))
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeSoulFactory, nil))
	records := sink.byKind(kinds.CASControlState)
	require.Len(t, records, 2)
	tombstone := records[1]
	require.True(t, hasTag(tombstone.Tags, "d", "operators:soul-factory"))
	require.True(t, hasTag(tombstone.Tags, "deleted", "true"))
	require.True(t, hasTag(tombstone.Tags, "t", kinds.CPStateTopicOperatorAllowlist))
	require.True(t, isTombstoneTags(tombstone.Tags))
	require.NotContains(t, tombstone.Content, alice)
	// A list of only invalid entries is an empty list.
	require.NoError(t, publisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeSoulFactory, []string{"npub1notvalid", ""}))
	require.Len(t, sink.byKind(kinds.CASControlState), 2, "an unchanged tombstone must not republish")
}

func TestOperatorAllowlistPublisherFailsClosed(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	ctx := context.Background()
	err := NewOperatorAllowlistPublisher(projector, nil, nil).PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, []string{strings.Repeat("a", 64)})
	require.ErrorContains(t, err, "confidential encryptor not configured")
	require.Empty(t, sink.byKind(kinds.CASControlState))

	publisher, _, _ := newAllowlistPublisherForTest(t)
	require.ErrorContains(t, publisher.PublishOperatorAllowlist(ctx, "Bad Scope", nil), "invalid")
	var nilPublisher *OperatorAllowlistPublisher
	require.Error(t, nilPublisher.PublishOperatorAllowlist(ctx, kinds.OperatorAllowlistScopeContinuity, nil))
}
