package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
)

// CanonicalRuntimeSource reads only signed, service-authored local cp-state.
// PostgreSQL is an optional write-behind index, never a fallback read source.
type CanonicalRuntimeSource struct {
	store  *localstore.Store
	author gonostr.PubKey
}

func NewCanonicalRuntimeSource(store *localstore.Store, servicePubkey string) (*CanonicalRuntimeSource, error) {
	if store == nil {
		return nil, fmt.Errorf("canonical runtime event store is required")
	}
	author, err := gonostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("canonical runtime service pubkey: %w", err)
	}
	return &CanonicalRuntimeSource{store: store, author: author}, nil
}

func (s *CanonicalRuntimeSource) records(ctx context.Context, legacyKind int, coordinate string) ([]gonostr.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	family, ok := canonicalRuntimeTopics[legacyKind]
	if !ok {
		return nil, fmt.Errorf("unknown canonical runtime family %d", legacyKind)
	}
	filter := gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Authors: []gonostr.PubKey{s.author}, Tags: gonostr.TagMap{"t": {family}}}
	if coordinate != "" {
		filter.Tags["d"] = []string{coordinate}
	}
	var out []gonostr.Event
	for ev := range s.store.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != s.author || !ev.CheckID() || !ev.VerifySignature() ||
			tagValue(ev.Tags, "schema") != kinds.CASControlStateSchema ||
			tagValue(ev.Tags, "legacy_kind") != strconv.Itoa(legacyKind) ||
			tagValue(ev.Tags, "t") != family || tagValue(ev.Tags, "d") == "" {
			continue
		}
		if expires := nostrutil.ExpiresAt(&ev); expires > 0 && expires <= gonostr.Timestamp(time.Now().Unix()) {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

var canonicalRuntimeTopics = map[int]string{
	kinds.ServiceRegistry:               kinds.CPStateTopicServiceRegistry,
	kinds.EnvironmentRegistry:           kinds.CPStateTopicEnvironmentRegistry,
	kinds.ServiceState:                  kinds.CPStateTopicServiceState,
	kinds.ArtifactRegistry:              kinds.CPStateTopicArtifactRegistry,
	kinds.DeploymentIntentRegistry:      kinds.CPStateTopicDeploymentIntent,
	kinds.DeploymentRunRegistry:         kinds.CPStateTopicDeploymentRun,
	int(kinds.CPStateFamilyWorkerState): kinds.WorkerStateTopic,
	kinds.LLMRouteRegistry:              kinds.CPStateTopicLLMRoute,
	kinds.LLMRouteState:                 kinds.CPStateTopicLLMState,
	kinds.MLInferenceEndpointRegistry:   kinds.CPStateTopicMLEndpoint,
	kinds.MLInferenceEndpointState:      kinds.CPStateTopicMLEndpointState,
}

func tagValue(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func signedTagMatchesIfPresent(tags gonostr.Tags, key, expected string) bool {
	value := tagValue(tags, key)
	return value == "" || value == expected
}

type canonicalRecord[T any] struct {
	Value   T
	Deleted bool
}

func decodeCanonical[T any](ev gonostr.Event, expectedD string) (canonicalRecord[T], bool) {
	var wire struct {
		Deleted bool `json:"deleted"`
	}
	var value T
	if tagValue(ev.Tags, "d") != expectedD || json.Unmarshal([]byte(ev.Content), &wire) != nil ||
		json.Unmarshal([]byte(ev.Content), &value) != nil ||
		tagValue(ev.Tags, "deleted") != strconv.FormatBool(wire.Deleted) {
		return canonicalRecord[T]{}, false
	}
	return canonicalRecord[T]{Value: value, Deleted: wire.Deleted}, true
}

func (s *CanonicalRuntimeSource) listServices(ctx context.Context) ([]domain.Service, error) {
	events, err := s.records(ctx, kinds.ServiceRegistry, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Service, 0, len(events))
	for _, ev := range events {
		var head struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.ID == uuid.Nil {
			continue
		}
		rec, ok := decodeCanonical[domain.Service](ev, head.ID.String())
		if ok && !rec.Deleted && rec.Value.ID == head.ID {
			out = append(out, rec.Value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (s *CanonicalRuntimeSource) service(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	events, err := s.records(ctx, kinds.ServiceRegistry, id.String())
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[domain.Service](ev, id.String())
		if ok && rec.Value.ID == id {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

type canonicalEnvironment struct {
	domain.Environment
	DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
}

func (s *CanonicalRuntimeSource) listEnvironments(ctx context.Context) ([]domain.Environment, error) {
	events, err := s.records(ctx, kinds.EnvironmentRegistry, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Environment, 0, len(events))
	for _, ev := range events {
		var head struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.ID == uuid.Nil {
			continue
		}
		rec, ok := decodeCanonical[domain.Environment](ev, head.ID.String())
		if ok && !rec.Deleted && rec.Value.ID == head.ID {
			out = append(out, rec.Value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (s *CanonicalRuntimeSource) environment(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	events, err := s.records(ctx, kinds.EnvironmentRegistry, id.String())
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[domain.Environment](ev, id.String())
		if ok && rec.Value.ID == id {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) unit(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	events, err := s.records(ctx, kinds.EnvironmentRegistry, "")
	if err != nil {
		return nil, err
	}
	var found *domain.DeploymentUnit
	for _, ev := range events {
		var head struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.ID == uuid.Nil {
			continue
		}
		rec, ok := decodeCanonical[canonicalEnvironment](ev, head.ID.String())
		if !ok || rec.Deleted || rec.Value.ID != head.ID {
			continue
		}
		for _, unit := range rec.Value.DeploymentUnits {
			if unit.ID == id {
				if unit.EnvironmentID != uuid.Nil && unit.EnvironmentID != head.ID {
					return nil, fmt.Errorf("canonical deployment unit %s declares a different environment than its signed registry coordinate", id)
				}
				if found != nil {
					return nil, fmt.Errorf("canonical deployment unit %s is present more than once in signed environment state", id)
				}
				unit.EnvironmentID = head.ID
				found = &unit
			}
		}
	}
	return found, nil
}

type canonicalState struct {
	domain.EnvironmentServiceState
	ObservationID       uuid.UUID                     `json:"observation_id"`
	ObservedHash        string                        `json:"observed_hash"`
	ObservedHost        string                        `json:"observed_host"`
	ObservedContainerID string                        `json:"observed_container_id"`
	ObservedImageRepo   string                        `json:"observed_image_repo"`
	ObservedVersion     string                        `json:"observed_version"`
	ObservedImageDigest string                        `json:"observed_image_digest"`
	NormalizedState     *domain.NormalizedObservation `json:"normalized_state"`
	HealthStatus        domain.HealthStatus           `json:"health_status"`
	ObservationSource   string                        `json:"observation_source"`
	ObservedAt          time.Time                     `json:"observed_at"`
}

func validateCanonicalDesiredState(state *domain.EnvironmentServiceState) error {
	spec := state.DesiredRuntimeState
	if spec == nil {
		return nil
	}
	if spec.ServiceID != state.ServiceID || spec.EnvironmentID != state.EnvironmentID ||
		state.DesiredArtifactID == nil || spec.ArtifactID != *state.DesiredArtifactID ||
		(spec.DeploymentUnitID == nil) != (state.DeploymentUnitID == nil) ||
		(spec.DeploymentUnitID != nil && *spec.DeploymentUnitID != *state.DeploymentUnitID) {
		return fmt.Errorf("canonical desired runtime state identity differs from service state")
	}
	if state.DesiredHash == "" || spec.DesiredHash != state.DesiredHash {
		return fmt.Errorf("canonical desired runtime state hash differs from service state")
	}
	// ComputeDesiredHash normalizes/sorts fields in place. Copy the signed body
	// before recomputing so validation never changes the projected state.
	wire, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode canonical desired runtime state: %w", err)
	}
	var copy domain.DesiredServiceSpec
	if err := json.Unmarshal(wire, &copy); err != nil {
		return fmt.Errorf("decode canonical desired runtime state: %w", err)
	}
	if copy.ComputeDesiredHash() != state.DesiredHash {
		return fmt.Errorf("canonical desired runtime state body does not match its hash")
	}
	return nil
}

func (s *CanonicalRuntimeSource) states(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	events, err := s.records(ctx, kinds.ServiceState, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.EnvironmentServiceState, 0, len(events))
	for _, ev := range events {
		var head struct {
			ServiceID     uuid.UUID `json:"service_id"`
			EnvironmentID uuid.UUID `json:"environment_id"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.ServiceID == uuid.Nil || head.EnvironmentID == uuid.Nil {
			continue
		}
		d := kinds.ServiceStateDTag(head.ServiceID.String(), head.EnvironmentID.String())
		rec, ok := decodeCanonical[canonicalState](ev, d)
		if ok && !rec.Deleted && rec.Value.ServiceID == head.ServiceID && rec.Value.EnvironmentID == head.EnvironmentID {
			if err := validateCanonicalDesiredState(&rec.Value.EnvironmentServiceState); err != nil {
				return nil, fmt.Errorf("reading canonical service state %s: %w", d, err)
			}
			out = append(out, rec.Value.EnvironmentServiceState)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ServiceID.String()+out[i].EnvironmentID.String() < out[j].ServiceID.String()+out[j].EnvironmentID.String()
	})
	return out, nil
}

func (s *CanonicalRuntimeSource) state(ctx context.Context, serviceID, envID uuid.UUID) (*canonicalState, error) {
	d := kinds.ServiceStateDTag(serviceID.String(), envID.String())
	events, err := s.records(ctx, kinds.ServiceState, d)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[canonicalState](ev, d)
		if ok && rec.Value.ServiceID == serviceID && rec.Value.EnvironmentID == envID {
			if rec.Deleted {
				return nil, nil
			}
			if err := validateCanonicalDesiredState(&rec.Value.EnvironmentServiceState); err != nil {
				return nil, fmt.Errorf("reading canonical service state %s: %w", d, err)
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) observation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	state, err := s.state(ctx, serviceID, envID)
	if err != nil || state == nil {
		return nil, err
	}
	id := state.ObservationID
	if id == uuid.Nil && state.CurrentObservationID != nil {
		id = *state.CurrentObservationID
	}
	if id == uuid.Nil || state.HealthStatus == "" {
		return nil, nil
	}
	return &domain.RuntimeObservation{ID: id, ServiceID: serviceID, EnvironmentID: envID,
		DeploymentUnitID: state.DeploymentUnitID, ObservedImageDigest: state.ObservedImageDigest,
		ObservedContainerID: state.ObservedContainerID, ObservedHost: state.ObservedHost,
		ObservedImageRepo: state.ObservedImageRepo, ObservedVersion: state.ObservedVersion,
		NormalizedState: state.NormalizedState,
		HealthStatus:    state.HealthStatus, Source: state.ObservationSource,
		NormalizedHash: state.ObservedHash, ObservedAt: state.ObservedAt}, nil
}

// List is the DNS worker read surface. Worker state is signed by this daemon
// and retained in the same validated local store as runtime desired state.
func (s *CanonicalRuntimeSource) List(ctx context.Context, status string, limit int) ([]domain.Worker, error) {
	events, err := s.records(ctx, int(kinds.CPStateFamilyWorkerState), "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Worker, 0, len(events))
	for _, ev := range events {
		var head struct {
			PubKey string `json:"pubkey"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.PubKey == "" {
			continue
		}
		d, _ := kinds.CPStateFamilyWorkerState.WorkerDTag(head.PubKey)
		rec, ok := decodeCanonical[domain.Worker](ev, d)
		if ok && !rec.Deleted && rec.Value.PubKey == head.PubKey && (status == "" || string(rec.Value.Status) == status) {
			out = append(out, rec.Value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PubKey < out[j].PubKey })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *CanonicalRuntimeSource) GetRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	events, err := s.records(ctx, kinds.LLMRouteRegistry, id.String())
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[domain.LLMRoute](ev, id.String())
		if ok && rec.Value.ID == id {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) ListAllRouteStates(ctx context.Context) ([]domain.LLMRouteState, error) {
	events, err := s.records(ctx, kinds.LLMRouteState, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.LLMRouteState, 0, len(events))
	for _, ev := range events {
		var state domain.LLMRouteState
		if json.Unmarshal([]byte(ev.Content), &state) != nil || state.RouteID == uuid.Nil || state.EnvironmentID == uuid.Nil {
			continue
		}
		d := state.RouteID.String() + ":" + state.EnvironmentID.String()
		rec, ok := decodeCanonical[domain.LLMRouteState](ev, d)
		if ok && !rec.Deleted {
			out = append(out, rec.Value)
		}
	}
	return out, nil
}

func (s *CanonicalRuntimeSource) GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	events, err := s.records(ctx, kinds.MLInferenceEndpointRegistry, "")
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		var endpoint domain.MLInferenceEndpoint
		if json.Unmarshal([]byte(ev.Content), &endpoint) != nil || endpoint.ID != id {
			continue
		}
		// The environment may have been renamed since this event was signed.
		// Validate the event's own immutable coordinate and content identity;
		// never reinterpret it using a mutable current registry name.
		d := tagValue(ev.Tags, "d")
		if endpoint.EnvironmentID == uuid.Nil || endpoint.Name == "" ||
			!strings.HasPrefix(d, "endpoint:"+endpoint.Name+":") ||
			strings.TrimPrefix(d, "endpoint:"+endpoint.Name+":") == "" ||
			!signedTagMatchesIfPresent(ev.Tags, "endpoint", d) ||
			!signedTagMatchesIfPresent(ev.Tags, "endpoint_id", id.String()) ||
			!signedTagMatchesIfPresent(ev.Tags, "environment_id", endpoint.EnvironmentID.String()) ||
			!signedTagMatchesIfPresent(ev.Tags, "name", endpoint.Name) ||
			!signedTagMatchesIfPresent(ev.Tags, "environment", strings.TrimPrefix(d, "endpoint:"+endpoint.Name+":")) {
			continue
		}
		rec, ok := decodeCanonical[domain.MLInferenceEndpoint](ev, d)
		if ok && rec.Value.ID == id && rec.Value.EnvironmentID == endpoint.EnvironmentID && rec.Value.Name == endpoint.Name {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error) {
	events, err := s.records(ctx, kinds.MLInferenceEndpointState, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.MLInferenceState, 0, len(events))
	for _, ev := range events {
		var state domain.MLInferenceState
		if json.Unmarshal([]byte(ev.Content), &state) != nil || state.EndpointID == uuid.Nil || state.EnvironmentID == uuid.Nil {
			continue
		}
		endpoint, err := s.GetInferenceEndpoint(ctx, state.EndpointID)
		if err != nil {
			return nil, err
		}
		if endpoint == nil || endpoint.EnvironmentID != state.EnvironmentID {
			continue
		}
		d := tagValue(ev.Tags, "d")
		if !strings.HasPrefix(d, "endpoint-state:"+endpoint.Name+":") ||
			strings.TrimPrefix(d, "endpoint-state:"+endpoint.Name+":") == "" ||
			!signedTagMatchesIfPresent(ev.Tags, "endpoint", "endpoint:"+endpoint.Name+":"+strings.TrimPrefix(d, "endpoint-state:"+endpoint.Name+":")) ||
			!signedTagMatchesIfPresent(ev.Tags, "endpoint_id", state.EndpointID.String()) ||
			!signedTagMatchesIfPresent(ev.Tags, "environment_id", state.EnvironmentID.String()) ||
			!signedTagMatchesIfPresent(ev.Tags, "environment", strings.TrimPrefix(d, "endpoint-state:"+endpoint.Name+":")) {
			continue
		}
		rec, ok := decodeCanonical[domain.MLInferenceState](ev, d)
		if ok && !rec.Deleted && rec.Value.EndpointID == state.EndpointID && rec.Value.EnvironmentID == state.EnvironmentID {
			out = append(out, rec.Value)
		}
	}
	return out, nil
}

func (s *CanonicalRuntimeSource) artifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	events, err := s.records(ctx, kinds.ArtifactRegistry, id.String())
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[domain.Artifact](ev, id.String())
		if ok && rec.Value.ID == id {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) intent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	events, err := s.records(ctx, kinds.DeploymentIntentRegistry, id.String())
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		rec, ok := decodeCanonical[domain.DeploymentIntent](ev, id.String())
		if ok && rec.Value.ID == id {
			if rec.Deleted {
				return nil, nil
			}
			return &rec.Value, nil
		}
	}
	return nil, nil
}

func (s *CanonicalRuntimeSource) runsForIntent(ctx context.Context, id uuid.UUID) ([]domain.DeploymentRun, error) {
	events, err := s.records(ctx, kinds.DeploymentRunRegistry, "")
	if err != nil {
		return nil, err
	}
	var out []domain.DeploymentRun
	for _, ev := range events {
		var head struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal([]byte(ev.Content), &head) != nil || head.ID == uuid.Nil {
			continue
		}
		rec, ok := decodeCanonical[domain.DeploymentRun](ev, head.ID.String())
		if ok && !rec.Deleted && rec.Value.ID == head.ID && rec.Value.DeploymentIntentID == id {
			out = append(out, rec.Value)
		}
	}
	return out, nil
}

// CanonicalRuntimeHistory supplies digest fallback and completed-route repair
// without allowing a SQL-only artifact, intent or run to authorize publication.
func CanonicalRuntimeHistory(source *CanonicalRuntimeSource) (repository.ArtifactRepository, repository.DeploymentIntentRepository, repository.DeploymentRunRepository) {
	return canonicalArtifacts{source: source}, canonicalIntents{source: source}, canonicalRuns{source: source}
}

type canonicalArtifacts struct {
	repository.ArtifactRepository
	source *CanonicalRuntimeSource
}

func (r canonicalArtifacts) GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return r.source.artifact(ctx, id)
}

type canonicalIntents struct {
	repository.DeploymentIntentRepository
	source *CanonicalRuntimeSource
}

func (r canonicalIntents) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.source.intent(ctx, id)
}

type canonicalRuns struct {
	repository.DeploymentRunRepository
	source *CanonicalRuntimeSource
}

func (r canonicalRuns) ListByIntent(ctx context.Context, id uuid.UUID) ([]domain.DeploymentRun, error) {
	return r.source.runsForIntent(ctx, id)
}

// CanonicalRuntimeRepositories return read-only canonical views with optional
// write-behind index methods needed by the existing reconciliation engine.
func CanonicalRuntimeRepositories(source *CanonicalRuntimeSource, services repository.ServiceRepository, environments repository.EnvironmentRepository, states repository.EnvironmentServiceStateRepository, observations repository.RuntimeObservationRepository, units repository.DeploymentUnitRepository) (repository.ServiceRepository, repository.EnvironmentRepository, repository.EnvironmentServiceStateRepository, repository.RuntimeObservationRepository, repository.DeploymentUnitRepository) {
	return canonicalServices{ServiceRepository: services, source: source},
		canonicalEnvironments{EnvironmentRepository: environments, source: source},
		canonicalStates{EnvironmentServiceStateRepository: states, source: source},
		canonicalObservations{RuntimeObservationRepository: observations, source: source},
		canonicalUnits{DeploymentUnitRepository: units, source: source}
}

type canonicalServices struct {
	repository.ServiceRepository
	source *CanonicalRuntimeSource
}

func (r canonicalServices) GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return r.source.service(ctx, id)
}
func (r canonicalServices) List(ctx context.Context) ([]domain.Service, error) {
	return r.source.listServices(ctx)
}

type canonicalEnvironments struct {
	repository.EnvironmentRepository
	source *CanonicalRuntimeSource
}

func (r canonicalEnvironments) GetByID(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.source.environment(ctx, id)
}
func (r canonicalEnvironments) List(ctx context.Context) ([]domain.Environment, error) {
	return r.source.listEnvironments(ctx)
}

type canonicalStates struct {
	repository.EnvironmentServiceStateRepository
	source *CanonicalRuntimeSource
}

func (r canonicalStates) Get(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, err := r.source.state(ctx, serviceID, envID)
	if state == nil {
		return nil, err
	}
	return &state.EnvironmentServiceState, err
}
func (r canonicalStates) ListAll(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.source.states(ctx)
}
func (r canonicalStates) ListDueForObservation(ctx context.Context, dueBefore time.Time) ([]domain.EnvironmentServiceState, error) {
	states, err := r.source.states(ctx)
	if err != nil {
		return nil, err
	}
	out := states[:0]
	for _, state := range states {
		if state.LastReconciledAt == nil || !state.LastReconciledAt.After(dueBefore) {
			out = append(out, state)
		}
	}
	return out, nil
}
func (r canonicalStates) Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error {
	if r.EnvironmentServiceStateRepository != nil {
		indexCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		_ = r.EnvironmentServiceStateRepository.Upsert(indexCtx, state)
	}
	return nil
}

type canonicalObservations struct {
	repository.RuntimeObservationRepository
	source *CanonicalRuntimeSource
}

func (r canonicalObservations) GetLatest(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.source.observation(ctx, serviceID, envID)
}
func (r canonicalObservations) GetByID(ctx context.Context, id uuid.UUID) (*domain.RuntimeObservation, error) {
	states, err := r.source.states(ctx)
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		if state.CurrentObservationID != nil && *state.CurrentObservationID == id {
			obs, err := r.source.observation(ctx, state.ServiceID, state.EnvironmentID)
			if obs != nil && obs.ID == id {
				return obs, err
			}
		}
	}
	return nil, nil
}
func (r canonicalObservations) Create(ctx context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	if r.RuntimeObservationRepository != nil {
		indexCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		_ = r.RuntimeObservationRepository.Create(indexCtx, obs)
	}
	return nil
}

type canonicalUnits struct {
	repository.DeploymentUnitRepository
	source *CanonicalRuntimeSource
}

func (r canonicalUnits) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	unit, err := r.source.unit(ctx, id)
	if err == nil && unit == nil {
		return nil, fmt.Errorf("deployment unit %s absent from canonical environment state", id)
	}
	return unit, err
}
