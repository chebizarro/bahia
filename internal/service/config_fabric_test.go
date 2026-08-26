package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
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
