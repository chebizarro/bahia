package nostr

import (
	"context"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fipsTestWorkerRepo struct {
	workers map[string]*domain.Worker
	upserts []*domain.Worker
}

func newFIPSTestWorkerRepo(workers ...*domain.Worker) *fipsTestWorkerRepo {
	repo := &fipsTestWorkerRepo{workers: map[string]*domain.Worker{}}
	for _, worker := range workers {
		copy := *worker
		repo.workers[copy.PubKey] = &copy
	}
	return repo
}

func (r *fipsTestWorkerRepo) Upsert(_ context.Context, worker *domain.Worker) error {
	copy := *worker
	r.workers[copy.PubKey] = &copy
	r.upserts = append(r.upserts, &copy)
	return nil
}

func (r *fipsTestWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	worker := r.workers[pubkey]
	if worker == nil {
		return nil, nil
	}
	copy := *worker
	return &copy, nil
}

func (r *fipsTestWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, nil
}

func (r *fipsTestWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

func TestParseOverlayAdvert(t *testing.T) {
	advert, err := ParseOverlayAdvert(`{
		"identifier":"fips-overlay-v1",
		"version":1,
		"endpoints":[
			{"transport":"udp","addr":"203.0.113.45:2121"},
			{"transport":"tor","addr":"xxxxx.onion:8443"}
		],
		"signalRelays":["wss://relay.example"],
		"stunServers":["stun:stun.example:19302"]
	}`, "fips-overlay-v1")
	require.NoError(t, err)
	require.Equal(t, "fips-overlay-v1", advert.Identifier)
	require.Equal(t, 1, advert.Version)
	require.Equal(t, []domain.FIPSTransportEndpoint{
		{Transport: "udp", Address: "203.0.113.45:2121"},
		{Transport: "tor", Address: "xxxxx.onion:8443"},
	}, advert.FIPSEndpoints())
}

func TestFIPSOverlayAddressKnownVector(t *testing.T) {
	ip, err := FIPSOverlayAddress("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	require.NoError(t, err)
	require.Equal(t, "fd63:dcd:2966:c433:6691:1254:48bb:b25b", ip.String())
}

func TestFIPSSubscriberFilterUsesFixedDTagAndOptionalProtocolNamespace(t *testing.T) {
	unscoped := NewFIPSSubscriber(nil, newFIPSTestWorkerRepo(), zap.NewNop())
	require.Equal(t, []gonostr.Kind{canonicalKind(FIPSOverlayAdvertKind)}, unscoped.filter().Kinds)
	require.Equal(t, []string{FIPSOverlayAdvertIdentifier}, unscoped.filter().Tags["d"])
	require.NotContains(t, unscoped.filter().Tags, "protocol")

	scoped := NewFIPSSubscriber(nil, newFIPSTestWorkerRepo(), zap.NewNop(), WithFIPSAppNamespace("bahia-mesh-v1"))
	require.Equal(t, []string{FIPSOverlayAdvertIdentifier}, scoped.filter().Tags["d"])
	require.Equal(t, []string{"bahia-mesh-v1"}, scoped.filter().Tags["protocol"])
}

// TestFIPSSubscriberReceivesAdvertsFromAuthRequiredRelay: the subscriber has
// no AUTH logic of its own (bahia-irsry.47). Against a relay that refuses
// unauthenticated REQs, the pool's AuthHandler authenticates and reissues the
// REQ on that relay, and the advert reaches the worker repository on the
// subscriber's first subscription.
func TestFIPSSubscriberReceivesAdvertsFromAuthRequiredRelay(t *testing.T) {
	relay := newPoolKhatruRelay(t, nil)
	ev := signedFIPSAdvertEvent(t, time.Now(), fipsAdvertContent("203.0.113.45:2121"))
	_, err := relay.relay.AddEvent(t.Context(), *ev)
	require.NoError(t, err)
	// Stored before the policy (khatru's AddEvent applies OnEvent), and
	// before any client connects.
	requireNIP42(false)(relay.relay)
	repo := &notifyingFIPSRepo{
		fipsTestWorkerRepo: newFIPSTestWorkerRepo(&domain.Worker{PubKey: eventPubKeyHex(ev), Name: "worker-a"}),
		upserted:           make(chan domain.Worker, 4),
	}
	pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithPrivateKey(testNostrPrivateKey))
	fastResubscribeBackoff(pool)
	defer pool.Close()
	subscriber := NewFIPSSubscriber(pool, repo, zap.NewNop())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- subscriber.Run(ctx) }()

	select {
	case worker := <-repo.upserted:
		require.Equal(t, []domain.FIPSTransportEndpoint{{Transport: "udp", Address: "203.0.113.45:2121"}}, worker.FIPSEndpoints)
	case <-ctx.Done():
		t.Fatal("advert from the auth-required relay never arrived")
	}
	cancel()
	require.NoError(t, <-done)
	status := pool.HealthSnapshot().Relays[0]
	require.Empty(t, status.ClosedReasons, "auth-required is answered by the pool, not surfaced as CLOSED")
}

// TestFIPSSubscriberAuthFailureIsTerminalAtThePool: without a signer the
// pool cannot answer "auth-required:"; it records the reason and surfaces a
// terminal CLOSED instead of retrying.
func TestFIPSSubscriberAuthFailureIsTerminalAtThePool(t *testing.T) {
	const relayURL = "wss://auth.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	reqs := newScriptedSubscribes(t)
	merged, err := pool.SubscribeAllWithEOSE(t.Context(), NewFIPSSubscriber(pool, newFIPSTestWorkerRepo(), zap.NewNop()).filters())
	require.NoError(t, err)
	defer merged.Close()
	closeScripted(reqs.next(t).sub, "auth-required: sign in")
	closeScripted(reqs.next(t).sub, "auth-required: sign in")
	for range 2 {
		closed := <-merged.Closed
		require.True(t, closed.Terminal)
		require.Equal(t, "auth-required: sign in", closed.Reason)
	}
	require.Contains(t, pool.HealthSnapshot().Relays[0].LastError, "auth-unavailable")
	reqs.none(t)
}

type notifyingFIPSRepo struct {
	*fipsTestWorkerRepo
	upserted chan domain.Worker
}

func (r *notifyingFIPSRepo) Upsert(ctx context.Context, worker *domain.Worker) error {
	err := r.fipsTestWorkerRepo.Upsert(ctx, worker)
	r.upserted <- *worker
	return err
}

func TestFIPSSubscriberMatchesWorkerByPubkeyAndAppliesAdvert(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := signedFIPSAdvertEvent(t, now, `{
		"identifier":"fips-overlay-v1",
		"version":1,
		"endpoints":[{"transport":"udp","addr":"203.0.113.45:2121"}]
	}`)
	repo := newFIPSTestWorkerRepo(&domain.Worker{
		PubKey:              eventPubKeyHex(ev),
		Name:                "worker-a",
		MaxConcurrentJobs:   2,
		LastAdvertisementAt: now,
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingActive,
	})
	subscriber := NewFIPSSubscriber(nil, repo, zap.NewNop(), withFIPSClock(func() time.Time { return now }))

	subscriber.handleEvent(context.Background(), ev)

	require.Len(t, repo.upserts, 1)
	updated := repo.upserts[0]
	require.Equal(t, eventPubKeyHex(ev), updated.PubKey)
	require.NotEmpty(t, updated.FIPSOverlayAddr)
	require.Equal(t, []domain.FIPSTransportEndpoint{{Transport: "udp", Address: "203.0.113.45:2121"}}, updated.FIPSEndpoints)
}

func TestFIPSSubscriberIgnoresUnknownWorkerWhenAutoRegisterDisabled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := signedFIPSAdvertEvent(t, now, `{
		"identifier":"fips-overlay-v1",
		"version":1,
		"endpoints":[{"transport":"udp","addr":"203.0.113.45:2121"}]
	}`)
	repo := newFIPSTestWorkerRepo()
	subscriber := NewFIPSSubscriber(nil, repo, zap.NewNop(), withFIPSClock(func() time.Time { return now }))

	subscriber.handleEvent(context.Background(), ev)

	require.Empty(t, repo.upserts)
}

func TestFIPSSubscriberRequiresFixedIdentifier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := signedFIPSAdvertEvent(t, now, `{
		"identifier":"bahia-mesh-v1",
		"version":1,
		"endpoints":[{"transport":"udp","addr":"203.0.113.45:2121"}]
	}`)
	repo := newFIPSTestWorkerRepo(&domain.Worker{PubKey: eventPubKeyHex(ev)})
	subscriber := NewFIPSSubscriber(nil, repo, zap.NewNop(), withFIPSClock(func() time.Time { return now }))

	subscriber.handleEvent(context.Background(), ev)

	require.Empty(t, repo.upserts)
}

func TestFIPSSubscriberRequiresConfiguredProtocolTag(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	content := `{
		"identifier":"fips-overlay-v1",
		"version":1,
		"endpoints":[{"transport":"udp","addr":"203.0.113.45:2121"}]
	}`
	missingProtocol := signedFIPSAdvertEvent(t, now, content)
	wrongProtocol := signedFIPSAdvertEvent(t, now, content, gonostr.Tag{"protocol", "other-namespace"})
	matchingProtocol := signedFIPSAdvertEvent(t, now, content, gonostr.Tag{"protocol", "bahia-mesh-v1"})
	repo := newFIPSTestWorkerRepo(&domain.Worker{PubKey: eventPubKeyHex(missingProtocol)})
	subscriber := NewFIPSSubscriber(nil, repo, zap.NewNop(), WithFIPSAppNamespace("bahia-mesh-v1"), withFIPSClock(func() time.Time { return now }))

	subscriber.handleEvent(context.Background(), missingProtocol)
	subscriber.handleEvent(context.Background(), wrongProtocol)
	require.Empty(t, repo.upserts)

	subscriber.handleEvent(context.Background(), matchingProtocol)
	require.Len(t, repo.upserts, 1)
}

func signedFIPSAdvertEvent(t *testing.T, createdAt time.Time, content string, extraTags ...gonostr.Tag) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{
		Kind:      canonicalKind(FIPSOverlayAdvertKind),
		CreatedAt: gonostr.Timestamp(createdAt.Unix()),
		Content:   content,
		Tags:      append(gonostr.Tags{{"d", FIPSOverlayAdvertIdentifier}}, extraTags...),
	}
	require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))
	return ev
}

func fipsAdvertContent(addr string) string {
	return `{"identifier":"fips-overlay-v1","version":1,"endpoints":[{"transport":"udp","addr":"` + addr + `"}]}`
}

func newFIPSLifecycleSubscriber(t *testing.T, now time.Time) (*FIPSSubscriber, *fipsTestWorkerRepo, string) {
	t.Helper()
	probe := signedFIPSAdvertEvent(t, now, fipsAdvertContent("203.0.113.1:2121"))
	pubkey := eventPubKeyHex(probe)
	repo := newFIPSTestWorkerRepo(&domain.Worker{PubKey: pubkey, Name: "worker-a"})
	return NewFIPSSubscriber(nil, repo, zap.NewNop(), withFIPSClock(func() time.Time { return now })), repo, pubkey
}

func fipsEndpoint(repo *fipsTestWorkerRepo, pubkey string) []domain.FIPSTransportEndpoint {
	return repo.workers[pubkey].FIPSEndpoints
}

func TestFIPSSubscriberPutsAllowlistIntoFilterAuthors(t *testing.T) {
	ev := signedFIPSAdvertEvent(t, time.Unix(1_700_000_000, 0), fipsAdvertContent("203.0.113.1:2121"))
	npub, err := nostrutil.EncodeNpubFromHex(eventPubKeyHex(ev))
	require.NoError(t, err)

	subscriber := NewFIPSSubscriber(nil, newFIPSTestWorkerRepo(), zap.NewNop(), WithFIPSAllowedNpubs([]string{npub}))
	require.Equal(t, []gonostr.PubKey{ev.PubKey}, subscriber.filter().Authors, "npub allowlist entries must decode")
	require.Equal(t, []gonostr.Kind{gonostr.KindDeletion}, subscriber.deletionFilter().Kinds)
	require.Equal(t, []gonostr.PubKey{ev.PubKey}, subscriber.deletionFilter().Authors)

	open := NewFIPSSubscriber(nil, newFIPSTestWorkerRepo(), zap.NewNop())
	require.Empty(t, open.filter().Authors)
	require.Equal(t, []string{"37195"}, open.deletionFilter().Tags["k"])
}

func TestFIPSSubscriberLatestWinsWithLowestIDTieBreak(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	a := signedFIPSAdvertEvent(t, now.Add(-time.Minute), fipsAdvertContent("203.0.113.1:2121"))
	b := signedFIPSAdvertEvent(t, now.Add(-time.Minute), fipsAdvertContent("203.0.113.2:2121"))
	low, high := a, b
	if low.ID.Hex() > high.ID.Hex() {
		low, high = high, low
	}
	lowEndpoint := fipsEndpointFromEvent(t, low)

	for _, order := range [][]*gonostr.Event{{low, high}, {high, low}} {
		subscriber, repo, pubkey := newFIPSLifecycleSubscriber(t, now)
		for _, ev := range order {
			subscriber.handleEvent(context.Background(), ev)
		}
		require.Equal(t, lowEndpoint, fipsEndpoint(repo, pubkey), "the lowest id must win an equal created_at regardless of arrival order")
	}

	subscriber, repo, pubkey := newFIPSLifecycleSubscriber(t, now)
	newer := signedFIPSAdvertEvent(t, now, fipsAdvertContent("203.0.113.9:2121"))
	subscriber.handleEvent(context.Background(), newer)
	subscriber.handleEvent(context.Background(), low)
	require.Equal(t, fipsEndpointFromEvent(t, newer), fipsEndpoint(repo, pubkey), "an older advert delivered late must not overwrite a newer one")
}

func fipsEndpointFromEvent(t *testing.T, ev *gonostr.Event) []domain.FIPSTransportEndpoint {
	t.Helper()
	advert, err := ParseOverlayAdvert(ev.Content, FIPSOverlayAdvertIdentifier)
	require.NoError(t, err)
	return advert.FIPSEndpoints()
}

func signedFIPSDeletion(t *testing.T, createdAt time.Time, tags ...gonostr.Tag) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: gonostr.KindDeletion, CreatedAt: gonostr.Timestamp(createdAt.Unix()), Tags: gonostr.Tags(tags)}
	require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))
	return ev
}

func TestFIPSSubscriberHonoursEDeletionWithoutResurrection(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	subscriber, repo, pubkey := newFIPSLifecycleSubscriber(t, now)
	advert := signedFIPSAdvertEvent(t, now.Add(-time.Hour), fipsAdvertContent("203.0.113.1:2121"))
	subscriber.handleEvent(context.Background(), advert)
	require.NotEmpty(t, fipsEndpoint(repo, pubkey))

	subscriber.handleEvent(context.Background(), signedFIPSDeletion(t, now.Add(-time.Minute), gonostr.Tag{"e", advert.ID.Hex()}, gonostr.Tag{"k", "37195"}))
	require.Empty(t, fipsEndpoint(repo, pubkey))
	require.Empty(t, repo.workers[pubkey].FIPSOverlayAddr)

	subscriber.handleEvent(context.Background(), advert)
	require.Empty(t, fipsEndpoint(repo, pubkey), "a deleted advert re-delivered by another relay must not resurrect")
}

func TestFIPSSubscriberHonoursADeletionWithoutResurrection(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	subscriber, repo, pubkey := newFIPSLifecycleSubscriber(t, now)
	older := signedFIPSAdvertEvent(t, now.Add(-2*time.Hour), fipsAdvertContent("203.0.113.1:2121"))
	current := signedFIPSAdvertEvent(t, now.Add(-time.Hour), fipsAdvertContent("203.0.113.2:2121"))
	subscriber.handleEvent(context.Background(), current)

	address := "37195:" + pubkey + ":" + FIPSOverlayAdvertIdentifier
	subscriber.handleEvent(context.Background(), signedFIPSDeletion(t, now.Add(-time.Minute), gonostr.Tag{"a", address}))
	require.Empty(t, fipsEndpoint(repo, pubkey))

	subscriber.handleEvent(context.Background(), older)
	subscriber.handleEvent(context.Background(), current)
	require.Empty(t, fipsEndpoint(repo, pubkey), "versions up to the deletion must not resurrect")

	newer := signedFIPSAdvertEvent(t, now, fipsAdvertContent("203.0.113.3:2121"))
	subscriber.handleEvent(context.Background(), newer)
	require.Equal(t, fipsEndpointFromEvent(t, newer), fipsEndpoint(repo, pubkey))
}

func TestFIPSSubscriberHonoursExpiration(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	subscriber, repo, pubkey := newFIPSLifecycleSubscriber(t, now)
	expired := signedFIPSAdvertEvent(t, now.Add(-time.Hour), fipsAdvertContent("203.0.113.1:2121"), gonostr.Tag{"expiration", strconv.FormatInt(now.Unix(), 10)})
	subscriber.handleEvent(context.Background(), expired)
	require.Empty(t, repo.upserts, "an expired advert must be ignored")

	expiring := signedFIPSAdvertEvent(t, now.Add(-time.Minute), fipsAdvertContent("203.0.113.2:2121"), gonostr.Tag{"expiration", strconv.FormatInt(now.Add(time.Minute).Unix(), 10)})
	subscriber.handleEvent(context.Background(), expiring)
	require.NotEmpty(t, fipsEndpoint(repo, pubkey))

	// Run hands the Lifecycle's expiry timer this drop path.
	subscriber.withdrawAdverts(context.Background(), subscriber.lifecycle.Expire(now.Add(time.Minute)), "expired")
	require.Empty(t, fipsEndpoint(repo, pubkey))
}
