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
	"github.com/openagentsinc/bahia/test/testkit/systemkeys"
	"github.com/stretchr/testify/require"
)

// testRelay is an in-process relay that answers at most 3 events per REQ and
// advertises no limit, like a relay that caps silently, and can refuse EVENTs.
type testRelay struct {
	url    string
	store  *slicestore.SliceStore
	mu     sync.Mutex
	reject func(nostr.Event) bool
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

// clock hands out increasing times, one second apart, from now-1h.
type clock struct{ next time.Time }

func (c *clock) now() time.Time {
	c.next = c.next.Add(time.Second)
	return c.next
}

type fixture struct {
	a, b       *testRelay
	oldKeyFile string
	oldSecret  nostr.SecretKey
	newSecret  nostr.SecretKey
	quorum     int
	deps       deps
}

// newFixture seeds relay a with a retired daemon's history (two checkpoints,
// two transcripts and the session's v2 projection), sealed exactly as it was
// with the key derived from the configured key text; relay b holds nothing.
func newFixture(t *testing.T, transcriptsAtOneSecond int) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{a: newTestRelay(t), b: newTestRelay(t), oldSecret: nostr.Generate(), newSecret: nostr.Generate(), quorum: 2}
	f.oldKeyFile = filepath.Join(t.TempDir(), "retired.key")
	require.NoError(t, os.WriteFile(f.oldKeyFile, []byte(f.oldSecret.Hex()+"\n"), 0o600))

	oldAuthor, oldKeys, err := retiredIdentity(f.oldKeyFile)
	require.NoError(t, err)
	oldSigner, err := nostrutil.NewLocalKeyer(f.oldSecret.Hex())
	require.NoError(t, err)
	tick := &clock{next: time.Now().Add(-time.Hour)}
	checkpoints := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: f.a, Signer: oldSigner, KeyProvider: oldKeys, Now: tick.now})
	execution := func(rev uint64) domain.AssistantExecution {
		return domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s1", RunID: "r1", Workflow: domain.AssistantWorkflowBatch, Revision: rev, Phase: domain.AssistantExecutionExecuting}
	}
	first, err := checkpoints.Append(ctx, execution(1), "")
	require.NoError(t, err)
	head, err := checkpoints.Append(ctx, execution(2), first)
	require.NoError(t, err)
	transcriptClock := tick.now
	if transcriptsAtOneSecond > 0 {
		at := tick.now()
		transcriptClock = func() time.Time { return at }
	}
	transcripts := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: f.a, Signer: oldSigner, KeyProvider: oldKeys, ServicePubkey: oldAuthor.Hex(), Now: transcriptClock})
	for i := range max(2, transcriptsAtOneSecond) {
		_, err = transcripts.AppendMessage(ctx, service.AssistantTranscriptAppend{SessionID: "s1", Sequence: i, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hello"}}}})
		require.NoError(t, err)
	}
	body, _ := json.Marshal(map[string]any{"schema": domain.AssistantSessionSchemaV2, "session_id": "s1", "current_run_id": "r1", "checkpoint_event_id": head})
	projection := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: nostr.Timestamp(tick.now().Unix()), Content: string(body), Tags: nostr.Tags{
		{"d", domain.AssistantSessionSchemaV2 + ":s1"}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"t", kinds.AssistantSessionTopic}, {"session", "s1"}}}
	require.NoError(t, projection.Sign(f.oldSecret))
	require.NoError(t, f.a.store.SaveEvent(projection))

	f.deps = defaultDeps()
	f.deps.loadConfig = func(string) (*config.Config, error) {
		cfg := config.Defaults()
		relays := []string{f.a.url, f.b.url}
		cfg.Nostr.ContextVMRelays, cfg.Nostr.ServiceRelays, cfg.Nostr.ControlledRelays = relays, relays, relays
		cfg.Nostr.PublishQuorum = f.quorum
		cfg.SecretStore.SystemKeys.AssistantTranscript = []config.SystemKeyConfig{{KeyID: systemkeys.KeyID, CredentialID: "cred_" + strings.Repeat("d", 64), Version: 1}}
		return cfg, nil
	}
	f.deps.openSigner = func(context.Context, *config.Config, *nostrout.Admission) (nostr.Keyer, error) {
		return nostrutil.NewLocalKeyer(f.newSecret.Hex())
	}
	f.deps.resolveKeys = func(context.Context, *config.Config, string, *nostrout.Admission) (*secretstore.Keyring, func(), error) {
		ring := systemkeys.Keyring(secretstore.PurposeAssistantTranscript, 7)
		return ring, ring.Zeroize, nil
	}
	// Each run is its own process with its own admission controller (which
	// caches publish results by event id).
	f.deps.admission = func(*config.Config) (*nostrout.Admission, error) {
		generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
		return nostrout.New(nostrout.Config{Aggregate: generous, RelayWire: generous, RelayWirePriority: generous, BreakerMin: time.Millisecond, BreakerMax: time.Millisecond,
			PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{nostrout.PurposePriority: generous, nostrout.PurposeState: generous, nostrout.PurposeGeneral: generous, nostrout.PurposeBulk: generous, nostrout.PurposeSigner: generous}}), nil
	}
	f.deps.wait = func(context.Context, time.Duration) error { return nil }
	return f
}

func (f fixture) run(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--config", "/unused.yaml", "--old-key-file", f.oldKeyFile, "--read-timeout", "10s"}, extra...)
	code := run(context.Background(), args, &stdout, &stderr, f.deps)
	return code, stdout.String(), stderr.String()
}

func TestReencryptDryRunThenExecuteThenRerun(t *testing.T) {
	f := newFixture(t, 0)
	newAuthor := f.newSecret.Public()

	code, out, errOut := f.run(t)
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "retired history: 5 events", "the history is read past the relay's silent cap of 3 per REQ")
	require.Contains(t, out, "to publish: 2 checkpoints, 2 transcripts, 1 session projections")
	require.Contains(t, out, "dry run")
	require.Empty(t, f.a.authored(newAuthor), "a dry run publishes nothing")

	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "published 5 of 5 events to quorum")
	for _, relay := range []*testRelay{f.a, f.b} {
		reestablished := relay.authored(newAuthor)
		require.Len(t, reestablished, 5, "quorum 2 of 2: every relay holds every event")
		for _, ev := range reestablished {
			require.True(t, ev.VerifySignature())
			require.NotContains(t, ev.Content, "hello")
			if ev.Kind != nostr.Kind(domain.KindAssistantSessionState) {
				require.Contains(t, ev.Content, "bahia-system/assistant-transcript/test")
			}
		}
	}

	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "already re-established: 5 items")
	require.Contains(t, out, "below quorum 2: 0")
	require.Contains(t, out, "published 0 of 0 events")
	for _, forbidden := range []string{f.oldSecret.Hex(), f.newSecret.Hex()} {
		require.NotContains(t, out+errOut, forbidden)
	}
}

// An event one relay lost is below quorum; a re-run republishes it there.
func TestReencryptTopsUpEventsBelowQuorum(t *testing.T) {
	f := newFixture(t, 0)
	newAuthor := f.newSecret.Public()
	code, _, errOut := f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	lost := f.b.authored(newAuthor)[0]
	require.NoError(t, f.b.store.DeleteEvent(lost.ID))

	code, out, errOut := f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "below quorum 2: 1")
	require.Contains(t, out, "published 1 of 1 events to quorum")
	require.Len(t, f.b.authored(newAuthor), 5)
}

// A copy on one relay is not enough: an event refused by the second relay
// stops the run (a later checkpoint names it), and a re-run resumes on what
// quorum relays hold without forking the chain.
func TestReencryptStopsBelowQuorumAndResumes(t *testing.T) {
	f := newFixture(t, 0)
	newAuthor := f.newSecret.Public()
	f.b.setReject(func(ev nostr.Event) bool {
		return ev.PubKey == newAuthor && ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) && tagValue(ev, "revision") == "2"
	})
	code, out, errOut := f.run(t, "--execute", "--max-attempts", "2")
	require.Equal(t, exitIncomplete, code)
	require.Contains(t, errOut, "is held by 1 relays, quorum 2, after 2 attempts")
	require.Contains(t, out, "published 1 of 5 events to quorum")

	f.b.setReject(nil)
	code, out, errOut = f.run(t, "--execute")
	require.Equal(t, exitOK, code, errOut)
	require.Contains(t, out, "already re-established: 2 items", "the root and the revision relay a holds")
	require.Contains(t, out, "below quorum 2: 1")
	checkpoints := 0
	for _, ev := range f.b.authored(newAuthor) {
		if ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) {
			checkpoints++
		}
	}
	require.Equal(t, 2, checkpoints, "the resumed chain reuses what was published")
}

// More events in one second than the relay returns at once cannot be paged
// completely: the read is incomplete, never silently short.
func TestReencryptRefusesAnIncompleteRead(t *testing.T) {
	f := newFixture(t, 4)
	code, _, errOut := f.run(t)
	require.Equal(t, exitIncomplete, code)
	require.Contains(t, errOut, "cannot be paged completely")
}

func TestReencryptRefuses(t *testing.T) {
	f := newFixture(t, 0)
	t.Run("same identity", func(t *testing.T) {
		g := f
		g.deps.openSigner = func(context.Context, *config.Config, *nostrout.Admission) (nostr.Keyer, error) {
			return nostrutil.NewLocalKeyer(f.oldSecret.Hex())
		}
		code, _, errOut := g.run(t, "--execute")
		require.Equal(t, exitFailure, code)
		require.Contains(t, errOut, "rotate the service key first")
	})
	withConfig := func(mutate func(*config.Config)) fixture {
		g := f
		load := f.deps.loadConfig
		g.deps.loadConfig = func(path string) (*config.Config, error) {
			cfg, err := load(path)
			mutate(cfg)
			return cfg, err
		}
		return g
	}
	for name, tc := range map[string]struct {
		mutate func(*config.Config)
		want   string
	}{
		"public relay":       {func(c *config.Config) { c.Nostr.ServiceRelays = []string{"wss://relay.damus.io"} }, "public relay"},
		"uncontrolled relay": {func(c *config.Config) { c.Nostr.ServiceRelays = []string{"wss://relay.example.org"} }, "not in nostr.controlled_relays"},
		"quorum over relays": {func(c *config.Config) { c.Nostr.PublishQuorum = 3 }, "fewer than nostr.publish_quorum 3"},
		"no transcript key":  {func(c *config.Config) { c.SecretStore.SystemKeys.AssistantTranscript = nil }, "secret_store.system_keys.assistant_transcript is not configured"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errOut := withConfig(tc.mutate).run(t)
			require.Equal(t, exitFailure, code)
			require.Contains(t, errOut, tc.want)
		})
	}
	keyFile := func(t *testing.T, mode os.FileMode, content string) string {
		path := filepath.Join(t.TempDir(), "k")
		require.NoError(t, os.WriteFile(path, []byte(content), mode))
		return path
	}
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(f.oldKeyFile, link))
	for name, path := range map[string]string{
		"loose key file": keyFile(t, 0o644, f.oldSecret.Hex()),
		"symlink":        link,
		"not hex":        keyFile(t, 0o600, "nsec1notsupported"),
	} {
		t.Run(name, func(t *testing.T) {
			g := f
			g.oldKeyFile = path
			code, _, errOut := g.run(t)
			require.Equal(t, exitFailure, code)
			require.Contains(t, errOut, "--old-key-file")
		})
	}
	t.Run("the wrong retired key", func(t *testing.T) {
		g := f
		g.oldKeyFile = keyFile(t, 0o600, nostr.Generate().Hex())
		code, out, errOut := g.run(t)
		require.Equal(t, exitOK, code, errOut)
		require.Contains(t, out, "retired history: 0 events", "another identity has no history here")
	})
}
