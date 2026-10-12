package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip19"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

// leakFixture is a leaked service identity whose status events carry its own
// private key as their d tag, as Bahia's did.
type leakFixture struct {
	secret  nostr.SecretKey
	author  nostr.PubKey
	dValue  string
	keyFile string
}

func newLeakFixture(t *testing.T) leakFixture {
	t.Helper()
	secret := nostr.Generate()
	keyFile := filepath.Join(t.TempDir(), "leaked.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(secret.Hex()+"\n"), 0o600))
	return leakFixture{secret: secret, author: secret.Public(), dValue: secret.Hex(), keyFile: keyFile}
}

// forbiddenStrings are the encodings of the leaked key no output may contain.
func (f leakFixture) forbiddenStrings() []string {
	return []string{f.dValue, nip19.EncodeNsec(f.secret)}
}

// eventSeq keeps fixture events distinct when kind and second repeat.
var eventSeq atomic.Int64

func (f leakFixture) statusEvent(t *testing.T, kind nostr.Kind, createdAt nostr.Timestamp) nostr.Event {
	t.Helper()
	content := fmt.Sprintf(`{"phase":"ready","seq":%d}`, eventSeq.Add(1))
	ev := nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: nostr.Tags{{"d", f.dValue}, {"t", "bahia"}}, Content: content}
	require.NoError(t, ev.Sign(f.secret))
	return ev
}

// history returns n leaked status events, one second apart, cycling kinds.
func (f leakFixture) history(t *testing.T, n int) []nostr.Event {
	t.Helper()
	newest := nostr.Now() - 600
	events := make([]nostr.Event, n)
	for i := range events {
		events[i] = f.statusEvent(t, leakedKinds[i%len(leakedKinds)], newest-nostr.Timestamp(i))
	}
	return events
}

func (f leakFixture) signerArgs() []string {
	return []string{"--signer", "local", "--key-file", f.keyFile}
}

// testRelay is an in-process khatru relay over a slice store. It applies
// NIP-09 kind-5 requests (khatru deletes e-tag targets of the same author)
// and records every EVENT frame it is sent.
type testRelay struct {
	url   string
	relay *khatru.Relay
	store *slicestore.SliceStore

	mu       sync.Mutex
	received []nostr.Event
	authed   []nostr.PubKey
	connects atomic.Int32
	// policy answers each EVENT; nil accepts.
	policy func(ctx context.Context, ev nostr.Event) (reject bool, reason string)
}

// newTestRelay serves at most maxLimit events per REQ without advertising a
// NIP-11 max_limit, like a relay that caps limit silently.
func newTestRelay(t *testing.T, maxLimit int, configure func(*testRelay)) *testRelay {
	t.Helper()
	r := &testRelay{relay: khatru.NewRelay(), store: &slicestore.SliceStore{}}
	require.NoError(t, r.store.Init())
	t.Cleanup(r.store.Close)
	r.relay.UseEventstore(r.store, maxLimit)
	r.relay.OnConnect = func(context.Context) { r.connects.Add(1) }
	r.relay.OnEvent = func(ctx context.Context, ev nostr.Event) (bool, string) {
		r.mu.Lock()
		r.received = append(r.received, ev)
		if pubkey, ok := khatru.GetAuthed(ctx); ok {
			r.authed = append(r.authed, pubkey)
		}
		policy := r.policy
		r.mu.Unlock()
		if policy != nil {
			return policy(ctx, ev)
		}
		return false, ""
	}
	if configure != nil {
		configure(r)
	}
	server := httptest.NewServer(r.relay)
	t.Cleanup(server.Close)
	r.url = nostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	return r
}

func (r *testRelay) seed(t *testing.T, events ...nostr.Event) {
	t.Helper()
	for _, ev := range events {
		require.NoError(t, r.store.SaveEvent(ev))
	}
}

func (r *testRelay) setPolicy(policy func(context.Context, nostr.Event) (bool, string)) {
	r.mu.Lock()
	r.policy = policy
	r.mu.Unlock()
}

// deletionsReceived returns the kind-5 frames the relay was sent.
func (r *testRelay) deletionsReceived() []nostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []nostr.Event
	for _, ev := range r.received {
		if ev.Kind == nostr.KindDeletion {
			out = append(out, ev)
		}
	}
	return out
}

func (r *testRelay) authedPubkeys() []nostr.PubKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nostr.PubKey(nil), r.authed...)
}

// stored returns the ids the relay still serves for the leaked identity.
func (r *testRelay) stored(t *testing.T, author nostr.PubKey) []string {
	t.Helper()
	var ids []string
	for ev := range r.store.QueryEvents(nostr.Filter{Kinds: leakedKinds, Authors: []nostr.PubKey{author}}, 10000) {
		ids = append(ids, ev.ID.Hex())
	}
	return ids
}

// requireAuthToRead makes REQs fail with "auth-required:" until the client
// authenticates, challenging on connect, as Bahia's sidecar does.
func requireAuthToRead(r *testRelay) {
	r.relay.OnConnect = func(ctx context.Context) {
		r.connects.Add(1)
		khatru.RequestAuth(ctx)
	}
	r.relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
		if _, ok := khatru.GetAuthed(ctx); !ok {
			return true, "auth-required: authenticated readers only"
		}
		return false, ""
	}
}

// testDeps gives each run its own admission controller with budgets no test
// reaches, and records retry waits instead of sleeping.
type recordedWaits struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (w *recordedWaits) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.waits)
}

func testDeps(waits *recordedWaits) deps {
	generous := nostrout.PurposeBudget{RatePerMinute: 600000, Burst: 10000}
	lanes := map[nostrout.Purpose]nostrout.PurposeBudget{}
	for _, purpose := range []nostrout.Purpose{nostrout.PurposePriority, nostrout.PurposeState, nostrout.PurposeGeneral, nostrout.PurposeBulk, nostrout.PurposeSigner} {
		lanes[purpose] = generous
	}
	return deps{
		admission: nostrout.New(nostrout.Config{
			Aggregate: generous, PurposeBudgets: lanes, RelayWire: generous, RelayWirePriority: generous,
			BreakerMin: time.Millisecond, BreakerMax: time.Millisecond,
		}),
		wait: func(ctx context.Context, d time.Duration) error {
			if waits != nil {
				waits.mu.Lock()
				waits.waits = append(waits.waits, d)
				waits.mu.Unlock()
			}
			return ctx.Err()
		},
		newBackoff: func() *nostradapter.Backoff {
			return &nostradapter.Backoff{Initial: time.Millisecond, Max: time.Millisecond}
		},
	}
}

type runResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, d deps, args ...string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, &stdout, &stderr, d)
	return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func scanArgs(f leakFixture, reportPath string, relays ...*testRelay) []string {
	args := []string{"scan", "--old-pubkey", f.author.Hex(), "--report", reportPath, "--page-timeout", "10s"}
	for _, relay := range relays {
		args = append(args, "--relay", relay.url)
	}
	return args
}

func readReport(t *testing.T, path string, author nostr.PubKey) *report {
	t.Helper()
	r, err := loadReport(path, author)
	require.NoError(t, err)
	return r
}

func scanOf(t *testing.T, r *report, relay *testRelay) relayScan {
	t.Helper()
	for _, scan := range r.Scans {
		if scan.Relay == relay.url {
			return scan
		}
	}
	t.Fatalf("no scan of %s", relay.url)
	return relayScan{}
}

func idsOf(events []nostr.Event) []string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID.Hex())
	}
	return ids
}

func scannedIDs(scan relayScan) []string {
	ids := make([]string, 0, len(scan.Events))
	for _, ev := range scan.Events {
		ids = append(ids, ev.ID)
	}
	return ids
}

// A relay that caps limit silently below the page size is paged with until,
// each page ending at EOSE, until every leaked event is found; other authors'
// events and other kinds are never collected.
func TestScanPagesEachRelayUntilEOSE(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 10)
	capped := newTestRelay(t, 3, nil)
	capped.seed(t, leaked...)
	other := newLeakFixture(t)
	capped.seed(t, other.history(t, 2)...)
	noise := nostr.Event{Kind: 1, CreatedAt: nostr.Now() - 30, Content: "unrelated note"}
	require.NoError(t, noise.Sign(f.secret))
	capped.seed(t, noise)
	roomy := newTestRelay(t, 500, nil)
	roomy.seed(t, leaked[:4]...)

	reportPath := filepath.Join(t.TempDir(), "report.json")
	res := runCLI(t, testDeps(nil), append(scanArgs(f, reportPath, capped, roomy), "--page-limit", "5")...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)

	r := readReport(t, reportPath, f.author)
	cappedScan := scanOf(t, r, capped)
	require.Equal(t, scanComplete, cappedScan.Status)
	require.False(t, cappedScan.Truncated)
	require.ElementsMatch(t, idsOf(leaked), scannedIDs(cappedScan))
	require.GreaterOrEqual(t, cappedScan.Pages, 4, "ten events at three per page take at least four pages")
	roomyScan := scanOf(t, r, roomy)
	require.Equal(t, scanComplete, roomyScan.Status)
	require.ElementsMatch(t, idsOf(leaked[:4]), scannedIDs(roomyScan))
	require.Len(t, r.DFingerprints, 1, "one d value across all events")
	for _, id := range idsOf(leaked) {
		require.Contains(t, res.stdout, id)
	}
}

// More events in one second than a page returns cannot be paged by NIP-01
// until; the scan steps past the second and says it may have missed some.
func TestScanReportsSameSecondOverflowAsTruncated(t *testing.T) {
	f := newLeakFixture(t)
	second := nostr.Now() - 100
	relay := newTestRelay(t, 2, nil)
	for i := range 4 {
		relay.seed(t, f.statusEvent(t, leakedKinds[i%len(leakedKinds)], second))
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	res := runCLI(t, testDeps(nil), scanArgs(f, reportPath, relay)...)
	require.Equal(t, exitIncomplete, res.code, res.stderr+res.stdout)
	scan := scanOf(t, readReport(t, reportPath, f.author), relay)
	require.True(t, scan.Truncated)
	require.Contains(t, res.stdout, "truncated")
}

// A relay that challenges with NIP-42 and refuses unauthenticated REQs is
// read after the pool answers AUTH as the leaked identity; without a signer
// its scan is incomplete with the relay's reason.
func TestScanAuthenticatesWhenChallenged(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 3)
	relay := newTestRelay(t, 500, requireAuthToRead)
	relay.seed(t, leaked...)
	dir := t.TempDir()

	unauthenticated := runCLI(t, testDeps(nil), scanArgs(f, filepath.Join(dir, "no-signer.json"), relay)...)
	require.Equal(t, exitIncomplete, unauthenticated.code, unauthenticated.stderr+unauthenticated.stdout)
	scan := scanOf(t, readReport(t, filepath.Join(dir, "no-signer.json"), f.author), relay)
	require.Equal(t, scanIncomplete, scan.Status)
	require.Contains(t, scan.Reason, "auth-required")

	var authed atomic.Bool
	relay.relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
		pubkey, ok := khatru.GetAuthed(ctx)
		if !ok {
			return true, "auth-required: authenticated readers only"
		}
		authed.Store(pubkey == f.author)
		return false, ""
	}
	reportPath := filepath.Join(dir, "report.json")
	res := runCLI(t, testDeps(nil), append(scanArgs(f, reportPath, relay), f.signerArgs()...)...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	require.True(t, authed.Load(), "the relay saw AUTH from the leaked identity")
	require.ElementsMatch(t, idsOf(leaked), scannedIDs(scanOf(t, readReport(t, reportPath, f.author), relay)))
}

// The d value (the leaked key) never reaches stdout, stderr, the report or
// the nostr library's logger: not from scanned events, not from an event a
// relay sends that does not match the REQ (which the library would log in
// full), and not from a relay's CLOSED reason that echoes it.
func TestScanNeverWritesDValues(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 5)
	var mismatchedSent atomic.Int32
	echoing := newTestRelay(t, 500, nil)
	echoing.seed(t, leaked...)
	stored := echoing.relay.QueryStored
	echoing.relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		return func(yield func(nostr.Event) bool) {
			mismatch := nostr.Event{Kind: 1, CreatedAt: nostr.Now() - 10, Tags: nostr.Tags{{"d", f.dValue}}, Content: "key " + f.dValue}
			if err := mismatch.Sign(f.secret); err != nil {
				return
			}
			mismatchedSent.Add(1)
			if !yield(mismatch) {
				return
			}
			for ev := range stored(ctx, filter) {
				if !yield(ev) {
					return
				}
			}
		}
	}
	closing := newTestRelay(t, 500, func(r *testRelay) {
		r.relay.OnRequest = func(context.Context, nostr.Filter) (bool, string) {
			return true, "blocked: refusing events with d=" + f.dValue
		}
	})

	var libraryLog bytes.Buffer
	nostr.InfoLogger.SetOutput(&libraryLog)
	nostr.DebugLogger.SetOutput(&libraryLog)
	t.Cleanup(silenceLibraryLogs)

	reportPath := filepath.Join(t.TempDir(), "report.json")
	res := runCLI(t, testDeps(nil), append(scanArgs(f, reportPath, echoing, closing), f.signerArgs()...)...)
	require.Equal(t, exitIncomplete, res.code, "the closing relay's scan is incomplete")
	require.Positive(t, mismatchedSent.Load(), "the relay sent the mismatching event")
	reportBytes, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	for _, secret := range f.forbiddenStrings() {
		require.NotContains(t, res.stdout, secret)
		require.NotContains(t, res.stderr, secret)
		require.NotContains(t, string(reportBytes), secret)
		require.NotContains(t, libraryLog.String(), secret)
	}
	r := readReport(t, reportPath, f.author)
	require.ElementsMatch(t, idsOf(leaked), scannedIDs(scanOf(t, r, echoing)))
	closedScan := scanOf(t, r, closing)
	require.Equal(t, scanIncomplete, closedScan.Status)
	require.Contains(t, closedScan.Reason, "blocked: refusing events with d=[redacted]")

	var raw map[string]any
	require.NoError(t, json.Unmarshal(reportBytes, &raw))
	require.NotContains(t, string(reportBytes), `"tags"`, "the report holds no scanned event bodies")
}

// The signer must report the leaked identity before any relay is contacted.
func TestSignerMustBeTheLeakedIdentity(t *testing.T) {
	f := newLeakFixture(t)
	relay := newTestRelay(t, 500, nil)
	relay.seed(t, f.history(t, 2)...)
	wrong := newLeakFixture(t)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	res := runCLI(t, testDeps(nil), append(scanArgs(f, reportPath, relay), wrong.signerArgs()...)...)
	require.Equal(t, exitFailure, res.code)
	require.Contains(t, res.stderr, "differs")
	require.Zero(t, relay.connects.Load(), "no relay was contacted")
	_, err := os.Stat(reportPath)
	require.ErrorIs(t, err, os.ErrNotExist)

	res = runCLI(t, testDeps(nil), "delete", "--old-pubkey", f.author.Hex(), "--report", reportPath, "--execute")
	require.Equal(t, exitFailure, res.code)
	require.Contains(t, res.stderr, "needs --signer")

	t.Setenv(keyEnvRefused, "anything")
	res = runCLI(t, testDeps(nil), append(scanArgs(f, reportPath, relay), f.signerArgs()...)...)
	require.Equal(t, exitFailure, res.code)
	require.Contains(t, res.stderr, keyEnvRefused)
	require.Zero(t, relay.connects.Load())
}
