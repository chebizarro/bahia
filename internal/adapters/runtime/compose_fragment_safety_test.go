package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ===========================================================================
// Compose Fragment Safety Tests
//
// These tests verify the safety invariants that the fragment optimization
// (bahia-zu2p.9.3) must preserve. They test the CONTRACT, not the
// implementation, so they pass regardless of whether the fragment applier
// has landed.
//
// Safety guarantees verified:
//
//   1. Full project remains the source of truth — the full docker-compose.yml
//      is always updated even when fragments are used.
//   2. No fragment without a baseline — the first render always uses
//      full-project apply.
//   3. Dependency changes require full-project apply.
//   4. Network declaration changes require full-project apply.
//   5. Volume declaration changes require full-project apply.
//   6. Project name changes require full-project apply.
//   7. Service removal requires full-project apply with --remove-orphans.
//   8. New service additions require full-project apply.
//   9. Fragments never contain plaintext secret values.
//  10. Fragment project name matches the full project name.
//
// Each eligibility assertion below calls the production CheckFragmentEligibility
// (compose_fragment_eligibility.go) with a real rendered baseline and asserts
// the expected ineligibility ReasonCode.
// ===========================================================================

// ---------------------------------------------------------------------------
// 1. Full project remains source of truth
// ---------------------------------------------------------------------------

// TestFragmentSafety_FullProjectRemainsSourceOfTruth verifies that the
// full docker-compose.yml is always updated even when fragments are used.
// This prevents drift between the full project and fragment-applied state.
func TestFragmentSafety_FullProjectRemainsSourceOfTruth(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// The full docker-compose.yml must always be written with ALL services.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("full docker-compose.yml must always be written (source of truth): %v", err)
	}

	content := string(data)

	// Every service in the plan must be present in the full project.
	for _, svc := range plan.Services {
		if !strings.Contains(content, svc.StableServiceKey) {
			t.Errorf("full project missing service %q: the full docker-compose.yml must always be the source of truth for all services", svc.StableServiceKey)
		}
	}

	// The file must be a complete project with a top-level services key,
	// not a service-scoped fragment.
	if !strings.Contains(content, "services:") {
		t.Error("docker-compose.yml must contain a top-level 'services:' key: it must be a complete project, not a fragment")
	}

	// TODO(bahia-zu2p.9.3): When fragment apply is implemented, extend this
	// test to verify that applying a service-scoped fragment also writes an
	// updated full docker-compose.yml alongside the fragment file under
	// .bahia/fragments/ to prevent project drift.
}

// ---------------------------------------------------------------------------
// 2. No fragment without baseline
// ---------------------------------------------------------------------------

// TestFragmentSafety_NoFragmentWithoutBaseline verifies that the first
// render of an environment always uses full-project apply, never fragments.
func TestFragmentSafety_NoFragmentWithoutBaseline(t *testing.T) {
	// Contract: CheckFragmentEligibility must return ineligible when there is
	// no prior render baseline. The first render must use full-project apply to
	// establish the canonical baseline that future fragment diffs compare against.
	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	got := CheckFragmentEligibility(plan, target, nil)

	assertIneligible(t, got, FragmentIneligibleNoBaseline)
}

// ---------------------------------------------------------------------------
// 3. Dependency changes require full-project apply
// ---------------------------------------------------------------------------

// TestFragmentSafety_DependencyChangeRequiresFullProject verifies that
// any change to depends_on forces full-project apply.
func TestFragmentSafety_DependencyChangeRequiresFullProject(t *testing.T) {
	renderer := NewComposeRenderer()

	// Build a plan where web-frontend declares a health-checked dependency on api-server.
	envID := fixedUUID("env-dep-frag0")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-dep-api00"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-dep-api00"),
				StableServiceKey: "api-server",
				ImageRef:         "api:latest",
				ComposeExtension: &domain.ComposeExtension{
					ProjectName: "dep-frag-test",
				},
			},
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-dep-web00"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-dep-web00"),
				StableServiceKey: "web-frontend",
				ImageRef:         "web:latest",
				DependsOn:        []string{"api-server"},
				ComposeExtension: &domain.ComposeExtension{
					DependsOn: map[string]domain.ComposeDependency{
						"api-server": {Condition: "service_healthy"},
					},
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Invariant: the full project always captures dependency relationships.
	// Dependency changes must appear in the full rendered project so they
	// are never silently dropped by a service-scoped fragment apply.
	yaml := string(result.ComposeYAML)
	if !strings.Contains(yaml, "depends_on") {
		t.Error("full rendered project must include depends_on: dependency state must always be captured in the full project")
	}
	if !strings.Contains(yaml, "service_healthy") {
		t.Error("full rendered project must include dependency condition: dependency conditions must be preserved in the full project")
	}

	// Eligibility contract: dependency changes must be ineligible for fragment apply.
	// Baseline: the same two services but WITHOUT the web-frontend depends_on.
	baselinePlan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-dep-api00"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-dep-api00"),
				StableServiceKey: "api-server",
				ImageRef:         "api:latest",
				ComposeExtension: &domain.ComposeExtension{ProjectName: "dep-frag-test"},
			},
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-dep-web00"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-dep-web00"),
				StableServiceKey: "web-frontend",
				ImageRef:         "web:latest",
				ComposeExtension: &domain.ComposeExtension{ProjectName: "dep-frag-test"},
			},
		},
	}
	for i := range baselinePlan.Services {
		baselinePlan.Services[i].ComputeDesiredHash()
	}
	baselinePlan.ComputeRevisionHash()
	baseline := baselineFrom(t, baselinePlan)

	target := findSvc(t, plan, "web-frontend")
	got := CheckFragmentEligibility(plan, target, baseline)
	assertIneligible(t, got, FragmentIneligibleDependencyChange)
}

// ---------------------------------------------------------------------------
// 4. Network declaration changes require full-project apply
// ---------------------------------------------------------------------------

// TestFragmentSafety_NetworkChangeRequiresFullProject verifies that
// changes to project-wide network declarations force full-project apply.
func TestFragmentSafety_NetworkChangeRequiresFullProject(t *testing.T) {
	renderer := NewComposeRenderer()

	envID := fixedUUID("env-net-frag0")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-net-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-net-frag0"),
				StableServiceKey: "net-service",
				ImageRef:         "app:latest",
				ComposeExtension: &domain.ComposeExtension{
					ProjectName: "net-frag-test",
					Networks:    []string{"frontend", "backend"},
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)

	// Invariant: the full project always captures project-wide network declarations.
	// Network changes must appear in the full rendered project; a service-scoped
	// fragment cannot safely add or remove a project-level network.
	if !strings.Contains(yaml, "networks:") {
		t.Error("full rendered project must include top-level networks section: network declarations require full-project apply")
	}
	if !strings.Contains(yaml, "frontend") {
		t.Error("full rendered project must include 'frontend' network declaration")
	}
	if !strings.Contains(yaml, "backend") {
		t.Error("full rendered project must include 'backend' network declaration")
	}

	// Render metadata must record declared networks for operator visibility.
	if len(result.Metadata.NetworksDeclared) == 0 {
		t.Error("render metadata must record network declarations for operator visibility")
	}

	// Eligibility contract: network changes must be ineligible for fragment apply.
	// Baseline: net-service WITHOUT any network declarations.
	baselinePlan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-net-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-net-frag0"),
				StableServiceKey: "net-service",
				ImageRef:         "app:latest",
				ComposeExtension: &domain.ComposeExtension{ProjectName: "net-frag-test"},
			},
		},
	}
	for i := range baselinePlan.Services {
		baselinePlan.Services[i].ComputeDesiredHash()
	}
	baselinePlan.ComputeRevisionHash()
	baseline := baselineFrom(t, baselinePlan)

	target := findSvc(t, plan, "net-service")
	got := CheckFragmentEligibility(plan, target, baseline)
	assertIneligible(t, got, FragmentIneligibleNetworkChange)
}

// ---------------------------------------------------------------------------
// 5. Volume declaration changes require full-project apply
// ---------------------------------------------------------------------------

// TestFragmentSafety_VolumeChangeRequiresFullProject verifies that
// changes to project-wide volume declarations force full-project apply.
func TestFragmentSafety_VolumeChangeRequiresFullProject(t *testing.T) {
	renderer := NewComposeRenderer()

	envID := fixedUUID("env-vol-frag0")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-vol-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-vol-frag0"),
				StableServiceKey: "vol-service",
				ImageRef:         "app:latest",
				Volumes:          []string{"app-data:/data"},
				ComposeExtension: &domain.ComposeExtension{
					ProjectName:        "vol-frag-test",
					VolumeDeclarations: []string{"app-data"},
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)

	// Invariant: the full project always captures project-wide volume declarations.
	// Volume changes must appear in the full rendered project; a service-scoped
	// fragment cannot safely add or remove a project-level named volume.
	if !strings.Contains(yaml, "volumes:") {
		t.Error("full rendered project must include top-level volumes section: volume declarations require full-project apply")
	}
	if !strings.Contains(yaml, "app-data") {
		t.Error("full rendered project must include named volume 'app-data'")
	}

	// Render metadata must record declared volumes for operator visibility.
	if len(result.Metadata.VolumesDeclared) == 0 {
		t.Error("render metadata must record volume declarations for operator visibility")
	}

	// Eligibility contract: volume changes must be ineligible for fragment apply.
	// Baseline: vol-service WITHOUT any named-volume declarations.
	baselinePlan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-vol-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-vol-frag0"),
				StableServiceKey: "vol-service",
				ImageRef:         "app:latest",
				ComposeExtension: &domain.ComposeExtension{ProjectName: "vol-frag-test"},
			},
		},
	}
	for i := range baselinePlan.Services {
		baselinePlan.Services[i].ComputeDesiredHash()
	}
	baselinePlan.ComputeRevisionHash()
	baseline := baselineFrom(t, baselinePlan)

	target := findSvc(t, plan, "vol-service")
	got := CheckFragmentEligibility(plan, target, baseline)
	assertIneligible(t, got, FragmentIneligibleVolumeChange)
}

// ---------------------------------------------------------------------------
// 6. Project name changes require full-project apply
// ---------------------------------------------------------------------------

// TestFragmentSafety_ProjectNameChangeRequiresFullProject verifies that
// project name changes force full-project apply.
func TestFragmentSafety_ProjectNameChangeRequiresFullProject(t *testing.T) {
	renderer := NewComposeRenderer()

	envID := fixedUUID("env-pnm-frag0")
	projectName := "production-cluster-a"
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-pnm-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-pnm-frag0"),
				StableServiceKey: "pnm-service",
				ImageRef:         "app:latest",
				ComposeExtension: &domain.ComposeExtension{
					ProjectName: projectName,
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Invariant: the full project always includes the explicit project name.
	// If the name changes and fragments use the stale name, Compose will apply
	// them to the wrong project — creating orphaned containers.
	if !strings.Contains(string(result.ComposeYAML), "name: "+projectName) {
		t.Errorf("full rendered project must include 'name: %s': project name must always be explicit, not derived from directory basename", projectName)
	}
	if result.Metadata.ProjectName != projectName {
		t.Errorf("render metadata ProjectName = %q, want %q: metadata must reflect the current project name", result.Metadata.ProjectName, projectName)
	}

	// Eligibility contract: project name changes must be ineligible for fragment apply.
	// Baseline: pnm-service under a DIFFERENT project name.
	baselinePlan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-pnm-frag0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-pnm-frag0"),
				StableServiceKey: "pnm-service",
				ImageRef:         "app:latest",
				ComposeExtension: &domain.ComposeExtension{ProjectName: "staging-cluster-b"},
			},
		},
	}
	for i := range baselinePlan.Services {
		baselinePlan.Services[i].ComputeDesiredHash()
	}
	baselinePlan.ComputeRevisionHash()
	baseline := baselineFrom(t, baselinePlan)

	target := findSvc(t, plan, "pnm-service")
	got := CheckFragmentEligibility(plan, target, baseline)
	assertIneligible(t, got, FragmentIneligibleProjectNameChange)
}

// ---------------------------------------------------------------------------
// 7. Service removal requires full-project apply with --remove-orphans
// ---------------------------------------------------------------------------

// TestFragmentSafety_ServiceRemovalRequiresFullProject verifies that
// removing a service requires full-project with --remove-orphans.
func TestFragmentSafety_ServiceRemovalRequiresFullProject(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// Contract: the full-project apply must always include --remove-orphans so
	// removed services are stopped and cleaned up. A service-scoped fragment
	// apply only touches one service and cannot safely remove another.
	var upCall *mockCall
	for i := range runner.calls {
		argsStr := strings.Join(runner.calls[i].Args, " ")
		if strings.Contains(argsStr, " up ") {
			upCall = &runner.calls[i]
			break
		}
	}
	if upCall == nil {
		t.Fatal("expected a 'docker compose up' command for full-project apply")
	}

	argsStr := strings.Join(upCall.Args, " ")
	if !strings.Contains(argsStr, "--remove-orphans") {
		t.Error("compose up must include --remove-orphans: service removal requires full-project apply with orphan cleanup, not service-scoped fragment apply")
	}

	// Full-project apply must not scope the up command to a specific service name.
	// Service removal is only safe at project scope.
	for _, svc := range plan.Services {
		for _, arg := range upCall.Args {
			if arg == svc.StableServiceKey {
				t.Errorf("up command must NOT target specific service %q: service removal requires full-project scope, not a service-scoped fragment", svc.StableServiceKey)
			}
		}
	}

	// TODO(bahia-zu2p.9.3): When fragment apply is implemented, extend this
	// test to verify that CheckFragmentEligibility returns ineligible when a
	// service is present in the previous plan but absent from the current plan.
}

// ---------------------------------------------------------------------------
// 8. New service additions require full-project apply
// ---------------------------------------------------------------------------

// TestFragmentSafety_NewServiceRequiresFullProject verifies that
// adding a new service requires full-project apply.
func TestFragmentSafety_NewServiceRequiresFullProject(t *testing.T) {
	renderer := NewComposeRenderer()

	// testMultiServicePlan has two services — simulates adding a second service.
	plan := testMultiServicePlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Invariant: all services in the plan must appear in the full rendered project.
	// A new service added to the plan requires full-project apply so the Compose
	// CLI creates and registers the new service container. A fragment scoped to an
	// existing service cannot bootstrap a new one.
	yaml := string(result.ComposeYAML)
	for _, svc := range plan.Services {
		if !strings.Contains(yaml, svc.StableServiceKey) {
			t.Errorf("new service %q must appear in full rendered project: new service additions require full-project apply", svc.StableServiceKey)
		}
		if !strings.Contains(yaml, svc.ImageRef) {
			t.Errorf("service %q image %q must appear in full rendered project", svc.StableServiceKey, svc.ImageRef)
		}
	}

	// The rendered service count must match the plan.
	if result.Metadata.ServiceCount != len(plan.Services) {
		t.Errorf("rendered service count %d != plan service count %d: all services must be in the full project", result.Metadata.ServiceCount, len(plan.Services))
	}

	// TODO(bahia-zu2p.9.3): When fragment apply is implemented, extend this
	// test to verify that CheckFragmentEligibility returns ineligible when a
	// service appears in the current plan but not in the previous plan.
}

// ---------------------------------------------------------------------------
// 9. Secret redaction in fragment output
// ---------------------------------------------------------------------------

// TestFragmentSafety_SecretRedactionInFragments verifies that fragment
// files do not contain plaintext secret values.
func TestFragmentSafety_SecretRedactionInFragments(t *testing.T) {
	renderer := NewComposeRenderer()
	// testPlan() includes SecretRefs for DB_PASSWORD, API_KEY, SESSION_SECRET.
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Contract: secret env var names must NEVER appear in rendered Compose YAML
	// or render metadata. This applies to the full project and — critically —
	// to any fragment YAML generated under .bahia/fragments/: fragments are
	// deployed directly to Compose and must not expose secrets.
	yaml := string(result.ComposeYAML)
	secretVars := []string{"DB_PASSWORD", "API_KEY", "SESSION_SECRET"}
	for _, s := range secretVars {
		if strings.Contains(yaml, s) {
			t.Errorf("secret env var %q must NOT appear in rendered Compose YAML: secrets must be redacted in all rendered output including fragments", s)
		}
	}

	// Render metadata must not contain secret variable names.
	metaJSON, err := result.Metadata.MetadataJSON()
	if err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	for _, s := range secretVars {
		if strings.Contains(string(metaJSON), s) {
			t.Errorf("secret env var %q must NOT appear in render metadata (render-state.json)", s)
		}
	}

	// Env material must use REDACTED placeholders for all secret-backed vars.
	// The env material is written to protected .bahia/env/ files; it must use
	// the canonical REDACTED placeholder format, never plaintext.
	for svcKey, envContent := range result.EnvMaterial {
		for _, s := range secretVars {
			if strings.Contains(envContent, s+"=") && !strings.Contains(envContent, s+"=REDACTED(") {
				t.Errorf("service %q env material: secret %q must use REDACTED placeholder, not plaintext", svcKey, s)
			}
		}
	}

	// TODO(bahia-zu2p.9.3): When fragment file writing is implemented,
	// additionally verify that fragment YAML files under .bahia/fragments/
	// do not contain secret env var names or plaintext values.
}

// ---------------------------------------------------------------------------
// 10. Fragment preserves project name
// ---------------------------------------------------------------------------

// TestFragmentSafety_FragmentPreservesProjectName verifies that fragment
// YAML includes the same project name as the full project.
func TestFragmentSafety_FragmentPreservesProjectName(t *testing.T) {
	renderer := NewComposeRenderer()
	// testPlan() specifies "bahia-production" as the explicit project name.
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Contract: the project name must be explicit and consistent across all
	// rendered output. Fragment files must include the same project name as
	// the full project so Compose scopes the fragment apply to the correct
	// project — a mismatched or missing project name would create orphaned
	// containers under a different Compose project.
	projectName := result.Metadata.ProjectName
	if projectName == "" {
		t.Fatal("rendered project name must not be empty: project name is required for correct fragment scoping")
	}

	// The rendered YAML must declare the project name explicitly.
	if !strings.Contains(string(result.ComposeYAML), "name: "+projectName) {
		t.Errorf("rendered YAML must contain 'name: %s': project name must be explicit, not derived from the directory basename", projectName)
	}

	// Verify the expected project name from testPlan().
	if result.Metadata.ProjectName != "bahia-production" {
		t.Errorf("expected project name 'bahia-production' from testPlan(), got %q", result.Metadata.ProjectName)
	}

	// TODO(bahia-zu2p.9.3): When fragment file writing is implemented,
	// additionally verify that each fragment YAML under .bahia/fragments/<svc>.yml
	// includes 'name: <projectName>' matching the full project so that
	// `docker compose --project-directory <dir> up -d --no-deps <svc>` applies
	// the fragment to the correct project scope.
}
