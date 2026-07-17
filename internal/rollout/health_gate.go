package rollout

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// HealthGate polls a runtime observer to determine if a service is healthy.
// It returns a pass/fail result with details.
type HealthGate struct {
	observer runtime.Observer
	logger   *zap.Logger
}

// NewHealthGate creates a new HealthGate.
func NewHealthGate(observer runtime.Observer, logger *zap.Logger) *HealthGate {
	return &HealthGate{
		observer: observer,
		logger:   logger,
	}
}

// HealthCheckResult captures the outcome of a health gate evaluation.
type HealthCheckResult struct {
	Passed          bool                `json:"passed"`
	TotalChecks     int                 `json:"total_checks"`
	HealthyChecks   int                 `json:"healthy_checks"`
	UnhealthyChecks int                 `json:"unhealthy_checks"`
	LastHealth      domain.HealthStatus `json:"last_health"`
	Duration        time.Duration       `json:"duration"`
	Error           string              `json:"error,omitempty"`
}

// Check runs health polling for the configured duration.
func (g *HealthGate) Check(ctx context.Context, serviceID, envID uuid.UUID, serviceName string, cfg domain.HealthGateConfig) (*HealthCheckResult, error) {
	if cfg.Interval == 0 {
		cfg = domain.DefaultHealthGate()
	}

	result := &HealthCheckResult{}
	start := time.Now()
	deadline := start.Add(cfg.Timeout)

	consecutiveHealthy := 0
	consecutiveUnhealthy := 0

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			result.Error = "context cancelled"
			return result, ctx.Err()

		case t := <-ticker.C:
			if t.After(deadline) {
				result.Duration = time.Since(start)
				result.Error = fmt.Sprintf("timeout after %s", cfg.Timeout)
				return result, nil
			}

			obs, err := g.observer.Observe(ctx, serviceID, envID, serviceName)
			if err != nil {
				g.logger.Warn("health check observation failed",
					zap.String("service", serviceName),
					zap.Error(err),
				)
				consecutiveUnhealthy++
				consecutiveHealthy = 0
				result.UnhealthyChecks++
				result.TotalChecks++
				result.Error = err.Error()
				if consecutiveUnhealthy >= cfg.FailureThreshold {
					result.Duration = time.Since(start)
					result.Error = fmt.Sprintf("failed after %d consecutive observation errors: %v", consecutiveUnhealthy, err)
					return result, nil
				}
				continue
			}

			result.TotalChecks++
			result.LastHealth = obs.HealthStatus

			if obs.HealthStatus == domain.HealthStatusHealthy {
				consecutiveHealthy++
				consecutiveUnhealthy = 0
				result.HealthyChecks++

				if consecutiveHealthy >= cfg.SuccessThreshold {
					result.Passed = true
					result.Duration = time.Since(start)
					g.logger.Info("health gate passed",
						zap.String("service", serviceName),
						zap.Int("consecutive_healthy", consecutiveHealthy),
					)
					return result, nil
				}
			} else {
				consecutiveUnhealthy++
				consecutiveHealthy = 0
				result.UnhealthyChecks++

				if consecutiveUnhealthy >= cfg.FailureThreshold {
					result.Passed = false
					result.Duration = time.Since(start)
					result.Error = fmt.Sprintf("failed after %d consecutive unhealthy checks (status: %s)",
						consecutiveUnhealthy, obs.HealthStatus)
					g.logger.Warn("health gate failed",
						zap.String("service", serviceName),
						zap.Int("consecutive_unhealthy", consecutiveUnhealthy),
						zap.String("last_status", string(obs.HealthStatus)),
					)
					return result, nil
				}
			}
		}
	}
}
