package soulfactory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Wizard canary constants. The pubkey is the authoritative Wizard identity and
// is pinned: the canary refuses to plan against any other identity, so a
// mis-targeted adoption can never reach an operator for execution.
const (
	// WizardCanaryAgentID is the canonical Wizard agent id.
	WizardCanaryAgentID = "wizard"
	// WizardCanaryPubkey is the authoritative Wizard agent pubkey. It must be
	// preserved exactly; the canary never creates, rotates, or replaces it.
	WizardCanaryPubkey = "e8351e63a713eef5a0167df23b9ce0cb677aeb4b0be9810dfaf2b32e571b798d"
	// WizardCanaryRuntimeSource is the exact runtime that must be adopted.
	WizardCanaryRuntimeSource = "wizard-dock"
	// WizardCanaryHost is the reviewed placement host. The canary adopts the
	// runtime exactly where it runs; any other host is refused.
	WizardCanaryHost = "max"

	WizardCanaryInputSchemaV1 = "soulfactory-wizard-canary-input/v1"
	WizardCanaryPlanSchemaV1  = "soulfactory-wizard-canary-plan/v1"
)

// ErrWizardCanaryRefused is returned when a preflight invariant fails. The
// canary fails closed: no plan is emitted for operator execution.
var ErrWizardCanaryRefused = errors.New("wizard canary preflight refused")

var wizardImageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// WizardRollbackBaseline is the immutable pre-rollout baseline an operator must
// be able to return to. Both references must be immutable: the exact prior Soul
// event and the exact prior runtime image digest.
type WizardRollbackBaseline struct {
	SoulEventID        string    `json:"soul_event_id"`
	RuntimeImageDigest string    `json:"runtime_image_digest"`
	CapturedAt         time.Time `json:"captured_at"`
	Ref                string    `json:"ref"`
}

// WizardCanaryInput is the secret-free snapshot the canary preflight consumes.
// SecretRefs carry no secret material — only safe references.
type WizardCanaryInput struct {
	Schema           string                  `json:"schema"`
	Soul             LegacyReconcileSoul     `json:"soul"`
	Runtime          LegacyRunningAgent      `json:"runtime"`
	Placement        LegacyReviewedPlacement `json:"operator_reviewed_placement"`
	TargetServiceID  string                  `json:"target_service_id,omitempty"`
	SecretRefs       []domain.SecretRef      `json:"secret_refs,omitempty"`
	RollbackBaseline WizardRollbackBaseline  `json:"rollback_baseline"`
}

// WizardCanaryPlan is the Track A deliverable: a validated, operator-executable
// canary plan. It performs no live action itself. RequiresOperatorExecution is
// always true — the reviewed Bahia rollout and every live acceptance check are
// Track B operations outside Track A authority.
type WizardCanaryPlan struct {
	Schema                    string `json:"schema"`
	ReadOnly                  bool   `json:"read_only"`
	RequiresOperatorExecution bool   `json:"requires_operator_execution"`

	AgentID              string `json:"agent_id"`
	PreservedAgentPubkey string `json:"preserved_agent_pubkey"`

	// ReconciliationRequest is fed verbatim to the supported reconciliation
	// surface (LegacyAgentReconciler Preview, then ReconcileApprovedLink with
	// an authenticated operator approval).
	ReconciliationRequest LegacyAgentReconciliationRequest `json:"reconciliation_request"`

	PreservedRuntimeBinding string   `json:"preserved_runtime_binding,omitempty"`
	PreservedCustodyRef     string   `json:"preserved_custody_ref,omitempty"`
	PreservedVolumes        []string `json:"preserved_volumes,omitempty"`
	AdoptedRuntimeSource    string   `json:"adopted_runtime_source"`
	AdoptedHost             string   `json:"adopted_host"`
	AdoptedImageDigest      string   `json:"adopted_image_digest"`

	ServiceScopedSecretRefs []WizardScopedSecretRef `json:"service_scoped_secret_refs,omitempty"`
	RollbackBaseline        WizardRollbackBaseline  `json:"rollback_baseline"`

	ProhibitedActions    []string `json:"prohibited_actions"`
	OperatorSteps        []string `json:"operator_steps"`
	LiveAcceptanceChecks []string `json:"live_acceptance_checks"`
}

// WizardScopedSecretRef is a safe, service-scoped secret reference. No secret
// value is ever carried.
type WizardScopedSecretRef struct {
	ID        string `json:"id"`
	ServiceID string `json:"service_id"`
	Name      string `json:"name"`
}

// wizardProhibitedActions enumerates what this canary must never do. They are
// emitted into the plan so the executing operator inherits the same boundary.
func wizardProhibitedActions() []string {
	return []string{
		"agent key generation, rotation, revocation, or replacement",
		"ACL or grant changes",
		"runtime binding changes",
		"custody changes",
		"destroying or recreating the running wizard-dock runtime",
		"mutating persistent state volumes",
	}
}

// PlanWizardCanary validates every Track A precondition and emits the operator
// plan. It is pure and performs no live action: no rollout, no relay publish,
// no runtime mutation. Any failed invariant refuses the plan outright.
func PlanWizardCanary(input WizardCanaryInput) (WizardCanaryPlan, error) {
	if strings.TrimSpace(input.Schema) != WizardCanaryInputSchemaV1 {
		return WizardCanaryPlan{}, fmt.Errorf("%w: unsupported input schema %q", ErrWizardCanaryRefused, input.Schema)
	}

	soul := input.Soul
	runtime := input.Runtime

	// 1. Pinned authoritative identity. Any deviation refuses.
	if strings.TrimSpace(soul.AgentID) != WizardCanaryAgentID {
		return WizardCanaryPlan{}, fmt.Errorf("%w: soul agent_id %q is not the Wizard canary agent %q",
			ErrWizardCanaryRefused, soul.AgentID, WizardCanaryAgentID)
	}
	if !strings.EqualFold(strings.TrimSpace(soul.AgentPubkey), WizardCanaryPubkey) {
		return WizardCanaryPlan{}, fmt.Errorf("%w: soul pubkey does not match the pinned authoritative Wizard identity",
			ErrWizardCanaryRefused)
	}
	if managed := strings.TrimSpace(runtime.ManagedPubkey); managed != "" &&
		!strings.EqualFold(managed, WizardCanaryPubkey) {
		return WizardCanaryPlan{}, fmt.Errorf("%w: observed runtime managed pubkey does not match the pinned Wizard identity",
			ErrWizardCanaryRefused)
	}
	if !strings.EqualFold(strings.TrimSpace(soul.Status), "active") {
		return WizardCanaryPlan{}, fmt.Errorf("%w: Wizard Soul status %q is not active", ErrWizardCanaryRefused, soul.Status)
	}
	if strings.TrimSpace(soul.EventID) == "" {
		return WizardCanaryPlan{}, fmt.Errorf("%w: authoritative Wizard Soul event id is required", ErrWizardCanaryRefused)
	}

	// 2. Exact wizard-dock runtime, actually running, with an immutable digest.
	if !runtime.Running {
		return WizardCanaryPlan{}, fmt.Errorf("%w: Wizard runtime is not running; the canary adopts an existing runtime and never starts one",
			ErrWizardCanaryRefused)
	}
	adopted := runtime.AdoptedRuntime
	if adopted == nil {
		return WizardCanaryPlan{}, fmt.Errorf("%w: exact adopted runtime configuration is required", ErrWizardCanaryRefused)
	}
	if !wizardRuntimeIsDock(adopted, runtime) {
		return WizardCanaryPlan{}, fmt.Errorf("%w: observed runtime is not the exact %q runtime",
			ErrWizardCanaryRefused, WizardCanaryRuntimeSource)
	}
	if !wizardImageDigestPattern.MatchString(strings.TrimSpace(adopted.ImageDigest)) {
		return WizardCanaryPlan{}, fmt.Errorf("%w: adopted runtime requires an immutable sha256 image digest",
			ErrWizardCanaryRefused)
	}

	// 3. Persistent state must be preserved, not recreated.
	if len(adopted.Volumes) == 0 {
		return WizardCanaryPlan{}, fmt.Errorf("%w: adopted runtime declares no persistent state volumes to preserve",
			ErrWizardCanaryRefused)
	}

	// 4. Identity-adjacent bindings must be carried unchanged, never altered.
	if strings.TrimSpace(soul.RuntimeBinding) != "" && strings.TrimSpace(runtime.RuntimeBinding) != "" &&
		soul.RuntimeBinding != runtime.RuntimeBinding {
		return WizardCanaryPlan{}, fmt.Errorf("%w: Soul runtime binding %q disagrees with observed %q; the canary must not change a binding",
			ErrWizardCanaryRefused, soul.RuntimeBinding, runtime.RuntimeBinding)
	}

	// 5. Dedicated reviewed placement (the max unit), never a shared default.
	if strings.TrimSpace(input.Placement.Ref) == "" || strings.TrimSpace(input.Placement.EnvironmentID) == "" {
		return WizardCanaryPlan{}, fmt.Errorf("%w: operator-reviewed placement with ref and environment is required",
			ErrWizardCanaryRefused)
	}
	// The adopted runtime must actually run on the reviewed max host; a
	// wizard-dock on any other host is not the Wizard canary.
	if !strings.EqualFold(strings.TrimSpace(adopted.HostAlias), WizardCanaryHost) {
		return WizardCanaryPlan{}, fmt.Errorf("%w: adopted runtime host %q is not the reviewed %q placement",
			ErrWizardCanaryRefused, adopted.HostAlias, WizardCanaryHost)
	}
	// The unit must be the dedicated Wizard identity — the canonical per-agent
	// deployment-unit key — never the shared/default unit or another agent's.
	unitKey := strings.TrimSpace(input.Placement.DeploymentUnitKey)
	if unitKey == "" || unitKey == domain.DefaultDeploymentUnitKey {
		return WizardCanaryPlan{}, fmt.Errorf("%w: a dedicated deployment unit is required; the shared/default unit %q is refused",
			ErrWizardCanaryRefused, unitKey)
	}
	if unitKey != WizardCanaryDeploymentUnitKey() {
		return WizardCanaryPlan{}, fmt.Errorf("%w: deployment unit %q is not the dedicated Wizard unit %q",
			ErrWizardCanaryRefused, unitKey, WizardCanaryDeploymentUnitKey())
	}

	// 6. Secret references must be service-scoped, never global.
	scoped, err := wizardScopedSecretRefs(input)
	if err != nil {
		return WizardCanaryPlan{}, err
	}

	// 7. Immutable rollback baseline must be registered before any rollout.
	if err := validateWizardRollbackBaseline(input.RollbackBaseline, soul.EventID); err != nil {
		return WizardCanaryPlan{}, err
	}

	plan := WizardCanaryPlan{
		Schema:                    WizardCanaryPlanSchemaV1,
		ReadOnly:                  true,
		RequiresOperatorExecution: true,
		AgentID:                   WizardCanaryAgentID,
		PreservedAgentPubkey:      WizardCanaryPubkey,
		ReconciliationRequest: LegacyAgentReconciliationRequest{
			AgentID:           WizardCanaryAgentID,
			RuntimeAgents:     []LegacyRunningAgent{runtime},
			ReviewedPlacement: input.Placement,
		},
		PreservedRuntimeBinding: firstNonEmpty(soul.RuntimeBinding, runtime.RuntimeBinding),
		PreservedCustodyRef:     firstNonEmpty(soul.CustodyRef, runtime.CustodyRef),
		PreservedVolumes:        append([]string(nil), adopted.Volumes...),
		AdoptedRuntimeSource:    WizardCanaryRuntimeSource,
		AdoptedHost:             WizardCanaryHost,
		AdoptedImageDigest:      strings.TrimSpace(adopted.ImageDigest),
		ServiceScopedSecretRefs: scoped,
		RollbackBaseline:        input.RollbackBaseline,
		ProhibitedActions:       wizardProhibitedActions(),
		OperatorSteps:           wizardOperatorSteps(),
		LiveAcceptanceChecks:    wizardLiveAcceptanceChecks(),
	}
	return plan, nil
}

// wizardRuntimeIsDock requires the observed runtime to actually be wizard-dock.
// Container name alone is never sufficient evidence of identity.
func wizardRuntimeIsDock(adopted *domain.AdoptedRuntimeConfig, runtime LegacyRunningAgent) bool {
	for _, candidate := range []string{adopted.SourceRuntime, adopted.TargetName} {
		if strings.EqualFold(strings.TrimSpace(candidate), WizardCanaryRuntimeSource) {
			return true
		}
	}
	// A container-name match is only corroborating evidence and requires the
	// documented agent id to agree, so a look-alike container cannot qualify.
	if strings.EqualFold(strings.TrimSpace(runtime.ContainerName), WizardCanaryRuntimeSource) &&
		strings.TrimSpace(runtime.DocumentedAgentID) == WizardCanaryAgentID {
		return true
	}
	return false
}

func wizardScopedSecretRefs(input WizardCanaryInput) ([]WizardScopedSecretRef, error) {
	target := strings.TrimSpace(input.TargetServiceID)
	scoped := make([]WizardScopedSecretRef, 0, len(input.SecretRefs))
	for _, ref := range input.SecretRefs {
		if ref.ServiceID == uuid.Nil {
			return nil, fmt.Errorf("%w: secret %q is not service-scoped", ErrWizardCanaryRefused, ref.Name)
		}
		if target != "" && ref.ServiceID.String() != target {
			return nil, fmt.Errorf("%w: secret %q is scoped to service %s, not the canary service %s",
				ErrWizardCanaryRefused, ref.Name, ref.ServiceID, target)
		}
		scoped = append(scoped, WizardScopedSecretRef{
			ID: ref.ID.String(), ServiceID: ref.ServiceID.String(), Name: ref.Name,
		})
	}
	return scoped, nil
}

func validateWizardRollbackBaseline(baseline WizardRollbackBaseline, currentSoulEventID string) error {
	if strings.TrimSpace(baseline.SoulEventID) == "" {
		return fmt.Errorf("%w: rollback baseline requires the immutable current Soul event id", ErrWizardCanaryRefused)
	}
	if baseline.SoulEventID != currentSoulEventID {
		return fmt.Errorf("%w: rollback baseline Soul event %q is not the current authoritative Soul %q",
			ErrWizardCanaryRefused, baseline.SoulEventID, currentSoulEventID)
	}
	if !wizardImageDigestPattern.MatchString(strings.TrimSpace(baseline.RuntimeImageDigest)) {
		return fmt.Errorf("%w: rollback baseline requires an immutable sha256 runtime digest", ErrWizardCanaryRefused)
	}
	if baseline.CapturedAt.IsZero() || strings.TrimSpace(baseline.Ref) == "" {
		return fmt.Errorf("%w: rollback baseline requires a capture time and durable ref", ErrWizardCanaryRefused)
	}
	return nil
}

// wizardOperatorSteps is the Track B execution sequence. Track A produces it;
// an authorized operator performs it against the live control plane.
func wizardOperatorSteps() []string {
	return []string{
		"confirm the registered immutable rollback baseline (Soul event + runtime digest) is durable",
		"run LegacyAgentReconciler.Preview for agent wizard and confirm the classification is unlinked and unambiguous",
		"obtain an authenticated NIP-98 operator approval bound to the exact agent_id, action, Soul event id, and Soul content hash",
		"apply LegacyAgentReconciler.ReconcileApprovedLink to create/link the Bahia service and dedicated max deployment unit and adopt the exact wizard-dock runtime without restart",
		"perform the reviewed Bahia rollout for the adopted unit",
		"confirm the superseding kind-31951 adds only bahia_service_id and preserves identity",
	}
}

// wizardLiveAcceptanceChecks enumerates the acceptance evidence that can only
// be produced against the live fleet. Track A cannot satisfy these.
func wizardLiveAcceptanceChecks() []string {
	return []string{
		"agent pubkey remains " + WizardCanaryPubkey,
		"persistent state preserved across the rollout (declared volumes intact)",
		"relays reach EOSE and deliver realtime events",
		"fleet_tasks subscription is active",
		"readiness probe passes",
		"rollback to the registered baseline is proven",
	}
}

// WizardCanaryDeploymentUnitKey is the dedicated Wizard deployment-unit
// identity: the canonical per-agent unit key the governed provisioner itself
// uses (soulServiceName), so the canary and provisioning never disagree.
func WizardCanaryDeploymentUnitKey() string {
	return soulServiceName(WizardCanaryAgentID)
}
