package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestAssistantWrappedStartupHistoricalStoreReadsFailClosed(t *testing.T) {
	ctx := context.Background()
	wrapper := assistantWrapFixture(t)
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	legacy, err := assistantTranscriptKeyProviderForStartup(ctx, cfg, wrapper.pubkey.Hex(), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay := newMemoryRelay()
	signer := keyer.NewPlainKeySigner([32]byte(wrapper.secret))
	oldTranscript := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: service.AssistantIdentity{Pubkey: wrapper.pubkey.Hex()}, KeyProvider: legacy, ServicePubkey: wrapper.pubkey.Hex()})
	_, err = oldTranscript.AppendMessage(ctx, service.AssistantTranscriptAppend{LogicalID: "history-1", SessionID: "s-wrapped-boot", Sequence: 1, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "historical private transcript"}}}})
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpoints := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: legacy, ServicePubkey: wrapper.pubkey.Hex()})
	execution := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s-wrapped-boot", RunID: "run-wrapped-boot", TurnID: "turn", RequestID: "request", Workflow: domain.AssistantWorkflowBatch, Revision: 1, Phase: domain.AssistantExecutionExecuting, Work: []domain.AssistantWorkItem{{WorkID: "work", ToolName: "private-tool", Arguments: map[string]any{"secret": "historical private checkpoint"}, State: domain.AssistantWorkReady}}}
	if _, err := oldCheckpoints.Append(ctx, execution, ""); err != nil {
		t.Fatal(err)
	}
	manifest, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "assistant-keys.json")
	if err := persistAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, path, manifest); err != nil {
		t.Fatal(err)
	}
	wrapped, err := assistantWrappedProviderFromStoredManifest(ctx, wrapper, wrapper.pubkey, path, manifest.Active.Version)
	if err != nil {
		t.Fatal(err)
	}
	newTranscript := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: service.AssistantIdentity{Pubkey: wrapper.pubkey.Hex()}, KeyProvider: wrapped, ServicePubkey: wrapper.pubkey.Hex()})
	records, err := newTranscript.Replay(ctx, service.AssistantTranscriptReplayQuery{SessionID: "s-wrapped-boot"})
	if err != nil || len(records) != 1 || records[0].Payload.Message.Content[0].Text != "historical private transcript" {
		t.Fatalf("wrapped transcript replay = %+v, %v", records, err)
	}
	newCheckpoints := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: wrapped, ServicePubkey: wrapper.pubkey.Hex()})
	head, err := newCheckpoints.Load(ctx, execution.SessionID, execution.RunID)
	if err != nil || head.Execution.Work[0].Arguments["secret"] != "historical private checkpoint" {
		t.Fatalf("wrapped checkpoint replay = %+v, %v", head, err)
	}
	if _, err := newCheckpoints.Append(ctx, execution, ""); err == nil {
		t.Fatal("wrapped read-only checkpoint write accepted")
	}
	if _, err := newTranscript.AppendMessage(ctx, service.AssistantTranscriptAppend{SessionID: "s-wrapped-boot", Sequence: 2, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser}}); err == nil {
		t.Fatal("wrapped read-only transcript write accepted")
	}
	if _, err := assistantWrappedProviderFromStoredManifest(ctx, wrapper, wrapper.pubkey, path, "v2-wrong"); err == nil {
		t.Fatal("wrong generation accepted")
	}
	if _, err := assistantWrappedProviderFromStoredManifest(ctx, wrapper, wrapper.pubkey, filepath.Join(dir, "missing"), manifest.Active.Version); err == nil {
		t.Fatal("missing manifest accepted")
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := assistantWrappedProviderFromStoredManifest(ctx, wrapper, wrapper.pubkey, path, manifest.Active.Version); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
}

func TestAssistantWrappedStartupNeverFallsBackOnMissingManifest(t *testing.T) {
	wrapper := assistantWrapFixture(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	cfg.Assistant.WrappedKeys.Mode = "wrapped_read_only"
	cfg.Assistant.WrappedKeys.ManifestPath = filepath.Join(dir, "missing.json")
	cfg.Assistant.WrappedKeys.ExpectedGeneration = "v2-pinned"
	if _, err := assistantTranscriptKeyProviderForStartup(t.Context(), cfg, wrapper.pubkey.Hex(), nil); err == nil {
		t.Fatal("missing wrapped manifest silently selected the raw-key provider")
	}
}
