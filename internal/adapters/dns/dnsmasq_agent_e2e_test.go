package dns_test

// End-to-end tests wiring the DnsmasqAgentBackend (Bahia side) to the DNS
// agent service core (resolver side) fully in-process. The shim between them
// marshals every request and response through the exact ContextVM JSON-RPC
// wire shapes (cascontextvm.Request / cascontextvm.Response byte round trips),
// so these tests prove wire compatibility of the two halves without relays.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	dnsadapter "github.com/openagentsinc/bahia/internal/adapters/dns"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	dnsagent "github.com/openagentsinc/bahia/internal/dnsagent/agent"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/reconcile"
	bahiaclient "github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

const e2eManagedIncludeHeader = "# Managed by Bahia. Manual changes may be replaced.\n# Zone: sharegap.net\n"

// agentHarness owns one in-process DNS agent with a stub reload runner.
type agentHarness struct {
	agent       *dnsagent.Agent
	includeDir  string
	reloadCalls *int
	reloadErr   *error
}

func newAgentHarness(t *testing.T, allowedZones []string) *agentHarness {
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
	agent, err := dnsagent.New(dnsagent.Config{
		Engine:        eng,
		IncludeDir:    dir,
		FilePrefix:    "bahia-",
		AllowedZones:  allowedZones,
		StateFilePath: filepath.Join(dir, "state", "dns-agent.json"),
	})
	if err != nil {
		t.Fatalf("construct DNS agent: %v", err)
	}
	return &agentHarness{agent: agent, includeDir: dir, reloadCalls: &calls, reloadErr: &runnerErr}
}

func (h *agentHarness) includePath() string {
	return filepath.Join(h.includeDir, "bahia-sharegap-net.conf")
}

// inProcessAgentShim implements dnsadapter.ContextVMRequester by routing every
// request through the serialized ContextVM JSON-RPC wire format into the
// agent's ContextVM handlers, then back through the serialized response.
type inProcessAgentShim struct {
	handlers map[string]controlplane.ContextVMHandler

	mu       sync.Mutex
	lastSync protocol.SyncResult
}

func newInProcessAgentShim(agent *dnsagent.Agent) *inProcessAgentShim {
	return &inProcessAgentShim{handlers: map[string]controlplane.ContextVMHandler{
		protocol.MethodHealth: agent.HealthHandler,
		protocol.MethodList:   agent.ListHandler,
		protocol.MethodSync:   agent.SyncHandler,
	}}
}

func (s *inProcessAgentShim) lastSyncResult() protocol.SyncResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync
}

func (s *inProcessAgentShim) Request(ctx context.Context, method string, params any, _ nostr.Tags, _ func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error) {
	// Bahia side: serialize the request exactly as it is framed on the wire.
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode ContextVM params: %w", err)
	}
	requestWire, err := json.Marshal(controlplane.ContextVMJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"e2e-request"`),
		Method:  method,
		Params:  payload,
	})
	if err != nil {
		return nil, fmt.Errorf("encode ContextVM request: %w", err)
	}

	// Agent side: decode the wire bytes and dispatch to the registered handler.
	var decoded controlplane.ContextVMJSONRPCRequest
	if err := json.Unmarshal(requestWire, &decoded); err != nil {
		return nil, fmt.Errorf("decode ContextVM request wire bytes: %w", err)
	}
	handler, ok := s.handlers[decoded.Method]
	if !ok {
		return nil, fmt.Errorf("no ContextVM handler registered for method %q", decoded.Method)
	}
	result, handlerErr := handler(ctx, controlplane.ContextVMRequest{
		RPC:        decoded,
		OuterEvent: &nostr.Event{Kind: nostr.Kind(controlplane.KindContextVMGiftWrap)},
	})

	// Agent side: serialize the JSON-RPC response onto the wire.
	response := controlplane.ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: decoded.ID}
	if handlerErr != nil {
		response.Error = &controlplane.JSONRPCError{Code: -32000, Message: handlerErr.Error()}
	} else {
		response.Result = result
	}
	responseWire, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode ContextVM response: %w", err)
	}

	// Bahia side: decode the wire bytes and surface result or error exactly as
	// the relay-backed client does (event content = raw JSON-RPC result).
	var parsed struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseWire, &parsed); err != nil {
		return nil, fmt.Errorf("decode ContextVM response wire bytes: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("ContextVM error code %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if decoded.Method == protocol.MethodSync {
		var syncResult protocol.SyncResult
		if err := json.Unmarshal(parsed.Result, &syncResult); err == nil {
			s.mu.Lock()
			s.lastSync = syncResult
			s.mu.Unlock()
		}
	}
	return &nostr.Event{Content: string(parsed.Result)}, nil
}

func (s *inProcessAgentShim) healthResult(t *testing.T) protocol.HealthResult {
	t.Helper()
	event, err := s.Request(context.Background(), protocol.MethodHealth, protocol.HealthParams{Schema: protocol.Schema}, nil, nil)
	if err != nil {
		t.Fatalf("health request through wire shim: %v", err)
	}
	var health protocol.HealthResult
	if err := json.Unmarshal([]byte(event.Content), &health); err != nil {
		t.Fatalf("decode health result: %v", err)
	}
	return health
}

func e2eZone() domain.DNSZone {
	return domain.DNSZone{Name: "sharegap.net", Visibility: domain.ZoneVisibilityInternal, BackendRef: "core-01", TTL: 300}
}

func astilleroRecord() domain.DNSRecord {
	return domain.DNSRecord{
		Zone: "sharegap.net", Name: "astillero", FQDN: "astillero.sharegap.net",
		Type: domain.DNSRecordTypeA, Value: "192.168.40.104", TTL: 300,
	}
}

func newE2EBackend(t *testing.T) (*dnsadapter.DnsmasqAgentBackend, *inProcessAgentShim, *agentHarness) {
	t.Helper()
	harness := newAgentHarness(t, []string{"sharegap.net"})
	shim := newInProcessAgentShim(harness.agent)
	backend, err := dnsadapter.NewDnsmasqAgentBackend(shim)
	if err != nil {
		t.Fatalf("construct dnsmasq agent backend: %v", err)
	}
	return backend, shim, harness
}

func readInclude(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read include file %q: %v", path, err)
	}
	return string(data)
}

func TestDnsmasqAgentBackendEndToEndAstilleroFlow(t *testing.T) {
	ctx := context.Background()
	backend, shim, harness := newE2EBackend(t)
	zone := e2eZone()

	if err := backend.SyncZone(ctx, zone, []domain.DNSRecord{astilleroRecord()}); err != nil {
		t.Fatalf("SyncZone through in-process agent: %v", err)
	}

	wantInclude := e2eManagedIncludeHeader + "address=/astillero.sharegap.net/192.168.40.104\n"
	if got := readInclude(t, harness.includePath()); got != wantInclude {
		t.Fatalf("include file content =\n%q\nwant exactly\n%q", got, wantInclude)
	}
	if *harness.reloadCalls != 1 {
		t.Fatalf("reload calls = %d, want 1", *harness.reloadCalls)
	}

	records, err := backend.ListRecords(ctx, zone)
	if err != nil {
		t.Fatalf("ListRecords through in-process agent: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want exactly one", records)
	}
	got := records[0]
	if got.FQDN != "astillero.sharegap.net" || got.Name != "astillero" || got.Zone != "sharegap.net" ||
		got.Type != domain.DNSRecordTypeA || got.Value != "192.168.40.104" || got.TTL != 300 {
		t.Fatalf("round-tripped record = %#v", got)
	}

	if err := backend.Health(ctx); err != nil {
		t.Fatalf("Health through in-process agent: %v", err)
	}
	syncResult := shim.lastSyncResult()
	if syncResult.Serial <= 0 || !syncResult.Changed {
		t.Fatalf("sync result = %#v, want positive serial and changed=true", syncResult)
	}
	health := shim.healthResult(t)
	if health.LastApplySerial != syncResult.Serial {
		t.Fatalf("health last applied serial = %d, want applied sync serial %d", health.LastApplySerial, syncResult.Serial)
	}
	if len(health.AllowedZones) != 1 || health.AllowedZones[0] != "sharegap.net" {
		t.Fatalf("health allowed zones = %#v, want [sharegap.net]", health.AllowedZones)
	}
}

func TestDnsmasqAgentBackendPreservesForeignIncludeFiles(t *testing.T) {
	ctx := context.Background()
	backend, _, harness := newE2EBackend(t)
	zone := e2eZone()

	foreignPath := filepath.Join(harness.includeDir, "sharegap-splitdns.conf")
	foreignContent := "# Manually managed split-horizon records. Not Bahia's.\naddress=/other.sharegap.net/192.168.40.50\naddress=/printer.sharegap.net/192.168.40.60\n"
	if err := os.WriteFile(foreignPath, []byte(foreignContent), 0o644); err != nil {
		t.Fatalf("seed foreign include file: %v", err)
	}

	if err := backend.SyncZone(ctx, zone, []domain.DNSRecord{astilleroRecord()}); err != nil {
		t.Fatalf("first SyncZone: %v", err)
	}
	changed := astilleroRecord()
	changed.Value = "192.168.40.105"
	if err := backend.SyncZone(ctx, zone, []domain.DNSRecord{changed}); err != nil {
		t.Fatalf("second SyncZone with changed records: %v", err)
	}

	if got := readInclude(t, foreignPath); got != foreignContent {
		t.Fatalf("foreign include file changed:\n%q\nwant byte-identical\n%q", got, foreignContent)
	}
	wantInclude := e2eManagedIncludeHeader + "address=/astillero.sharegap.net/192.168.40.105\n"
	if got := readInclude(t, harness.includePath()); got != wantInclude {
		t.Fatalf("managed include = %q, want %q", got, wantInclude)
	}
}

func TestDnsmasqAgentBackendReloadFailureRollsBackAndRecovers(t *testing.T) {
	ctx := context.Background()
	backend, shim, harness := newE2EBackend(t)
	zone := e2eZone()

	if err := backend.SyncZone(ctx, zone, []domain.DNSRecord{astilleroRecord()}); err != nil {
		t.Fatalf("initial SyncZone: %v", err)
	}
	priorInclude := readInclude(t, harness.includePath())
	priorSerial := shim.lastSyncResult().Serial

	*harness.reloadErr = fmt.Errorf("dnsmasq reload failed")
	changed := astilleroRecord()
	changed.Value = "192.168.40.105"
	err := backend.SyncZone(ctx, zone, []domain.DNSRecord{changed})
	if err == nil || !strings.Contains(err.Error(), "reload dnsmasq after syncing zone") {
		t.Fatalf("failed reload SyncZone error = %v, want reload failure surfaced through backend", err)
	}
	if got := readInclude(t, harness.includePath()); got != priorInclude {
		t.Fatalf("include after failed reload = %q, want prior content preserved %q", got, priorInclude)
	}
	if health := shim.healthResult(t); health.LastApplySerial != priorSerial {
		t.Fatalf("serial after failed reload = %d, want unchanged %d", health.LastApplySerial, priorSerial)
	}

	*harness.reloadErr = nil
	if err := backend.SyncZone(ctx, zone, []domain.DNSRecord{changed}); err != nil {
		t.Fatalf("recovery SyncZone: %v", err)
	}
	wantInclude := e2eManagedIncludeHeader + "address=/astillero.sharegap.net/192.168.40.105\n"
	if got := readInclude(t, harness.includePath()); got != wantInclude {
		t.Fatalf("include after recovery = %q, want %q", got, wantInclude)
	}
	if health := shim.healthResult(t); health.LastApplySerial <= priorSerial {
		t.Fatalf("serial after recovery = %d, want advanced past %d", health.LastApplySerial, priorSerial)
	}
}

func TestDnsmasqAgentBackendAllowlistRejectionSurfaces(t *testing.T) {
	ctx := context.Background()
	backend, _, harness := newE2EBackend(t)
	zone := domain.DNSZone{Name: "evil.example", Visibility: domain.ZoneVisibilityInternal, BackendRef: "core-01", TTL: 300}
	record := domain.DNSRecord{Zone: "evil.example", Name: "www", FQDN: "www.evil.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300}

	err := backend.SyncZone(ctx, zone, []domain.DNSRecord{record})
	if err == nil || !strings.Contains(err.Error(), `not allowed by agent allowlist`) {
		t.Fatalf("SyncZone for disallowed zone error = %v, want allowlist rejection", err)
	}
	if _, err := backend.ListRecords(ctx, zone); err == nil || !strings.Contains(err.Error(), `not allowed by agent allowlist`) {
		t.Fatalf("ListRecords for disallowed zone error = %v, want allowlist rejection", err)
	}
	if entries, globErr := filepath.Glob(filepath.Join(harness.includeDir, "bahia-*.conf")); globErr != nil || len(entries) != 0 {
		t.Fatalf("include files after rejected sync = %v (err=%v), want none", entries, globErr)
	}
}

// reconcileResolverBridge mirrors the app-side bridge from the DNS adapter
// static resolver to the reconcile resolver interface.
type reconcileResolverBridge struct {
	resolver *dnsadapter.StaticResolver
}

func (b reconcileResolverBridge) Resolve(ref string) (reconcile.DNSBackend, bool) {
	backend, ok := b.resolver.Resolve(ref)
	if !ok {
		return nil, false
	}
	return backend, true
}

func TestDNSReconcilerConvergesRemoteAgentInclude(t *testing.T) {
	ctx := context.Background()
	backend, _, harness := newE2EBackend(t)

	staticResolver, err := dnsadapter.NewStaticResolver(dnsadapter.BackendRegistration{Ref: "core-01", Backend: backend})
	if err != nil {
		t.Fatalf("construct static resolver: %v", err)
	}

	serviceID := uuid.New()
	envID := uuid.New()
	projector := reconcile.NewDNSProjector(
		&e2eServiceRepo{services: []domain.Service{{ID: serviceID, Name: "astillero", RuntimeType: domain.RuntimeTypeCompose}}},
		&e2eEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "edge-01-production"}}},
		&e2eStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
		&e2eObservationRepo{latest: &domain.RuntimeObservation{ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "192.168.40.104", HealthStatus: domain.HealthStatusHealthy}},
		nil, nil, nil,
		config.DNSConfig{
			Enabled: true, DefaultTTL: 300,
			Zones:      []config.DNSZoneConfig{{Name: "sharegap.net", Visibility: "internal", Backend: "core-01", TTL: 300}},
			Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"edge-01-production": "sharegap.net"}},
		},
		zap.NewNop(),
	)
	reconciler := reconcile.NewDNSReconciler(projector, []domain.DNSZone{e2eZone()}, reconcileResolverBridge{resolver: staticResolver}, 0, zap.NewNop())

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first ReconcileOnce: %v", err)
	}
	wantInclude := e2eManagedIncludeHeader + "address=/astillero.sharegap.net/192.168.40.104\n"
	if got := readInclude(t, harness.includePath()); got != wantInclude {
		t.Fatalf("include after reconcile = %q, want %q", got, wantInclude)
	}
	if *harness.reloadCalls != 1 {
		t.Fatalf("reload calls after first reconcile = %d, want 1", *harness.reloadCalls)
	}

	// The periodic path is convergent: a second reconcile over the same
	// projected state must not rewrite or reload the remote include.
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second ReconcileOnce: %v", err)
	}
	if got := readInclude(t, harness.includePath()); got != wantInclude {
		t.Fatalf("include after second reconcile = %q, want unchanged %q", got, wantInclude)
	}
	if *harness.reloadCalls != 1 {
		t.Fatalf("reload calls after second reconcile = %d, want still 1", *harness.reloadCalls)
	}
}

type e2eServiceRepo struct{ services []domain.Service }

func (r *e2eServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *e2eServiceRepo) GetByID(context.Context, uuid.UUID) (*domain.Service, error) {
	return nil, nil
}
func (r *e2eServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *e2eServiceRepo) List(context.Context) ([]domain.Service, error) {
	return append([]domain.Service(nil), r.services...), nil
}
func (r *e2eServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *e2eServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *e2eServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type e2eEnvironmentRepo struct{ environments []domain.Environment }

func (r *e2eEnvironmentRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *e2eEnvironmentRepo) GetByID(context.Context, uuid.UUID) (*domain.Environment, error) {
	return nil, nil
}
func (r *e2eEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r *e2eEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	return append([]domain.Environment(nil), r.environments...), nil
}
func (r *e2eEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *e2eEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *e2eEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type e2eStateRepo struct{ states []domain.EnvironmentServiceState }

func (r *e2eStateRepo) Upsert(context.Context, *domain.EnvironmentServiceState) error { return nil }
func (r *e2eStateRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *e2eStateRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *e2eStateRepo) ListByService(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *e2eStateRepo) ListDrifted(context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *e2eStateRepo) ListDueForObservation(context.Context, time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *e2eStateRepo) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return append([]domain.EnvironmentServiceState(nil), r.states...), nil
}

type e2eObservationRepo struct{ latest *domain.RuntimeObservation }

func (r *e2eObservationRepo) Create(context.Context, *domain.RuntimeObservation) error { return nil }
func (r *e2eObservationRepo) GetLatest(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.latest, nil
}
func (r *e2eObservationRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}
