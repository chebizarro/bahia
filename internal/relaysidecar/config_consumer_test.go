package relaysidecar

import (
	"context"
	"encoding/json"
	"fmt"
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
	return c.state.Applied[author+"\x00"+"relay-sidecar-test"+"\x00"+"edge"+"\x00"+policyName].Version
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

func TestConfigConsumerFailedActivationLeavesAppliedBehind(t *testing.T) {
	sk := nostr.Generate()
	author := sk.Public().Hex()
	dir := t.TempDir()

	failCount := 0
	consumer, err := NewConfigConsumer(ConfigConsumerConfig{
		ServiceID:      "relay-sidecar-test",
		Scope:          "edge",
		ProjectionPath: filepath.Join(dir, "projection.json"),
		TrustedAuthors: []string{author},
		Signer:         stubConfigSigner{},
		Publisher:      stubConfigPublisher{},
		Apply: func(p ConfigProjection) error {
			if failCount == 0 {
				failCount++
				return fmt.Errorf("activation failed")
			}
			return nil
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer.Start(ctx)

	event := membershipEvent(t, sk, nostr.Tags{{"p", author}})
	require.NoError(t, consumer.Handle(ctx, event))

	time.Sleep(100 * time.Millisecond)

	consumer.mu.Lock()
	appliedVersion := consumer.state.Applied[author+"\x00"+"relay-sidecar-test"+"\x00"+"edge"+"\x00"+"membership"].Version
	consumer.mu.Unlock()
	require.Equal(t, 0, appliedVersion, "applied should still be 0 after failed activation")

	consumer.activateCh <- struct{}{}
	time.Sleep(100 * time.Millisecond)

	consumer.mu.Lock()
	appliedVersion = consumer.state.Applied[author+"\x00"+"relay-sidecar-test"+"\x00"+"edge"+"\x00"+"membership"].Version
	consumer.mu.Unlock()
	require.Equal(t, 1, appliedVersion, "applied should advance to 1 after successful retry")
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
