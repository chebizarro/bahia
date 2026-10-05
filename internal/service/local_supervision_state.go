package service

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strconv"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SupervisionEventStore is the read surface of the daemon's local event
// store. The store is filled by relay catch-up, live subscriptions and the
// daemon's own publisher, holds verified events only, and collapses every
// addressable coordinate to its latest version.
type SupervisionEventStore interface {
	QueryEvents(gonostr.Filter) iter.Seq[gonostr.Event]
}

// LocalSupervisionState reads the daemon's canonical cp-state (kind 30900,
// signed by the service key) from the local event store. Supervisors derive
// their desired set and their durable progress from it; no SQL repository is
// consulted.
type LocalSupervisionState struct {
	store  SupervisionEventStore
	author gonostr.PubKey
}

// NewLocalSupervisionState returns a reader over store limited to records
// signed by servicePubkey (hex).
func NewLocalSupervisionState(store SupervisionEventStore, servicePubkey string) (LocalSupervisionState, error) {
	if store == nil {
		return LocalSupervisionState{}, fmt.Errorf("local supervision state requires an event store")
	}
	author, err := gonostr.PubKeyFromHex(strings.TrimSpace(servicePubkey))
	if err != nil {
		return LocalSupervisionState{}, fmt.Errorf("local supervision state requires the service pubkey: %w", err)
	}
	return LocalSupervisionState{store: store, author: author}, nil
}

// family returns the latest record of every coordinate carrying the cp-state
// topic, tombstones included.
func (s LocalSupervisionState) family(ctx context.Context, topic string) ([]gonostr.Event, error) {
	return s.query(ctx, gonostr.TagMap{"t": {topic}})
}

// coordinate returns the latest record addressed by d, or nil.
func (s LocalSupervisionState) coordinate(ctx context.Context, d string) (*gonostr.Event, error) {
	records, err := s.query(ctx, gonostr.TagMap{kinds.CASControlStateTagD: {d}})
	if err != nil || len(records) == 0 {
		return nil, err
	}
	latest := records[0]
	for _, record := range records[1:] {
		if record.CreatedAt > latest.CreatedAt {
			latest = record
		}
	}
	return &latest, nil
}

func (s LocalSupervisionState) query(ctx context.Context, tags gonostr.TagMap) ([]gonostr.Event, error) {
	if s.store == nil {
		return nil, fmt.Errorf("local supervision state is not configured")
	}
	filter := gonostr.Filter{
		Kinds:   []gonostr.Kind{gonostr.Kind(kinds.CASControlState)},
		Authors: []gonostr.PubKey{s.author},
		Tags:    tags,
	}
	var out []gonostr.Event
	for ev := range s.store.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != s.author || int(ev.Kind) != kinds.CASControlState {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func supervisionTag(ev gonostr.Event, name string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// liveControlState reports whether ev is a live (not tombstoned) record of the
// projected cp-state family legacyKind.
func liveControlState(ev gonostr.Event, legacyKind int) bool {
	return supervisionTag(ev, kinds.CASControlStateTagSchema) == kinds.CASControlStateSchema &&
		supervisionTag(ev, kinds.CASControlStateTagLegacyKind) == strconv.Itoa(legacyKind) &&
		supervisionTag(ev, kinds.CASControlStateTagDeleted) != "true"
}

// decodeServiceStateRecord decodes a service-state record (the wire shape of
// the relay-first state publisher). It reports false for tombstones, other
// families and records that do not name a service and an environment.
func decodeServiceStateRecord(ev gonostr.Event) (domain.EnvironmentServiceState, bool) {
	var state domain.EnvironmentServiceState
	if !liveControlState(ev, kinds.ServiceState) {
		return state, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(ev.Content), &raw) != nil || string(raw["deleted"]) == "true" {
		return state, false
	}
	// Earlier publishers encoded an absent deployment unit as "".
	if string(raw["deployment_unit_id"]) == `""` {
		delete(raw, "deployment_unit_id")
	}
	normalized, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(normalized, &state) != nil {
		return domain.EnvironmentServiceState{}, false
	}
	if state.ServiceID == uuid.Nil || state.EnvironmentID == uuid.Nil {
		return domain.EnvironmentServiceState{}, false
	}
	return state, true
}

// decodeServiceRecord decodes a service-registry record.
func decodeServiceRecord(ev gonostr.Event) (*domain.Service, bool) {
	if !liveControlState(ev, kinds.ServiceRegistry) {
		return nil, false
	}
	var record struct {
		Deleted bool `json:"deleted"`
		domain.Service
	}
	if json.Unmarshal([]byte(ev.Content), &record) != nil || record.Deleted || record.ID == uuid.Nil {
		return nil, false
	}
	return &record.Service, true
}

// localEnvironment is an environment-registry record: the environment and the
// deployment-unit set its record carries.
type localEnvironment struct {
	Environment domain.Environment
	Units       []domain.DeploymentUnit
}

// decodeEnvironmentRecord decodes an environment-registry record. The implicit
// default unit a record carries as {"key", "implicit": true} is rebuilt from
// the environment, as the repository's default-unit resolution does.
func decodeEnvironmentRecord(ev gonostr.Event) (*localEnvironment, bool) {
	if !liveControlState(ev, kinds.EnvironmentRegistry) {
		return nil, false
	}
	var record struct {
		Deleted bool `json:"deleted"`
		domain.Environment
		DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
	}
	if json.Unmarshal([]byte(ev.Content), &record) != nil || record.Deleted || record.ID == uuid.Nil {
		return nil, false
	}
	env := record.Environment
	domain.NormalizeEnvironmentTargeting(&env)
	out := &localEnvironment{Environment: env}
	for _, unit := range record.DeploymentUnits {
		if unit.Implicit {
			envCopy := env
			implicit, err := domain.NewImplicitDefaultDeploymentUnit(&envCopy)
			if err != nil {
				return nil, false
			}
			out.Units = append(out.Units, *implicit)
			continue
		}
		unit.EnvironmentID = env.ID
		out.Units = append(out.Units, unit)
	}
	return out, true
}

// unit resolves the deployment unit a service state targets: the unit with
// id, or the environment's default unit when id is nil.
func (e *localEnvironment) unit(id *uuid.UUID) *domain.DeploymentUnit {
	for i := range e.Units {
		candidate := &e.Units[i]
		if id != nil && !candidate.Implicit && candidate.ID == *id {
			return candidate
		}
		if id == nil && candidate.Key == e.Environment.Targeting.DefaultUnitKey {
			return candidate
		}
	}
	return nil
}

// SupervisionReadiness signals that the local event store completed its first
// relay catch-up, so the state read from it is current. The signal is a
// channel that is closed once; nothing polls it.
type SupervisionReadiness interface {
	ReadySignal() <-chan struct{}
}

func waitForSupervisionReadiness(ctx context.Context, readiness SupervisionReadiness) error {
	if readiness == nil {
		return nil
	}
	select {
	case <-readiness.ReadySignal():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
