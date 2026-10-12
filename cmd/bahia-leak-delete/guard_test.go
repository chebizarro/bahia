package main

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// guardFixture is a leaked identity whose status events carried its own key
// as the d tag, as Bahia's did.
type guardFixture struct {
	secret  nostr.SecretKey
	author  nostr.PubKey
	dValue  string
	targets []scannedEvent
	guard   deletionGuard
}

func newGuardFixture(t *testing.T) guardFixture {
	t.Helper()
	secret := nostr.Generate()
	f := guardFixture{secret: secret, author: secret.Public(), dValue: secret.Hex()}
	r := newReport(f.author)
	for i, kind := range leakedKinds {
		ev := nostr.Event{Kind: kind, CreatedAt: nostr.Timestamp(1_700_000_000 + i), Tags: nostr.Tags{{"d", f.dValue}}}
		require.NoError(t, ev.Sign(secret))
		f.targets = append(f.targets, scannedEvent{ID: ev.ID.Hex(), Kind: int(kind), CreatedAt: int64(ev.CreatedAt)})
	}
	dValues := fingerprintSet{}
	dValues.add(f.dValue)
	require.NoError(t, r.mergeFingerprints(dValues))
	r.putScan(relayScan{Relay: "wss://relay.example", Status: scanComplete, Events: f.targets})
	guard, err := r.guard(f.author)
	require.NoError(t, err)
	f.guard = guard
	return f
}

// The requests the tool builds carry only e tags (one per scanned id) and k
// tags (one per kind), no content, and pass the guard signed and unsigned.
func TestDeletionRequestsCarryOnlyETagsAndKTags(t *testing.T) {
	f := newGuardFixture(t)
	r := newReport(f.author)
	r.putScan(relayScan{Relay: "wss://relay.example", Status: scanComplete, Events: f.targets})
	planned := planDeletions(r, f.author, 2, nostr.Now())
	require.Len(t, planned, 2, "three events in batches of two")

	named := map[string]bool{}
	for _, del := range planned {
		require.NoError(t, f.guard.check(del.Event))
		for _, tag := range del.Event.Tags {
			require.Len(t, tag, 2)
			require.Contains(t, []string{"e", "k"}, tag[0], "only e and k tags")
			require.NotEqual(t, f.dValue, tag[1])
			if tag[0] == "e" {
				named[tag[1]] = true
			}
		}
		require.Empty(t, del.Event.Content)
		require.Equal(t, nostr.KindDeletion, del.Event.Kind)

		signed := del.Event
		require.NoError(t, signed.Sign(f.secret))
		require.NoError(t, f.guard.checkSigned(signed))
		require.NotContains(t, signed.String(), f.dValue, "the signed request never contains the d value")
	}
	for _, target := range f.targets {
		require.True(t, named[target.ID], "every scanned id is named by an e tag")
	}
}

// An a tag is refused in any position, spelling or padding, even when every
// other tag is valid; so is any tag value equal to a scanned d value.
func TestGuardRefusesATagsAndScannedDValues(t *testing.T) {
	f := newGuardFixture(t)
	valid := deletionRequest(f.author, f.targets[:1], nostr.Now())
	require.NoError(t, f.guard.check(valid))
	coordinate := f.targets[0].ID // any value: the a tag itself is refused
	withTag := func(tag nostr.Tag, first bool) nostr.Event {
		ev := valid
		ev.Tags = append(nostr.Tags(nil), valid.Tags...)
		if first {
			ev.Tags = append(nostr.Tags{tag}, ev.Tags...)
		} else {
			ev.Tags = append(ev.Tags, tag)
		}
		return ev
	}
	for name, ev := range map[string]nostr.Event{
		"a tag with the coordinate": withTag(nostr.Tag{"a", "31410:" + f.author.Hex() + ":" + f.dValue}, false),
		"a tag first":               withTag(nostr.Tag{"a", coordinate}, true),
		"upper-case A":              withTag(nostr.Tag{"A", coordinate}, false),
		"padded a":                  withTag(nostr.Tag{" a ", coordinate}, false),
		"a tag with a relay hint":   withTag(nostr.Tag{"a", coordinate, "wss://relay.example"}, false),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, f.guard.check(ev), errATag)
			signed := ev
			require.NoError(t, signed.Sign(f.secret))
			require.ErrorIs(t, f.guard.checkSigned(signed), errATag)
		})
	}
	for name, ev := range map[string]nostr.Event{
		"e tag with the d value":  withTag(nostr.Tag{"e", f.dValue}, false),
		"k tag with the d value":  withTag(nostr.Tag{"k", f.dValue}, false),
		"tag name is the d value": withTag(nostr.Tag{f.dValue, "x"}, false),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, f.guard.check(ev), errDValue)
		})
	}
}

// Everything outside the allowlist is refused.
func TestGuardRefusesAnythingButTheAllowlist(t *testing.T) {
	f := newGuardFixture(t)
	valid := deletionRequest(f.author, f.targets, nostr.Now())
	require.NoError(t, f.guard.check(valid))
	mutate := func(change func(*nostr.Event)) nostr.Event {
		ev := valid
		ev.Tags = append(nostr.Tags(nil), valid.Tags...)
		change(&ev)
		return ev
	}
	unscanned := nostr.Event{Kind: leakedKinds[0], CreatedAt: 1}
	require.NoError(t, unscanned.Sign(f.secret))
	cases := map[string]struct {
		ev   nostr.Event
		want string
	}{
		"kind 1":          {mutate(func(ev *nostr.Event) { ev.Kind = 1 }), "only kind 5"},
		"other author":    {mutate(func(ev *nostr.Event) { ev.PubKey = nostr.Generate().Public() }), "only the leaked identity"},
		"content":         {mutate(func(ev *nostr.Event) { ev.Content = "leaked key rotation" }), "with content"},
		"p tag":           {mutate(func(ev *nostr.Event) { ev.Tags = append(ev.Tags, nostr.Tag{"p", f.author.Hex()}) }), `"p" tag`},
		"t tag":           {mutate(func(ev *nostr.Event) { ev.Tags = append(ev.Tags, nostr.Tag{"t", "bahia"}) }), `"t" tag`},
		"d tag":           {mutate(func(ev *nostr.Event) { ev.Tags = append(ev.Tags, nostr.Tag{"d", "x"}) }), `"d" tag`},
		"unscanned id":    {mutate(func(ev *nostr.Event) { ev.Tags[0] = nostr.Tag{"e", unscanned.ID.Hex()} }), "no scan found"},
		"upper-case id":   {mutate(func(ev *nostr.Event) { ev.Tags[0] = nostr.Tag{"e", strings.ToUpper(ev.Tags[0][1])} }), "lowercase 64-hex"},
		"repeated id":     {mutate(func(ev *nostr.Event) { ev.Tags = append(nostr.Tags{ev.Tags[0]}, ev.Tags...) }), "repeats"},
		"three-element e": {mutate(func(ev *nostr.Event) { ev.Tags[0] = append(ev.Tags[0], "wss://hint") }), "3-element tag"},
		"other kind in k": {mutate(func(ev *nostr.Event) { ev.Tags = append(ev.Tags, nostr.Tag{"k", "1"}) }), "not a leaked status kind"},
		"missing k":       {mutate(func(ev *nostr.Event) { ev.Tags = ev.Tags[:len(ev.Tags)-1] }), "k tags must list exactly"},
		"no e tags":       {mutate(func(ev *nostr.Event) { ev.Tags = nostr.Tags{{"k", "31410"}} }), "without e tags"},
		"nsec in a value": {mutate(func(ev *nostr.Event) { ev.Tags = append(ev.Tags, nostr.Tag{"k", "nsec1abc"}) }), "nsec"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, f.guard.check(c.ev), c.want)
		})
	}

	signed := valid
	require.NoError(t, signed.Sign(f.secret))
	require.NoError(t, f.guard.checkSigned(signed))
	forged := signed
	forged.Sig[0] ^= 1
	require.ErrorContains(t, f.guard.checkSigned(forged), "does not verify")
	edited := signed
	edited.Tags = edited.Tags[1:]
	require.ErrorContains(t, f.guard.checkSigned(edited), "k tags must list exactly")
}

func TestRedactRemovesScannedDValuesAndNsecs(t *testing.T) {
	set := fingerprintSet{}
	secret := nostr.Generate().Hex()
	set.add(secret)
	got := set.redact(`blocked: event ["d","` + secret + `"] from nsec1qqqqq is not allowed; id 3f00`)
	require.NotContains(t, got, secret)
	require.NotContains(t, got, "nsec1")
	require.Equal(t, `blocked: event ["d","[redacted]"] from [redacted] is not allowed; id 3f00`, got)
	require.Equal(t, "rate-limited: slow down", set.redact("rate-limited: slow down"))
}
