package runtime

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// ComposeDesiredStateApplier — full-project desired-state apply for Compose
// ---------------------------------------------------------------------------

// ComposeDesiredStateApplier implements desired-state convergence for the
// Compose runtime. It renders a full environment plan into canonical Compose
// YAML, stages and validates the output, promotes to live files, and runs
// `docker compose up -d --remove-orphans` against the full project.
//
// Key design decisions:
//   - Full-project apply: the entire environment plan is rendered and applied
//     as a single Compose project. No per-service image substitution or
//     service-scoped `up` commands.
//   - No <SERVICE>_IMAGE mutation: image references come from the rendered
//     Compose YAML, not environment variable overrides.
//   - No unconditional --force-recreate: Compose computes minimal changes
//     from the rendered project diff.
//   - Pull policy is forwarded as --pull <policy> to `docker compose up`.
type ComposeDesiredStateApplier struct {
	runtime  *ComposeRuntime
	renderer *ComposeRenderer
	staging  *ComposeStagingManager
	runner   CommandRunner
	executor ComposeExecutor
	logger   *zap.Logger
}

// NewComposeDesiredStateApplier creates a new applier wired to the given
// Compose runtime. It uses the production command runner for staging
// validation and Compose CLI execution.
func NewComposeDesiredStateApplier(rt *ComposeRuntime, logger *zap.Logger) *ComposeDesiredStateApplier {
	runner := &execCommandRunner{}
	return &ComposeDesiredStateApplier{
		runtime:  rt,
		renderer: NewComposeRenderer(),
		staging:  NewComposeStagingManager(logger),
		runner:   runner,
		executor: NewCLIComposeExecutor(rt, runner, logger),
		logger:   logger,
	}
}

// NewComposeDesiredStateApplierWithRunner creates an applier with a custom
// CommandRunner, useful for testing without Docker.
func NewComposeDesiredStateApplierWithRunner(rt *ComposeRuntime, runner CommandRunner, logger *zap.Logger) *ComposeDesiredStateApplier {
	return &ComposeDesiredStateApplier{
		runtime:  rt,
		renderer: NewComposeRenderer(),
		staging:  NewComposeStagingManagerWithRunner(logger, runner),
		runner:   runner,
		executor: NewCLIComposeExecutor(rt, runner, logger),
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// ApplyDesiredState — DesiredStateApplier implementation for Compose
// ---------------------------------------------------------------------------

// ApplyDesiredState converges the Compose project to match the desired
// environment plan. The flow is:
//
//  1. Validate ownership of the compose directory.
//  2. Render the full environment plan into canonical Compose YAML.
//  3. Stage rendered files under .bahia/staging/.
//  4. Validate staged output with `docker compose config -q`.
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

	// Step 2: Render the full environment plan.
	renderResult, err := a.renderer.RenderEnvironmentPlan(ctx, req.EnvironmentPlan)
	if err != nil {
		return nil, fmt.Errorf("compose desired-state apply: render failed: %w", err)
	}

	// Step 3–4: Stage and validate.
	staged, err := a.staging.StageAndValidate(ctx, composeDir, renderResult)
	if err != nil {
		// Clean up staging on failure.
		a.staging.Rollback(ctx, staged)
		return nil, fmt.Errorf("compose desired-state apply: stage/validate failed: %w", err)
	}

	// Dry run stops after validation — do not promote or run up.
	if req.DryRun {
		a.staging.Rollback(ctx, staged)

		serviceKeys := make([]string, 0, len(req.EnvironmentPlan.Services))
		for _, svc := range req.EnvironmentPlan.Services {
			serviceKeys = append(serviceKeys, svc.StableServiceKey)
		}

		return &DesiredStateApplyResult{
			Renderer:            "compose",
			ExecutionMode:       executionMode,
			DesiredHash:         req.TargetService.DesiredHash,
			EnvironmentRevision: req.EnvironmentPlan.RevisionHash,
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
	serviceKeys := make([]string, 0, len(req.EnvironmentPlan.Services))
	for _, svc := range req.EnvironmentPlan.Services {
		serviceKeys = append(serviceKeys, svc.StableServiceKey)
	}

	a.logger.Info("compose desired-state apply: completed",
		zap.String("compose_dir", composeDir),
		zap.String("revision_hash", req.EnvironmentPlan.RevisionHash),
		zap.Int("services_applied", len(serviceKeys)),
	)

	return &DesiredStateApplyResult{
		Renderer:            "compose",
		ExecutionMode:       executionMode,
		DesiredHash:         req.TargetService.DesiredHash,
		EnvironmentRevision: req.EnvironmentPlan.RevisionHash,
		ResourceNames:       serviceKeys,
		ObservationHints:    &ObservationHints{},
	}, nil
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
