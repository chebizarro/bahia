package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// ComposeDesiredStateApplier — full-project desired-state apply for Compose
// ---------------------------------------------------------------------------

// ComposeDesiredStateApplier implements desired-state convergence for the
// Compose runtime. It selects the target deployment unit, renders that unit's
// full Compose project into canonical Compose YAML, validates through the
// executor seam, promotes to live files, and runs `docker compose up -d
// --remove-orphans` against the unit-owned project.
//
// Key design decisions:
//   - Unit-owned full-project apply: the target deployment unit is rendered and
//     applied as a single Compose project. No per-service image substitution or
//     service-scoped `up` commands.
//   - No <SERVICE>_IMAGE mutation: image references come from the rendered
//     Compose YAML, not environment variable overrides.
//   - No unconditional --force-recreate: Compose computes minimal changes
//     from the rendered project diff.
//   - Pull policy is forwarded as --pull <policy> to `docker compose up`.
//   - Fragment optimisation: when eligibility passes, a service-scoped fragment
//     overlay is applied first. The full-project path is always the fallback.
type ComposeDesiredStateApplier struct {
	runtime  *ComposeRuntime
	renderer *ComposeRenderer
	staging  *ComposeStagingManager
	runner   CommandRunner
	executor ComposeExecutor
	logger   *zap.Logger

	// fragmentEligibilityFn is the function used to check fragment eligibility.
	// When nil the production CheckFragmentEligibility is used.
	// Override in tests to control eligibility behaviour.
	fragmentEligibilityFn func(
		*domain.DesiredEnvironmentPlan,
		*domain.DesiredServiceSpec,
		*RenderMetadata,
	) *FragmentEligibility

	// fragmentRendererFn is the function used to render a service fragment.
	// When nil NewComposeFragmentRenderer().RenderServiceFragment is used.
	// Override in tests to inject synthetic fragment YAML.
	fragmentRendererFn func(projectName string, svc domain.DesiredServiceSpec) (*FragmentLayout, error)
}

// NewComposeDesiredStateApplier creates a new applier wired to the given
// Compose runtime. It uses the production command runner for staging
// validation, and selects the Compose executor (CLI compatibility mode or
// embedded Compose SDK) from the runtime's configured execution mode.
func NewComposeDesiredStateApplier(rt *ComposeRuntime, logger *zap.Logger) *ComposeDesiredStateApplier {
	runner := &execCommandRunner{}
	return &ComposeDesiredStateApplier{
		runtime:  rt,
		renderer: NewComposeRenderer(),
		staging:  NewComposeStagingManager(logger),
		runner:   runner,
		executor: newComposeExecutor(rt, runner, logger),
		logger:   logger,
	}
}

// newComposeExecutor selects the Compose executor for the runtime's
// configured execution mode: the embedded Compose v5 SDK for
// execution_mode=sdk, the CLI compatibility path otherwise.
func newComposeExecutor(rt *ComposeRuntime, runner CommandRunner, logger *zap.Logger) ComposeExecutor {
	if rt != nil && rt.ExecutionMode() == ExecutionModeSDK {
		return NewSDKComposeExecutor(rt, logger)
	}
	return NewCLIComposeExecutor(rt, runner, logger)
}

// NewComposeDesiredStateApplierWithRunner creates an applier with a custom
// CommandRunner, useful for testing without Docker.
func NewComposeDesiredStateApplierWithRunner(rt *ComposeRuntime, runner CommandRunner, logger *zap.Logger) *ComposeDesiredStateApplier {
	return &ComposeDesiredStateApplier{
		runtime:  rt,
		renderer: NewComposeRenderer(),
		staging:  NewComposeStagingManagerWithRunner(logger, runner),
		runner:   runner,
		executor: newComposeExecutor(rt, runner, logger),
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — DesiredStateApplier implementation for Compose
// ---------------------------------------------------------------------------

// ApplyDesiredState converges the Compose project to match the desired
// environment plan. The flow is:
//
//  1. Validate ownership of the unit-owned compose directory.
//  2. Select and render the target deployment unit into canonical Compose YAML.
//  3. Stage rendered files under .bahia/staging/.
//  4. Validate staged output through the ComposeExecutor control seam.
//  5. Atomically promote staged files to live locations.
//  6. Run `docker compose --project-directory <dir> up -d --remove-orphans`
//     with pull policy.
//  7. Return apply result with resource names and observation hints.
//
// Secrets from req.Secrets are NOT injected into the Compose YAML or env
// material at this stage — secret resolution happens at the env-file level
// during staging.
func (a *ComposeDesiredStateApplier) ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error) {
	if req.EnvironmentPlan == nil {
		return nil, fmt.Errorf("compose desired-state apply: environment plan is nil")
	}
	if req.TargetService == nil {
		return nil, fmt.Errorf("compose desired-state apply: target service is nil")
	}

	composeDir := a.runtime.projectDir
	executionMode := a.executor.ExecutionMode()

	// Step 1: Validate ownership.
	if err := a.runtime.ValidateOwnership(ComposeOwnershipConfig{}); err != nil {
		return nil, fmt.Errorf("compose desired-state apply blocked: %w", err)
	}

	a.logger.Info("compose desired-state apply: starting",
		zap.String("compose_dir", composeDir),
		zap.String("environment_id", req.EnvironmentPlan.EnvironmentID.String()),
		zap.String("target_service", req.TargetService.StableServiceKey),
		zap.Int("service_count", len(req.EnvironmentPlan.Services)),
	)

	// Step 2: Select the target deployment unit plan.
	unitPlan, err := selectComposeDeploymentUnitPlan(req.EnvironmentPlan, req.TargetService)
	if err != nil {
		return nil, fmt.Errorf("compose desired-state apply: unit selection failed: %w", err)
	}

	// Step 2a: Try the fragment apply optimisation (service-scoped overlay).
	// Returns (result, nil) on success, (nil, nil) to fall through, or (nil, err)
	// on hard failure. Any failure falls through to the full-project path.
	if fragmentResult, fragmentErr := a.tryFragmentApply(ctx, req, unitPlan); fragmentErr != nil {
		a.logger.Warn("compose desired-state apply: fragment path error, falling through to full-project",
			zap.Error(fragmentErr))
	} else if fragmentResult != nil {
		return fragmentResult, nil
	}

	// Step 3: Render the full project owned by the target deployment unit.
	renderResult, err := a.renderer.RenderDeploymentUnitPlan(ctx, req.EnvironmentPlan.EnvironmentID.String(), unitPlan)
	if err != nil {
		return nil, fmt.Errorf("compose desired-state apply: render failed: %w", err)
	}

	// Step 3–4: Stage rendered files and validate through the executor seam.
	staged, err := a.staging.Stage(ctx, composeDir, renderResult)
	if err != nil {
		a.staging.Rollback(ctx, staged)
		return nil, fmt.Errorf("compose desired-state apply: stage failed: %w", err)
	}
	if _, _, err := a.executor.Validate(ctx, staged); err != nil {
		a.staging.Rollback(ctx, staged)
		return nil, fmt.Errorf("compose desired-state apply: validation failed: %w", err)
	}

	// Dry run stops after validation — do not promote or run up.
	if req.DryRun {
		a.staging.Rollback(ctx, staged)

		serviceKeys := composeUnitServiceKeys(unitPlan)

		return &DesiredStateApplyResult{
			Renderer:            "compose",
			ExecutionMode:       executionMode,
			DesiredHash:         req.TargetService.DesiredHash,
			EnvironmentRevision: unitPlan.RevisionHash,
			ResourceNames:       serviceKeys,
			Warnings:            []string{"dry-run: staged and validated but not applied"},
		}, nil
	}

	// Step 5: Promote staged files to live locations.
	if err := a.staging.Promote(ctx, staged); err != nil {
		return nil, fmt.Errorf("compose desired-state apply: promote failed: %w", err)
	}

	// Step 6: Run docker compose up -d --remove-orphans.
	if err := a.composeUp(ctx, composeDir, req.PullPolicy); err != nil {
		return nil, fmt.Errorf("compose desired-state apply: up failed: %w", err)
	}

	// Step 7: Build result.
	serviceKeys := composeUnitServiceKeys(unitPlan)

	a.logger.Info("compose desired-state apply: completed",
		zap.String("compose_dir", composeDir),
		zap.String("revision_hash", req.EnvironmentPlan.RevisionHash),
		zap.Int("services_applied", len(serviceKeys)),
	)

	return &DesiredStateApplyResult{
		Renderer:            "compose",
		ExecutionMode:       executionMode,
		DesiredHash:         req.TargetService.DesiredHash,
		EnvironmentRevision: unitPlan.RevisionHash,
		ResourceNames:       serviceKeys,
		ObservationHints:    &ObservationHints{},
	}, nil
}

// ---------------------------------------------------------------------------
// Deployment-unit selection
// ---------------------------------------------------------------------------

func selectComposeDeploymentUnitPlan(plan *domain.DesiredEnvironmentPlan, target *domain.DesiredServiceSpec) (*domain.DesiredDeploymentUnitPlan, error) {
	if plan == nil {
		return nil, fmt.Errorf("environment plan is nil")
	}
	if target == nil {
		return nil, fmt.Errorf("target service is nil")
	}
	plan.NormalizeUnitIdentity()
	plan.GroupByDeploymentUnit()
	targetKey := composeUnitIdentityKey(target.DeploymentUnitID, target.DeploymentUnitKey)
	for i := range plan.UnitPlans {
		unit := &plan.UnitPlans[i]
		if composeUnitIdentityKey(unit.DeploymentUnitID, unit.DeploymentUnitKey) != targetKey {
			continue
		}
		if unit.RuntimeType != "" && unit.RuntimeType != domain.RuntimeTypeCompose {
			return nil, fmt.Errorf("target unit %q runtime type %q is not compose", unit.DeploymentUnitKey, unit.RuntimeType)
		}
		for _, svc := range unit.Services {
			if svc.UnitRuntimeType != "" && svc.UnitRuntimeType != domain.RuntimeTypeCompose {
				return nil, fmt.Errorf("service %q runtime type %q is not compose", svc.StableServiceKey, svc.UnitRuntimeType)
			}
		}
		return unit, nil
	}
	return nil, fmt.Errorf("target service unit %q not present in environment plan", targetKey)
}

func composeUnitIdentityKey(unitID *uuid.UUID, unitKey string) string {
	if unitID != nil && *unitID != uuid.Nil {
		return "id:" + unitID.String()
	}
	unitKey = strings.TrimSpace(unitKey)
	if unitKey == "" {
		unitKey = domain.DefaultDeploymentUnitKey
	}
	return "key:" + unitKey
}

func composeUnitServiceKeys(unitPlan *domain.DesiredDeploymentUnitPlan) []string {
	if unitPlan == nil {
		return nil
	}
	serviceKeys := make([]string, 0, len(unitPlan.Services))
	for _, svc := range unitPlan.Services {
		serviceKeys = append(serviceKeys, svc.StableServiceKey)
	}
	return serviceKeys
}

// ---------------------------------------------------------------------------
// composeUp — full-project docker compose up
// ---------------------------------------------------------------------------

// composeUp runs `docker compose --project-directory <dir> up -d --remove-orphans`
// with optional pull policy. This is the ONLY mutating Compose command in the
// desired-state path — no service-scoped up, no --force-recreate, no
// <SERVICE>_IMAGE env overrides.
func (a *ComposeDesiredStateApplier) composeUp(ctx context.Context, composeDir string, pullPolicy string) error {
	// Run without any <SERVICE>_IMAGE env overrides — the rendered
	// docker-compose.yml is the sole source of truth.
	stdout, stderr, err := a.executor.Up(ctx, composeDir, pullPolicy)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			return fmt.Errorf("docker compose up: %s: %w", detail, err)
		}
		return fmt.Errorf("docker compose up: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Pull policy normalization
// ---------------------------------------------------------------------------

// normalizeComposePullPolicy maps pull policy values to Docker Compose --pull
// flag values. Empty or unrecognized values return "" (omit the flag, letting
// Compose use its default behavior).
//
// Docker Compose supports: always, never, missing (default), build.
func normalizeComposePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return "always"
	case "never":
		return "never"
	case "missing", "if-not-present", "ifnotpresent":
		return "missing"
	default:
		return ""
	}
}
