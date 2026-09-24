package relaysidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type configStatusPublisherFunc func(context.Context, nostr.Event) (int, error)

func (f configStatusPublisherFunc) Publish(ctx context.Context, event nostr.Event) (int, error) {
	return f(ctx, event)
}

func TestConfigConsumerPublishesStatusesConcurrently(t *testing.T) {
	secret := nostr.Generate()
	cfg := sidecarTestConfig(t)
	cfg.PrivateKey = secret.Hex()
	cfg.Sidecar.ServiceID = "relay-sidecar-test"
	cfg.Sidecar.Scope = "edge"
	cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")
	cfg.Sidecar.ConfigTrustedPubkeys = []string{secret.Public().Hex()}
	server, err := New(cfg, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.store.Close()) }()
	consumer := server.consumer
	require.NotNil(t, consumer)

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

	// The statuses share a replaceable coordinate; arrival order is unspecified.
	var stored []nostr.Event
	for event := range server.store.Query(ctx, nostr.Filter{Kinds: []nostr.Kind{configStatusKind}}, 10) {
		require.True(t, ids[event.ID], "stored status was not published by this consumer")
		stored = append(stored, event)
	}
	require.Len(t, stored, 1)
}

// Correct NIP-01 retention alone cannot guarantee terminal status: these two
// phases still share one address. Keep this distinction explicit for bahia-1antv.
func TestConfigStatusNIP01DoesNotImplyAppliedPrecedence(t *testing.T) {
	for _, winnerStatus := range []string{"accepted", "applied"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-wins/reverse=%t", winnerStatus, reverse), func(t *testing.T) {
				secret := nostr.SecretKey{1}
				cfg := sidecarTestConfig(t)
				cfg.PrivateKey = secret.Hex()
				cfg.Sidecar.ServiceID = "relay-sidecar-test"
				cfg.Sidecar.Scope = "edge"
				cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")
				cfg.Sidecar.ConfigTrustedPubkeys = []string{secret.Public().Hex()}
				server, err := New(cfg, nil)
				require.NoError(t, err)
				defer server.store.Close()
				consumer := server.consumer
				fixedNow := time.Now().Truncate(time.Second)
				consumer.now = func() time.Time { return fixedNow }
				publisher := consumer.publisher
				var pair []nostr.Event
				consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
					pair = append(pair, event)
					return 1, nil
				})
				projection := ConfigProjection{ServiceID: cfg.Sidecar.ServiceID, Scope: cfg.Sidecar.Scope, PolicyName: "membership", Schema: configMembershipSchema, Version: 1}
				found := false
				for nonce := 1; nonce <= 256; nonce++ {
					pair = nil
					desiredID := fmt.Sprintf("%064x", nonce)
					require.NoError(t, consumer.publishStatus(t.Context(), projection, desiredID, "accepted", ""))
					require.NoError(t, consumer.publishStatus(t.Context(), projection, desiredID, "applied", ""))
					lowest := pair[0]
					if pair[1].ID.Hex() < lowest.ID.Hex() {
						lowest = pair[1]
					}
					if statusNameForTest(t, lowest) == winnerStatus {
						found = true
						break
					}
				}
				require.True(t, found, "could not construct the requested lexical winner")
				require.Equal(t, pair[0].CreatedAt, pair[1].CreatedAt)
				require.Equal(t, pair[0].Tags.GetD(), pair[1].Tags.GetD())
				require.NotEqual(t, pair[0].ID, pair[1].ID)
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
				require.Len(t, stored, 1)
				require.Equal(t, winnerStatus, statusNameForTest(t, stored[0]))
			})
		}
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
