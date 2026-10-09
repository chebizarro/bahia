package app

import "go.uber.org/zap"

// registerSQLWorkflowRecoveryDegraded makes the disabled SQL-backed recovery
// path visible without allowing an index row to authorize work at boot.
func registerSQLWorkflowRecoveryDegraded(health *HealthProvider, logger *zap.Logger, name, reason string) {
	logger.Warn("automatic workflow recovery disabled", zap.String("workflow", name), zap.String("reason", reason))
	health.RegisterCheck(name, func() HealthCheck {
		return HealthCheck{
			Name:    name,
			Status:  HealthStatusWarn,
			Message: reason + "; SQL-only work is retained but not executed automatically",
			Details: map[string]string{
				"recovery_source": "canonical_signed_intent_unavailable",
				"operator_action": "inspect retained SQL work; do not replay it without verified signed intent provenance",
			},
		}
	})
}
