//go:build signetinterop

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// The disposable loopback runner supplies a synthetic adopted service nsec on
// stdin. It is never read from a committed file, test flag, argument or log.
func TestLiveAssistantWrappedStartupHistoricalReads(t *testing.T) {
	fixturePath := os.Getenv("BAHIA_SIGNET_INTEROP_CONFIG")
	if fixturePath == "" {
		t.Fatal("disposable Signet fixture is required")
	}
	info, err := os.Stat(fixturePath)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private disposable Signet fixture is required")
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal("read disposable fixture")
	}
	var fixture struct {
		Disposable            bool   `json:"disposable"`
		SignetCommit          string `json:"signet_commit"`
		BunkerURI             string `json:"bunker_uri"`
		OwnerSecretKeyHex     string `json:"owner_secret_key_hex"`
		ExpectedBunkerPubkey  string `json:"expected_bunker_pubkey"`
		ExpectedServicePubkey string `json:"expected_service_pubkey"`
		Epoch                 uint64 `json:"epoch"`
		ExpiresAt             string `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal("invalid disposable fixture")
	}
	if !fixture.Disposable || fixture.SignetCommit != "5b16b81578a53aeb68c25119ca36ef55d0aa504d" || fixture.Epoch < 2 || !assistantLiveLoopbackBunker(fixture.BunkerURI, fixture.ExpectedBunkerPubkey) {
		t.Fatal("unapproved Signet fixture")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, fixture.ExpiresAt)
	if err != nil || !time.Now().Add(2*time.Minute).Before(expiresAt) {
		t.Fatal("short or invalid disposable lease")
	}
	owner, err := nostr.SecretKeyFromHex(fixture.OwnerSecretKeyHex)
	if err != nil {
		t.Fatal("invalid synthetic owner key")
	}
	serviceSecretBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 129))
	if err != nil || len(strings.TrimSpace(string(serviceSecretBytes))) != 64 {
		t.Fatal("runner must supply synthetic adopted service secret on stdin")
	}
	serviceSecret, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(serviceSecretBytes)))
	if err != nil || serviceSecret.Public().Hex() != fixture.ExpectedServicePubkey || owner.Public() == serviceSecret.Public() {
		t.Fatal("runner service key does not match adopted Signet pubkey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: serviceSecret.Hex()}}
	legacy, err := assistantTranscriptKeyProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	relay := newMemoryRelay()
	historicalSigner := keyer.NewPlainKeySigner([32]byte(serviceSecret))
	oldTranscript := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: historicalSigner, Identity: service.AssistantIdentity{Pubkey: serviceSecret.Public().Hex()}, KeyProvider: legacy, ServicePubkey: serviceSecret.Public().Hex()})
	_, err = oldTranscript.AppendMessage(ctx, service.AssistantTranscriptAppend{LogicalID: "live-history", SessionID: "s-live", Sequence: 1, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "private historical transcript"}}}})
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpoint := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: historicalSigner, KeyProvider: legacy, ServicePubkey: serviceSecret.Public().Hex()})
	execution := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s-live", RunID: "run-live", TurnID: "turn", RequestID: "request", Workflow: domain.AssistantWorkflowBatch, Revision: 1, Phase: domain.AssistantExecutionExecuting, Work: []domain.AssistantWorkItem{{WorkID: "work", ToolName: "private-tool", Arguments: map[string]any{"secret": "private historical checkpoint"}, State: domain.AssistantWorkReady}}}
	if _, err := oldCheckpoint.Append(ctx, execution, ""); err != nil {
		t.Fatal(err)
	}
	lease := signet.WriterLease{Epoch: fixture.Epoch, OwnerPubkey: owner.Public(), ExpiresAt: expiresAt}
	leaseSource := func(context.Context) (signet.WriterLease, error) { return lease, nil }
	initial, err := signet.NewClient(signet.Config{BunkerURI: fixture.BunkerURI, ClientSecretKey: fixture.OwnerSecretKeyHex, RequireReal: true, EpochLease: leaseSource, ExpectedServicePubkey: fixture.ExpectedServicePubkey}, slog.Default())
	if err != nil {
		t.Fatal("cannot construct fenced Signet client")
	}
	defer initial.Close()
	if err := initial.Connect(ctx); err != nil {
		t.Fatal("disposable Signet did not connect")
	}
	epochSigner, err := signet.NewEpochSigner(initial, fixture.ExpectedServicePubkey, leaseSource)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := CreateAssistantWrappedKeyManifest(ctx, epochSigner, serviceSecret.Public(), cfg)
	if err != nil {
		t.Fatal("live Signet could not wrap assistant keys")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "assistant-keys.json")
	if err := persistAssistantWrappedKeyManifest(ctx, epochSigner, serviceSecret.Public(), path, manifest); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Assistant.WrappedKeys = config.AssistantWrappedKeysConfig{Mode: "wrapped_read_only", ManifestPath: path, ExpectedGeneration: manifest.Active.Version, SignetBunkerURI: fixture.BunkerURI, OwnerClientSecretKey: fixture.OwnerSecretKeyHex, LeaseEpoch: fixture.Epoch, LeaseExpiresAt: fixture.ExpiresAt, ConnectTimeout: time.Minute}
	var startupClient *signet.Client
	provider, err := assistantTranscriptKeyProviderForStartupWithClient(ctx, cfg, serviceSecret.Public().Hex(), nil, func(options signet.Config) (*signet.Client, error) {
		client, createErr := signet.NewClient(options, slog.Default())
		startupClient = client
		return client, createErr
	})
	if err != nil {
		t.Fatal("real assistant wrapped startup failed")
	}
	if startupClient == nil || !errors.Is(startupClient.Ping(ctx), signet.ErrNotConnected) {
		t.Fatal("startup Signet bootstrap connection remained open")
	}
	transcript := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: historicalSigner, Identity: service.AssistantIdentity{Pubkey: serviceSecret.Public().Hex()}, KeyProvider: provider, ServicePubkey: serviceSecret.Public().Hex()})
	records, err := transcript.Replay(ctx, service.AssistantTranscriptReplayQuery{SessionID: "s-live"})
	if err != nil || len(records) != 1 || records[0].Payload.Message.Content[0].Text != "private historical transcript" {
		t.Fatal("wrapped startup did not read historical transcript")
	}
	checkpoint := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: historicalSigner, KeyProvider: provider, ServicePubkey: serviceSecret.Public().Hex()})
	head, err := checkpoint.Load(ctx, execution.SessionID, execution.RunID)
	if err != nil || head.Execution.Work[0].Arguments["secret"] != "private historical checkpoint" {
		t.Fatal("wrapped startup did not read historical checkpoint")
	}
	if _, err := checkpoint.Append(ctx, execution, ""); err == nil {
		t.Fatal("wrapped startup permitted checkpoint write")
	}
	if _, err := transcript.AppendMessage(ctx, service.AssistantTranscriptAppend{SessionID: "s-live", Sequence: 2, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser}}); err == nil {
		t.Fatal("wrapped startup permitted transcript write")
	}
	cfg.Assistant.WrappedKeys.ExpectedGeneration = "v2-wrong"
	if _, err := assistantTranscriptKeyProviderForStartup(ctx, cfg, serviceSecret.Public().Hex(), nil); err == nil {
		t.Fatal("wrong generation fell back to raw key")
	}
	cfg.Assistant.WrappedKeys.ExpectedGeneration = manifest.Active.Version
	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	if _, err := assistantTranscriptKeyProviderForStartup(expired, cfg, serviceSecret.Public().Hex(), nil); err == nil {
		t.Fatal("expired startup context succeeded")
	}
}

func assistantLiveLoopbackBunker(rawURI, bunkerPubkey string) bool {
	uri, err := url.Parse(rawURI)
	if err != nil || uri.Scheme != "bunker" || !strings.EqualFold(uri.Hostname(), bunkerPubkey) || uri.User != nil || uri.Fragment != "" {
		return false
	}
	if _, err := nostr.PubKeyFromHex(bunkerPubkey); err != nil {
		return false
	}
	query, err := url.ParseQuery(uri.RawQuery)
	if err != nil || len(query["relay"]) != 1 || len(query["secret"]) > 1 {
		return false
	}
	for key := range query {
		if key != "relay" && key != "secret" {
			return false
		}
	}
	relay, err := url.Parse(query.Get("relay"))
	if err != nil || (relay.Scheme != "ws" && relay.Scheme != "wss") || relay.User != nil {
		return false
	}
	ip := net.ParseIP(relay.Hostname())
	return ip != nil && ip.IsLoopback() && relay.Port() != ""
}
