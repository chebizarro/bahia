package app

import (
	"fmt"
	"strconv"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const adoptionBackgroundScanCheck = "adoption_background_scan"

// newAdoptionBackgroundScanRunner builds the background adoption scan runner
// from configuration. It returns nil when background scanning is disabled, or
// when there is no adoption service or publishing projector to feed.
func newAdoptionBackgroundScanRunner(cfg *config.Config, scanner *service.AdoptionService, retirer service.RuntimeTargetScanRetirer, projecting bool, logger *zap.Logger) (*service.AdoptionBackgroundScanRunner, error) {
	if cfg == nil || !cfg.Adoption.BackgroundScanEnabled() {
		return nil, nil
	}
	if scanner == nil || retirer == nil || !projecting {
		logger.Info("background adoption scans inactive: nostr inventory projection is not publishing")
		return nil, nil
	}
	configured := cfg.BackgroundScanTargets()
	targets := make([]service.AdoptionTarget, 0, len(configured))
	seen := map[string]string{}
	for _, target := range configured {
		// Resolve one at a time with the adoption scan's own normalization,
		// so published coordinates match operator scans of the same alias.
		resolved, err := scanner.ResolveScanTargets([]service.AdoptionTarget{{Name: target.Name, EndpointRef: target.EndpointRef, EnvironmentName: target.Environment}})
		if err != nil {
			return nil, fmt.Errorf("resolve background adoption scan target %q: %w", target.Name, err)
		}
		if previous, dup := seen[resolved[0].Name]; dup {
			// Aliases such as edge_01 and edge-01 normalize to one target;
			// scan it once rather than refusing to start.
			logger.Warn("background adoption scan target skipped: normalized name collides",
				zap.String("target", target.Name), zap.String("collides_with", previous), zap.String("normalized", resolved[0].Name))
			continue
		}
		seen[resolved[0].Name] = target.Name
		targets = append(targets, resolved[0])
	}
	bg := cfg.Adoption.BackgroundScan
	runner, err := service.NewAdoptionBackgroundScanRunner(scanner, retirer, service.AdoptionBackgroundScanConfig{
		Targets:     targets,
		Interval:    bg.Interval,
		Jitter:      bg.Jitter,
		Timeout:     bg.Timeout,
		Concurrency: bg.Concurrency,
		MaxBackoff:  bg.MaxBackoff,
	}, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("background adoption scans enabled",
		zap.Int("targets", len(targets)),
		zap.Duration("interval", bg.Interval),
		zap.Duration("jitter", bg.Jitter),
		zap.Duration("timeout", bg.Timeout),
		zap.Int("concurrency", bg.Concurrency),
		zap.Duration("max_backoff", bg.MaxBackoff),
	)
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
	if status.Failing > 0 {
		check.Status = HealthStatusWarn
		check.Message = fmt.Sprintf("%d of %d background scan target(s) failing; affected aggregates age into stale", status.Failing, len(status.Targets))
	}
	return check
}
