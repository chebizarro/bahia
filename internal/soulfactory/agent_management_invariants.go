package soulfactory

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AgentManagementInvariantInputSchemaV1 identifies the secret-free snapshot the
// invariant checker consumes.
const AgentManagementInvariantInputSchemaV1 = "soulfactory-agent-management-invariant-input/v1"

// AgentManagementInvariantReportSchemaV1 identifies the emitted report.
const AgentManagementInvariantReportSchemaV1 = "soulfactory-agent-management-invariant-report/v1"

// ErrAgentManagementInvariantViolation is returned alongside a complete report
// when at least one managed Soul or service violates an invariant. Callers get
// the full inventory so an operator view can render every Soul, healthy or not.
var ErrAgentManagementInvariantViolation = errors.New("agent management invariant violation")

// Provenance distinguishes canonical control-plane state from relay/dashboard
// hydration. Hydration is never promoted to canonical: it may only corroborate
// canonical state or be reported as a stale projection.
type Provenance string

const (
	// ProvenanceCanonical is authoritative control-plane/database state.
	ProvenanceCanonical Provenance = "canonical"
	// ProvenanceHydration is a relay or dashboard projection of canonical state.
	ProvenanceHydration Provenance = "hydration"
)

// Invariant violation reason codes.
const (
	InvariantMissingServiceLink   = "missing_service_link"
	InvariantServiceNotFound      = "service_not_found"
	InvariantDuplicateService     = "duplicate_service"
	InvariantServiceIdentityDrift = "service_identity_mismatch"
	InvariantPubkeyMismatch       = "pubkey_mismatch"
	InvariantRuntimeBindingDrift  = "runtime_binding_mismatch"
	InvariantMissingUnit          = "missing_placement_unit"
	InvariantMissingObservation   = "missing_observation"
	InvariantMissingDesiredState  = "missing_desired_state"
	InvariantStaleProjection      = "stale_relay_projection"
	InvariantReleaseLag           = "release_lag"
	InvariantReverseOrphan        = "reverse_orphan_service"
)

// Per-Soul invariant status.
const (
	InvariantStatusHealthy  = "healthy"
	InvariantStatusViolated = "violated"
)

// ManagedSoulRecord is one active kind-31951 Soul as read from canonical state.
// It is deliberately secret-free: no bunker URIs, keys, or decrypted content.
type ManagedSoulRecord struct {
	EventID        string     `json:"event_id"`
	AgentID        string     `json:"agent_id"`
	Name           string     `json:"name,omitempty"`
	Status         string     `json:"status"`
	AgentPubkey    string     `json:"agent_pubkey,omitempty"`
	RuntimeBinding string     `json:"runtime_binding,omitempty"`
	RuntimeTarget  string     `json:"runtime_target,omitempty"`
	BahiaServiceID string     `json:"bahia_service_id,omitempty"`
	Provenance     Provenance `json:"provenance"`
	ObservedAt     time.Time  `json:"observed_at,omitempty"`
	SourceRef      string     `json:"source_ref"`
}

// ManagedServiceRecord is one Bahia service.
type ManagedServiceRecord struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	ArtifactRepo string     `json:"artifact_repo,omitempty"`
	RuntimeType  string     `json:"runtime_type,omitempty"`
	AgentManaged bool       `json:"agent_managed"`
	Provenance   Provenance `json:"provenance"`
	SourceRef    string     `json:"source_ref"`
}

// ManagedUnitRecord is a placement/deployment unit bound to a service.
type ManagedUnitRecord struct {
	ID         string     `json:"id"`
	ServiceID  string     `json:"service_id"`
	Key        string     `json:"key,omitempty"`
	Provenance Provenance `json:"provenance"`
	SourceRef  string     `json:"source_ref"`
}

// ManagedObservationRecord is the latest runtime observation for a service.
type ManagedObservationRecord struct {
	ServiceID      string     `json:"service_id"`
	AgentPubkey    string     `json:"agent_pubkey,omitempty"`
	RuntimeBinding string     `json:"runtime_binding,omitempty"`
	Provenance     Provenance `json:"provenance"`
	ObservedAt     time.Time  `json:"observed_at,omitempty"`
	SourceRef      string     `json:"source_ref"`
}

// ManagedDesiredStateRecord is the desired artifact/run state for a service.
type ManagedDesiredStateRecord struct {
	ServiceID   string     `json:"service_id"`
	ArtifactRef string     `json:"artifact_ref,omitempty"`
	RunRef      string     `json:"run_ref,omitempty"`
	Provenance  Provenance `json:"provenance"`
	SourceRef   string     `json:"source_ref"`
}

// ManagedReleaseRecord is the release binding for a service plus the latest
// verified release available on its channel, so release lag is detectable.
type ManagedReleaseRecord struct {
	ServiceID       string     `json:"service_id"`
	BoundReleaseID  string     `json:"bound_release_id,omitempty"`
	LatestReleaseID string     `json:"latest_release_id,omitempty"`
	ReleaseChannel  string     `json:"release_channel,omitempty"`
	Provenance      Provenance `json:"provenance"`
	SourceRef       string     `json:"source_ref"`
}

// AgentManagementInvariantInput is the complete secret-free snapshot. Souls
// must contain every active managed Soul so the report can inventory all of
// them without mutation.
type AgentManagementInvariantInput struct {
	Schema        string                      `json:"schema"`
	Souls         []ManagedSoulRecord         `json:"souls"`
	Services      []ManagedServiceRecord      `json:"services"`
	Units         []ManagedUnitRecord         `json:"units,omitempty"`
	Observations  []ManagedObservationRecord  `json:"observations,omitempty"`
	DesiredStates []ManagedDesiredStateRecord `json:"desired_states,omitempty"`
	Releases      []ManagedReleaseRecord      `json:"releases,omitempty"`

	// ConsecutivePriorFailures is the durable count of prior consecutive
	// checks in which a subject already violated an invariant. Alerts fire
	// only once AlertAfterConsecutive is reached, so transient reconcile
	// windows do not page an operator.
	ConsecutivePriorFailures map[string]int `json:"consecutive_prior_failures,omitempty"`
	AlertAfterConsecutive    int            `json:"alert_after_consecutive,omitempty"`
}

// InvariantViolation is one detected violation with its evidence.
type InvariantViolation struct {
	Code      string   `json:"code"`
	Detail    string   `json:"detail"`
	Evidence  []string `json:"evidence,omitempty"`
	Canonical bool     `json:"canonical"`
}

// SoulInvariantResult is the per-Soul inventory entry.
type SoulInvariantResult struct {
	AgentID        string               `json:"agent_id"`
	EventID        string               `json:"event_id"`
	SourceRef      string               `json:"source_ref"`
	Status         string               `json:"status"`
	BahiaServiceID string               `json:"bahia_service_id,omitempty"`
	Violations     []InvariantViolation `json:"violations,omitempty"`
}

// ServiceOrphanResult reports a Bahia agent service with no active Soul.
type ServiceOrphanResult struct {
	ServiceID  string               `json:"service_id"`
	Name       string               `json:"name"`
	SourceRef  string               `json:"source_ref"`
	Violations []InvariantViolation `json:"violations"`
}

// InvariantAlert is emitted only after bounded retries.
type InvariantAlert struct {
	Subject             string   `json:"subject"`
	Codes               []string `json:"codes"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	Threshold           int      `json:"threshold"`
}

// AgentManagementInvariantSummary counts the inventory.
type AgentManagementInvariantSummary struct {
	ActiveSouls    int `json:"active_souls"`
	Healthy        int `json:"healthy"`
	Violated       int `json:"violated"`
	ReverseOrphans int `json:"reverse_orphans"`
	Alerts         int `json:"alerts"`
	HydrationOnly  int `json:"hydration_only_records_ignored"`
}

// AgentManagementInvariantReport is the read-only operator view.
type AgentManagementInvariantReport struct {
	Schema          string                          `json:"schema"`
	ReadOnly        bool                            `json:"read_only"`
	MutationAllowed bool                            `json:"mutation_allowed"`
	Souls           []SoulInvariantResult           `json:"souls"`
	Orphans         []ServiceOrphanResult           `json:"orphans,omitempty"`
	Alerts          []InvariantAlert                `json:"alerts,omitempty"`
	Summary         AgentManagementInvariantSummary `json:"summary"`
}

// CheckAgentManagementInvariants evaluates every managed active Soul against
// the cross-control-plane invariants. It is pure and read-only: it never
// mutates Souls, services, units, intents, or any control-plane state, and it
// never publishes an event. The complete inventory is always returned; when a
// violation exists the report is returned together with
// ErrAgentManagementInvariantViolation.
func CheckAgentManagementInvariants(input AgentManagementInvariantInput) (AgentManagementInvariantReport, error) {
	if err := validateInvariantInput(input); err != nil {
		return AgentManagementInvariantReport{}, err
	}

	report := AgentManagementInvariantReport{
		Schema:          AgentManagementInvariantReportSchemaV1,
		ReadOnly:        true,
		MutationAllowed: false,
	}

	// Canonical state is authoritative. Hydration records are retained only to
	// corroborate canonical state or to flag a stale projection; they are never
	// promoted into canonical truth.
	canonicalServices := map[string]ManagedServiceRecord{}
	hydrationServices := map[string]ManagedServiceRecord{}
	servicesByName := map[string][]ManagedServiceRecord{}
	for _, svc := range input.Services {
		if svc.Provenance == ProvenanceHydration {
			hydrationServices[svc.ID] = svc
			report.Summary.HydrationOnly++
			continue
		}
		canonicalServices[svc.ID] = svc
		servicesByName[strings.TrimSpace(svc.Name)] = append(servicesByName[strings.TrimSpace(svc.Name)], svc)
	}

	unitsByService := map[string][]ManagedUnitRecord{}
	for _, unit := range input.Units {
		if unit.Provenance == ProvenanceHydration {
			report.Summary.HydrationOnly++
			continue
		}
		unitsByService[unit.ServiceID] = append(unitsByService[unit.ServiceID], unit)
	}
	observationsByService := map[string]ManagedObservationRecord{}
	hydrationObservations := map[string]ManagedObservationRecord{}
	for _, obs := range input.Observations {
		if obs.Provenance == ProvenanceHydration {
			hydrationObservations[obs.ServiceID] = obs
			report.Summary.HydrationOnly++
			continue
		}
		observationsByService[obs.ServiceID] = obs
	}
	desiredByService := map[string]ManagedDesiredStateRecord{}
	for _, ds := range input.DesiredStates {
		if ds.Provenance == ProvenanceHydration {
			report.Summary.HydrationOnly++
			continue
		}
		desiredByService[ds.ServiceID] = ds
	}
	releasesByService := map[string]ManagedReleaseRecord{}
	for _, rel := range input.Releases {
		if rel.Provenance == ProvenanceHydration {
			report.Summary.HydrationOnly++
			continue
		}
		releasesByService[rel.ServiceID] = rel
	}

	souls := append([]ManagedSoulRecord(nil), input.Souls...)
	sort.Slice(souls, func(i, j int) bool { return souls[i].AgentID < souls[j].AgentID })

	linkedServiceIDs := map[string]bool{}
	for _, soul := range souls {
		if !isActiveManagedSoul(soul) {
			continue
		}
		report.Summary.ActiveSouls++
		result := evaluateSoulInvariants(soul, canonicalServices, servicesByName, unitsByService,
			observationsByService, hydrationObservations, desiredByService, releasesByService)
		if strings.TrimSpace(soul.BahiaServiceID) != "" {
			linkedServiceIDs[strings.TrimSpace(soul.BahiaServiceID)] = true
		}
		if len(result.Violations) == 0 {
			result.Status = InvariantStatusHealthy
			report.Summary.Healthy++
		} else {
			result.Status = InvariantStatusViolated
			report.Summary.Violated++
		}
		report.Souls = append(report.Souls, result)
	}

	// Reverse orphans: canonical agent-managed services with no active Soul.
	orphanIDs := make([]string, 0)
	for id := range canonicalServices {
		orphanIDs = append(orphanIDs, id)
	}
	sort.Strings(orphanIDs)
	for _, id := range orphanIDs {
		svc := canonicalServices[id]
		if !svc.AgentManaged || linkedServiceIDs[id] {
			continue
		}
		report.Orphans = append(report.Orphans, ServiceOrphanResult{
			ServiceID: svc.ID, Name: svc.Name, SourceRef: svc.SourceRef,
			Violations: []InvariantViolation{{
				Code:      InvariantReverseOrphan,
				Detail:    "agent-managed Bahia service has no active managed Soul",
				Evidence:  []string{svc.SourceRef},
				Canonical: true,
			}},
		})
		report.Summary.ReverseOrphans++
	}

	report.Alerts = buildInvariantAlerts(report, input)
	report.Summary.Alerts = len(report.Alerts)

	if report.Summary.Violated > 0 || report.Summary.ReverseOrphans > 0 {
		return report, ErrAgentManagementInvariantViolation
	}
	return report, nil
}

func validateInvariantInput(input AgentManagementInvariantInput) error {
	if strings.TrimSpace(input.Schema) != AgentManagementInvariantInputSchemaV1 {
		return fmt.Errorf("unsupported agent management invariant input schema %q", input.Schema)
	}
	if input.AlertAfterConsecutive < 0 {
		return errors.New("alert_after_consecutive must not be negative")
	}
	for _, soul := range input.Souls {
		if strings.TrimSpace(soul.AgentID) == "" || strings.TrimSpace(soul.SourceRef) == "" {
			return errors.New("every Soul record requires agent_id and source_ref")
		}
		if soul.Provenance != ProvenanceCanonical && soul.Provenance != ProvenanceHydration {
			return fmt.Errorf("Soul %q requires an explicit canonical or hydration provenance", soul.AgentID)
		}
	}
	for _, svc := range input.Services {
		if strings.TrimSpace(svc.ID) == "" || strings.TrimSpace(svc.SourceRef) == "" {
			return errors.New("every service record requires id and source_ref")
		}
		if svc.Provenance != ProvenanceCanonical && svc.Provenance != ProvenanceHydration {
			return fmt.Errorf("service %q requires an explicit canonical or hydration provenance", svc.ID)
		}
	}
	return nil
}

// isActiveManagedSoul reports whether a Soul record is an active Soul held in
// canonical state. A hydration-only Soul is never treated as canonical truth;
// it is surfaced as a stale projection against its canonical counterpart.
func isActiveManagedSoul(soul ManagedSoulRecord) bool {
	return soul.Provenance == ProvenanceCanonical && strings.EqualFold(strings.TrimSpace(soul.Status), "active")
}

func evaluateSoulInvariants(
	soul ManagedSoulRecord,
	canonicalServices map[string]ManagedServiceRecord,
	servicesByName map[string][]ManagedServiceRecord,
	unitsByService map[string][]ManagedUnitRecord,
	observations map[string]ManagedObservationRecord,
	hydrationObservations map[string]ManagedObservationRecord,
	desired map[string]ManagedDesiredStateRecord,
	releases map[string]ManagedReleaseRecord,
) SoulInvariantResult {
	result := SoulInvariantResult{
		AgentID: soul.AgentID, EventID: soul.EventID, SourceRef: soul.SourceRef,
		BahiaServiceID: soul.BahiaServiceID,
	}
	add := func(code, detail string, evidence ...string) {
		result.Violations = append(result.Violations, InvariantViolation{
			Code: code, Detail: detail, Evidence: evidence, Canonical: true,
		})
	}

	expectedName := soulServiceName(soul.AgentID)
	expectedRepo := soulServiceArtifactRepo(soul.AgentID)

	// Exactly one canonical service must match this agent by canonical name.
	byName := servicesByName[expectedName]
	if len(byName) > 1 {
		ids := make([]string, 0, len(byName))
		for _, svc := range byName {
			ids = append(ids, svc.ID)
		}
		sort.Strings(ids)
		add(InvariantDuplicateService,
			fmt.Sprintf("%d canonical services named %q match this agent, want exactly one", len(byName), expectedName), ids...)
	}

	serviceID := strings.TrimSpace(soul.BahiaServiceID)
	if serviceID == "" {
		add(InvariantMissingServiceLink, "active managed Soul has no bahia_service_id", soul.SourceRef)
		return result
	}
	svc, ok := canonicalServices[serviceID]
	if !ok {
		add(InvariantServiceNotFound,
			fmt.Sprintf("bahia_service_id %s does not resolve to a canonical Bahia service", serviceID), soul.SourceRef)
		return result
	}

	// Linked service identity must actually match this agent, not merely exist.
	if strings.TrimSpace(svc.Name) != expectedName {
		add(InvariantServiceIdentityDrift,
			fmt.Sprintf("linked service name %q does not match canonical %q", svc.Name, expectedName), svc.SourceRef)
	}
	if repo := strings.TrimSpace(svc.ArtifactRepo); repo != "" && repo != expectedRepo {
		add(InvariantServiceIdentityDrift,
			fmt.Sprintf("linked service artifact repository %q does not match canonical %q", repo, expectedRepo), svc.SourceRef)
	}

	if len(unitsByService[serviceID]) == 0 {
		add(InvariantMissingUnit, "linked service has no canonical placement/deployment unit", svc.SourceRef)
	}

	obs, hasObs := observations[serviceID]
	if !hasObs {
		add(InvariantMissingObservation, "linked service has no canonical runtime observation", svc.SourceRef)
		// A hydration observation that exists without canonical backing is a
		// stale projection, never a substitute for canonical truth.
		if stale, ok := hydrationObservations[serviceID]; ok {
			add(InvariantStaleProjection,
				"relay/dashboard observation exists without canonical runtime observation", stale.SourceRef)
		}
	} else {
		if soul.AgentPubkey != "" && obs.AgentPubkey != "" && !strings.EqualFold(soul.AgentPubkey, obs.AgentPubkey) {
			add(InvariantPubkeyMismatch,
				fmt.Sprintf("observed agent pubkey %q does not match Soul pubkey %q", obs.AgentPubkey, soul.AgentPubkey), obs.SourceRef)
		}
		if soul.RuntimeBinding != "" && obs.RuntimeBinding != "" && soul.RuntimeBinding != obs.RuntimeBinding {
			add(InvariantRuntimeBindingDrift,
				fmt.Sprintf("observed runtime binding %q does not match Soul binding %q", obs.RuntimeBinding, soul.RuntimeBinding), obs.SourceRef)
		}
		// A hydration projection older than canonical is stale.
		if stale, ok := hydrationObservations[serviceID]; ok && !stale.ObservedAt.IsZero() &&
			!obs.ObservedAt.IsZero() && stale.ObservedAt.Before(obs.ObservedAt) {
			add(InvariantStaleProjection,
				fmt.Sprintf("relay/dashboard projection observed at %s is behind canonical %s",
					stale.ObservedAt.UTC().Format(time.RFC3339), obs.ObservedAt.UTC().Format(time.RFC3339)), stale.SourceRef)
		}
	}

	if _, ok := desired[serviceID]; !ok {
		add(InvariantMissingDesiredState, "linked service has no canonical desired artifact/run state", svc.SourceRef)
	}

	if rel, ok := releases[serviceID]; ok {
		bound := strings.TrimSpace(rel.BoundReleaseID)
		latest := strings.TrimSpace(rel.LatestReleaseID)
		if latest != "" && bound != "" && bound != latest {
			add(InvariantReleaseLag,
				fmt.Sprintf("service is bound to release %s but channel %q latest verified release is %s",
					bound, rel.ReleaseChannel, latest), rel.SourceRef)
		}
	}
	return result
}

// buildInvariantAlerts emits an alert only once a subject has violated an
// invariant for AlertAfterConsecutive consecutive checks (counting this one),
// so a single transient reconcile window never pages an operator.
func buildInvariantAlerts(report AgentManagementInvariantReport, input AgentManagementInvariantInput) []InvariantAlert {
	threshold := input.AlertAfterConsecutive
	if threshold <= 0 {
		return nil
	}
	var alerts []InvariantAlert
	appendAlert := func(subject string, violations []InvariantViolation) {
		consecutive := input.ConsecutivePriorFailures[subject] + 1
		if consecutive < threshold {
			return
		}
		codes := make([]string, 0, len(violations))
		for _, v := range violations {
			codes = append(codes, v.Code)
		}
		sort.Strings(codes)
		alerts = append(alerts, InvariantAlert{
			Subject: subject, Codes: dedupeStrings(codes),
			ConsecutiveFailures: consecutive, Threshold: threshold,
		})
	}
	for _, soul := range report.Souls {
		if len(soul.Violations) == 0 {
			continue
		}
		appendAlert(soul.AgentID, soul.Violations)
	}
	for _, orphan := range report.Orphans {
		appendAlert(orphan.ServiceID, orphan.Violations)
	}
	return alerts
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0:0]
	var prev string
	for i, v := range in {
		if i > 0 && v == prev {
			continue
		}
		out = append(out, v)
		prev = v
	}
	return out
}
