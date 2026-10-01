package relaysidecar

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type stubConfigSigner struct{}

func (stubConfigSigner) Sign(context.Context, *nostr.Event) error { return nil }

type stubConfigPublisher struct{}

func (stubConfigPublisher) Publish(context.Context, nostr.Event) (int, error) { return 1, nil }

// membershipEvent builds a signed NIP-51 membership list carrying the supplied
// p tags verbatim, so tests can exercise tag shapes a publisher may legitimately
// emit (bare pubkey, pubkey plus relay hint, pubkey plus hint plus petname).
func membershipEvent(t *testing.T, sk nostr.SecretKey, pTags nostr.Tags) nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"service_id": "relay-sidecar-test",
		"scope":      "edge",
		"version":    1,
		"schema":     configMembershipSchema,
	})
	require.NoError(t, err)

	tags := nostr.Tags{
		{"service", "relay-sidecar-test"},
		{"scope", "edge"},
		{"version", "1"},
		{"schema", configMembershipSchema},
		{"d", "service:relay-sidecar-test:membership"},
	}
	tags = append(tags, pTags...)

	event := nostr.Event{
		Kind:      configListKind,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}
	require.NoError(t, event.Sign(sk))
	return event
}

// A p tag may carry a relay hint and a petname after the pubkey. Requiring an
// exact length silently dropped those members from the allowlist, shrinking an
// authorization set without any error.
func buildConfigEvent(t *testing.T, sk nostr.SecretKey, policyName, schema string, version int, pTags nostr.Tags) nostr.Event {
	t.Helper()
	var kind nostr.Kind
	switch policyName {
	case "membership":
		kind = configListKind
	case "relay-sidecar":
		kind = configPolicyKind
	default:
		t.Fatalf("unknown policy name: %s", policyName)
	}
	contentMap := map[string]any{
		"service_id": "relay-sidecar-test",
		"scope":      "edge",
		"version":    version,
		"schema":     schema,
	}
	if policyName == "relay-sidecar" {
		contentMap["policy"] = map[string]any{
			"name": "test-relay",
		}
	}
	content, err := json.Marshal(contentMap)
	require.NoError(t, err)

	tags := nostr.Tags{
		{"service", "relay-sidecar-test"},
		{"scope", "edge"},
		{"version", strconv.Itoa(version)},
		{"schema", schema},
		{"d", "service:relay-sidecar-test:" + policyName},
	}
	tags = append(tags, pTags...)

	event := nostr.Event{
		Kind:      kind,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}
	require.NoError(t, event.Sign(sk))
	return event
}

// appliedVersionForTest reads the applied coordinate the same way the consumer
// keys it, under the consumer's own lock so it is safe to call while the
// activation worker is running.
func appliedVersionForTest(c *ConfigConsumer, author, policyName string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Applied[author+"\x00"+c.serviceID+"\x00"+c.scope+"\x00"+policyName].Version
}

// Version ordering only means anything WITHIN one coordinate: two versions of
// the same service/scope/policy racing is what can leave the relay running
// older config than Applied claims. Two different coordinates have no ordering
// relationship, so activating one before the other proves nothing.
func TestConfigConsumerActivatesSameCoordinateInVersionOrder(t *testing.T) {
	sk := nostr.Generate()
	author := sk.Public().Hex()
	dir := t.TempDir()

	// Sign both events up front: event.Sign trips a known checkptr bug in the
	// nostr library under -race (bahia-4fz4z), so it must stay off the raced path.
	eventV1 := buildConfigEvent(t, sk, "relay-sidecar", configRelaySchema, 1, nostr.Tags{})
	eventV2 := buildConfigEvent(t, sk, "relay-sidecar", configRelaySchema, 2, nostr.Tags{})

	activations := make(chan int, 4)
	consumer, err := NewConfigConsumer(ConfigConsumerConfig{
		ServiceID:      "relay-sidecar-test",
		Scope:          "edge",
		ProjectionPath: filepath.Join(dir, "projection.json"),
		TrustedAuthors: []string{author},
		Signer:         stubConfigSigner{},
		Publisher:      stubConfigPublisher{},
		Apply: func(p ConfigProjection) error {
			activations <- p.Version
			return nil
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer.Start(ctx)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = consumer.Handle(ctx, eventV1) }()
	go func() { defer wg.Done(); _ = consumer.Handle(ctx, eventV2) }()
	wg.Wait()

	seen := []int{}
	timeout := time.After(3 * time.Second)
collect:
	for {
		select {
		case v := <-activations:
			seen = append(seen, v)
			if applied := appliedVersionForTest(consumer, author, "relay-sidecar"); applied == 2 {
				break collect
			}
		case <-timeout:
			break collect
		}
	}

	require.NotEmpty(t, seen, "no activation ran")
	for i := 1; i < len(seen); i++ {
		require.Greaterf(t, seen[i], seen[i-1],
			"same coordinate activated out of version order: %v", seen)
	}
	require.Equal(t, 2, appliedVersionForTest(consumer, author, "relay-sidecar"),
		"applied must end at the highest handled version, got %v", seen)
}

// A failed activation leaves the coordinate pending and Applied behind; the
// next activation trigger retries it. The test waits on the consumer's own
// signals instead of sleeping: Apply reports every attempt, and the "applied"
// status is published only after processPending has advanced Applied.
func TestConfigConsumerFailedActivationLeavesAppliedBehind(t *testing.T) {
	sk := nostr.Generate()
	author := sk.Public().Hex()
	// Sign before Start: event.Sign trips a known checkptr bug in the nostr
	// library under -race (bahia-4fz4z), so it must stay off the raced path.
	event := membershipEvent(t, sk, nostr.Tags{{"p", author}})

	attempts := make(chan error, 2)
	statuses := make(chan nostr.Event, 8)
	failNext := true // only the activation goroutine reads or writes it
	consumer, err := NewConfigConsumer(ConfigConsumerConfig{
		ServiceID:      "relay-sidecar-test",
		Scope:          "edge",
		ProjectionPath: filepath.Join(t.TempDir(), "projection.json"),
		TrustedAuthors: []string{author},
		Signer:         stubConfigSigner{},
		Publisher: configStatusPublisherFunc(func(_ context.Context, status nostr.Event) (int, error) {
			statuses <- status
			return 1, nil
		}),
		Apply: func(ConfigProjection) error {
			var err error
			if failNext {
				failNext = false
				err = errors.New("activation failed")
			}
			attempts <- err
			return err
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	consumer.Start(ctx)

	pendingAndApplied := func() (int, int) {
		consumer.mu.Lock()
		defer consumer.mu.Unlock()
		return len(consumer.state.Pending), consumer.state.Applied[author+"\x00relay-sidecar-test\x00edge\x00membership"].Version
	}
	awaitAttempt := func() error {
		t.Helper()
		select {
		case err := <-attempts:
			return err
		case <-ctx.Done():
			t.Fatal("activation was not attempted")
			return nil
		}
	}

	// Handle publishes "accepted" before it returns, then signals activation.
	require.NoError(t, consumer.Handle(ctx, event))
	awaitStatus(t, ctx, statuses, "accepted", event.ID.Hex())

	// A failed Apply returns from processPending without touching state, so
	// once the attempt is observed nothing else can change it until the next
	// trigger.
	require.Error(t, awaitAttempt())
	pending, applied := pendingAndApplied()
	require.Equal(t, 1, pending, "a failed activation must stay pending for retry")
	require.Equal(t, 0, applied, "applied should still be 0 after failed activation")
	require.Empty(t, statuses, "a failed activation must not publish applied status")

	consumer.activateCh <- struct{}{}
	require.NoError(t, awaitAttempt())
	awaitStatus(t, ctx, statuses, "applied", event.ID.Hex())
	pending, applied = pendingAndApplied()
	require.Equal(t, 0, pending, "the retried activation must leave the queue")
	require.Equal(t, 1, applied, "applied should advance to 1 after successful retry")
}

func TestValidateAcceptsMembershipTagsWithRelayHints(t *testing.T) {
	sk := nostr.Generate()
	author := sk.Public().Hex()
	member := nostr.Generate().Public().Hex()
	hinted := nostr.Generate().Public().Hex()
	named := nostr.Generate().Public().Hex()

	consumer, err := NewConfigConsumer(ConfigConsumerConfig{
		ServiceID:      "relay-sidecar-test",
		Scope:          "edge",
		ProjectionPath: filepath.Join(t.TempDir(), "projection.json"),
		TrustedAuthors: []string{author},
		Signer:         stubConfigSigner{},
		Publisher:      stubConfigPublisher{},
		Apply:          func(ConfigProjection) error { return nil },
	})
	require.NoError(t, err)

	event := membershipEvent(t, sk, nostr.Tags{
		{"p", member},
		{"p", hinted, "wss://relay.example"},
		{"p", named, "wss://relay.example", "operator"},
	})

	projection, err := consumer.validate(event)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{member, hinted, named}, projection.AllowedPubkeys)
}
