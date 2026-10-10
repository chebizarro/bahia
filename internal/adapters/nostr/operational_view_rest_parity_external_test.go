package nostr_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type paritySource struct{ nostradapter.ProjectionSource }
type paritySink struct{ events []gonostr.Event }

func (s *paritySink) PublishProjection(_ context.Context, event gonostr.Event, _ string, _ *uuid.UUID) error {
	s.events = append(s.events, event)
	return nil
}

type parityEncryptor struct{}

func (parityEncryptor) EncryptConfidential(_ context.Context, _ string, content []byte, _ int, _, _ string, _ []byte) (string, error) {
	return "encrypted:" + base64.StdEncoding.EncodeToString(content), nil
}
func (parityEncryptor) DecryptConfidential(_ context.Context, content string, _ int, _, _ string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(content, "encrypted:"))
}
func (parityEncryptor) DecryptServiceInner(context.Context, string) ([]byte, error) { return nil, nil }
func (parityEncryptor) RotateKey(context.Context, string) error                     { return nil }
func (parityEncryptor) RotateKeyExcluding(context.Context, string, string) error    { return nil }
func (parityEncryptor) WrapKeyForMember(context.Context, string, string) error      { return nil }

func TestOperationalViewProjectionMatchesSource(t *testing.T) {
	cfg := &config.Config{}
	cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "metiq"}

	sink := &paritySink{}
	projector := nostradapter.NewProjector(config.NostrConfig{PublishEnabled: true}, &paritySource{}, sink, nil, zap.NewNop(), localServiceSigner(t))
	require.True(t, projector.Enabled())
	publisher := nostradapter.NewOperationalViewPublisher(projector, parityEncryptor{})
	require.NoError(t, publisher.PublishSoulRuntimePolicy(context.Background(), nostradapter.SoulRuntimePolicy{AgentRuntimes: cfg.SoulFactory.AgentRuntimes}))
	require.Len(t, sink.events, 1)
	require.Equal(t, gonostr.Kind(kinds.CASControlState), sink.events[0].Kind)
	var soulState struct {
		AgentRuntimes []string `json:"agent_runtimes"`
	}
	require.NoError(t, json.Unmarshal([]byte(sink.events[0].Content), &soulState))
	require.Equal(t, cfg.SoulFactory.AgentRuntimes, soulState.AgentRuntimes)

	owner, hash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var server *httptest.Server
	descriptor := blossom.BlobDescriptor{SHA256: hash, Size: 17, Type: "image/png", Uploaded: blossom.BlossomTimestamp{Time: time.Unix(100, 0).UTC()}}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/list/"+owner:
			copy := descriptor
			copy.URL = server.URL + "/" + hash
			require.NoError(t, json.NewEncoder(w).Encode([]blossom.BlobDescriptor{copy}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := blossom.NewClient(blossom.Config{Servers: []string{server.URL}, MaxRetries: 1}, slog.Default())
	servers := client.Servers()
	healthResults := client.HealthCheck(context.Background())
	health := make(map[string]string, len(healthResults))
	for server, err := range healthResults {
		if err != nil {
			health[server] = err.Error()
		} else {
			health[server] = "ok"
		}
	}
	require.NoError(t, publisher.PublishBlossomAdmin(context.Background(), servers, health))
	require.Len(t, sink.events, 2)
	plaintext, err := (parityEncryptor{}).DecryptConfidential(context.Background(), sink.events[1].Content, 0, "", "")
	require.NoError(t, err)
	var adminState struct {
		Servers []string          `json:"servers"`
		Health  map[string]string `json:"health"`
	}
	require.NoError(t, json.Unmarshal(plaintext, &adminState))
	require.Equal(t, servers, adminState.Servers)
	require.Equal(t, health, adminState.Health)

	blobs, err := client.ListByPubkey(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, blobs, 1)
	require.NoError(t, publisher.PublishBlossomBlob(context.Background(), owner, blobs[0]))
	require.Len(t, sink.events, 3)
	plaintext, err = (parityEncryptor{}).DecryptConfidential(context.Background(), sink.events[2].Content, 0, "", "")
	require.NoError(t, err)
	var blobState struct {
		Pubkey string `json:"pubkey"`
		blossom.BlobDescriptor
	}
	require.NoError(t, json.Unmarshal(plaintext, &blobState))
	require.Equal(t, owner, blobState.Pubkey)
	require.Equal(t, blobs[0], blobState.BlobDescriptor)
}

// localServiceSigner injects the local-mode service Keyer a deployed daemon
// builds from nostr.private_key.
func localServiceSigner(t *testing.T) nostradapter.ProjectorOption {
	t.Helper()
	signer, err := nostrutil.NewLocalKeyer(strings.Repeat("1", 64))
	require.NoError(t, err)
	pubkey, err := signer.GetPublicKey(context.Background())
	require.NoError(t, err)
	return nostradapter.WithProjectorSigner(signer, pubkey.Hex())
}
