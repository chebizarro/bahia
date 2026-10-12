package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

// testRelay is an in-process relay that serves at most 3 events per REQ
// page, so reads exercise paging, and can refuse EVENTs.
type testRelay struct {
	url    string
	store  *slicestore.SliceStore
	mu     sync.Mutex
	reject func(nostr.Event) bool
	sent   int
}

func newTestRelay(t *testing.T) *testRelay {
	t.Helper()
	r := &testRelay{store: &slicestore.SliceStore{}}
	require.NoError(t, r.store.Init())
	t.Cleanup(r.store.Close)
	relay := khatru.NewRelay()
	relay.UseEventstore(r.store, 3)
	relay.OnEvent = func(_ context.Context, ev nostr.Event) (bool, string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.sent++
		if r.reject != nil && r.reject(ev) {
			return true, "blocked: test policy"
		}
		return false, ""
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	r.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return r
}

func (r *testRelay) setReject(reject func(nostr.Event) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reject = reject
}

func (r *testRelay) authored(author nostr.PubKey) []nostr.Event {
	var out []nostr.Event
	for ev := range r.store.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{author}}, 1000) {
		out = append(out, ev)
	}
	return out
}

// Publish lets the relay's store stand in for the retired daemon's publisher.
func (r *testRelay) Publish(_ context.Context, ev nostr.Event) (int, error) {
	return 1, r.store.SaveEvent(ev)
}

type fixture struct {
	relay      *testRelay
	oldKeyFile string
	oldSecret  nostr.SecretKey
	newSecret  nostr.SecretKey
	deps       deps
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{relay: newTestRelay(t), oldSecret: nostr.Generate(), newSecret: nostr.Generate()}
	f.oldKeyFile = filepath.Join(t.TempDir(), "retired.key")
	require.NoError(t, os.WriteFile(f.oldKeyFile, []byte(f.oldSecret.Hex()+"\n"), 0o600))

	// The retired daemon's history, sealed exactly as it was: the key
	// derived from the configured key text.
	oldAuthor, oldKeys, err := retiredIdentity(f.oldKeyFile)
	require.NoError(t, err)
	oldSigner, err := nostrutil.NewLocalKeyer(f.oldSecret.Hex())
	require.NoError(t, err)
	checkpoints := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: f.relay, Signer: oldSigner, KeyProvider: oldKeys})
	execution := func(rev uint64) domain.AssistantExecution {
		return domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s1", RunID: "r1", Workflow: domain.AssistantWorkflowBatch, Revision: rev, Phase: domain.AssistantExecutionExecuting}
	}
	first, err := checkpoints.Append(ctx, execution(1), "")
	require.NoError(t, err)
	head, err := checkpoints.Append(ctx, execution(2), first)
	require.NoError(t, err)
	transcripts := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: f.relay, Signer: oldSigner, KeyProvider: oldKeys, ServicePubkey: oldAuthor.Hex()})
	_, err = transcripts.AppendMessage(ctx, service.AssistantTranscriptAppend{SessionID: "s1", Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hello"}}}})
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]any{"schema": domain.AssistantSessionSchemaV2, "session_id": "s1", "current_run_id": "r1", "checkpoint_event_id": head})
	projection := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: nostr.Now(), Content: string(body), Tags: nostr.Tags{
		{"d", domain.AssistantSessionSchemaV2 + ":s1"}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"t", kinds.AssistantSessionTopic}, {"session", "s1"}}}
	require.NoError(t, projection.Sign(f.oldSecret))
	require.NoError(t, f.relay.store.SaveEvent(projection))

	f.deps = defaultDeps()
	f.deps.loadConfig = func(string) (*config.Config, error) {
		cfg := config.Defaults()
		cfg.Nostr.ControlledRelays = []string{f.relay.url}
		cfg.SecretStore.SystemKeys.AssistantTranscript = []config.SystemKeyConfig{{KeyID: "k1", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}}
		return cfg, nil
	}
	f.deps.openSigner = func(context.Context, *config.Config, *nostrout.Admission) (nostr.Keyer, error) {
		return nostrutil.NewLocalKeyer(f.newSecret.Hex())
	}
	f.deps.resolveKeys = func(ctx context.Context, cfg *config.Config, _ string, _ *nostrout.Admission) (*secretstore.Keyring, func(), error) {
		ring, err := secretstore.NewKeyring([]secretstore.SystemKeyRef{{Purpose: secretstore.PurposeAssistantTranscript, KeyID: "k1", Credential: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}}})
		if err != nil {
			return nil, nil, err
		}
		if err := ring.Load(ctx, oneKeyStore{}); err != nil {
			return nil, nil, err
		}
		return ring, ring.Zeroize, nil
	}
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	admission := nostrout.New(nostrout.Config{Aggregate: generous, RelayWire: generous, RelayWirePriority: generous, BreakerMin: time.Millisecond, BreakerMax: time.Millisecond,
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{nostrout.PurposePriority: generous, nostrout.PurposeState: generous, nostrout.PurposeGeneral: generous, nostrout.PurposeBulk: generous, nostrout.PurposeSigner: generous}})
	f.deps.admission = func(*config.Config) (*nostrout.Admission, error) { return admission, nil }
	f.deps.wait = func(context.Context, time.Duration) error { return nil }
	return f
}

// oneKeyStore delivers the new assistant-transcript key.
type oneKeyStore struct{}

func (oneKeyStore) Resolve(context.Context, domain.SecretCredentialRef, secretstore.Scope) (*secretstore.Value, error) {
	return secretstore.NewValue(bytes.Repeat([]byte{7}, secretstore.SystemKeySize), time.Now().Add(time.Minute)), nil
}
func (oneKeyStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}
func (oneKeyStore) Health(context.Context) error { return nil }

func (f fixture) run(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--config", "/unused.yaml", "--old-key-file", f.oldKeyFile, "--relay", f.relay.url, "--read-timeout", "10s"}, extra...)
	code := run(context.Background(), args, &stdout, &stderr, f.deps)
	return code, stdout.String(), stderr.String()
}

func TestReencryptDryRunThenExecuteThenRerun(t *testing.T) {
	f := newFixture(t)
	newAuthor := f.newSecret.Public()

	code, out, errOut := f.run(t)
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "to publish: 2 checkpoints, 1 transcripts, 1 session projections")
	require.Contains(t, out, "dry run")
	require.Empty(t, f.relay.authored(newAuthor), "a dry run publishes nothing")

	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "published 4 of 4 events")
	reestablished := f.relay.authored(newAuthor)
	require.Len(t, reestablished, 4)
	for _, ev := range reestablished {
		require.True(t, ev.VerifySignature())
		require.NotContains(t, ev.Content, "hello")
		if ev.Kind != nostr.Kind(domain.KindAssistantSessionState) {
			require.Contains(t, ev.Content, "bahia-system/assistant-transcript/k1")
		}
	}

	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "already re-established: 4 items")
	require.Contains(t, out, "published 0 of 0 events")
	require.Len(t, f.relay.authored(newAuthor), 4)
	for _, forbidden := range []string{f.oldSecret.Hex(), f.newSecret.Hex()} {
		require.NotContains(t, out+errOut, forbidden)
	}
}

// A refused publish stops the run at that event (later checkpoints name it),
// and a re-run resumes on what was published without forking the chain.
func TestReencryptStopsAtARefusedEventAndResumes(t *testing.T) {
	f := newFixture(t)
	newAuthor := f.newSecret.Public()
	refused := 0
	f.relay.setReject(func(ev nostr.Event) bool {
		if ev.PubKey == newAuthor && ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) && hasTag(ev, "revision", "2") {
			refused++
			return true
		}
		return false
	})
	code, out, errOut := f.run(t, "--execute", "--max-attempts", "2")
	require.Equal(t, exitIncomplete, code)
	require.Contains(t, errOut, "accepted by no relay after 2 attempts")
	require.Contains(t, out, "published 1 of 4 events")
	require.Equal(t, 2, refused)

	f.relay.setReject(nil)
	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "already re-established: 1 items")
	require.Contains(t, out, "published 3 of 3 events")
	checkpoints := 0
	for _, ev := range f.relay.authored(newAuthor) {
		if ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) {
			checkpoints++
		}
	}
	require.Equal(t, 2, checkpoints, "the resumed chain reuses the published root")
}

func TestReencryptRefuses(t *testing.T) {
	f := newFixture(t)
	t.Run("same identity", func(t *testing.T) {
		g := f
		g.deps.openSigner = func(context.Context, *config.Config, *nostrout.Admission) (nostr.Keyer, error) {
			return nostrutil.NewLocalKeyer(f.oldSecret.Hex())
		}
		code, _, errOut := g.run(t, "--execute")
		require.Equal(t, exitFailure, code)
		require.Contains(t, errOut, "rotate the service key first")
	})
	t.Run("public relay", func(t *testing.T) {
		var stderr bytes.Buffer
		code := run(context.Background(), []string{"--config", "x", "--old-key-file", f.oldKeyFile, "--relay", "wss://relay.damus.io"}, &bytes.Buffer{}, &stderr, f.deps)
		require.Equal(t, exitFailure, code)
		require.Contains(t, stderr.String(), "public relay")
	})
	t.Run("uncontrolled relay", func(t *testing.T) {
		var stderr bytes.Buffer
		code := run(context.Background(), []string{"--config", "x", "--old-key-file", f.oldKeyFile, "--relay", "wss://relay.example.org"}, &bytes.Buffer{}, &stderr, f.deps)
		require.Equal(t, exitFailure, code)
		require.Contains(t, stderr.String(), "not in nostr.controlled_relays")
	})
	t.Run("loose key file", func(t *testing.T) {
		loose := filepath.Join(t.TempDir(), "loose.key")
		require.NoError(t, os.WriteFile(loose, []byte(f.oldSecret.Hex()), 0o644))
		g := f
		g.oldKeyFile = loose
		code, _, errOut := g.run(t)
		require.Equal(t, exitFailure, code)
		require.Contains(t, errOut, "owner only")
	})
	t.Run("no transcript key", func(t *testing.T) {
		g := f
		g.deps.loadConfig = func(string) (*config.Config, error) { return config.Defaults(), nil }
		code, _, errOut := g.run(t)
		require.Equal(t, exitFailure, code)
		require.Contains(t, errOut, "secret_store.system_keys.assistant_transcript is not configured")
	})
	t.Run("the wrong retired key", func(t *testing.T) {
		g := f
		other := filepath.Join(t.TempDir(), "other.key")
		require.NoError(t, os.WriteFile(other, []byte(nostr.Generate().Hex()), 0o600))
		g.oldKeyFile = other
		code, out, errOut := g.run(t)
		require.Equal(t, exitOK, code, errOut)
		require.Contains(t, out, "retired history: 0 events", "another identity has no history here")
	})
}

func hasTag(ev nostr.Event, name, value string) bool {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}
