package domain

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// InstanceHealthStatus is the observed operating state of a managed runtime instance.
type InstanceHealthStatus string

const (
	InstanceHealthStatusHealthy        InstanceHealthStatus = "healthy"
	InstanceHealthStatusRunning        InstanceHealthStatus = "running"
	InstanceHealthStatusDegraded       InstanceHealthStatus = "degraded"
	InstanceHealthStatusStopped        InstanceHealthStatus = "stopped"
	InstanceHealthStatusUnhealthy      InstanceHealthStatus = "unhealthy"
	InstanceHealthStatusOOMKilled      InstanceHealthStatus = "oom_killed"
	InstanceHealthStatusRestartLoop    InstanceHealthStatus = "restart_loop"
	InstanceHealthStatusUnknown        InstanceHealthStatus = "unknown"
	InstanceHealthStatusManualOverride InstanceHealthStatus = "manual_override"
)

// InstanceSupervisorType identifies the runtime control mechanism for an instance.
type InstanceSupervisorType string

const (
	InstanceSupervisorDocker      InstanceSupervisorType = "docker"
	InstanceSupervisorCompose     InstanceSupervisorType = "compose"
	InstanceSupervisorSystemd     InstanceSupervisorType = "systemd"
	InstanceSupervisorUserSystemd InstanceSupervisorType = "user-systemd"
)

// ManagedInstanceKey uniquely identifies one managed runtime target.
type ManagedInstanceKey struct {
	ServiceID         uuid.UUID `json:"service_id"`
	EnvironmentID     uuid.UUID `json:"environment_id"`
	DeploymentUnitID  uuid.UUID `json:"deployment_unit_id"`
	RuntimeTargetName string    `json:"runtime_target_name"`
}

// ManagedInstanceHealth is the current-state read model for one managed runtime target.
type ManagedInstanceHealth struct {
	ManagedInstanceKey
	Host                    string                 `json:"host"`
	SupervisorType          InstanceSupervisorType `json:"supervisor_type"`
	Status                  InstanceHealthStatus   `json:"status"`
	FailureReason           string                 `json:"failure_reason,omitempty"`
	LastObservedAt          time.Time              `json:"last_observed_at"`
	RestartCount            int                    `json:"restart_count"`
	ConsecutiveRestartCount int                    `json:"consecutive_restart_count"`
	MemoryCurrentBytes      int64                  `json:"memory_current_bytes"`
	MemoryPeakBytes         int64                  `json:"memory_peak_bytes"`
	MemoryLimitBytes        int64                  `json:"memory_limit_bytes"`
	LastRecoveryAttempt     *RecoveryAttempt       `json:"last_recovery_attempt,omitempty"`
	UpdatedAt               time.Time              `json:"updated_at"`
}

// AlertSeverity identifies an operator-alert urgency level.
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityError    AlertSeverity = "error"
	AlertSeverityCritical AlertSeverity = "critical"
)

// RecoveryAlertPolicy controls immediate and warning alert delivery behavior.
type RecoveryAlertPolicy struct {
	ImmediateSeverities []AlertSeverity `json:"immediate_severities,omitempty"`
	WarningMinInterval  time.Duration   `json:"warning_min_interval"`
}

// RestartBudget stores restart policy limits and the immutable history used for accounting.
type RestartBudget struct {
	MaxAttempts int           `json:"max_attempts"`
	Window      time.Duration `json:"window"`
	AttemptedAt []time.Time   `json:"attempted_at,omitempty"`
}

// RecoveryPolicy controls observation, automatic restart, probing, and operator alerts.
type RecoveryPolicy struct {
	Enabled              bool                `json:"enabled"`
	ObserveOnly          bool                `json:"observe_only"`
	RestartBudget        RestartBudget       `json:"restart_budget"`
	BackoffBase          time.Duration       `json:"backoff_base"`
	BackoffCap           time.Duration       `json:"backoff_cap"`
	ProbeConfigReference string              `json:"probe_config_reference,omitempty"`
	AlertPolicy          RecoveryAlertPolicy `json:"alert_policy"`
}

// MaintenanceOverride temporarily suppresses automatic recovery for one instance.
type MaintenanceOverride struct {
	ID uuid.UUID `json:"id"`
	ManagedInstanceKey
	Actor     string     `json:"actor"`
	Reason    string     `json:"reason"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ActiveAt reports whether the override applies at the supplied observation time.
func (o *MaintenanceOverride) ActiveAt(at time.Time) bool {
	if o == nil || o.CreatedAt.After(at) {
		return false
	}
	return o.ExpiresAt == nil || o.ExpiresAt.After(at)
}

// RecoveryAttemptResult is the durable outcome of a correlated recovery attempt.
type RecoveryAttemptResult string

const (
	RecoveryAttemptSuccess         RecoveryAttemptResult = "success"
	RecoveryAttemptDegraded        RecoveryAttemptResult = "degraded"
	RecoveryAttemptFailed          RecoveryAttemptResult = "failed"
	RecoveryAttemptBudgetExhausted RecoveryAttemptResult = "budget_exhausted"
	RecoveryAttemptSkippedOverride RecoveryAttemptResult = "skipped_override"
)

// RecoveryAttempt records one idempotently correlated recovery request and result.
type RecoveryAttempt struct {
	ID uuid.UUID `json:"id"`
	ManagedInstanceKey
	CorrelationID string                `json:"correlation_id"`
	RequestedAt   time.Time             `json:"requested_at"`
	Result        RecoveryAttemptResult `json:"result"`
	Evidence      string                `json:"evidence,omitempty"`
}

// ManagedInstanceHealthEvent is an append-only health observation or transition.
type ManagedInstanceHealthEvent struct {
	ID uuid.UUID `json:"id"`
	ManagedInstanceKey
	PreviousStatus InstanceHealthStatus `json:"previous_status,omitempty"`
	Status         InstanceHealthStatus `json:"status"`
	Reason         string               `json:"reason,omitempty"`
	Evidence       string               `json:"evidence,omitempty"`
	ObservedAt     time.Time            `json:"observed_at"`
}

// RecoveryDecisionAction identifies the supervisor action selected by policy evaluation.
type RecoveryDecisionAction string

const (
	RecoveryDecisionNoAction             RecoveryDecisionAction = "no_action"
	RecoveryDecisionRestart              RecoveryDecisionAction = "restart"
	RecoveryDecisionObserveOnly          RecoveryDecisionAction = "observe_only"
	RecoveryDecisionWaitBackoff          RecoveryDecisionAction = "wait_backoff"
	RecoveryDecisionBudgetExhausted      RecoveryDecisionAction = "budget_exhausted"
	RecoveryDecisionSkippedOverride      RecoveryDecisionAction = "skipped_override"
	RecoveryDecisionIntentionallyStopped RecoveryDecisionAction = "intentionally_stopped"
)

// RecoveryDecision is the pure result consumed by the managed-instance supervisor.
type RecoveryDecision struct {
	Action        RecoveryDecisionAction `json:"action"`
	Reason        string                 `json:"reason"`
	NextAllowedAt *time.Time             `json:"next_allowed_at,omitempty"`
}

// RestartAttemptsInWindow returns the number of attempts inside the budget window at now.
func RestartAttemptsInWindow(budget RestartBudget, now time.Time) int {
	if budget.Window <= 0 {
		return len(budget.AttemptedAt)
	}
	cutoff := now.Add(-budget.Window)
	count := 0
	for _, attemptedAt := range budget.AttemptedAt {
		if !attemptedAt.Before(cutoff) && !attemptedAt.After(now) {
			count++
		}
	}
	return count
}

// RestartBudgetExhausted reports whether another attempt would exceed the configured budget.
func RestartBudgetExhausted(budget RestartBudget, now time.Time) bool {
	return budget.MaxAttempts <= 0 || RestartAttemptsInWindow(budget, now) >= budget.MaxAttempts
}

// RecordRestartAttempt returns a new budget with the attempt recorded and expired history removed.
func RecordRestartAttempt(budget RestartBudget, attemptedAt time.Time) RestartBudget {
	attempts := make([]time.Time, 0, len(budget.AttemptedAt)+1)
	cutoff := attemptedAt.Add(-budget.Window)
	for _, existing := range budget.AttemptedAt {
		if budget.Window <= 0 || !existing.Before(cutoff) {
			attempts = append(attempts, existing)
		}
	}
	attempts = append(attempts, attemptedAt)
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Before(attempts[j]) })
	budget.AttemptedAt = attempts
	return budget
}

// NextRestartAllowedAt returns the earliest restart time after exponential backoff.
func NextRestartAllowedAt(budget RestartBudget, now time.Time, base, cap time.Duration) time.Time {
	if base <= 0 || len(budget.AttemptedAt) == 0 {
		return now
	}
	attempts := RestartAttemptsInWindow(budget, now)
	if attempts == 0 {
		return now
	}
	delay := base
	for i := 1; i < attempts; i++ {
		if cap > 0 && delay >= cap {
			delay = cap
			break
		}
		if delay >= time.Duration(1<<62) {
			if cap > 0 {
				delay = cap
			}
			break
		}
		delay *= 2
	}
	if cap > 0 && delay > cap {
		delay = cap
	}
	last := budget.AttemptedAt[len(budget.AttemptedAt)-1]
	return last.Add(delay)
}

// EvaluateRecovery applies desired state, observation, policy, budget, and override inputs without side effects.
func EvaluateRecovery(desiredRunning bool, observation ManagedInstanceHealth, policy RecoveryPolicy, budget RestartBudget, override *MaintenanceOverride) RecoveryDecision {
	now := observation.LastObservedAt
	if !desiredRunning {
		return RecoveryDecision{Action: RecoveryDecisionIntentionallyStopped, Reason: "desired state is stopped"}
	}
	if observation.Status == InstanceHealthStatusManualOverride || override.ActiveAt(now) {
		return RecoveryDecision{Action: RecoveryDecisionSkippedOverride, Reason: "maintenance override suppresses automatic recovery"}
	}
	if !policy.Enabled {
		return RecoveryDecision{Action: RecoveryDecisionNoAction, Reason: "recovery policy is disabled"}
	}
	if !statusNeedsRecovery(observation.Status) {
		return RecoveryDecision{Action: RecoveryDecisionNoAction, Reason: "observed status does not require restart"}
	}
	if policy.ObserveOnly {
		return RecoveryDecision{Action: RecoveryDecisionObserveOnly, Reason: "policy is observe-only"}
	}
	if RestartBudgetExhausted(budget, now) {
		return RecoveryDecision{Action: RecoveryDecisionBudgetExhausted, Reason: "restart budget is exhausted"}
	}
	nextAllowed := NextRestartAllowedAt(budget, now, policy.BackoffBase, policy.BackoffCap)
	if nextAllowed.After(now) {
		return RecoveryDecision{Action: RecoveryDecisionWaitBackoff, Reason: "restart backoff is active", NextAllowedAt: &nextAllowed}
	}
	return RecoveryDecision{Action: RecoveryDecisionRestart, Reason: "observed status requires recovery"}
}

func statusNeedsRecovery(status InstanceHealthStatus) bool {
	switch status {
	case InstanceHealthStatusStopped, InstanceHealthStatusUnhealthy, InstanceHealthStatusOOMKilled, InstanceHealthStatusRestartLoop:
		return true
	default:
		return false
	}
}

// MaxSanitizedEvidenceLength is the maximum number of Unicode code points retained in evidence.
const MaxSanitizedEvidenceLength = 1024

var (
	nsecEvidencePattern          = regexp.MustCompile(`(?i)nsec1[023456789acdefghjklmnpqrstuvwxyz]+`)
	bunkerEvidencePattern        = regexp.MustCompile(`(?i)bunker://[^\s]+`)
	dockerSocketPattern          = regexp.MustCompile(`(?i)(?:unix://)?(?:/[^\s:=]+)*/docker\.sock|DOCKER_HOST\s*[:=]\s*[^\s,;]+`)
	authorizationEvidencePattern = regexp.MustCompile(`(?i)\bauthorization\s*:\s*[^\r\n,;]+|\bbearer\s+[^\s,;]+`)
	credentialEvidencePattern    = regexp.MustCompile(`(?i)\b(?:[a-z0-9]+[_-])*(?:api[_-]?key|access[_-]?key|access[_-]?token|client[_-]?secret|private[_-]?key|secret[_-]?access[_-]?key|password|passwd|secret|token)\b\s*[:=]\s*[^\s,;]+`)
)

// SanitizeEvidence removes common secrets and runtime control endpoints and caps stored evidence length.
func SanitizeEvidence(evidence string) string {
	sanitized := nsecEvidencePattern.ReplaceAllString(evidence, "[REDACTED]")
	sanitized = bunkerEvidencePattern.ReplaceAllString(sanitized, "[REDACTED]")
	sanitized = dockerSocketPattern.ReplaceAllString(sanitized, "[REDACTED]")
	sanitized = authorizationEvidencePattern.ReplaceAllString(sanitized, "[REDACTED]")
	sanitized = credentialEvidencePattern.ReplaceAllString(sanitized, "[REDACTED]")
	sanitized = strings.TrimSpace(sanitized)
	if utf8.RuneCountInString(sanitized) <= MaxSanitizedEvidenceLength {
		return sanitized
	}
	runes := []rune(sanitized)
	return string(runes[:MaxSanitizedEvidenceLength])
}
