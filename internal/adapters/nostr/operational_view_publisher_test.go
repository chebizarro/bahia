package nostr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOperationalViewPublisherSoulPolicyMatchesRESTPayload(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	publisher := NewOperationalViewPublisher(projector, nil)
	runtimes := []string{"openclaw", "metiq"}
	require.NoError(t, publisher.PublishSoulRuntimePolicy(context.Background(), runtimes))
	records := sink.byKind(kinds.CASControlState)
	require.Len(t, records, 1)
	require.True(t, hasTag(records[0].Tags, "t", kinds.CPStateTopicSoulRuntimePolicy))
	require.True(t, hasTag(records[0].Tags, "legacy_kind", "32042"))
	var content map[string][]string
	require.NoError(t, json.Unmarshal([]byte(records[0].Content), &content))
	require.Equal(t, map[string][]string{"agent_runtimes": runtimes}, content)
	require.NoError(t, publisher.PublishSoulRuntimePolicy(context.Background(), runtimes))
	require.Len(t, sink.byKind(kinds.CASControlState), 1, "unchanged policy must not republish")
}

func TestOperationalViewPublisherBlossomConfidentialAndBounded(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	encryptor := &mockConfidentialEncryptor{}
	publisher := NewOperationalViewPublisher(projector, encryptor)
	ctx := context.Background()
	server := "https://blossom.example"
	require.NoError(t, publisher.PublishBlossomAdmin(ctx, []string{server}, map[string]string{server: "ok"}))
	admin := sink.byKind(kinds.CASControlState)
	require.Len(t, admin, 1)
	require.True(t, hasTag(admin[0].Tags, "t", kinds.CPStateTopicBlossomAdmin))
	require.Equal(t, kinds.FleetOCKScope, encryptor.lastOrgID)
	require.Contains(t, admin[0].Content, `"_test_encrypted":true`)
	plaintext, err := encryptor.DecryptConfidential(ctx, admin[0].Content, 0, "", "")
	require.NoError(t, err)
	var adminData struct {
		Servers []string          `json:"servers"`
		Health  map[string]string `json:"health"`
	}
	require.NoError(t, json.Unmarshal(plaintext, &adminData))
	require.Equal(t, []string{server}, adminData.Servers)
	require.Equal(t, "ok", adminData.Health[server])

	owner, hash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	descriptor := blossom.BlobDescriptor{URL: server + "/" + hash, SHA256: hash, Size: 9, Type: "image/png", Uploaded: blossom.BlossomTimestamp{Time: time.Unix(100, 0).UTC()}}
	require.NoError(t, publisher.PublishBlossomBlob(ctx, owner, descriptor))
	records := sink.byKind(kinds.CASControlState)
	require.Len(t, records, 2)
	require.True(t, hasTag(records[1].Tags, "t", kinds.CPStateTopicBlossomBlob))
	require.True(t, hasTag(records[1].Tags, "d", "blossom:blob:"+owner+":"+hash))
	require.Contains(t, records[1].Content, `"_test_encrypted":true`)
	plaintext, err = encryptor.DecryptConfidential(ctx, records[1].Content, 0, "", "")
	require.NoError(t, err)
	var blob map[string]any
	require.NoError(t, json.Unmarshal(plaintext, &blob))
	require.Equal(t, owner, blob["pubkey"])
	require.Equal(t, descriptor.URL, blob["url"])
	require.Equal(t, float64(descriptor.Size), blob["size"])
	require.NoError(t, publisher.PublishBlossomBlob(ctx, owner, descriptor))
	require.Len(t, sink.byKind(kinds.CASControlState), 2, "same descriptor must not republish")
	require.Error(t, publisher.PublishBlossomBlob(ctx, "invalid", descriptor))
	require.Error(t, publisher.PublishBlossomAdmin(ctx, []string{strings.Repeat("x", 32769)}, nil))
	require.Error(t, NewOperationalViewPublisher(projector, nil).PublishBlossomAdmin(ctx, nil, nil))
}
