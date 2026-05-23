package nostr

import (
	"context"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
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

func TestFIPSSubscriberMatchesWorkerByPubkeyAndAppliesAdvert(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := signedFIPSAdvertEvent(t, now, `{
		"identifier":"fips-overlay-v1",
		"version":1,
		"endpoints":[{"transport":"udp","addr":"203.0.113.45:2121"}]
	}`)
	repo := newFIPSTestWorkerRepo(&domain.Worker{
		PubKey:              ev.PubKey,
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
	require.Equal(t, ev.PubKey, updated.PubKey)
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

func signedFIPSAdvertEvent(t *testing.T, createdAt time.Time, content string) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{
		Kind:      FIPSOverlayAdvertKind,
		CreatedAt: gonostr.Timestamp(createdAt.Unix()),
		Content:   content,
		Tags:      gonostr.Tags{{"d", "fips-overlay-v1"}},
	}
	require.NoError(t, ev.Sign(testNostrPrivateKey))
	return ev
}
