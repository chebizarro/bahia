package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/stretchr/testify/require"
)

func deleteArgs(f leakFixture, reportPath string, extra ...string) []string {
	return append([]string{"delete", "--old-pubkey", f.author.Hex(), "--report", reportPath}, extra...)
}

func executeArgs(f leakFixture, reportPath string, extra ...string) []string {
	return deleteArgs(f, reportPath, append(append([]string{"--execute"}, f.signerArgs()...), extra...)...)
}

// requireETagOnly asserts a published request is a valid kind-5 by the
// leaked identity carrying only e and k tags, and returns its e ids.
func requireETagOnly(t *testing.T, f leakFixture, ev nostr.Event) []string {
	t.Helper()
	require.Equal(t, nostr.KindDeletion, ev.Kind)
	require.Equal(t, f.author, ev.PubKey)
	require.True(t, ev.CheckID() && ev.VerifySignature())
	require.Empty(t, ev.Content)
	var ids []string
	for _, tag := range ev.Tags {
		require.Len(t, tag, 2)
		require.Contains(t, []string{"e", "k"}, tag[0], "a published deletion carries only e and k tags")
		if tag[0] == "e" {
			ids = append(ids, tag[1])
		}
	}
	for _, secret := range f.forbiddenStrings() {
		require.NotContains(t, ev.String(), secret)
	}
	return ids
}

func scanned(t *testing.T, f leakFixture, relays ...*testRelay) string {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	res := runCLI(t, testDeps(nil), scanArgs(f, reportPath, relays...)...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	return reportPath
}

// Without --execute nothing is signed, published or recorded; the plan lists
// each request's e and k tags.
func TestDeleteDryRunPrintsETagOnlyPlan(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 5)
	relay := newTestRelay(t, 500, nil)
	relay.seed(t, leaked...)
	reportPath := scanned(t, f, relay)
	before, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	res := runCLI(t, testDeps(nil), deleteArgs(f, reportPath, "--batch-size", "2")...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	for _, id := range idsOf(leaked) {
		require.Contains(t, res.stdout, `["e", "`+id+`"]`)
	}
	require.Contains(t, res.stdout, `["k", "31410"]`)
	require.NotContains(t, res.stdout, `["a"`)
	require.Contains(t, res.stdout, "new deletion 3 (unsigned)", "five events in batches of two")
	require.Contains(t, res.stdout, "dry run")
	after, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "a dry run does not change the report")
	require.Empty(t, relay.deletionsReceived())
	require.Len(t, relay.stored(t, f.author), 5)
}

// --execute publishes kind-5 requests naming every scanned event by id only,
// records each relay's OK, OK=false and its reason, and the relay that
// honours NIP-09 no longer serves the events.
func TestDeleteExecutePublishesETagOnlyAndRecordsEveryRelay(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 10)
	honouring := newTestRelay(t, 500, nil)
	honouring.seed(t, leaked...)
	refusing := newTestRelay(t, 500, nil)
	refusing.seed(t, leaked[:3]...)
	refusing.setPolicy(func(_ context.Context, ev nostr.Event) (bool, string) {
		if ev.Kind == nostr.KindDeletion {
			return true, "blocked: deletions are disabled here"
		}
		return false, ""
	})
	reportPath := scanned(t, f, honouring, refusing)

	res := runCLI(t, testDeps(nil), executeArgs(f, reportPath, "--batch-size", "4")...)
	require.Equal(t, exitIncomplete, res.code, "one relay refused: "+res.stderr+res.stdout)

	published := honouring.deletionsReceived()
	require.Len(t, published, 3, "ten events in batches of four")
	var named []string
	for _, ev := range published {
		named = append(named, requireETagOnly(t, f, ev)...)
	}
	require.ElementsMatch(t, idsOf(leaked), named, "every scanned id is named exactly once")
	require.Empty(t, honouring.stored(t, f.author), "the relay applied the e-tag deletions")
	require.Len(t, refusing.deletionsReceived(), 3, "every request reached the refusing relay too")
	require.Len(t, refusing.stored(t, f.author), 3)

	r := readReport(t, reportPath, f.author)
	require.Len(t, r.Deletions, 3)
	for _, del := range r.Deletions {
		require.Equal(t, outcomeAccepted, del.Relays[honouring.url].Status)
		refused := del.Relays[refusing.url]
		require.Equal(t, outcomeRejected, refused.Status)
		require.Equal(t, "blocked: deletions are disabled here", refused.Reason)
		require.Equal(t, 1, refused.Attempts, "a policy refusal is not retried in the same run")
	}
	require.Contains(t, res.stdout, "relay "+refusing.url+": 3 deletions: rejected=3")

	// A verification scan of the honouring relay finds nothing left.
	verify := runCLI(t, testDeps(nil), scanArgs(f, reportPath, honouring)...)
	require.Equal(t, exitOK, verify.code)
	require.Empty(t, scanOf(t, readReport(t, reportPath, f.author), honouring).Events)
}

// rate-limited: answers are retried after a backoff in the next round until
// the relay accepts; error: answers likewise.
func TestDeleteBacksOffOnRateLimitedOK(t *testing.T) {
	f := newLeakFixture(t)
	relay := newTestRelay(t, 500, nil)
	relay.seed(t, f.history(t, 3)...)
	var refusals atomic.Int32
	relay.setPolicy(func(_ context.Context, ev nostr.Event) (bool, string) {
		if ev.Kind != nostr.KindDeletion {
			return false, ""
		}
		switch refusals.Add(1) {
		case 1:
			return true, "rate-limited: slow down"
		case 2:
			return true, "error: database busy"
		}
		return false, ""
	})
	reportPath := scanned(t, f, relay)

	waits := &recordedWaits{}
	res := runCLI(t, testDeps(waits), executeArgs(f, reportPath)...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	require.Equal(t, 2, waits.count(), "one backoff before each retry round")
	r := readReport(t, reportPath, f.author)
	require.Len(t, r.Deletions, 1)
	outcome := r.Deletions[0].Relays[relay.url]
	require.Equal(t, outcomeAccepted, outcome.Status)
	require.Equal(t, 3, outcome.Attempts)
	require.Contains(t, res.stdout, "rate-limited (rate-limited: slow down)")
	require.Empty(t, relay.stored(t, f.author))
}

// A relay demanding NIP-42 for writes is answered with AUTH as the leaked
// identity and accepts; a relay that still answers auth-required after AUTH
// is recorded as auth-required.
func TestDeleteAuthenticatesAndRecordsAuthRequired(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 2)
	writeAuth := func(allowed nostr.PubKey) func(*testRelay) {
		return func(r *testRelay) {
			r.policy = func(ctx context.Context, ev nostr.Event) (bool, string) {
				if ev.Kind != nostr.KindDeletion {
					return false, ""
				}
				pubkey, ok := khatru.GetAuthed(ctx)
				if !ok {
					khatru.RequestAuth(ctx)
					return true, "auth-required: authenticate to delete"
				}
				if pubkey != allowed {
					return true, "auth-required: this key may not write here"
				}
				return false, ""
			}
		}
	}
	authing := newTestRelay(t, 500, writeAuth(f.author))
	authing.seed(t, leaked...)
	closedToUs := newTestRelay(t, 500, writeAuth(nostr.Generate().Public()))
	closedToUs.seed(t, leaked...)
	reportPath := scanned(t, f, authing, closedToUs)

	res := runCLI(t, testDeps(nil), executeArgs(f, reportPath)...)
	require.Equal(t, exitIncomplete, res.code, res.stderr+res.stdout)
	require.Contains(t, authing.authedPubkeys(), f.author)
	r := readReport(t, reportPath, f.author)
	require.Equal(t, outcomeAccepted, r.Deletions[0].Relays[authing.url].Status)
	require.Equal(t, outcomeAuthRequired, r.Deletions[0].Relays[closedToUs.url].Status)
	require.Contains(t, r.Deletions[0].Relays[closedToUs.url].Reason, "auth-required")
	require.Empty(t, authing.stored(t, f.author))
}

// An interrupted or partly refused run resumes from the report: the same
// signed requests are republished only to relays that have not accepted them,
// and nothing is re-planned.
func TestDeleteResumesFromReport(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 6)
	steady := newTestRelay(t, 500, nil)
	steady.seed(t, leaked...)
	var limiting atomic.Bool
	limiting.Store(true)
	flaky := newTestRelay(t, 500, nil)
	flaky.seed(t, leaked...)
	flaky.setPolicy(func(_ context.Context, ev nostr.Event) (bool, string) {
		if ev.Kind == nostr.KindDeletion && limiting.Load() {
			return true, "rate-limited: try tomorrow"
		}
		return false, ""
	})
	reportPath := scanned(t, f, steady, flaky)

	first := runCLI(t, testDeps(nil), executeArgs(f, reportPath, "--batch-size", "3", "--max-attempts", "1")...)
	require.Equal(t, exitIncomplete, first.code, first.stderr+first.stdout)
	afterFirst := readReport(t, reportPath, f.author)
	require.Len(t, afterFirst.Deletions, 2)
	var signedIDs []string
	for _, del := range afterFirst.Deletions {
		signedIDs = append(signedIDs, del.Event.ID.Hex())
		require.Equal(t, outcomeAccepted, del.Relays[steady.url].Status)
		require.Equal(t, outcomeRateLimited, del.Relays[flaky.url].Status)
	}
	require.Len(t, steady.deletionsReceived(), 2)

	limiting.Store(false)
	second := runCLI(t, testDeps(nil), executeArgs(f, reportPath, "--batch-size", "3")...)
	require.Equal(t, exitOK, second.code, second.stderr+second.stdout)
	require.Len(t, steady.deletionsReceived(), 2, "an accepting relay is not sent the requests again")
	var republished []string
	for _, ev := range flaky.deletionsReceived() {
		republished = append(republished, ev.ID.Hex())
	}
	require.Len(t, republished, 4, "two refused, then two accepted")
	for _, id := range republished {
		require.Contains(t, signedIDs, id, "the resumed run republishes the signed requests from the report")
	}
	afterSecond := readReport(t, reportPath, f.author)
	require.Len(t, afterSecond.Deletions, 2, "nothing was re-planned")
	for _, del := range afterSecond.Deletions {
		require.Equal(t, outcomeAccepted, del.Relays[flaky.url].Status)
		require.Equal(t, 2, del.Relays[flaky.url].Attempts)
	}
	require.Empty(t, flaky.stored(t, f.author))

	again := runCLI(t, testDeps(nil), executeArgs(f, reportPath)...)
	require.Equal(t, exitOK, again.code)
	require.Len(t, flaky.deletionsReceived(), 4, "a finished report publishes nothing")
}

// A report edited to carry an a-tag deletion, even one validly signed by the
// leaked key, is refused before anything is published, in a dry run too.
func TestDeleteRefusesATagInReport(t *testing.T) {
	f := newLeakFixture(t)
	leaked := f.history(t, 2)
	relay := newTestRelay(t, 500, nil)
	relay.seed(t, leaked...)
	var limited atomic.Bool
	limited.Store(true)
	relay.setPolicy(func(_ context.Context, ev nostr.Event) (bool, string) {
		if ev.Kind == nostr.KindDeletion && limited.Load() {
			return true, "blocked: not today"
		}
		return false, ""
	})
	reportPath := scanned(t, f, relay)
	res := runCLI(t, testDeps(nil), executeArgs(f, reportPath)...)
	require.Equal(t, exitIncomplete, res.code)
	require.Len(t, relay.deletionsReceived(), 1)

	var raw report
	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &raw))
	tampered := raw.Deletions[0].Event
	tampered.Tags = append(slices.Clone(tampered.Tags), nostr.Tag{"a", "31410:" + f.author.Hex() + ":" + f.dValue})
	require.NoError(t, tampered.Sign(f.secret))
	raw.Deletions[0].Event = tampered
	data, err = json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(reportPath, data, 0o600))
	limited.Store(false)

	for _, args := range [][]string{deleteArgs(f, reportPath), executeArgs(f, reportPath)} {
		res = runCLI(t, testDeps(nil), args...)
		require.Equal(t, exitFailure, res.code)
		require.Contains(t, res.stderr, "refusing deletion with an a tag")
		for _, secret := range f.forbiddenStrings() {
			require.NotContains(t, res.stdout+res.stderr, secret)
		}
	}
	require.Len(t, relay.deletionsReceived(), 1, "nothing more was published")
	require.Len(t, relay.stored(t, f.author), 2)
}
