package soulfactory

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// The reporter collects a secret-free snapshot from canonical control-plane
// reads and evaluates the invariants. Every dependency is a narrow READ-ONLY
// interface: the reporter has no write path, so it can never mutate a Soul,
// service, unit, intent, or publish an event.

// ManagedSoulLister returns every active managed kind-31951 Soul from the
// authoritative source. Implementations must return canonical records; relay or
// dashboard projections must be marked ProvenanceHydration so the checker can
// flag them as stale rather than trust them.
type ManagedSoulLister interface {
	ListManagedSouls(ctx context.Context) ([]ManagedSoulRecord, error)
}

// ManagedServiceLister is satisfied by *service.RegistryService.
type ManagedServiceLister interface {
	ListServices(ctx context.Context) ([]domain.Service, error)
}

// ManagedPlacementReader supplies canonical placement/unit, observation,
// desired-state, and release-binding facts. Each method is optional: a nil
// reader simply yields no records, and the corresponding invariant is then
// reported as missing rather than silently passing.
type ManagedPlacementReader interface {
	ListManagedUnits(ctx context.Context) ([]ManagedUnitRecord, error)
	ListManagedObservations(ctx context.Context) ([]ManagedObservationRecord, error)
	ListManagedDesiredStates(ctx context.Context) ([]ManagedDesiredStateRecord, error)
	ListManagedReleases(ctx context.Context) ([]ManagedReleaseRecord, error)
}

// InvariantFailureHistory supplies the durable consecutive-failure counts that
// bound alerting, so a transient reconcile window never pages an operator.
type InvariantFailureHistory interface {
	ConsecutiveFailures(ctx context.Context) (map[string]int, error)
}

// AgentManagementInvariantReporter produces the production operator report.
type AgentManagementInvariantReporter struct {
	souls      ManagedSoulLister
	services   ManagedServiceLister
	placement  ManagedPlacementReader
	history    InvariantFailureHistory
	alertAfter int
}

// NewAgentManagementInvariantReporter builds the read-only reporter. souls and
// services are required; placement and history are optional.
func NewAgentManagementInvariantReporter(
	souls ManagedSoulLister,
	services ManagedServiceLister,
	placement ManagedPlacementReader,
	history InvariantFailureHistory,
	alertAfterConsecutive int,
) (*AgentManagementInvariantReporter, error) {
	if souls == nil {
		return nil, fmt.Errorf("agent management invariant reporter requires a managed Soul lister")
	}
	if services == nil {
		return nil, fmt.Errorf("agent management invariant reporter requires a service lister")
	}
	if alertAfterConsecutive < 0 {
		return nil, fmt.Errorf("alert threshold must not be negative")
	}
	return &AgentManagementInvariantReporter{
		souls: souls, services: services, placement: placement,
		history: history, alertAfter: alertAfterConsecutive,
	}, nil
}

// Collect assembles the secret-free snapshot from canonical reads only.
func (r *AgentManagementInvariantReporter) Collect(ctx context.Context) (AgentManagementInvariantInput, error) {
	input := AgentManagementInvariantInput{
		Schema:                AgentManagementInvariantInputSchemaV1,
		AlertAfterConsecutive: r.alertAfter,
	}
	souls, err := r.souls.ListManagedSouls(ctx)
	if err != nil {
		return AgentManagementInvariantInput{}, fmt.Errorf("list managed souls: %w", err)
	}
	input.Souls = souls

	services, err := r.services.ListServices(ctx)
	if err != nil {
		return AgentManagementInvariantInput{}, fmt.Errorf("list services: %w", err)
	}
	for _, svc := range services {
		input.Services = append(input.Services, ManagedServiceRecord{
			ID:           svc.ID.String(),
			Name:         svc.Name,
			ArtifactRepo: svc.ArtifactRepo,
			RuntimeType:  string(svc.RuntimeType),
			AgentManaged: isAgentManagedServiceName(svc.Name),
			Provenance:   ProvenanceCanonical,
			SourceRef:    "bahia:service:" + svc.ID.String(),
		})
	}

	if r.placement != nil {
		if units, err := r.placement.ListManagedUnits(ctx); err != nil {
			return AgentManagementInvariantInput{}, fmt.Errorf("list managed units: %w", err)
		} else {
			input.Units = units
		}
		if obs, err := r.placement.ListManagedObservations(ctx); err != nil {
			return AgentManagementInvariantInput{}, fmt.Errorf("list managed observations: %w", err)
		} else {
			input.Observations = obs
		}
		if desired, err := r.placement.ListManagedDesiredStates(ctx); err != nil {
			return AgentManagementInvariantInput{}, fmt.Errorf("list managed desired states: %w", err)
		} else {
			input.DesiredStates = desired
		}
		if releases, err := r.placement.ListManagedReleases(ctx); err != nil {
			return AgentManagementInvariantInput{}, fmt.Errorf("list managed releases: %w", err)
		} else {
			input.Releases = releases
		}
	}

	if r.history != nil {
		counts, err := r.history.ConsecutiveFailures(ctx)
		if err != nil {
			return AgentManagementInvariantInput{}, fmt.Errorf("load invariant failure history: %w", err)
		}
		input.ConsecutivePriorFailures = counts
	}
	return input, nil
}

// Report collects the canonical snapshot and evaluates every invariant. It
// performs no mutation and publishes no event. A violation is returned as
// ErrAgentManagementInvariantViolation alongside the complete inventory, so an
// operator view always renders every active Soul.
func (r *AgentManagementInvariantReporter) Report(ctx context.Context) (AgentManagementInvariantReport, error) {
	input, err := r.Collect(ctx)
	if err != nil {
		return AgentManagementInvariantReport{}, err
	}
	return CheckAgentManagementInvariants(input)
}

// isAgentManagedServiceName reports whether a Bahia service name is in the
// canonical Soul Factory agent namespace, so reverse orphans are only claimed
// for services this control plane actually owns.
func isAgentManagedServiceName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "agent-")
}
