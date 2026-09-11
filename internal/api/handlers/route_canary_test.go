package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// routeCanaryReaderFake stores route state keyed the way production does: every
// row carries the deployment unit of the plan that produced it.
type routeCanaryReaderFake struct {
	states    []domain.RouteCanaryState
	events    map[string][]domain.RouteCanaryEvent
	eventKeys []domain.RouteCanaryKey
	getKeys   []domain.RouteCanaryKey
}

func (f *routeCanaryReaderFake) ListState(context.Context) ([]domain.RouteCanaryState, error) {
	return append([]domain.RouteCanaryState(nil), f.states...), nil
}

func (f *routeCanaryReaderFake) ListStateByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.RouteCanaryState, error) {
	var out []domain.RouteCanaryState
	for _, state := range f.states {
		if state.EnvironmentID == environmentID {
			out = append(out, state)
		}
	}
	return out, nil
}

func (f *routeCanaryReaderFake) GetState(_ context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	f.getKeys = append(f.getKeys, key)
	for _, state := range f.states {
		if state.Coordinate() == key.Coordinate() {
			found := state
			return &found, nil
		}
	}
	return nil, nil
}

func (f *routeCanaryReaderFake) ListRecentEvents(_ context.Context, key domain.RouteCanaryKey, limit int) ([]domain.RouteCanaryEvent, error) {
	f.eventKeys = append(f.eventKeys, key)
	events := f.events[key.Coordinate()]
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

type routeHealthFake struct{ status domain.InstanceHealthStatus }

func (f routeHealthFake) InstanceStatusForRoute(context.Context, domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool) {
	return f.status, f.status != ""
}

var (
	routeTestService = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	routeTestEnv     = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	routeTestUnitA   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	routeTestUnitB   = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

func routeTestState(unit uuid.UUID, hostname string, open bool) domain.RouteCanaryState {
	u := unit
	state := domain.RouteCanaryState{
		RouteCanaryKey: domain.RouteCanaryKey{ServiceID: routeTestService, EnvironmentID: routeTestEnv, DeploymentUnitID: &u, Hostname: hostname},
		Open:           open,
		Classification: domain.RouteCanaryClassificationRouteOK,
		Perspective:    domain.RouteCanaryPerspectivePublicEdge,
		LastObservedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
	if open {
		state.Classification = domain.RouteCanaryClassificationUpstreamError
		state.FailureReason = "upstream returned HTTP 502"
	}
	return state
}

// routeCanaryDetailRequest builds a detail request with chi URL params, exactly
// as the router dispatches /services/{serviceId}/environments/{envId}/routes/{hostname}/canary.
func routeCanaryDetailRequest(serviceID, environmentID uuid.UUID, hostname, query string) *http.Request {
	target := "/canary"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("serviceId", serviceID.String())
	rctx.URLParams.Add("envId", environmentID.String())
	rctx.URLParams.Add("hostname", hostname)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// decodeEnvelope decodes the body the way the web client does: the resource is
// whatever sits under the top-level "data" key.
func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not a JSON object: %v body=%s", err, w.Body.String())
	}
	if _, ok := envelope["data"]; !ok {
		t.Fatalf("response has no data envelope (web client would read undefined): %s", w.Body.String())
	}
	return envelope
}

func TestRouteCanaryGetWithoutUnitResolvesUniqueRouteInDataEnvelope(t *testing.T) {
	repo := &routeCanaryReaderFake{states: []domain.RouteCanaryState{
		routeTestState(routeTestUnitA, "git.example.test", true),
		// Same service, other hostname; same hostname, other service. Neither
		// may make the lookup ambiguous.
		routeTestState(routeTestUnitB, "other.example.test", false),
		func() domain.RouteCanaryState {
			s := routeTestState(routeTestUnitB, "git.example.test", false)
			s.ServiceID = uuid.New()
			return s
		}(),
	}}
	h := NewRouteCanaryHandler(repo, routeHealthFake{status: domain.InstanceHealthStatusHealthy})
	w := httptest.NewRecorder()

	// The hostname is normalized the same way stored keys are.
	h.Get(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "GIT.Example.test.", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	envelope := decodeEnvelope(t, w)
	if _, leaked := envelope["hostname"]; leaked {
		t.Fatalf("detail was written unwrapped: %s", w.Body.String())
	}
	var detail struct {
		ServiceID                 uuid.UUID                        `json:"service_id"`
		EnvironmentID             uuid.UUID                        `json:"environment_id"`
		DeploymentUnitID          *uuid.UUID                       `json:"deployment_unit_id"`
		Hostname                  string                           `json:"hostname"`
		Open                      bool                             `json:"open"`
		Classification            domain.RouteCanaryClassification `json:"classification"`
		ObservedInstanceStatus    domain.InstanceHealthStatus      `json:"observed_instance_status"`
		ServiceHealthyRouteBroken bool                             `json:"service_healthy_route_broken"`
	}
	if err := json.Unmarshal(envelope["data"], &detail); err != nil {
		t.Fatalf("data is not a route canary object: %v body=%s", err, w.Body.String())
	}
	if detail.ServiceID != routeTestService || detail.EnvironmentID != routeTestEnv || detail.Hostname != "git.example.test" {
		t.Fatalf("resolved the wrong route: %+v", detail)
	}
	if detail.DeploymentUnitID == nil || *detail.DeploymentUnitID != routeTestUnitA {
		t.Fatalf("response does not name the resolved deployment unit: %+v", detail)
	}
	if !detail.Open || detail.Classification != domain.RouteCanaryClassificationUpstreamError {
		t.Fatalf("unexpected state: %+v", detail)
	}
	if detail.ObservedInstanceStatus != domain.InstanceHealthStatusHealthy || !detail.ServiceHealthyRouteBroken {
		t.Fatalf("container contrast lost: %+v", detail)
	}
}

func TestRouteCanaryGetWithoutUnitIsConflictWhenSeveralUnitsMatch(t *testing.T) {
	repo := &routeCanaryReaderFake{states: []domain.RouteCanaryState{
		routeTestState(routeTestUnitB, "git.example.test", false),
		routeTestState(routeTestUnitA, "git.example.test", true),
	}}
	h := NewRouteCanaryHandler(repo, nil)
	w := httptest.NewRecorder()

	h.Get(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", ""))

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data != nil {
		t.Fatalf("an ambiguous lookup must not return one of the routes: %s", w.Body.String())
	}
	for _, want := range []string{"deployment_unit_id", routeTestUnitA.String(), routeTestUnitB.String()} {
		if !strings.Contains(body.Error, want) {
			t.Fatalf("conflict error %q does not mention %q", body.Error, want)
		}
	}
}

func TestRouteCanaryGetWithoutUnitIsNotFoundWhenNoRouteMatches(t *testing.T) {
	repo := &routeCanaryReaderFake{states: []domain.RouteCanaryState{routeTestState(routeTestUnitA, "git.example.test", true)}}
	h := NewRouteCanaryHandler(repo, nil)

	for name, req := range map[string]*http.Request{
		"other hostname":    routeCanaryDetailRequest(routeTestService, routeTestEnv, "missing.example.test", ""),
		"other service":     routeCanaryDetailRequest(uuid.New(), routeTestEnv, "git.example.test", ""),
		"other environment": routeCanaryDetailRequest(routeTestService, uuid.New(), "git.example.test", ""),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.Get(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRouteCanaryGetWithExplicitUnitAddressesThatUnit(t *testing.T) {
	repo := &routeCanaryReaderFake{states: []domain.RouteCanaryState{
		routeTestState(routeTestUnitA, "git.example.test", true),
		routeTestState(routeTestUnitB, "git.example.test", false),
	}}
	h := NewRouteCanaryHandler(repo, nil)

	w := httptest.NewRecorder()
	h.Get(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "deployment_unit_id="+routeTestUnitB.String()))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var detail domain.RouteCanaryState
	if err := json.Unmarshal(decodeEnvelope(t, w)["data"], &detail); err != nil {
		t.Fatal(err)
	}
	if detail.DeploymentUnitID == nil || *detail.DeploymentUnitID != routeTestUnitB || detail.Open {
		t.Fatalf("explicit unit resolved the wrong route: %+v", detail)
	}

	w = httptest.NewRecorder()
	h.Get(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "deployment_unit_id="+uuid.NewString()))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown unit status=%d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.Get(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "deployment_unit_id=not-a-uuid"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid unit status=%d, want 400", w.Code)
	}
}

func TestRouteCanaryEventsWithoutUnitUseTheResolvedRouteKey(t *testing.T) {
	state := routeTestState(routeTestUnitA, "git.example.test", true)
	opened := domain.RouteCanaryEvent{
		ID:             uuid.New(),
		RouteCanaryKey: state.RouteCanaryKey,
		Transition:     domain.RouteCanaryTransitionOpened,
		Classification: domain.RouteCanaryClassificationUpstreamError,
		ObservedAt:     state.LastObservedAt,
	}
	repo := &routeCanaryReaderFake{
		states: []domain.RouteCanaryState{state},
		events: map[string][]domain.RouteCanaryEvent{state.Coordinate(): {opened}},
	}
	h := NewRouteCanaryHandler(repo, nil)
	w := httptest.NewRecorder()

	h.ListEvents(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "limit=10"))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(repo.eventKeys) != 1 || repo.eventKeys[0].Coordinate() != state.Coordinate() {
		t.Fatalf("events were read under the wrong key: %+v", repo.eventKeys)
	}
	envelope := decodeEnvelope(t, w)
	var events []domain.RouteCanaryEvent
	if err := json.Unmarshal(envelope["data"], &events); err != nil {
		t.Fatalf("data is not an event array: %v body=%s", err, w.Body.String())
	}
	if len(events) != 1 || events[0].ID != opened.ID || events[0].Transition != domain.RouteCanaryTransitionOpened {
		t.Fatalf("unexpected lineage: %+v", events)
	}
	var limit int
	if err := json.Unmarshal(envelope["limit"], &limit); err != nil || limit != 10 {
		t.Fatalf("limit=%d err=%v", limit, err)
	}
}

func TestRouteCanaryEventsResolutionFailuresAndEmptyLineage(t *testing.T) {
	ambiguous := &routeCanaryReaderFake{states: []domain.RouteCanaryState{
		routeTestState(routeTestUnitA, "git.example.test", true),
		routeTestState(routeTestUnitB, "git.example.test", false),
	}}
	h := NewRouteCanaryHandler(ambiguous, nil)

	w := httptest.NewRecorder()
	h.ListEvents(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", ""))
	if w.Code != http.StatusConflict || len(ambiguous.eventKeys) != 0 {
		t.Fatalf("ambiguous status=%d reads=%d, want 409 and no read; body=%s", w.Code, len(ambiguous.eventKeys), w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ListEvents(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "missing.example.test", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.ListEvents(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "limit=0"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d, want 400 before resolution", w.Code)
	}

	// An explicit unit selects that route's lineage, and a route with no lineage
	// yields an empty array rather than null.
	w = httptest.NewRecorder()
	h.ListEvents(w, routeCanaryDetailRequest(routeTestService, routeTestEnv, "git.example.test", "deployment_unit_id="+routeTestUnitB.String()))
	if w.Code != http.StatusOK {
		t.Fatalf("explicit unit status=%d body=%s", w.Code, w.Body.String())
	}
	if got := string(decodeEnvelope(t, w)["data"]); got != "[]" {
		t.Fatalf("empty lineage data = %s, want []", got)
	}
	last := ambiguous.eventKeys[len(ambiguous.eventKeys)-1]
	if last.DeploymentUnitID == nil || *last.DeploymentUnitID != routeTestUnitB {
		t.Fatalf("explicit unit not used for lineage: %+v", last)
	}
}
