package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"github.com/openagentsinc/bahia/internal/repository"
)

type configTestSigner struct {
	delegate nostr.Signer
}

func newConfigTestSigner(t *testing.T) *configTestSigner {
	t.Helper()
	secret := nostr.Generate()
	return &configTestSigner{delegate: keyer.NewPlainKeySigner([32]byte(secret))}
}

func (s *configTestSigner) GetPublicKey(ctx context.Context) (string, error) {
	pubkey, err := s.delegate.GetPublicKey(ctx)
	return pubkey.Hex(), err
}

func (s *configTestSigner) Sign(ctx context.Context, event *nostr.Event) error {
	return s.delegate.SignEvent(ctx, event)
}

type configTestPublisher struct {
	events []nostr.Event
}

func (p *configTestPublisher) Publish(_ context.Context, event nostr.Event) (int, error) {
	p.events = append(p.events, event)
	return 1, nil
}

func validPolicyRequest(version int) ConfigPublishRequest {
	return ConfigPublishRequest{
		Kind: ConfigFabricPolicyKind, ServiceID: "khatru-relay", PolicyName: "rate-limits",
		Scope: "prod", Version: version, Schema: "cascadia.config.rate-limits.v1",
		Policy: map[string]any{"query": map[string]any{"max_limit": 500.0}},
	}
}

type configStatusRepoPublisher struct {
	repo   repository.NostrEventRepository
	events []nostr.Event
}

func (p *configStatusRepoPublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if !event.CheckID() || !event.VerifySignature() {
		return 0, fmt.Errorf("status event is not validly signed")
	}
	tags, err := json.Marshal(event.Tags)
	if err != nil {
		return 0, err
	}
	_, err = p.repo.Record(ctx, &repository.NostrEventRecord{
		ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
		Content: event.Content, Tags: tags, Sig: hex.EncodeToString(event.Sig[:]),
		CreatedAt: event.CreatedAt.Time(), ReceivedAt: time.Now(),
	})
	if err != nil {
		return 0, err
	}
	p.events = append(p.events, event)
	return 1, nil
}

func TestConfigFabricPublishApplyStatusClearsDriftEndToEnd(t *testing.T) {
	repo := repository.NewInMemoryNostrEventRepository()
	desiredPublisher := &configTestPublisher{}
	operator := newConfigTestSigner(t)
	console := NewConfigFabricService(repo, desiredPublisher, operator)
	console.now = func() time.Time { return time.Unix(1787625660, 0) }
	managedPubkey := strings.Repeat("a", 64)
	request := ConfigPublishRequest{
		Kind:       ConfigFabricPolicyKind,
		ServiceID:  "bahia-relay-sidecar",
		PolicyName: "relay-sidecar",
		Scope:      "prod",
		Version:    1,
		Schema:     "cascadia.config.relay-sidecar.v1",
		Policy: map[string]any{
			"allowed_pubkeys": []any{managedPubkey},
			"banned_pubkeys":  []any{},
			"name":            "Fleet Bahia Relay",
		},
	}
	receipt, err := console.Publish(t.Context(), request)
	if err != nil {
		t.Fatalf("console publish desired config: %v", err)
	}
	drift, err := console.ListDrift(t.Context())
	if err != nil || len(drift) != 1 || !drift[0].Drift {
		t.Fatalf("drift before apply = %#v err=%v", drift, err)
	}

	statusPublisher := &configStatusRepoPublisher{repo: repo}
	applied := relaysidecar.ConfigProjection{}
	consumer, err := relaysidecar.NewConfigConsumer(relaysidecar.ConfigConsumerConfig{
		ServiceID:      "bahia-relay-sidecar",
		Scope:          "prod",
		ProjectionPath: filepath.Join(t.TempDir(), "projection.json"),
		TrustedAuthors: []string{desiredPublisher.events[0].PubKey.Hex()},
		Signer:         operator,
		Publisher:      statusPublisher,
		Now:            func() time.Time { return time.Unix(1787625661, 0) },
		Apply: func(projection relaysidecar.ConfigProjection) error {
			applied = projection
			return nil
		},
	})
	if err != nil {
		t.Fatalf("configure consumer: %v", err)
	}
	if err := consumer.Handle(t.Context(), desiredPublisher.events[0]); err != nil {
		t.Fatalf("consumer apply desired config: %v", err)
	}
	if applied.EventID != receipt.EventID || len(applied.AllowedPubkeys) != 1 || applied.AllowedPubkeys[0] != managedPubkey {
		t.Fatalf("applied projection = %#v", applied)
	}
	if len(statusPublisher.events) != 1 {
		t.Fatalf("status events = %d, want 1", len(statusPublisher.events))
	}
	drift, err = console.ListDrift(t.Context())
	if err != nil {
		t.Fatalf("console drift after status: %v", err)
	}
	if len(drift) != 1 || drift[0].Drift || drift[0].AppliedEventID != receipt.EventID {
		t.Fatalf("drift after apply status = %#v", drift)
	}
}

func TestComposeConfigEventRequiredTagShape(t *testing.T) {
	event, err := composeConfigEvent(validPolicyRequest(7), time.Unix(1787625660, 0))
	if err != nil {
		t.Fatalf("composeConfigEvent() error = %v", err)
	}
	if int(event.Kind) != ConfigFabricPolicyKind {
		t.Fatalf("kind = %d", event.Kind)
	}
	want := map[string]string{
		"d": "service:khatru-relay:rate-limits", "service": "khatru-relay", "scope": "prod",
		"version": "7", "schema": "cascadia.config.rate-limits.v1",
	}
	for name, value := range want {
		got, err := exactlyOneTag(event.Tags, name)
		if err != nil || got != value {
			t.Fatalf("tag %s = %q, err=%v, want %q", name, got, err, value)
		}
	}
	if len(event.Tags) != len(want) {
		t.Fatalf("tags = %#v, want exactly five required tags", event.Tags)
	}
}

func TestConfigFabricPublisherRejectsNonMonotonicVersion(t *testing.T) {
	repo := repository.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{}
	svc := NewConfigFabricService(repo, publisher, newConfigTestSigner(t))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }

	if _, err := svc.Publish(context.Background(), validPolicyRequest(3)); err != nil {
		t.Fatalf("first Publish() error = %v", err)
	}
	if _, err := svc.Publish(context.Background(), validPolicyRequest(3)); err == nil || !strings.Contains(err.Error(), "advance monotonically") {
		t.Fatalf("second Publish() error = %v, want monotonic rejection", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
}

func TestConfigFabricPublisherRejectsSecretContent(t *testing.T) {
	tests := []map[string]any{
		{"api_token": "super-secret"},
		{"headers": map[string]any{"authorization": "Bearer abc123"}},
		{"signer": "nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"},
	}
	for _, policy := range tests {
		request := validPolicyRequest(1)
		request.Policy = policy
		if _, err := composeConfigEvent(request, time.Now()); err == nil || !strings.Contains(err.Error(), "secret") {
			t.Fatalf("composeConfigEvent(%#v) error = %v, want secret rejection", policy, err)
		}
	}
}

func TestConfigFabricListValidatesStandardItemTags(t *testing.T) {
	request := ConfigPublishRequest{
		Kind: ConfigFabricListKind, ServiceID: "groups-relay", PolicyName: "membership",
		Scope: "prod", Version: 1, Schema: "cascadia.config.membership.v1",
		Items: []ConfigListItem{{Tag: "p", Value: strings.Repeat("a", 64)}},
	}
	event, err := composeConfigEvent(request, time.Now())
	if err != nil {
		t.Fatalf("compose list: %v", err)
	}
	if got, err := exactlyOneTag(event.Tags, "p"); err != nil || got != strings.Repeat("a", 64) {
		t.Fatalf("p tag = %q, err=%v", got, err)
	}
	request.Items[0].Tag = "member"
	if _, err := composeConfigEvent(request, time.Now()); err == nil {
		t.Fatal("expected custom list tag rejection")
	}
}

func TestConfigFabricDriftAppliedAndRejectedProjection(t *testing.T) {
	repo := repository.NewInMemoryNostrEventRepository()
	svc := NewConfigFabricService(repo, &configTestPublisher{}, newConfigTestSigner(t))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }
	receipt, err := svc.Publish(context.Background(), validPolicyRequest(4))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	recordStatus(t, repo, receipt.EventID, "applied", 4, receipt.EventID, "", time.Unix(1787625661, 0))
	recordStatus(t, repo, strings.Repeat("b", 64), "rejected", 5, "", "limit exceeds service maximum", time.Unix(1787625662, 0))

	view, err := svc.ListDrift(context.Background())
	if err != nil {
		t.Fatalf("ListDrift() error = %v", err)
	}
	if len(view) != 1 {
		t.Fatalf("view = %#v", view)
	}
	if view[0].Drift || view[0].AppliedEventID != receipt.EventID || view[0].AppliedVersion != 4 {
		t.Fatalf("applied view = %#v", view[0])
	}
	if view[0].LastRejectionReason != "limit exceeds service maximum" {
		t.Fatalf("rejection reason = %q", view[0].LastRejectionReason)
	}
}

func TestConfigFabricRollbackRepublishesPriorContentAtHigherVersion(t *testing.T) {
	repo := repository.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{}
	svc := NewConfigFabricService(repo, publisher, newConfigTestSigner(t))
	base := time.Unix(1787625660, 0)
	tick := 0
	svc.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	first, err := svc.Publish(context.Background(), validPolicyRequest(1))
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	secondRequest := validPolicyRequest(2)
	secondRequest.Policy["query"].(map[string]any)["max_limit"] = 1000.0
	if _, err := svc.Publish(context.Background(), secondRequest); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	rollback, err := svc.Rollback(context.Background(), first.EventID)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if rollback.Version != 3 || len(publisher.events) != 3 {
		t.Fatalf("rollback receipt=%#v events=%d", rollback, len(publisher.events))
	}
	var content struct {
		Version int            `json:"version"`
		Policy  map[string]any `json:"policy"`
	}
	if err := json.Unmarshal([]byte(publisher.events[2].Content), &content); err != nil {
		t.Fatal(err)
	}
	if content.Version != 3 || content.Policy["query"].(map[string]any)["max_limit"] != float64(500) {
		t.Fatalf("rollback content = %#v", content)
	}
}

func recordStatus(t *testing.T, repo repository.NostrEventRepository, configEventID, status string, version int, lastAppliedID, reason string, createdAt time.Time) {
	t.Helper()
	payload := map[string]any{
		"service_id": "khatru-relay", "scope": "prod", "version": version,
		"policy_schema": "cascadia.config.rate-limits.v1", "config_event_id": configEventID, "status": status,
	}
	if status == "applied" {
		payload["effective_version"] = version
		payload["last_applied_event_id"] = lastAppliedID
	} else {
		payload["reason"] = reason
	}
	content, _ := json.Marshal(payload)
	tags, _ := json.Marshal(nostr.Tags{
		{"d", "config-status:khatru-relay:rate-limits:prod"}, {"domain", "config-status"},
		{"schema", configStatusSchema}, {"status", status}, {"service", "khatru-relay"},
		{"scope", "prod"}, {"version", strconv.Itoa(version)}, {"e", configEventID},
	})
	id := strings.Repeat("c", 64)
	if status == "rejected" {
		id = strings.Repeat("d", 64)
	}
	if _, err := repo.Record(context.Background(), &repository.NostrEventRecord{ID: id, Kind: ConfigFabricStatusKind, Content: string(content), Tags: tags, CreatedAt: createdAt}); err != nil {
		t.Fatalf("record status: %v", err)
	}
}
