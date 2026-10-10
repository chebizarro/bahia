package relaysidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"

	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type configStatusPublisherFunc func(context.Context, nostr.Event) (int, error)

func (f configStatusPublisherFunc) Publish(ctx context.Context, event nostr.Event) (int, error) {
	return f(ctx, event)
}

func TestConfigConsumerPublishesStatusesConcurrently(t *testing.T) {
	secret := nostr.Generate()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.ServiceID = "relay-sidecar-test"
	cfg.Sidecar.Scope = "edge"
	cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")
	cfg.Sidecar.ConfigTrustedPubkeys = []string{secret.Public().Hex()}
	server, err := New(t.Context(), cfg, localSigner(t, secret), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.store.Close()) }()
	consumer := server.consumer
	require.NotNil(t, consumer)
	consumer.now = func() time.Time { return time.Unix(1790200000, 0) }

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered := make(chan nostr.Event, 2)
	release := make(chan struct{})
	type publishResult struct {
		accepted int
		err      error
	}
	completed := make(chan publishResult, 2)
	publisher := consumer.publisher
	consumer.publisher = configStatusPublisherFunc(func(ctx context.Context, event nostr.Event) (int, error) {
		entered <- event
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		// Exercise the actual production publisher, policy, and SQLite store.
		accepted, err := publisher.Publish(ctx, event)
		completed <- publishResult{accepted: accepted, err: err}
		return accepted, err
	})

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		consumer.activateLoop(ctx)
	}()
	defer func() {
		cancel()
		<-workerDone
	}()
	desired := membershipEvent(t, secret, nil)
	handled := make(chan error, 1)
	go func() { handled <- consumer.Handle(ctx, desired) }()

	// Neither Publish may return until both paths have entered. A serializing
	// lock in ConfigConsumer would fail this rendezvous, even with -cpu=1.
	statuses := make(map[string]bool, 2)
	ids := make(map[nostr.ID]bool, 2)
	for range 2 {
		select {
		case event := <-entered:
			require.True(t, event.CheckID())
			require.True(t, event.VerifySignature())
			require.Equal(t, secret.Public(), event.PubKey)
			var content struct {
				Status        string `json:"status"`
				ConfigEventID string `json:"config_event_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(event.Content), &content))
			require.Equal(t, desired.ID.Hex(), content.ConfigEventID)
			statuses[content.Status] = true
			ids[event.ID] = true
		case <-ctx.Done():
			t.Fatalf("status publications did not overlap: %v", ctx.Err())
		}
	}
	require.Equal(t, map[string]bool{"accepted": true, "applied": true}, statuses)
	close(release)
	for range 2 {
		select {
		case result := <-completed:
			require.NoError(t, result.err)
			require.Equal(t, 1, result.accepted)
		case <-ctx.Done():
			t.Fatalf("production status publisher did not complete: %v", ctx.Err())
		}
	}
	select {
	case err := <-handled:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("Handle did not complete: %v", ctx.Err())
	}
	require.Equal(t, 1, appliedVersionForTest(consumer, secret.Public().Hex(), "membership"))

	// stable d-tag means only the terminal (applied) status survives
	// NIP-01 replacement in the store. The applied event has created_at + 1,
	// so it always wins.
	var stored []nostr.Event
	for event := range server.store.Query(ctx, nostr.Filter{Kinds: []nostr.Kind{configStatusKind}}, 10) {
		require.True(t, ids[event.ID], "stored status was not published by this consumer")
		stored = append(stored, event)
	}
	require.Len(t, stored, 1, "stable d-tag: only the terminal status survives NIP-01 replacement")
	require.Equal(t, "applied", statusNameForTest(t, stored[0]))
}

// stable d-tags mean only the terminal status survives NIP-01
// replacement. The applied event always wins because it has created_at + 1
// relative to accepted. Both relay arrival orders must retain applied truth.
func TestConfigStatusAppliedSurvivesReplay(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("applied-lowest-id/reverse=%t", reverse), func(t *testing.T) {
			server, secret := configStatusServerForTest(t)
			consumer := server.consumer
			publisher := consumer.publisher
			var pair []nostr.Event
			consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
				pair = append(pair, event)
				return 1, nil
			})
			desired := configStatusDesiredForTest(t, secret, 1, 0)
			require.NoError(t, consumer.Handle(t.Context(), desired))
			consumer.processPending(t.Context())
			require.Equal(t, 1, appliedVersionForTest(consumer, secret.Public().Hex(), "membership"))
			require.Len(t, pair, 2)
			// applied has created_at + 1, so it has a later timestamp.
			require.Equal(t, pair[0].CreatedAt+1, pair[1].CreatedAt, "applied event must have later created_at")
			t.Logf("status d-tags: accepted=%s applied=%s", pair[0].Tags.GetD(), pair[1].Tags.GetD())
			// Both share the same stable d-tag.
			require.Equal(t, pair[0].Tags.GetD(), pair[1].Tags.GetD(), "stable d-tag: both share the same coordinate")
			require.NotEqual(t, pair[0].ID, pair[1].ID)
			require.NoError(t, server.store.Replace(t.Context(), desired))
			replay := service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
			before, err := replay.ListDrift(t.Context())
			require.NoError(t, err)
			require.Len(t, before, 1)
			require.True(t, before[0].Drift)
			server.relay.ReplaceEvent = func(ctx context.Context, event nostr.Event) error {
				err := server.store.Replace(ctx, event)
				t.Logf("store %s: %v", statusNameForTest(t, event), err)
				return err
			}
			if reverse {
				pair[0], pair[1] = pair[1], pair[0]
			}
			for _, event := range pair {
				require.True(t, event.CheckID())
				require.True(t, event.VerifySignature())
				accepted, err := publisher.Publish(t.Context(), event)
				require.NoError(t, err)
				require.Equal(t, 1, accepted)
			}
			var stored []nostr.Event
			for event := range server.store.Query(t.Context(), nostr.Filter{Kinds: []nostr.Kind{configStatusKind}}, 10) {
				stored = append(stored, event)
			}
			// only the terminal (applied) survives NIP-01 replacement.
			require.Len(t, stored, 1, "stable d-tag: only applied survives")
			require.Equal(t, "applied", statusNameForTest(t, stored[0]))
			// Restart the durable store, then build a fresh production replay reader.
			require.NoError(t, server.store.Close())
			server.store, err = openEventStore(t.Context(), server.cfg.DataDir, nil)
			require.NoError(t, err)
			replay = service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
			after, err := replay.ListDrift(t.Context())
			require.NoError(t, err)
			require.Len(t, after, 1)
			require.False(t, after[0].Drift, "retained applied truth must clear drift on replay")
			require.Equal(t, desired.ID.Hex(), after[0].AppliedEventID)
		})
	}
}

func statusNameForTest(t *testing.T, event nostr.Event) string {
	t.Helper()
	var content struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(event.Content), &content))
	return content.Status
}

// ListDrift reads directly from relay retention, never from an append-only
// publication capture. A fresh reader therefore has no memory of evicted events.
type configStatusReplayRepository struct {
	repository.NostrEventRepository
	store *eventStore
}

func (r configStatusReplayRepository) ListByKind(ctx context.Context, kind, limit int) ([]repository.NostrEventRecord, error) {
	var records []repository.NostrEventRecord
	for event := range r.store.Query(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kind)}}, limit) {
		tags, err := json.Marshal(event.Tags)
		if err != nil {
			return nil, err
		}
		records = append(records, repository.NostrEventRecord{
			ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
			Content: event.Content, Tags: tags, CreatedAt: event.CreatedAt.Time(),
		})
	}
	return records, nil
}

func configStatusServerForTest(t *testing.T) (*Server, nostr.SecretKey) {
	t.Helper()
	secret := nostr.SecretKey{1}
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.ServiceID = "relay-sidecar-test"
	cfg.Sidecar.Scope = "prod"
	cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")
	cfg.Sidecar.ConfigTrustedPubkeys = []string{secret.Public().Hex()}
	server, err := New(t.Context(), cfg, localSigner(t, secret), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.store.Close()) })
	server.consumer.now = func() time.Time { return time.Unix(1790200000, 0) }
	return server, secret
}

func configStatusDesiredForTest(t *testing.T, secret nostr.SecretKey, version, nonce int) nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"service_id": "relay-sidecar-test", "scope": "prod", "version": version,
		"schema": configMembershipSchema,
	})
	require.NoError(t, err)
	desired := nostr.Event{
		Kind: configListKind, CreatedAt: nostr.Timestamp(1790200000 + version), Content: string(content),
		Tags: nostr.Tags{
			{"d", "service:relay-sidecar-test:membership"}, {"service", "relay-sidecar-test"},
			{"scope", "prod"}, {"version", strconv.Itoa(version)}, {"schema", configMembershipSchema},
			{"p", secret.Public().Hex()}, {"nonce", strconv.Itoa(nonce)},
		},
	}
	require.NoError(t, desired.Sign(secret))
	return desired
}

func TestConfigStatusAppliedVersionsSurviveReplay(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			server, secret := configStatusServerForTest(t)
			consumer := server.consumer
			publisher := consumer.publisher
			var events []nostr.Event
			consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
				events = append(events, event)
				return 1, nil
			})
			var desired nostr.Event
			for version := 1; version <= 2; version++ {
				// advance c.now() between versions so later status
				// events win NIP-01 replacement on the shared stable d-tag.
				ts := int64(1790200000 + 200*(version-1))
				consumer.now = func() time.Time { return time.Unix(ts, 0) }
				desired = configStatusDesiredForTest(t, secret, version, 0)
				require.NoError(t, server.store.Replace(t.Context(), desired))
				require.NoError(t, consumer.Handle(t.Context(), desired))
				consumer.processPending(t.Context())
			}
			require.Len(t, events, 4)
			for i := range events {
				event := events[i]
				if reverse {
					event = events[len(events)-1-i]
				}
				accepted, err := publisher.Publish(t.Context(), event)
				require.NoError(t, err)
				require.Equal(t, 1, accepted)
			}
			require.Equal(t, 2, appliedVersionForTest(consumer, secret.Public().Hex(), "membership"))
			consumer.publisher = publisher
			replay := service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
			assertApplied := func(want nostr.Event, drifted bool) {
				t.Helper()
				drift, err := replay.ListDrift(t.Context())
				require.NoError(t, err)
				require.Len(t, drift, 1)
				require.Equal(t, want.ID.Hex(), drift[0].AppliedEventID)
				require.Equal(t, drifted, drift[0].Drift)
			}
			assertApplied(desired, false)

			// Later progress, rejection of a duplicate, and a delayed old applied
			// receipt must not erase or demote the effective version.
			// delayed old receipts have lower timestamps than the current
			// version's applied event, so NIP-01 replacement protects newer truth.
			consumer.now = func() time.Time { return time.Unix(1790200100, 0) }
			old := configStatusDesiredForTest(t, secret, 1, 0)
			oldProjection, err := consumer.validate(old)
			require.NoError(t, err)
			require.NoError(t, consumer.publishStatus(t.Context(), oldProjection, old.ID.Hex(), "applied", ""))
			projection, err := consumer.validate(desired)
			require.NoError(t, err)
			require.NoError(t, consumer.publishStatus(t.Context(), projection, desired.ID.Hex(), "accepted", ""))
			require.ErrorContains(t, consumer.Handle(t.Context(), desired), "does not advance desired version")
			assertApplied(desired, false)

			// v3 accepted (higher timestamp) replaces v2 applied in the
			// store via NIP-01 replacement. Drift is true because no applied
			// event for v3 exists yet — accepted is not evidence of activation.
			consumer.now = func() time.Time { return time.Unix(1790200400, 0) }
			next := configStatusDesiredForTest(t, secret, 3, 0)
			require.NoError(t, server.store.Replace(t.Context(), next))
			require.NoError(t, consumer.Handle(t.Context(), next))
			drift, err := replay.ListDrift(t.Context())
			require.NoError(t, err)
			require.Len(t, drift, 1)
			require.True(t, drift[0].Drift, "accepted is not evidence of activation")
			consumer.processPending(t.Context())
			assertApplied(next, false)

			require.NoError(t, server.store.Close())
			server.store, err = openEventStore(t.Context(), server.cfg.DataDir, nil)
			require.NoError(t, err)
			replay = service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
			assertApplied(next, false)
		})
	}
}

func TestConfigStatusMixedSchemaReplay(t *testing.T) {
	server, secret := configStatusServerForTest(t)
	consumer := server.consumer
	publisher := consumer.publisher
	var captured []nostr.Event
	consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
		captured = append(captured, event)
		return 1, nil
	})
	old := configStatusDesiredForTest(t, secret, 1, 0)
	require.NoError(t, consumer.Handle(t.Context(), old))
	consumer.processPending(t.Context())
	require.Len(t, captured, 2)
	legacy := captured[1]
	require.Equal(t, "applied", statusNameForTest(t, legacy))
	// Model a persisted, signed receipt from the previous publisher schema.
	for i := range legacy.Tags {
		switch legacy.Tags[i][0] {
		case "d":
			legacy.Tags[i][1] = "config-status:relay-sidecar-test:membership:prod"
		case "schema":
			legacy.Tags[i][1] = "cascadia.config.status.v1"
		}
	}
	require.NoError(t, consumer.signer.Sign(t.Context(), &legacy))
	consumer.publisher = publisher
	accepted, err := publisher.Publish(t.Context(), legacy)
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	require.NoError(t, server.store.Replace(t.Context(), old))
	assertReplay := func(want nostr.Event, drifted bool) {
		t.Helper()
		replay := service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
		drift, err := replay.ListDrift(t.Context())
		require.NoError(t, err)
		require.Len(t, drift, 1)
		require.Equal(t, want.ID.Hex(), drift[0].AppliedEventID)
		require.Equal(t, drifted, drift[0].Drift)
	}
	assertReplay(old, false)

	// advance c.now() so v2 status events have strictly higher
	// created_at and win NIP-01 replacement over the legacy v1 event.
	consumer.now = func() time.Time { return time.Unix(1790200200, 0) }
	next := configStatusDesiredForTest(t, secret, 2, 0)
	require.NoError(t, server.store.Replace(t.Context(), next))
	require.NoError(t, consumer.Handle(t.Context(), next))
	// v2 accepted (ts=1790200200) replaces v1 legacy (ts=1790200001)
	// in the store. No applied event for v2 yet → drift is true.
	replay := service.NewConfigFabricService(configStatusReplayRepository{store: server.store}, nil, nil)
	drift, err := replay.ListDrift(t.Context())
	require.NoError(t, err)
	require.Len(t, drift, 1)
	require.True(t, drift[0].Drift)
	consumer.processPending(t.Context())
	assertReplay(next, false)

	// A stale legacy receipt arriving late must not overwrite newer truth.
	// legacy.CreatedAt + 100 is still lower than v2 applied's
	// timestamp, so NIP-01 replacement rejects it.
	legacy.CreatedAt += 100
	require.NoError(t, consumer.signer.Sign(t.Context(), &legacy))
	accepted, err = publisher.Publish(t.Context(), legacy)
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	assertReplay(next, false)
	require.NoError(t, server.store.Close())
	server.store, err = openEventStore(t.Context(), server.cfg.DataDir, nil)
	require.NoError(t, err)
	assertReplay(next, false)
}
