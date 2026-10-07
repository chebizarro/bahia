package runtime

import (
	"sort"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// FragmentIneligibilityReason — machine-readable eligibility codes
// ---------------------------------------------------------------------------

// FragmentIneligibilityReason is a machine-readable code explaining why a
// service change cannot use fragment apply.
type FragmentIneligibilityReason string

const (
	// FragmentEligible indicates the change is safe for service-scoped apply.
	FragmentEligible FragmentIneligibilityReason = ""

	// FragmentIneligibleDependencyChange indicates depends_on entries changed.
	FragmentIneligibleDependencyChange FragmentIneligibilityReason = "dependency_change"

	// FragmentIneligibleNetworkChange indicates project-wide network declarations changed.
	FragmentIneligibleNetworkChange FragmentIneligibilityReason = "network_change"

	// FragmentIneligibleVolumeChange indicates project-wide volume declarations changed.
	FragmentIneligibleVolumeChange FragmentIneligibilityReason = "volume_change"

	// FragmentIneligibleProjectNameChange indicates the Compose project name changed.
	FragmentIneligibleProjectNameChange FragmentIneligibilityReason = "project_name_change"

	// FragmentIneligibleNewService indicates the target service has no baseline.
	FragmentIneligibleNewService FragmentIneligibilityReason = "new_service"

	// FragmentIneligibleServiceRemoval indicates a service was removed from the plan.
	FragmentIneligibleServiceRemoval FragmentIneligibilityReason = "service_removal"

	// FragmentIneligibleMultipleChanged indicates more than one service changed.
	FragmentIneligibleMultipleChanged FragmentIneligibilityReason = "multiple_services_changed"

	// FragmentIneligibleNoBaseline indicates no prior full-project render exists.
	FragmentIneligibleNoBaseline FragmentIneligibilityReason = "no_baseline"
)

// ---------------------------------------------------------------------------
// FragmentEligibility — eligibility result
// ---------------------------------------------------------------------------

// FragmentEligibility reports whether a service change is eligible for
// service-scoped Compose fragment apply.
type FragmentEligibility struct {
	// Eligible is true when the change can safely use fragment apply.
	Eligible bool

	// Reason is a human-readable explanation of the eligibility decision.
	Reason string

	// ReasonCode is the machine-readable eligibility code.
	ReasonCode FragmentIneligibilityReason
}

// ---------------------------------------------------------------------------
// CheckFragmentEligibility — main eligibility decision function
// ---------------------------------------------------------------------------

// CheckFragmentEligibility determines if a single-service change can safely
// use service-scoped fragment apply instead of full-project apply.
//
// A change is INELIGIBLE when:
//   - depends_on entries changed (could affect ordering/health conditions)
//   - Project-wide network declarations changed
//   - Project-wide volume declarations changed
//   - Project name changed
//   - Service is new (no baseline)
//   - Service is being removed (requires full-project --remove-orphans)
//   - Multiple services changed in the same apply
//   - No baseline render-state exists (first render must be full-project)
//
// A change IS eligible when:
//   - Only image, env, command, entrypoint, labels, ports, healthcheck,
//     restart policy, or pull policy changed on a single existing service
//   - No cross-service dependency or infrastructure changes
func CheckFragmentEligibility(
	plan *domain.DesiredEnvironmentPlan,
	target *domain.DesiredServiceSpec,
	baseline *RenderMetadata,
) *FragmentEligibility {
	// No baseline → first render must be full-project.
	if baseline == nil {
		return ineligible(FragmentIneligibleNoBaseline,
			"no baseline render state exists; first render must use full-project apply")
	}

	// Build lookup sets for current and baseline service keys.
	currentKeys := make(map[string]struct{}, len(plan.Services))
	for _, svc := range plan.Services {
		currentKeys[svc.StableServiceKey] = struct{}{}
	}
	baselineKeys := make(map[string]struct{}, len(baseline.ServiceKeys))
	for _, key := range baseline.ServiceKeys {
		baselineKeys[key] = struct{}{}
	}

	// Target is a new service (not in baseline) → full-project apply required.
	if _, exists := baselineKeys[target.StableServiceKey]; !exists {
		return ineligible(FragmentIneligibleNewService,
			"service is new and has no baseline; full-project apply required")
	}

	// A service was removed from the plan → full-project --remove-orphans required.
	for _, baselineKey := range baseline.ServiceKeys {
		if _, exists := currentKeys[baselineKey]; !exists {
			return ineligible(FragmentIneligibleServiceRemoval,
				"service removal requires full-project apply with --remove-orphans")
		}
	}

	// Project name changed → full-project apply required.
	currentProjectName := deriveProjectName(plan)
	if currentProjectName != baseline.ProjectName {
		return ineligible(FragmentIneligibleProjectNameChange,
			"project name changed; full-project apply required")
	}

	// Network declarations changed → full-project apply required.
	currentNetworks, currentVolumes := collectPlanDeclarations(plan)
	if !stringSetsEqual(currentNetworks, baseline.NetworksDeclared) {
		return ineligible(FragmentIneligibleNetworkChange,
			"network declarations changed; full-project apply required")
	}

	// Volume declarations changed → full-project apply required.
	if !stringSetsEqual(currentVolumes, baseline.VolumesDeclared) {
		return ineligible(FragmentIneligibleVolumeChange,
			"volume declarations changed; full-project apply required")
	}

	// Count changed services using stored service hashes from the baseline.
	// When ServiceHashes is absent (pre-fragment baseline), every service
	// appears changed, making multi-service plans ineligible conservatively.
	changedCount := 0
	for _, svc := range plan.Services {
		baselineHash, ok := baseline.ServiceHashes[svc.StableServiceKey]
		if !ok || baselineHash != svc.DesiredHash {
			changedCount++
		}
	}
	if changedCount > 1 {
		return ineligible(FragmentIneligibleMultipleChanged,
			"multiple services changed; full-project apply required")
	}

	// Depends_on changed for the target service → full-project apply required.
	// Changes here affect startup ordering and health conditions across services.
	currentDeps := collectEffectiveDependsOn(*target)
	baselineDeps := baseline.ServiceDependsOn[target.StableServiceKey]
	if !stringSlicesEqual(sortedUnique(currentDeps), sortedUnique(baselineDeps)) {
		return ineligible(FragmentIneligibleDependencyChange,
			"service depends_on changed; full-project apply required")
	}

	return &FragmentEligibility{
		Eligible:   true,
		Reason:     "single-service safe-field change; eligible for fragment apply",
		ReasonCode: FragmentEligible,
	}
}

// ---------------------------------------------------------------------------
// Package-level helpers (also used by compose_renderer.go)
// ---------------------------------------------------------------------------

// collectEffectiveDependsOn returns the sorted, deduplicated effective
// depends_on keys for a service. It merges the plain DependsOn slice with
// any keys declared in ComposeExtension.DependsOn.
func collectEffectiveDependsOn(svc domain.DesiredServiceSpec) []string {
	depSet := make(map[string]struct{})
	for _, dep := range svc.DependsOn {
		depSet[dep] = struct{}{}
	}
	if svc.ComposeExtension != nil {
		for key := range svc.ComposeExtension.DependsOn {
			depSet[key] = struct{}{}
		}
	}
	return sortedKeysFromSet(depSet)
}

// deriveProjectName extracts the Compose project name from the plan using the
// same logic as ComposeRenderer.projectName. Defined here so that eligibility
// checks can compare against the stored baseline project name without needing
// a renderer instance.
func deriveProjectName(plan *domain.DesiredEnvironmentPlan) string {
	for _, svc := range plan.Services {
		if svc.ComposeExtension != nil && svc.ComposeExtension.ProjectName != "" {
			return domain.NormalizeServiceKey(svc.ComposeExtension.ProjectName)
		}
	}
	envID := plan.EnvironmentID.String()
	if len(envID) >= 8 {
		return "bahia-" + envID[:8]
	}
	return "bahia-project"
}

// collectPlanDeclarations returns sorted network and volume declaration names
// from the plan. Mirrors ComposeRenderer.collectDeclarations.
func collectPlanDeclarations(plan *domain.DesiredEnvironmentPlan) (networks, volumes []string) {
	netSet := make(map[string]struct{})
	volSet := make(map[string]struct{})
	for _, svc := range plan.Services {
		if svc.ComposeExtension == nil {
			continue
		}
		for _, n := range svc.ComposeExtension.Networks {
			netSet[n] = struct{}{}
		}
		for _, v := range svc.ComposeExtension.VolumeDeclarations {
			volSet[v] = struct{}{}
		}
	}
	return sortedKeysFromSet(netSet), sortedKeysFromSet(volSet)
}

// ---------------------------------------------------------------------------
// String comparison helpers
// ---------------------------------------------------------------------------

// stringSetsEqual returns true when both slices contain the same set of values
// (order-independent). Nil and empty slices are considered equal.
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	setA := make(map[string]struct{}, len(a))
	for _, v := range a {
		setA[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := setA[v]; !ok {
			return false
		}
	}
	return true
}

// stringSlicesEqual returns true when two string slices are identical element-by-element.
// Nil and empty slices are considered equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortedUnique returns a sorted, deduplicated copy of s. Returns nil for empty input.
func sortedUnique(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	sort.Strings(cp)
	out := cp[:1]
	for i := 1; i < len(cp); i++ {
		if cp[i] != cp[i-1] {
			out = append(out, cp[i])
		}
	}
	return out
}

// ineligible is a convenience constructor for an ineligible FragmentEligibility.
func ineligible(code FragmentIneligibilityReason, reason string) *FragmentEligibility {
	return &FragmentEligibility{Eligible: false, Reason: reason, ReasonCode: code}
}
