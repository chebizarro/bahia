package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// bahia-irsry.35: service/environment create intents carry a client-minted id
// that becomes the relay coordinate, and retries are resolved by content.

type identityRelayPublisher struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (p *identityRelayPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return 1, nil
}

func (p *identityRelayPublisher) coordinates() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.events))
	for _, ev := range p.events {
		if tag := ev.Tags.Find("d"); len(tag) >= 2 {
			out = append(out, tag[1])
		}
	}
	return out
}

type identityServiceRepo struct {
	mu       sync.Mutex
	services map[uuid.UUID]domain.Service
}

func (r *identityServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.services == nil {
		r.services = map[uuid.UUID]domain.Service{}
	}
	r.services[svc.ID] = *svc
	return nil
}
func (r *identityServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	svc, ok := r.services[id]
	if !ok {
		return nil, nil
	}
	return &svc, nil
}
func (r *identityServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *identityServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *identityServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *identityServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *identityServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type identityEnvironmentRepo struct {
	mu           sync.Mutex
	environments map[uuid.UUID]domain.Environment
}

func (r *identityEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.environments == nil {
		r.environments = map[uuid.UUID]domain.Environment{}
	}
	r.environments[env.ID] = *env
	return nil
}
func (r *identityEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	env, ok := r.environments[id]
	if !ok {
		return nil, nil
	}
	return &env, nil
}
func (r *identityEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r *identityEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	return nil, nil
}
func (r *identityEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *identityEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *identityEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type identityHarness struct {
	transport *EncryptedRequestTransport
	responses *mockEncryptedPublisher
	relay     *identityRelayPublisher
	services  *identityServiceRepo
	envs      *identityEnvironmentRepo
	seq       int
}

func newIdentityHarness(t *testing.T, orgID uuid.UUID) *identityHarness {
	t.Helper()
	services := &identityServiceRepo{}
	envs := &identityEnvironmentRepo{}
	delegate := service.NewRegistryService(services, envs, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	relay := &identityRelayPublisher{}
	// The production relay-first writer: the projector's record builders and
	// coordinate state over the fake relay.
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: nostr.Generate().Hex()}, nil, nil, nil, zap.NewNop())
	registry := service.NewRelayFirstRegistry(delegate, nostrpool.NewRelayFirstStatePublisher(projector, relay), zap.NewNop())
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, responses := encryptedRouteTransport(t, h)
	return &identityHarness{transport: transport, responses: responses, relay: relay, services: services, envs: envs}
}

// send publishes a freshly signed request with its own JSON-RPC id, as a
// client retry does, so the transport's response cache cannot answer it.
func (h *identityHarness) send(t *testing.T, method string, params map[string]any) ContextVMJSONRPCResponse {
	t.Helper()
	h.seq++
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	request := ContextVMJSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf(`"identity-%d"`, h.seq)), Method: method, Params: raw}
	content, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	h.transport.HandleEvent(context.Background(), makeContextVMEvent(t, testRequesterKey, string(content)))
	return contextVMResponse(t, h.responses.events[len(h.responses.events)-1])
}

func resultField(t *testing.T, response ContextVMJSONRPCResponse, key string) string {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected ContextVM error: %+v", response.Error)
	}
	payload, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", response.Result)
	}
	value, _ := payload[key].(string)
	return value
}

func TestContextVMServiceCreateClientIDRoundTripIdempotencyAndConflict(t *testing.T) {
	orgID := uuid.New()
	h := newIdentityHarness(t, orgID)
	clientID := domain.NewEntityID().String()
	intent := map[string]any{"id": clientID, "org_id": orgID.String(), "name": "payments-api", "artifact_repo": "registry.example/payments"}

	// Client-supplied id round-trips from the create intent to the relay coordinate.
	first := h.send(t, ContextVMMethodServiceCreate, intent)
	if got := resultField(t, first, "service_id"); got != clientID {
		t.Fatalf("service_id = %q, want client id %q", got, clientID)
	}
	if coords := h.relay.coordinates(); len(coords) != 1 || coords[0] != clientID {
		t.Fatalf("relay coordinates = %v, want [%s]", coords, clientID)
	}

	// A retried create with the same id and content is idempotent.
	retry := h.send(t, ContextVMMethodServiceCreate, intent)
	if got := resultField(t, retry, "service_id"); got != clientID || resultField(t, retry, "status") != "created" {
		t.Fatalf("retry result service_id=%q status=%q", got, resultField(t, retry, "status"))
	}
	if len(h.relay.coordinates()) != 1 || len(h.services.services) != 1 {
		t.Fatalf("retry wrote again: relay=%d stored=%d", len(h.relay.coordinates()), len(h.services.services))
	}

	// Same id, different content is rejected with a dedicated code.
	conflicting := map[string]any{"id": clientID, "org_id": orgID.String(), "name": "payments-api", "artifact_repo": "registry.example/other"}
	rejected := h.send(t, ContextVMMethodServiceCreate, conflicting)
	if rejected.Error == nil || rejected.Error.Code != ContextVMEntityIDConflictErrorCode || !strings.Contains(rejected.Error.Message, "already exists with different content") {
		t.Fatalf("conflicting create response = %+v, want code %d", rejected.Error, ContextVMEntityIDConflictErrorCode)
	}
	if len(h.relay.coordinates()) != 1 || h.services.services[uuid.MustParse(clientID)].ArtifactRepo != "registry.example/payments" {
		t.Fatal("conflicting create mutated relay or stored state")
	}

	// An absent id is still minted (UUIDv7) and used as the coordinate.
	minted := h.send(t, ContextVMMethodServiceCreate, map[string]any{"org_id": orgID.String(), "name": "billing", "artifact_repo": "registry.example/billing"})
	mintedID, err := uuid.Parse(resultField(t, minted, "service_id"))
	if err != nil || mintedID.Version() != 7 {
		t.Fatalf("minted service_id = %s (%v), want a UUIDv7", mintedID, err)
	}
	if coords := h.relay.coordinates(); coords[len(coords)-1] != mintedID.String() {
		t.Fatalf("minted coordinate = %q, want %s", coords[len(coords)-1], mintedID)
	}
}

func TestContextVMServiceCreateRejectsMalformedClientID(t *testing.T) {
	orgID := uuid.New()
	h := newIdentityHarness(t, orgID)
	for _, bad := range []string{"service:acme:payments", strings.ToUpper(domain.NewEntityID().String()), uuid.NewSHA1(uuid.NameSpaceURL, []byte("x")).String()} {
		response := h.send(t, ContextVMMethodServiceCreate, map[string]any{"id": bad, "org_id": orgID.String(), "name": "payments-api", "artifact_repo": "registry.example/payments"})
		if response.Error == nil || !strings.Contains(response.Error.Message, "invalid id") {
			t.Fatalf("id %q response = %+v, want invalid id error", bad, response.Error)
		}
	}
	if len(h.relay.coordinates()) != 0 {
		t.Fatalf("malformed id published: %v", h.relay.coordinates())
	}
}

func TestContextVMEnvironmentCreateClientIDRoundTripIdempotencyAndConflict(t *testing.T) {
	orgID := uuid.New()
	h := newIdentityHarness(t, orgID)
	clientID := domain.NewEntityID().String()
	intent := map[string]any{"id": clientID, "org_id": orgID.String(), "name": "staging", "runtime_config": map[string]any{"type": "compose"}, "deploy_strategy": "blue_green"}

	first := h.send(t, ContextVMMethodEnvironmentCreate, intent)
	if got := resultField(t, first, "environment_id"); got != clientID {
		t.Fatalf("environment_id = %q, want %q", got, clientID)
	}
	if coords := h.relay.coordinates(); len(coords) != 1 || coords[0] != clientID {
		t.Fatalf("relay coordinates = %v, want [%s]", coords, clientID)
	}
	if got := resultField(t, h.send(t, ContextVMMethodEnvironmentCreate, intent), "environment_id"); got != clientID {
		t.Fatalf("retry environment_id = %q", got)
	}
	if len(h.relay.coordinates()) != 1 || len(h.envs.environments) != 1 {
		t.Fatalf("retry wrote again: relay=%d stored=%d", len(h.relay.coordinates()), len(h.envs.environments))
	}
	intent["protected"] = true
	rejected := h.send(t, ContextVMMethodEnvironmentCreate, intent)
	if rejected.Error == nil || rejected.Error.Code != ContextVMEntityIDConflictErrorCode {
		t.Fatalf("conflicting environment create = %+v, want code %d", rejected.Error, ContextVMEntityIDConflictErrorCode)
	}
	minted := h.send(t, ContextVMMethodEnvironmentCreate, map[string]any{"org_id": orgID.String(), "name": "prod"})
	if id, err := uuid.Parse(resultField(t, minted, "environment_id")); err != nil || id.Version() != 7 {
		t.Fatalf("minted environment_id = %s (%v), want UUIDv7", id, err)
	}
}
