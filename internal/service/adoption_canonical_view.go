package service

import (
	"context"
	"encoding/json"
	"sort"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AdoptionEnvironment is an environment together with the explicit deployment
// units its registry record carries. The implicit default unit is not listed:
// a record that names no explicit unit has none.
type AdoptionEnvironment struct {
	Environment domain.Environment
	Units       []domain.DeploymentUnit
}

// AdoptionCanonicalView reads the daemon's own canonical records, in their
// latest state, from the local event store. Adoption plans and resumes from
// this view only; it never reads a SQL repository (audit B-35).
type AdoptionCanonicalView interface {
	ListServices(ctx context.Context) ([]domain.Service, error)
	ListEnvironments(ctx context.Context) ([]AdoptionEnvironment, error)
	GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error)
	ListBuilds(ctx context.Context) ([]domain.Build, error)
	GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	ListArtifacts(ctx context.Context) ([]domain.Artifact, error)
	GetServiceState(ctx context.Context, serviceID, environmentID uuid.UUID) (*domain.EnvironmentServiceState, error)
	GetRuntimeObservation(ctx context.Context, serviceID, environmentID uuid.UUID) (*domain.RuntimeObservation, error)
	ListAdoptionBindings(ctx context.Context) ([]domain.AdoptionBinding, error)
}

// LocalAdoptionView is the AdoptionCanonicalView over the local event store.
// A family is read by its cp-state topic and one record by its d, both
// tag-indexed, and only records signed by the service key are considered.
type LocalAdoptionView struct {
	state LocalSupervisionState
}

// NewLocalAdoptionView returns the view over state.
func NewLocalAdoptionView(state LocalSupervisionState) *LocalAdoptionView {
	return &LocalAdoptionView{state: state}
}

// ListServices returns every live service, oldest first.
func (v *LocalAdoptionView) ListServices(ctx context.Context) ([]domain.Service, error) {
	records, err := v.state.family(ctx, kinds.CPStateTopicServiceRegistry)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Service, 0, len(records))
	for _, record := range records {
		if svc, ok := decodeServiceRecord(record); ok {
			out = append(out, *svc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// ListEnvironments returns every live environment with its explicit units,
// oldest first.
func (v *LocalAdoptionView) ListEnvironments(ctx context.Context) ([]AdoptionEnvironment, error) {
	records, err := v.state.family(ctx, kinds.CPStateTopicEnvironmentRegistry)
	if err != nil {
		return nil, err
	}
	out := make([]AdoptionEnvironment, 0, len(records))
	for _, record := range records {
		env, ok := decodeEnvironmentRecord(record)
		if !ok {
			continue
		}
		entry := AdoptionEnvironment{Environment: env.Environment}
		for _, unit := range env.Units {
			if !unit.Implicit {
				entry.Units = append(entry.Units, unit)
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Environment, out[j].Environment
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID.String() < b.ID.String()
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return out, nil
}

// GetBuild returns the live build with id, or nil.
func (v *LocalAdoptionView) GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	record, err := v.state.coordinate(ctx, id.String())
	if err != nil || record == nil {
		return nil, err
	}
	build, ok := decodeBuildRecord(*record)
	if !ok || build.ID != id {
		return nil, nil
	}
	return build, nil
}

// ListBuilds returns every live build.
func (v *LocalAdoptionView) ListBuilds(ctx context.Context) ([]domain.Build, error) {
	records, err := v.state.family(ctx, kinds.CPStateTopicBuildRegistry)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Build, 0, len(records))
	for _, record := range records {
		if build, ok := decodeBuildRecord(record); ok {
			out = append(out, *build)
		}
	}
	return out, nil
}

// GetArtifact returns the live artifact with id, or nil.
func (v *LocalAdoptionView) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	record, err := v.state.coordinate(ctx, id.String())
	if err != nil || record == nil {
		return nil, err
	}
	artifact, ok := decodeArtifactRecord(*record)
	if !ok || artifact.ID != id {
		return nil, nil
	}
	return artifact, nil
}

// ListArtifacts returns every live artifact.
func (v *LocalAdoptionView) ListArtifacts(ctx context.Context) ([]domain.Artifact, error) {
	records, err := v.state.family(ctx, kinds.CPStateTopicArtifactRegistry)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Artifact, 0, len(records))
	for _, record := range records {
		if artifact, ok := decodeArtifactRecord(record); ok {
			out = append(out, *artifact)
		}
	}
	return out, nil
}

// GetServiceState returns the live state of a service in an environment, or
// nil.
func (v *LocalAdoptionView) GetServiceState(ctx context.Context, serviceID, environmentID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	record, err := v.state.coordinate(ctx, kinds.ServiceStateDTag(serviceID.String(), environmentID.String()))
	if err != nil || record == nil {
		return nil, err
	}
	state, ok := decodeServiceStateRecord(*record)
	if !ok || state.ServiceID != serviceID || state.EnvironmentID != environmentID {
		return nil, nil
	}
	return &state, nil
}

// GetRuntimeObservation returns the live runtime observation of a service in
// an environment, or nil. A record carries only the fields the family
// projects: identity, image digest, container, health and time.
func (v *LocalAdoptionView) GetRuntimeObservation(ctx context.Context, serviceID, environmentID uuid.UUID) (*domain.RuntimeObservation, error) {
	record, err := v.state.coordinate(ctx, kinds.RuntimeObservationDTag(serviceID.String(), environmentID.String()))
	if err != nil || record == nil {
		return nil, err
	}
	obs, ok := decodeRuntimeObservationRecord(*record)
	if !ok || obs.ServiceID != serviceID || obs.EnvironmentID != environmentID {
		return nil, nil
	}
	return obs, nil
}

// ListAdoptionBindings returns every live adoption binding, ordered by
// coordinate.
func (v *LocalAdoptionView) ListAdoptionBindings(ctx context.Context) ([]domain.AdoptionBinding, error) {
	records, err := v.state.family(ctx, kinds.CPStateTopicAdoptionBinding)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AdoptionBinding, 0, len(records))
	for _, record := range records {
		if binding, ok := decodeAdoptionBindingRecord(record); ok {
			out = append(out, *binding)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return adoptionBindingKey(out[i].ServiceID, out[i].EnvironmentID) < adoptionBindingKey(out[j].ServiceID, out[j].EnvironmentID)
	})
	return out, nil
}

func adoptionBindingKey(serviceID, environmentID uuid.UUID) string {
	return kinds.AdoptionBindingDTag(serviceID.String(), environmentID.String())
}

// decodeLiveRecord decodes the JSON content of a live record of the cp-state
// family legacyKind into out. It reports false for tombstones, other families
// and content that does not decode.
func decodeLiveRecord(ev gonostr.Event, legacyKind int, out any) bool {
	if !liveControlState(ev, legacyKind) {
		return false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(ev.Content), &raw) != nil || string(raw["deleted"]) == "true" {
		return false
	}
	// Registry record builders write an unset timestamp as "", which a
	// time.Time field does not decode.
	for key, value := range raw {
		if string(value) == `""` {
			delete(raw, key)
		}
	}
	normalized, err := json.Marshal(raw)
	return err == nil && json.Unmarshal(normalized, out) == nil
}

// decodeBuildRecord decodes a build-registry record.
func decodeBuildRecord(ev gonostr.Event) (*domain.Build, bool) {
	var build domain.Build
	if !decodeLiveRecord(ev, kinds.BuildRegistry, &build) || build.ID == uuid.Nil || build.ServiceID == uuid.Nil {
		return nil, false
	}
	return &build, true
}

// decodeArtifactRecord decodes an artifact-registry record.
func decodeArtifactRecord(ev gonostr.Event) (*domain.Artifact, bool) {
	var artifact domain.Artifact
	if !decodeLiveRecord(ev, kinds.ArtifactRegistry, &artifact) || artifact.ID == uuid.Nil || artifact.ServiceID == uuid.Nil {
		return nil, false
	}
	return &artifact, true
}

// decodeRuntimeObservationRecord decodes a runtime-observation record.
func decodeRuntimeObservationRecord(ev gonostr.Event) (*domain.RuntimeObservation, bool) {
	var obs domain.RuntimeObservation
	if !decodeLiveRecord(ev, kinds.RuntimeObservationState, &obs) || obs.ID == uuid.Nil || obs.ServiceID == uuid.Nil || obs.EnvironmentID == uuid.Nil {
		return nil, false
	}
	return &obs, true
}

// decodeAdoptionBindingRecord decodes an adoption binding record. A binding
// whose content does not name the coordinate it is stored on is ignored.
func decodeAdoptionBindingRecord(ev gonostr.Event) (*domain.AdoptionBinding, bool) {
	var binding domain.AdoptionBinding
	if !decodeLiveRecord(ev, kinds.CPStateFamilyAdoptionBinding.LegacyKind(), &binding) {
		return nil, false
	}
	if binding.ServiceID == uuid.Nil || binding.EnvironmentID == uuid.Nil ||
		adoptionBindingKey(binding.ServiceID, binding.EnvironmentID) != supervisionTag(ev, kinds.CASControlStateTagD) {
		return nil, false
	}
	return &binding, true
}

var _ AdoptionCanonicalView = (*LocalAdoptionView)(nil)
