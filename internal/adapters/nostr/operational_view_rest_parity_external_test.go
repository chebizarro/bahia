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
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
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
func (parityEncryptor) WrapKeyForMember(context.Context, string, string) error      { return nil }

func TestOperationalViewRESTPayloadParity(t *testing.T) {
	cfg := &config.Config{}
	cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "metiq"}
	soulResponse := httptest.NewRecorder()
	handlers.NewSoulFactoryHandler(cfg).GetRuntimes(soulResponse, httptest.NewRequest(http.MethodGet, "/soulfactory/runtimes", nil))
	require.Equal(t, http.StatusOK, soulResponse.Code)
	var soulEnvelope struct {
		Data struct {
			AgentRuntimes []string `json:"agent_runtimes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(soulResponse.Body.Bytes(), &soulEnvelope))

	sink := &paritySink{}
	projector := nostradapter.NewProjector(config.NostrConfig{PrivateKey: strings.Repeat("1", 64), PublishEnabled: true}, &paritySource{}, sink, nil, zap.NewNop())
	require.True(t, projector.Enabled())
	publisher := nostradapter.NewOperationalViewPublisher(projector, parityEncryptor{})
	require.NoError(t, publisher.PublishSoulRuntimePolicy(context.Background(), cfg.SoulFactory.AgentRuntimes))
	require.Len(t, sink.events, 1)
	require.Equal(t, gonostr.Kind(kinds.CASControlState), sink.events[0].Kind)
	var soulState struct {
		AgentRuntimes []string `json:"agent_runtimes"`
	}
	require.NoError(t, json.Unmarshal([]byte(sink.events[0].Content), &soulState))
	require.Equal(t, soulEnvelope.Data.AgentRuntimes, soulState.AgentRuntimes)

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
	handler := handlers.NewBlossomHandler(client)
	serversResponse := httptest.NewRecorder()
	handler.GetServers(serversResponse, httptest.NewRequest(http.MethodGet, "/blossom/servers", nil))
	require.Equal(t, http.StatusOK, serversResponse.Code)
	var serversEnvelope struct {
		Data []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(serversResponse.Body.Bytes(), &serversEnvelope))
	healthResponse := httptest.NewRecorder()
	handler.HealthCheck(healthResponse, httptest.NewRequest(http.MethodGet, "/blossom/health", nil))
	require.Equal(t, http.StatusOK, healthResponse.Code)
	var healthEnvelope struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(healthResponse.Body.Bytes(), &healthEnvelope))
	require.NoError(t, publisher.PublishBlossomAdmin(context.Background(), serversEnvelope.Data, healthEnvelope.Data))
	require.Len(t, sink.events, 2)
	plaintext, err := (parityEncryptor{}).DecryptConfidential(context.Background(), sink.events[1].Content, 0, "", "")
	require.NoError(t, err)
	var adminState struct {
		Servers []string          `json:"servers"`
		Health  map[string]string `json:"health"`
	}
	require.NoError(t, json.Unmarshal(plaintext, &adminState))
	require.Equal(t, serversEnvelope.Data, adminState.Servers)
	require.Equal(t, healthEnvelope.Data, adminState.Health)

	listResponse := httptest.NewRecorder()
	handler.ListBlobs(listResponse, httptest.NewRequest(http.MethodPost, "/blossom/list", strings.NewReader(`{"pubkey":"`+owner+`"}`)))
	require.Equal(t, http.StatusOK, listResponse.Code)
	var listEnvelope struct {
		Data []blossom.BlobDescriptor `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listEnvelope))
	require.Len(t, listEnvelope.Data, 1)
	require.NoError(t, publisher.PublishBlossomBlob(context.Background(), owner, listEnvelope.Data[0]))
	require.Len(t, sink.events, 3)
	plaintext, err = (parityEncryptor{}).DecryptConfidential(context.Background(), sink.events[2].Content, 0, "", "")
	require.NoError(t, err)
	var blobState struct {
		Pubkey string `json:"pubkey"`
		blossom.BlobDescriptor
	}
	require.NoError(t, json.Unmarshal(plaintext, &blobState))
	require.Equal(t, owner, blobState.Pubkey)
	require.Equal(t, listEnvelope.Data[0], blobState.BlobDescriptor)
}
