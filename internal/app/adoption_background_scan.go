package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const adoptionBackgroundScanCheck = "adoption_background_scan"

// newAdoptionBackgroundScanRunner builds the background adoption scan runner
// from configuration. It returns nil when background scanning is disabled or
// there is no adoption service. The caller registers it only when the Nostr
// projector publishes, because the aggregates are its only output.
//
// Explicitly configured targets must resolve (config error otherwise).
// Targets derived from runtime.endpoints never block startup: an alias that
// cannot be scanned, or that collides with another after normalization, is
// skipped and reported as a health warning.
func newAdoptionBackgroundScanRunner(cfg *config.Config, scanner *service.AdoptionService, logger *zap.Logger) (*service.AdoptionBackgroundScanRunner, error) {
	if cfg == nil || scanner == nil || !cfg.Adoption.BackgroundScanEnabled() {
		return nil, nil
	}
	explicit := len(cfg.Adoption.BackgroundScan.Targets) > 0
	configured := cfg.BackgroundScanTargets()
	targets := make([]service.AdoptionTarget, 0, len(configured))
	var skipped []string
	seen := map[string]string{}
	for _, target := range configured {
		// Resolve one at a time with the adoption scan's own normalization,
		// so published coordinates match operator scans of the same alias.
		resolved, err := scanner.ResolveScanTargets([]service.AdoptionTarget{{Name: target.Name, EndpointRef: target.EndpointRef, EnvironmentName: target.Environment}})
		if err != nil {
			if explicit {
				return nil, fmt.Errorf("resolve background adoption scan target %q: %w", target.Name, err)
			}
			logger.Warn("background adoption scan endpoint skipped: not a valid scan target", zap.String("endpoint_ref", target.EndpointRef), zap.Error(err))
			skipped = append(skipped, target.Name)
			continue
		}
		if previous, dup := seen[resolved[0].Name]; dup {
			if explicit {
				return nil, fmt.Errorf("background adoption scan targets %q and %q normalize to the same target %q", previous, target.Name, resolved[0].Name)
			}
			logger.Warn("background adoption scan endpoint skipped: normalized name collides; list both under adoption.background_scan.targets with distinct names",
				zap.String("endpoint_ref", target.EndpointRef), zap.String("collides_with", previous), zap.String("normalized", resolved[0].Name))
			skipped = append(skipped, target.Name)
			continue
		}
		seen[resolved[0].Name] = target.Name
		targets = append(targets, resolved[0])
	}
	bg := cfg.Adoption.BackgroundScan
	runner, err := service.NewAdoptionBackgroundScanRunner(scanner, service.AdoptionBackgroundScanConfig{
		Targets:        targets,
		SkippedTargets: skipped,
		Interval:       bg.Interval,
		Jitter:         bg.Jitter,
		Timeout:        bg.Timeout,
		Concurrency:    bg.Concurrency,
		MaxBackoff:     bg.MaxBackoff,
	}, logger)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

// registerAdoptionBackgroundScanHealthCheck exposes runner progress. A target
// that keeps failing is a warning (degraded), never a readiness failure: the
// aggregates it feeds are informational and age into "stale" on their own.
func registerAdoptionBackgroundScanHealthCheck(provider *HealthProvider, runner *service.AdoptionBackgroundScanRunner, tier Tier) {
	if provider == nil || runner == nil {
		return
	}
	provider.RegisterCheck(adoptionBackgroundScanCheck, int(tier), func() HealthCheck {
		return adoptionBackgroundScanHealth(runner.Status(), tier)
	})
}

func adoptionBackgroundScanHealth(status service.AdoptionBackgroundScanStatus, tier Tier) HealthCheck {
	check := HealthCheck{
		Name:    adoptionBackgroundScanCheck,
		Status:  HealthStatusPass,
		Message: fmt.Sprintf("%d target(s) scanned in the background", len(status.Targets)),
		Tier:    int(tier),
		Details: map[string]string{
			"targets":         strconv.Itoa(len(status.Targets)),
			"failing":         strconv.Itoa(status.Failing),
			"cycles":          strconv.FormatInt(status.Cycles, 10),
			"overlap_skipped": strconv.FormatInt(status.OverlapSkipped, 10),
			"last_cycle_at":   "",
		},
	}
	if !status.LastCycleAt.IsZero() {
		check.Details["last_cycle_at"] = status.LastCycleAt.UTC().Format(time.RFC3339)
	}
	// Target aliases and outcome codes only; raw scan errors can carry
	// Docker hosts and stay in logs.
	for _, target := range status.Targets {
		check.Details["target."+target.Target] = target.Outcome
	}
	if len(status.SkippedTargets) > 0 {
		check.Details["skipped"] = strings.Join(status.SkippedTargets, ",")
	}
	switch {
	case status.Failing > 0:
		check.Status = HealthStatusWarn
		check.Message = fmt.Sprintf("%d of %d background scan target(s) failing; affected aggregates age into stale", status.Failing, len(status.Targets))
	case len(status.SkippedTargets) > 0:
		check.Status = HealthStatusWarn
		check.Message = fmt.Sprintf("%d runtime endpoint(s) not scanned in the background; see logs", len(status.SkippedTargets))
	}
	return check
}
