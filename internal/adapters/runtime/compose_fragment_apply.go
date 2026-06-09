package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// Note: bahiaFragmentsDir is declared in compose_fragment_layout.go.

// ---------------------------------------------------------------------------
// LoadRenderMetadata — load baseline render-state.json
// ---------------------------------------------------------------------------

// LoadRenderMetadata reads and parses the render-state.json from a compose
// directory's .bahia/ marker directory.
//
// Returns (nil, nil) when the file does not exist — this indicates a first
// render and the caller should fall through to the full-project path.
// Returns (nil, err) if the file exists but cannot be read or parsed.
func LoadRenderMetadata(composeDir string) (*RenderMetadata, error) {
	path := filepath.Join(composeDir, bahiaMarkerDir, bahiaRenderStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // first render — no baseline
		}
		return nil, fmt.Errorf("load render metadata: read %s: %w", path, err)
	}
	var metadata RenderMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("load render metadata: parse %s: %w", path, err)
	}
	return &metadata, nil
}

// ---------------------------------------------------------------------------
// tryFragmentApply — service-scoped Compose fragment optimisation
// ---------------------------------------------------------------------------

// tryFragmentApply attempts a service-scoped Compose fragment apply instead of
// the full-project `docker compose up -d --remove-orphans`.
//
// Return semantics:
//   - (result, nil)  → fragment apply succeeded; return this result to the caller.
//   - (nil, nil)     → fragment path not taken (ineligible, no baseline, or any
//     non-fatal soft failure); caller should fall through to full-project apply.
//   - (nil, err)     → hard failure; caller logs and falls through to full-project.
//
// The fragment apply NEVER blocks the full-project path — any problem silently
// falls through via (nil, nil).
func (a *ComposeDesiredStateApplier) tryFragmentApply(
	ctx context.Context,
	req DesiredStateApplyRequest,
	unitPlan *domain.DesiredDeploymentUnitPlan,
) (*DesiredStateApplyResult, error) {
	composeDir, err := filepath.Abs(a.runtime.projectDir)
	if err != nil {
		a.logger.Warn("compose fragment apply: cannot resolve compose dir, falling back",
			zap.Error(err))
		return nil, nil
	}

	// --- Step 1: Load baseline render-state.json. ---
	baseline, err := LoadRenderMetadata(composeDir)
	if err != nil {
		a.logger.Warn("compose fragment apply: failed to load baseline metadata, falling back",
			zap.Error(err))
		return nil, nil
	}
	if baseline == nil {
		a.logger.Info("compose fragment apply: no baseline metadata (first render), using full-project path")
		return nil, nil
	}

	// --- Step 2: Check fragment eligibility. ---
	checker := a.fragmentEligibilityFn
	if checker == nil {
		checker = CheckFragmentEligibility
	}
	elig := checker(req.EnvironmentPlan, req.TargetService, baseline)
	if !elig.Eligible {
		a.logger.Info("compose fragment apply: ineligible, using full-project path",
			zap.String("reason", elig.Reason),
			zap.String("reason_code", string(elig.ReasonCode)),
		)
		return nil, nil
	}

	serviceKey := req.TargetService.StableServiceKey

	// --- Step 3: Render the service fragment. ---
	// fragmentRendererFn matches RenderServiceFragment's signature:
	// func(projectName string, svc domain.DesiredServiceSpec) (*FragmentLayout, error)
	rendererFn := a.fragmentRendererFn
	if rendererFn == nil {
		rendererFn = NewComposeFragmentRenderer().RenderServiceFragment
	}
	projectName := deriveProjectName(req.EnvironmentPlan)
	rendered, err := rendererFn(projectName, *req.TargetService)
	if err != nil {
		a.logger.Warn("compose fragment apply: render failed, falling back to full-project",
			zap.String("service", serviceKey),
			zap.Error(err))
		return nil, nil
	}

	// --- Step 4: Write fragment to .bahia/fragments/<service-key>.yml ---
	// NewFragmentLayout provides canonical filesystem paths; the rendered YAML
	// comes from the renderer result's FragmentYAML field.
	layout := NewFragmentLayout(composeDir, serviceKey)
	if err := os.MkdirAll(layout.FragmentDir, 0o755); err != nil {
		a.logger.Warn("compose fragment apply: failed to create fragments dir, falling back",
			zap.Error(err))
		return nil, nil
	}
	if err := os.WriteFile(layout.FragmentFile, rendered.FragmentYAML, 0o644); err != nil {
		a.logger.Warn("compose fragment apply: failed to write fragment file, falling back",
			zap.String("fragment_file", layout.FragmentFile),
			zap.Error(err))
		return nil, nil
	}

	// --- Step 5: Validate merged full-project + fragment. ---
	if _, _, err := a.executor.ValidateWithFragment(ctx, composeDir, layout.FragmentFile); err != nil {
		a.logger.Warn("compose fragment apply: merged validation failed, falling back to full-project",
			zap.String("service", serviceKey),
			zap.Error(err))
		_ = os.Remove(layout.FragmentFile)
		return nil, nil
	}

	executionMode := a.executor.ExecutionMode()

	// Dry run stops after validation — report fragment would be used, then stop.
	if req.DryRun {
		_ = os.Remove(layout.FragmentFile)
		serviceKeys := composeUnitServiceKeys(unitPlan)
		return &DesiredStateApplyResult{
			Renderer:            "compose",
			ExecutionMode:       executionMode,
			DesiredHash:         req.TargetService.DesiredHash,
			EnvironmentRevision: unitPlan.RevisionHash,
			ResourceNames:       serviceKeys,
			Warnings:            []string{"dry-run: fragment would be applied for service " + serviceKey},
		}, nil
	}

	// --- Step 6: Apply the service fragment. ---
	if _, _, err := a.executor.UpService(ctx, composeDir, layout.FragmentFile, serviceKey, req.PullPolicy); err != nil {
		a.logger.Warn("compose fragment apply: up service failed, falling back to full-project",
			zap.String("service", serviceKey),
			zap.Error(err))
		_ = os.Remove(layout.FragmentFile)
		return nil, nil
	}

	// --- Step 7: Sync full docker-compose.yml and render-state.json. ---
	// The fragment apply already succeeded; keep the full project current so
	// subsequent baseline checks and full-project applies are consistent.
	a.syncFullProject(ctx, composeDir, req.EnvironmentPlan.EnvironmentID.String(), unitPlan)

	serviceKeys := composeUnitServiceKeys(unitPlan)
	a.logger.Info("compose fragment apply: completed",
		zap.String("compose_dir", composeDir),
		zap.String("service", serviceKey),
	)

	return &DesiredStateApplyResult{
		Renderer:            "compose",
		ExecutionMode:       executionMode,
		DesiredHash:         req.TargetService.DesiredHash,
		EnvironmentRevision: unitPlan.RevisionHash,
		ResourceNames:       serviceKeys,
		ObservationHints:    &ObservationHints{},
		Warnings:            []string{"fragment: applied service " + serviceKey + " via Compose fragment overlay"},
	}, nil
}

// ---------------------------------------------------------------------------
// syncFullProject — post-fragment full-project sync
// ---------------------------------------------------------------------------

// syncFullProject updates the live docker-compose.yml and render-state.json
// after a successful fragment apply. This keeps the full project state current
// so subsequent applies and baseline checks are consistent.
//
// All errors are logged but not returned — the fragment apply has already
// succeeded and the full-project sync is a best-effort consistency operation.
func (a *ComposeDesiredStateApplier) syncFullProject(
	ctx context.Context,
	composeDir string,
	environmentID string,
	unitPlan *domain.DesiredDeploymentUnitPlan,
) {
	renderResult, err := a.renderer.RenderDeploymentUnitPlan(ctx, environmentID, unitPlan)
	if err != nil {
		a.logger.Warn("compose fragment apply: full-project sync render failed",
			zap.String("compose_dir", composeDir),
			zap.Error(err))
		return
	}

	staged, err := a.staging.Stage(ctx, composeDir, renderResult)
	if err != nil {
		a.logger.Warn("compose fragment apply: full-project sync stage failed",
			zap.Error(err))
		a.staging.Rollback(ctx, staged)
		return
	}

	if _, _, err := a.executor.Validate(ctx, staged); err != nil {
		a.logger.Warn("compose fragment apply: full-project sync validate failed",
			zap.Error(err))
		a.staging.Rollback(ctx, staged)
		return
	}

	if err := a.staging.Promote(ctx, staged); err != nil {
		a.logger.Warn("compose fragment apply: full-project sync promote failed",
			zap.Error(err))
		return
	}

	a.logger.Info("compose fragment apply: full-project sync completed",
		zap.String("compose_dir", composeDir),
	)
}
