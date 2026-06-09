package runtime

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Eligibility test helpers
// ---------------------------------------------------------------------------

// baselineFrom renders plan through the full ComposeRenderer and returns the
// resulting RenderMetadata as a baseline for eligibility tests.
func baselineFrom(t *testing.T, plan *domain.DesiredEnvironmentPlan) *RenderMetadata {
	t.Helper()
	r := NewComposeRenderer()
	result, err := r.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("baselineFrom: render failed: %v", err)
	}
	m := result.Metadata
	return &m
}

// eligibilitySvc builds a minimal DesiredServiceSpec for eligibility tests.
// EnvironmentID and hash are computed by eligibilityPlan.
func eligibilitySvc(key, image string) domain.DesiredServiceSpec {
	seed := key + "00000000"
	return domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         fixedUUID("svc-" + seed[:8]),
		ArtifactID:        fixedUUID("art-" + seed[:8]),
		StableServiceKey:  key,
		DeploymentUnitKey: domain.DefaultDeploymentUnitKey,
		ImageRef:          image,
		RestartPolicy:     "unless-stopped",
	}
}

// eligibilityPlan builds a DesiredEnvironmentPlan from the given service
// specs, stamping a shared EnvironmentID and recomputing all hashes.
func eligibilityPlan(svcs ...domain.DesiredServiceSpec) *domain.DesiredEnvironmentPlan {
	envID := fixedUUID("env-frag-test0")
	services := make([]domain.DesiredServiceSpec, len(svcs))
	copy(services, svcs)
	for i := range services {
		services[i].EnvironmentID = envID
		services[i].ComputeDesiredHash()
	}
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services:      services,
	}
	plan.ComputeRevisionHash()
	return plan
}

// findSvc returns a pointer to the named service in a plan, or fails the test.
func findSvc(t *testing.T, plan *domain.DesiredEnvironmentPlan, key string) *domain.DesiredServiceSpec {
	t.Helper()
	for i := range plan.Services {
		if plan.Services[i].StableServiceKey == key {
			return &plan.Services[i]
		}
	}
	t.Fatalf("findSvc: service %q not found in plan", key)
	return nil
}

// assertEligible fails the test if the result is not eligible.
func assertEligible(t *testing.T, got *FragmentEligibility) {
	t.Helper()
	if !got.Eligible {
		t.Errorf("expected eligible, got ineligible: code=%q reason=%q",
			got.ReasonCode, got.Reason)
	}
	if got.ReasonCode != FragmentEligible {
		t.Errorf("expected reason code %q, got %q", FragmentEligible, got.ReasonCode)
	}
}

// assertIneligible fails the test if the result is eligible or has the wrong code.
func assertIneligible(t *testing.T, got *FragmentEligibility, wantCode FragmentIneligibilityReason) {
	t.Helper()
	if got.Eligible {
		t.Errorf("expected ineligible(%s), got eligible", wantCode)
		return
	}
	if got.ReasonCode != wantCode {
		t.Errorf("expected reason code %q, got %q (reason: %q)",
			wantCode, got.ReasonCode, got.Reason)
	}
	if got.Reason == "" {
		t.Errorf("expected non-empty Reason string for code %q", wantCode)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFragmentEligibility_NoBaseline(t *testing.T) {
	plan := eligibilityPlan(eligibilitySvc("api", "api:v1"))
	target := findSvc(t, plan, "api")

	got := CheckFragmentEligibility(plan, target, nil)

	assertIneligible(t, got, FragmentIneligibleNoBaseline)
}

func TestFragmentEligibility_SingleServiceImageChange(t *testing.T) {
	// Baseline: api:v1
	baseline := baselineFrom(t, eligibilityPlan(eligibilitySvc("api", "api:v1")))

	// Apply: api:v2 — image-only change on a single known service.
	after := eligibilityPlan(eligibilitySvc("api", "api:v2"))
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertEligible(t, got)
}

func TestFragmentEligibility_DependencyChange(t *testing.T) {
	// Baseline: api with no depends_on
	baseline := baselineFrom(t, eligibilityPlan(
		eligibilitySvc("api", "api:v1"),
		eligibilitySvc("db", "postgres:14"),
	))

	// Apply: api now depends_on db
	apiWithDep := eligibilitySvc("api", "api:v1")
	apiWithDep.DependsOn = []string{"db"}
	after := eligibilityPlan(apiWithDep, eligibilitySvc("db", "postgres:14"))
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleDependencyChange)
}

func TestFragmentEligibility_NetworkChange(t *testing.T) {
	// Baseline: api with no network declarations
	baseline := baselineFrom(t, eligibilityPlan(eligibilitySvc("api", "api:v1")))

	// Apply: api now declares a network
	apiWithNet := eligibilitySvc("api", "api:v1")
	apiWithNet.ComposeExtension = &domain.ComposeExtension{
		Networks: []string{"mynet"},
	}
	after := eligibilityPlan(apiWithNet)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleNetworkChange)
}

func TestFragmentEligibility_VolumeChange(t *testing.T) {
	// Baseline: api with no volume declarations
	baseline := baselineFrom(t, eligibilityPlan(eligibilitySvc("api", "api:v1")))

	// Apply: api now declares a named volume
	apiWithVol := eligibilitySvc("api", "api:v1")
	apiWithVol.ComposeExtension = &domain.ComposeExtension{
		VolumeDeclarations: []string{"mydata"},
	}
	after := eligibilityPlan(apiWithVol)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleVolumeChange)
}

func TestFragmentEligibility_ProjectNameChange(t *testing.T) {
	// Baseline: api under project "myproject"
	apiV1 := eligibilitySvc("api", "api:v1")
	apiV1.ComposeExtension = &domain.ComposeExtension{ProjectName: "myproject"}
	baseline := baselineFrom(t, eligibilityPlan(apiV1))

	// Apply: api under project "newproject"
	apiV2 := eligibilitySvc("api", "api:v1")
	apiV2.ComposeExtension = &domain.ComposeExtension{ProjectName: "newproject"}
	after := eligibilityPlan(apiV2)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleProjectNameChange)
}

func TestFragmentEligibility_NewService(t *testing.T) {
	// Baseline: only api
	baseline := baselineFrom(t, eligibilityPlan(eligibilitySvc("api", "api:v1")))

	// Apply: api + worker (worker is brand-new)
	after := eligibilityPlan(
		eligibilitySvc("api", "api:v1"),
		eligibilitySvc("worker", "worker:v1"),
	)
	// Target is the new service
	target := findSvc(t, after, "worker")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleNewService)
}

func TestFragmentEligibility_ServiceRemoval(t *testing.T) {
	// Baseline: api + worker
	baseline := baselineFrom(t, eligibilityPlan(
		eligibilitySvc("api", "api:v1"),
		eligibilitySvc("worker", "worker:v1"),
	))

	// Apply: worker removed — only api remains
	after := eligibilityPlan(eligibilitySvc("api", "api:v1"))
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleServiceRemoval)
}

func TestFragmentEligibility_MultipleServicesChanged(t *testing.T) {
	// Baseline: api:v1 + web:v1
	baseline := baselineFrom(t, eligibilityPlan(
		eligibilitySvc("api", "api:v1"),
		eligibilitySvc("web", "web:v1"),
	))

	// Apply: both api:v2 and web:v2 changed simultaneously
	after := eligibilityPlan(
		eligibilitySvc("api", "api:v2"),
		eligibilitySvc("web", "web:v2"),
	)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertIneligible(t, got, FragmentIneligibleMultipleChanged)
}

func TestFragmentEligibility_NoChanges(t *testing.T) {
	// Baseline and apply are identical — hash matches for all services.
	plan := eligibilityPlan(eligibilitySvc("api", "api:v1"))
	baseline := baselineFrom(t, plan)
	target := findSvc(t, plan, "api")

	got := CheckFragmentEligibility(plan, target, baseline)

	// A no-op is still safe for fragment apply (changedCount == 0).
	assertEligible(t, got)
}

func TestFragmentEligibility_EnvChange(t *testing.T) {
	// Baseline: api with LOG_LEVEL=debug
	apiV1 := eligibilitySvc("api", "api:v1")
	apiV1.Env = map[string]string{"LOG_LEVEL": "debug"}
	baseline := baselineFrom(t, eligibilityPlan(apiV1))

	// Apply: api with LOG_LEVEL=info — env-only change
	apiV2 := eligibilitySvc("api", "api:v1")
	apiV2.Env = map[string]string{"LOG_LEVEL": "info"}
	after := eligibilityPlan(apiV2)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertEligible(t, got)
}

func TestFragmentEligibility_PortChange(t *testing.T) {
	// Baseline: api on port 8080
	apiV1 := eligibilitySvc("api", "api:v1")
	apiV1.Ports = []string{"8080:8080"}
	baseline := baselineFrom(t, eligibilityPlan(apiV1))

	// Apply: api on port 9090 — port-only change
	apiV2 := eligibilitySvc("api", "api:v1")
	apiV2.Ports = []string{"9090:9090"}
	after := eligibilityPlan(apiV2)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertEligible(t, got)
}

func TestFragmentEligibility_HealthcheckChange(t *testing.T) {
	// Baseline: api without healthcheck
	baseline := baselineFrom(t, eligibilityPlan(eligibilitySvc("api", "api:v1")))

	// Apply: api gains a healthcheck — still safe for fragment apply
	apiWithHC := eligibilitySvc("api", "api:v1")
	apiWithHC.Healthcheck = &domain.HealthcheckConfig{
		Test:     []string{"CMD", "curl", "-f", "http://localhost/health"},
		Interval: "30s",
		Retries:  3,
	}
	after := eligibilityPlan(apiWithHC)
	target := findSvc(t, after, "api")

	got := CheckFragmentEligibility(after, target, baseline)

	assertEligible(t, got)
}
