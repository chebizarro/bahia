package controlplane

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func testDelegationStore(t *testing.T) *localstore.Store {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "delegations.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func signedDelegationList(t *testing.T, author nostr.SecretKey, agent nostr.PubKey, created nostr.Timestamp, humans ...nostr.PubKey) nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", delegationDTag(agent.Hex())}}
	for _, human := range humans {
		tags = append(tags, nostr.Tag{"p", human.Hex()})
	}
	ev := nostr.Event{Kind: nostr.Kind(kinds.ConfigACLList), CreatedAt: created, Tags: tags, Content: ""}
	require.NoError(t, ev.Sign(author))
	return ev
}

func TestDelegationListsRequireSignedCurrentGrantAndCatchup(t *testing.T) {
	store := testDelegationStore(t)
	author, otherAuthor, agent, otherAgent, human, otherHuman := nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate()
	lists := NewDelegationLists(nil, store, []string{author.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	grant := signedDelegationList(t, author, agent.Public(), 100, human.Public())
	require.NoError(t, lists.Apply(&grant))
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()), "cached grant is not authority before relay EOSE")
	lists.mu.Lock()
	lists.caughtUp = true
	lists.mu.Unlock()
	require.True(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
	require.False(t, lists.Allows(agent.Public().Hex(), otherHuman.Public().Hex()))
	require.False(t, lists.Allows(otherAgent.Public().Hex(), human.Public().Hex()))

	untrusted := signedDelegationList(t, otherAuthor, agent.Public(), 200, otherHuman.Public())
	require.ErrorContains(t, lists.Apply(&untrusted), "not trusted")
	invalidSig := signedDelegationList(t, author, agent.Public(), 200, otherHuman.Public())
	invalidSig.Sig[0] ^= 1
	require.ErrorContains(t, lists.Apply(&invalidSig), "signature")
	wrongCoordinate := signedDelegationList(t, author, otherAgent.Public(), 200, otherHuman.Public())
	require.ErrorContains(t, lists.Apply(&wrongCoordinate), "coordinate")
	badP := signedDelegationList(t, author, agent.Public(), 200)
	badP.Tags = append(badP.Tags, nostr.Tag{"p", "not-a-pubkey"})
	require.NoError(t, badP.Sign(author))
	require.ErrorContains(t, lists.Apply(&badP), "p tag")
	require.False(t, lists.Allows(agent.Public().Hex(), otherHuman.Public().Hex()))

	revoke := signedDelegationList(t, author, agent.Public(), 101)
	require.NoError(t, lists.Apply(&revoke))
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
	require.NoError(t, lists.Apply(&grant))
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()), "older list cannot restore a revoked grant")
	newGrant := signedDelegationList(t, author, agent.Public(), 102, otherHuman.Public())
	require.NoError(t, lists.Apply(&newGrant))
	require.True(t, lists.Allows(agent.Public().Hex(), otherHuman.Public().Hex()))
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))

	reloaded := NewDelegationLists(nil, store, []string{author.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	require.NoError(t, reloaded.Load())
	require.False(t, reloaded.Allows(agent.Public().Hex(), otherHuman.Public().Hex()), "restart must wait for fresh EOSE")
	reloaded.mu.Lock()
	reloaded.caughtUp = true
	reloaded.mu.Unlock()
	require.True(t, reloaded.Allows(agent.Public().Hex(), otherHuman.Public().Hex()))
}

func TestDelegationListsNIP01TieBreakAndIndependentAuthors(t *testing.T) {
	store := testDelegationStore(t)
	authorA, authorB, agent, humanA, humanB := nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate()
	lists := NewDelegationLists(nil, store, []string{authorA.Public().Hex(), authorB.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	first := signedDelegationList(t, authorA, agent.Public(), 100, humanA.Public())
	second := signedDelegationList(t, authorA, agent.Public(), 100, humanB.Public())
	require.NoError(t, lists.Apply(&first))
	require.NoError(t, lists.Apply(&second))
	lists.mu.Lock()
	lists.caughtUp = true
	lists.mu.Unlock()
	winner := first
	loser := second
	if strings.Compare(second.ID.Hex(), first.ID.Hex()) < 0 {
		winner, loser = second, first
	}
	require.True(t, lists.Allows(agent.Public().Hex(), winner.Tags[1][1]))
	require.False(t, lists.Allows(agent.Public().Hex(), loser.Tags[1][1]))
	loserPubkey, err := nostr.PubKeyFromHex(loser.Tags[1][1])
	require.NoError(t, err)
	other := signedDelegationList(t, authorB, agent.Public(), 101, loserPubkey)
	require.NoError(t, lists.Apply(&other))
	require.True(t, lists.Allows(agent.Public().Hex(), loser.Tags[1][1]), "a separate trusted author may grant independently")
}

type delegationFakePool struct {
	urls    []string
	filters chan nostr.Filter
	events  chan *nostr.Event
	eose    chan nostradapter.RelayEOSE
	closed  chan nostradapter.RelayClosed
	done    chan struct{}
}

func (p *delegationFakePool) URLs() []string { return p.urls }
func (p *delegationFakePool) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostradapter.MergedSubscription, error) {
	if p.closed == nil {
		p.closed = make(chan nostradapter.RelayClosed)
	}
	p.filters <- filters[0]
	return &nostradapter.MergedSubscription{Events: p.events, RelayEOSE: p.eose, EndOfStoredEvents: p.done, Closed: p.closed}, nil
}

func TestDelegationListsREQIsScopedAndEOSEGatesGrant(t *testing.T) {
	store := testDelegationStore(t)
	author, agent, human := nostr.Generate(), nostr.Generate(), nostr.Generate()
	pool := &delegationFakePool{urls: []string{"wss://trusted.example"}, filters: make(chan nostr.Filter, 1), events: make(chan *nostr.Event), eose: make(chan nostradapter.RelayEOSE), done: make(chan struct{})}
	lists := NewDelegationLists(pool, store, []string{author.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	require.NoError(t, store.AdvanceCursor(pool.urls[0], lists.cursor, 1000))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- lists.subscribe(ctx) }()
	filter := <-pool.filters
	require.Equal(t, []nostr.Kind{nostr.Kind(kinds.ConfigACLList)}, filter.Kinds)
	require.Equal(t, []nostr.PubKey{author.Public()}, filter.Authors)
	require.Equal(t, []string{delegationDTag(agent.Public().Hex())}, filter.Tags["d"])
	require.Equal(t, nostr.Timestamp(400), filter.Since)
	lists.mu.RLock()
	caughtUpCh := lists.caughtUpCh
	lists.mu.RUnlock()
	grant := signedDelegationList(t, author, agent.Public(), 1001, human.Public())
	pool.events <- &grant
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
	pool.eose <- nostradapter.RelayEOSE{RelayURL: pool.urls[0]}
	close(pool.done)
	select {
	case <-caughtUpCh:
	case <-time.After(5 * time.Second):
		t.Fatal("delegation EOSE catch-up did not complete")
	}
	require.True(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
	pool.closed <- nostradapter.RelayClosed{RelayURL: pool.urls[0], Reason: "rate-limited:", Terminal: false}
	pool.eose <- nostradapter.RelayEOSE{RelayURL: pool.urls[0], Reissued: true}
	lists.mu.RLock()
	reissuedCatchup := lists.caughtUpCh
	lists.mu.RUnlock()
	select {
	case <-reissuedCatchup:
	case <-time.After(5 * time.Second):
		t.Fatal("delegation reissued EOSE did not restore catch-up")
	}
	require.True(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

func TestDelegationListsRefuseCatchupWithoutRelayEOSE(t *testing.T) {
	store := testDelegationStore(t)
	author, agent, human := nostr.Generate(), nostr.Generate(), nostr.Generate()
	pool := &delegationFakePool{urls: []string{"wss://trusted.example"}, filters: make(chan nostr.Filter, 1), events: make(chan *nostr.Event), eose: make(chan nostradapter.RelayEOSE), done: make(chan struct{})}
	lists := NewDelegationLists(pool, store, []string{author.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	grant := signedDelegationList(t, author, agent.Public(), 100, human.Public())
	require.NoError(t, lists.Apply(&grant))
	close(pool.done)
	close(pool.eose)
	err := lists.subscribe(t.Context())
	require.ErrorContains(t, err, "EOSE")
	require.False(t, lists.CaughtUp())
	require.False(t, lists.Allows(agent.Public().Hex(), human.Public().Hex()))
}
