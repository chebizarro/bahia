package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

type testService struct {
	agent       *Agent
	includeDir  string
	statePath   string
	reloadCalls *int
	reloadErr   *error
}

func newTestService(t *testing.T, requireEncryption bool) testService {
	t.Helper()
	dir := t.TempDir()
	calls := 0
	var runnerErr error
	eng := engine.New(engine.Config{
		IncludeDir: dir,
		FilePrefix: "bahia-",
		Reload:     engine.ReloadConfig{ExplicitCommand: "reload dnsmasq"},
		Runner: func(context.Context, []string) error {
			calls++
			return runnerErr
		},
	})
	statePath := filepath.Join(dir, "state", "dns-agent.json")
	agent, err := New(Config{
		Engine:            eng,
		IncludeDir:        dir,
		FilePrefix:        "bahia-",
		AllowedZones:      []string{"Example.Internal."},
		StateFilePath:     statePath,
		RequireEncryption: requireEncryption,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testService{agent: agent, includeDir: dir, statePath: statePath, reloadCalls: &calls, reloadErr: &runnerErr}
}

func testZone() domain.DNSZone {
	return domain.DNSZone{Name: "example.internal", Visibility: domain.ZoneVisibilityInternal, BackendRef: "core-01", TTL: 60}
}

func testRecords(value string) []domain.DNSRecord {
	return []domain.DNSRecord{{Zone: "example.internal", Name: "api", FQDN: "api.example.internal", Type: domain.DNSRecordTypeA, Value: value, TTL: 60}}
}

func requestWithParams(t *testing.T, params any) controlplane.ContextVMRequest {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return controlplane.ContextVMRequest{RPC: controlplane.ContextVMJSONRPCRequest{Params: data}}
}

func syncAgent(t *testing.T, agent *Agent, serial int64, records []domain.DNSRecord) (protocol.SyncResult, error) {
	t.Helper()
	result, err := agent.SyncHandler(context.Background(), requestWithParams(t, protocol.SyncParams{
		Schema: protocol.Schema, Zone: testZone(), Records: records, Serial: serial,
	}))
	if err != nil {
		return protocol.SyncResult{}, err
	}
	return result.(protocol.SyncResult), nil
}

func TestHealthListSyncRoundTrip(t *testing.T) {
	service := newTestService(t, false)

	healthAny, err := service.agent.HealthHandler(context.Background(), requestWithParams(t, protocol.HealthParams{Schema: protocol.Schema}))
	if err != nil {
		t.Fatal(err)
	}
	health := healthAny.(protocol.HealthResult)
	if health.Status != "ok" || health.IncludeDir != service.includeDir || health.FilePrefix != "bahia-" {
		t.Fatalf("unexpected health result: %+v", health)
	}
	if health.ReloadStrategy != "sh -c reload dnsmasq" || len(health.AllowedZones) != 1 || health.AllowedZones[0] != "example.internal" {
		t.Fatalf("unexpected health configuration: %+v", health)
	}

	syncResult, err := syncAgent(t, service.agent, 7, testRecords("10.0.0.7"))
	if err != nil {
		t.Fatal(err)
	}
	if !syncResult.Changed || syncResult.Serial != 7 || syncResult.Status != "ok" {
		t.Fatalf("unexpected sync result: %+v", syncResult)
	}

	listAny, err := service.agent.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: testZone()}))
	if err != nil {
		t.Fatal(err)
	}
	list := listAny.(protocol.ListResult)
	if list.Serial != 7 || len(list.Records) != 1 || list.Records[0].Value != "10.0.0.7" {
		t.Fatalf("unexpected list result: %+v", list)
	}

	healthAny, err = service.agent.HealthHandler(context.Background(), requestWithParams(t, protocol.HealthParams{Schema: protocol.Schema}))
	if err != nil {
		t.Fatal(err)
	}
	health = healthAny.(protocol.HealthResult)
	if health.LastApplySerial != 7 || health.LastApplyAt == "" {
		t.Fatalf("health did not expose last apply: %+v", health)
	}
}

func TestListExposesObservedAuthoritativeState(t *testing.T) {
	service := newTestService(t, false)
	zone := testZone()
	zone.Authoritative = true
	_, err := service.agent.SyncHandler(context.Background(), requestWithParams(t, protocol.SyncParams{
		Schema: protocol.Schema, Zone: zone, Records: testRecords("10.0.0.7"), Serial: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}

	listAny, err := service.agent.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: zone}))
	if err != nil {
		t.Fatal(err)
	}
	if list := listAny.(protocol.ListResult); !list.Authoritative {
		t.Fatalf("list result did not expose authoritative state: %+v", list)
	}
}

func TestAllowlistAndSchemaRejections(t *testing.T) {
	service := newTestService(t, false)
	zone := testZone()
	zone.Name = "other.internal"
	_, err := service.agent.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: zone}))
	if err == nil || err.Error() != `zone "other.internal" not allowed by agent allowlist` {
		t.Fatalf("unexpected allowlist error: %v", err)
	}
	_, err = service.agent.HealthHandler(context.Background(), requestWithParams(t, protocol.HealthParams{Schema: "wrong"}))
	if err == nil || !strings.Contains(err.Error(), "unsupported DNS agent schema") {
		t.Fatalf("unexpected schema error: %v", err)
	}
}

func TestStaleSerialRejectedAndEqualSerialIsIdempotent(t *testing.T) {
	service := newTestService(t, false)
	if _, err := syncAgent(t, service.agent, 5, testRecords("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	calls := *service.reloadCalls
	result, err := syncAgent(t, service.agent, 5, testRecords("10.0.0.99"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || *service.reloadCalls != calls {
		t.Fatalf("equal serial was not an idempotent no-op: result=%+v reloads=%d", result, *service.reloadCalls)
	}
	result, err = syncAgent(t, service.agent, 4, testRecords("10.0.0.4"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.SyncStatusStale || result.Changed || result.Serial != 5 {
		t.Fatalf("stale sync did not report agent serial: %+v", result)
	}
	if *service.reloadCalls != calls {
		t.Fatalf("stale sync triggered a reload: %d", *service.reloadCalls)
	}
}

func TestStateFilePersistenceAcrossAgentRestarts(t *testing.T) {
	service := newTestService(t, false)
	if _, err := syncAgent(t, service.agent, 12, testRecords("10.0.0.12")); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		IncludeDir: service.includeDir,
		FilePrefix: "bahia-",
		Reload:     engine.ReloadConfig{ExplicitCommand: "reload dnsmasq"},
		Runner: func(context.Context, []string) error {
			*service.reloadCalls++
			return nil
		},
	})
	restarted, err := New(Config{Engine: eng, IncludeDir: service.includeDir, FilePrefix: "bahia-", AllowedZones: []string{"example.internal"}, StateFilePath: service.statePath})
	if err != nil {
		t.Fatal(err)
	}
	calls := *service.reloadCalls
	result, err := syncAgent(t, restarted, 12, testRecords("10.0.0.200"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || *service.reloadCalls != calls {
		t.Fatalf("restarted agent did not preserve serial guard: result=%+v reloads=%d", result, *service.reloadCalls)
	}
	result, err = syncAgent(t, restarted, 11, testRecords("10.0.0.11"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.SyncStatusStale || result.Changed || result.Serial != 12 {
		t.Fatalf("restarted agent stale sync did not report agent serial: %+v", result)
	}
}

func TestEmptyZoneAndSerialGuardSurviveRestart(t *testing.T) {
	service := newTestService(t, false)
	if _, err := syncAgent(t, service.agent, 10, testRecords("10.0.0.10")); err != nil {
		t.Fatal(err)
	}
	if _, err := syncAgent(t, service.agent, 11, nil); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		IncludeDir: service.includeDir,
		FilePrefix: "bahia-",
		Reload:     engine.ReloadConfig{ExplicitCommand: "reload dnsmasq"},
		Runner:     func(context.Context, []string) error { *service.reloadCalls++; return nil },
	})
	restarted, err := New(Config{Engine: eng, IncludeDir: service.includeDir, FilePrefix: "bahia-", AllowedZones: []string{"example.internal"}, StateFilePath: service.statePath})
	if err != nil {
		t.Fatal(err)
	}
	list, err := restarted.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: testZone()}))
	if err != nil {
		t.Fatal(err)
	}
	if got := list.(protocol.ListResult); len(got.Records) != 0 || got.Serial != 11 {
		t.Fatalf("cleared zone did not survive restart: %+v", got)
	}
	stale, err := syncAgent(t, restarted, 10, testRecords("10.0.0.10"))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != protocol.SyncStatusStale || stale.Changed {
		t.Fatalf("stale request resurrected cleared zone: %+v", stale)
	}
}

func TestSyncRewritesHandEditedIncludeFile(t *testing.T) {
	service := newTestService(t, false)
	records := testRecords("10.0.0.5")
	if _, err := syncAgent(t, service.agent, 1, records); err != nil {
		t.Fatal(err)
	}
	includePath := filepath.Join(service.includeDir, "bahia-example-internal.conf")
	desired, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}

	// Hand-edit the Bahia-owned include with a directive the parser ignores.
	rogue := append(append([]byte(nil), desired...), []byte("server=/foo/8.8.8.8\n")...)
	if err := os.WriteFile(includePath, rogue, 0o644); err != nil {
		t.Fatal(err)
	}

	// Next sync with UNCHANGED records must rewrite the file to exactly the
	// desired render, dropping the rogue line.
	result, err := syncAgent(t, service.agent, 2, records)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Status != protocol.SyncStatusOK || result.Serial != 2 {
		t.Fatalf("hand-edited include was not detected as changed: %+v", result)
	}
	after, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(desired) {
		t.Fatalf("include file was not restored to the desired render\nwant:\n%s\ngot:\n%s", desired, after)
	}

	// With the file matching the desired render, a further sync is a no-op.
	calls := *service.reloadCalls
	result, err = syncAgent(t, service.agent, 3, records)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || *service.reloadCalls != calls {
		t.Fatalf("clean include was rewritten: result=%+v reloads=%d", result, *service.reloadCalls)
	}
}

func TestReloadFailureSurfacesAndPreservesPreviousInclude(t *testing.T) {
	service := newTestService(t, false)
	if _, err := syncAgent(t, service.agent, 1, testRecords("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	includePath := filepath.Join(service.includeDir, "bahia-example-internal.conf")
	before, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}
	*service.reloadErr = errors.New("reload failed")
	_, err = syncAgent(t, service.agent, 2, testRecords("10.0.0.2"))
	if err == nil || !strings.Contains(err.Error(), "reload dnsmasq after syncing zone") {
		t.Fatalf("unexpected reload error: %v", err)
	}
	after, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("include file changed after rollback\nbefore:\n%s\nafter:\n%s", before, after)
	}
	listAny, err := service.agent.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: testZone()}))
	if err != nil {
		t.Fatal(err)
	}
	if listAny.(protocol.ListResult).Serial != 1 {
		t.Fatalf("serial advanced after failed reload: %+v", listAny)
	}
}

func TestRequireEncryptionUsesOuterEnvelopeKind(t *testing.T) {
	service := newTestService(t, true)
	request := requestWithParams(t, protocol.HealthParams{Schema: protocol.Schema})
	request.OuterEvent = &nostr.Event{Kind: controlplane.KindContextVMMessage}
	if _, err := service.agent.HealthHandler(context.Background(), request); err == nil || !strings.Contains(err.Error(), "encrypted ContextVM envelope is required") {
		t.Fatalf("bare request was not rejected: %v", err)
	}
	request.OuterEvent = &nostr.Event{Kind: controlplane.KindContextVMGiftWrap}
	if _, err := service.agent.HealthHandler(context.Background(), request); err != nil {
		t.Fatalf("encrypted request rejected: %v", err)
	}
}

func syncRequest(t *testing.T, serial int64, records []domain.DNSRecord, id byte, tags nostr.Tags) controlplane.ContextVMRequest {
	t.Helper()
	request := requestWithParams(t, protocol.SyncParams{Schema: protocol.Schema, Zone: testZone(), Records: records, Serial: serial})
	request.Event = &nostr.Event{ID: nostr.ID{id}, Kind: nostr.Kind(kinds.ContextVMMessage), Tags: tags}
	return request
}

func appliedRecordValue(t *testing.T, agent *Agent) string {
	t.Helper()
	result, err := agent.ListHandler(context.Background(), requestWithParams(t, protocol.ListParams{Schema: protocol.Schema, Zone: testZone()}))
	if err != nil {
		t.Fatal(err)
	}
	records := result.(protocol.ListResult).Records
	if len(records) != 1 {
		t.Fatalf("expected one applied record, got %+v", records)
	}
	return records[0].Value
}

// equal-serial syncs from racing backends converge on the lowest
// request event id, whichever arrives first.
func TestEqualSerialResolvesByLowestRequestID(t *testing.T) {
	low := func(t *testing.T) controlplane.ContextVMRequest {
		return syncRequest(t, 7, testRecords("10.0.0.1"), 0x01, nil)
	}
	high := func(t *testing.T) controlplane.ContextVMRequest {
		return syncRequest(t, 7, testRecords("10.0.0.2"), 0x02, nil)
	}
	for name, order := range map[string][]func(*testing.T) controlplane.ContextVMRequest{"low-first": {low, high}, "high-first": {high, low}} {
		t.Run(name, func(t *testing.T) {
			service := newTestService(t, false)
			for _, request := range order {
				if _, err := service.agent.SyncHandler(context.Background(), request(t)); err != nil {
					t.Fatal(err)
				}
			}
			if got := appliedRecordValue(t, service.agent); got != "10.0.0.1" {
				t.Fatalf("applied %s, want the lowest request id's records 10.0.0.1", got)
			}
		})
	}
}

// an expired request (NIP-40) is never applied.
func TestExpiredRequestIsRejected(t *testing.T) {
	service := newTestService(t, false)
	now := time.Unix(1_800_000_000, 0)
	service.agent.now = func() time.Time { return now }
	expired := syncRequest(t, 3, testRecords("10.0.0.3"), 0x03, nostr.Tags{{"expiration", strconv.FormatInt(now.Unix(), 10)}})
	if _, err := service.agent.SyncHandler(context.Background(), expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired request was not rejected: %v", err)
	}
	if *service.reloadCalls != 0 {
		t.Fatalf("expired request triggered a reload")
	}
	live := syncRequest(t, 3, testRecords("10.0.0.3"), 0x03, nostr.Tags{{"expiration", strconv.FormatInt(now.Add(time.Minute).Unix(), 10)}})
	if _, err := service.agent.SyncHandler(context.Background(), live); err != nil {
		t.Fatalf("unexpired request rejected: %v", err)
	}
}
