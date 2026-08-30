package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRestartBudgetTransitions(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	original := RestartBudget{
		MaxAttempts: 3,
		Window:      time.Hour,
		AttemptedAt: []time.Time{now.Add(-2 * time.Hour), now.Add(-30 * time.Minute)},
	}

	updated := RecordRestartAttempt(original, now)

	require.Len(t, original.AttemptedAt, 2, "transition must not mutate its input")
	require.Equal(t, 2, RestartAttemptsInWindow(updated, now))
	require.Equal(t, []time.Time{now.Add(-30 * time.Minute), now}, updated.AttemptedAt)
	require.False(t, RestartBudgetExhausted(updated, now))

	exhausted := RecordRestartAttempt(updated, now.Add(time.Minute))
	require.True(t, RestartBudgetExhausted(exhausted, now.Add(time.Minute)))
	require.Equal(t, 3, RestartAttemptsInWindow(exhausted, now.Add(time.Minute)))
	require.Equal(t, 2, RestartAttemptsInWindow(exhausted, now.Add(31*time.Minute)))
}

func TestNextRestartAllowedAtExponentialGrowthAndCap(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := 10 * time.Second
	capDelay := 25 * time.Second
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{name: "no attempts", attempts: 0, want: 0},
		{name: "first attempt", attempts: 1, want: 10 * time.Second},
		{name: "second attempt", attempts: 2, want: 20 * time.Second},
		{name: "third attempt capped", attempts: 3, want: 25 * time.Second},
		{name: "later attempt remains capped", attempts: 5, want: 25 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			budget := RestartBudget{MaxAttempts: 10, Window: time.Hour}
			for i := 0; i < tc.attempts; i++ {
				budget.AttemptedAt = append(budget.AttemptedAt, now.Add(time.Duration(i-tc.attempts+1)*time.Second))
			}
			last := now
			if tc.attempts > 0 {
				last = budget.AttemptedAt[len(budget.AttemptedAt)-1]
			}
			require.Equal(t, last.Add(tc.want), NextRestartAllowedAt(budget, now, base, capDelay))
		})
	}
}

func TestMaintenanceOverrideActiveAt(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	override := &MaintenanceOverride{CreatedAt: now.Add(-time.Minute), ExpiresAt: &expires}
	require.True(t, override.ActiveAt(now))
	require.False(t, override.ActiveAt(expires))
	require.False(t, (*MaintenanceOverride)(nil).ActiveAt(now))
}

func TestEvaluateRecovery(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	defaultPolicy := RecoveryPolicy{Enabled: true, BackoffBase: time.Minute, BackoffCap: 10 * time.Minute}
	availableBudget := RestartBudget{MaxAttempts: 3, Window: time.Hour}
	activeExpiry := now.Add(time.Hour)
	expired := now.Add(-time.Minute)

	tests := []struct {
		name           string
		desiredRunning bool
		status         InstanceHealthStatus
		policy         RecoveryPolicy
		budget         RestartBudget
		override       *MaintenanceOverride
		wantAction     RecoveryDecisionAction
		wantNext       *time.Time
	}{
		{name: "healthy", desiredRunning: true, status: InstanceHealthStatusHealthy, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionNoAction},
		{name: "running", desiredRunning: true, status: InstanceHealthStatusRunning, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionNoAction},
		{name: "degraded is observed without blind restart", desiredRunning: true, status: InstanceHealthStatusDegraded, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionNoAction},
		{name: "unknown is observed without blind restart", desiredRunning: true, status: InstanceHealthStatusUnknown, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionNoAction},
		{name: "stopped restarts", desiredRunning: true, status: InstanceHealthStatusStopped, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionRestart},
		{name: "unhealthy restarts", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionRestart},
		{name: "oom killed restarts", desiredRunning: true, status: InstanceHealthStatusOOMKilled, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionRestart},
		{name: "restart loop restarts when budget permits", desiredRunning: true, status: InstanceHealthStatusRestartLoop, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionRestart},
		{name: "intentionally stopped suppresses recovery", desiredRunning: false, status: InstanceHealthStatusStopped, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionIntentionallyStopped},
		{name: "manual override status suppresses recovery", desiredRunning: true, status: InstanceHealthStatusManualOverride, policy: defaultPolicy, budget: availableBudget, wantAction: RecoveryDecisionSkippedOverride},
		{name: "active maintenance suppresses recovery", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: availableBudget, override: &MaintenanceOverride{CreatedAt: now.Add(-time.Minute), ExpiresAt: &activeExpiry}, wantAction: RecoveryDecisionSkippedOverride},
		{name: "expired maintenance permits recovery", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: availableBudget, override: &MaintenanceOverride{CreatedAt: now.Add(-time.Hour), ExpiresAt: &expired}, wantAction: RecoveryDecisionRestart},
		{name: "disabled policy", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: RecoveryPolicy{}, budget: availableBudget, wantAction: RecoveryDecisionNoAction},
		{name: "observe only", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: RecoveryPolicy{Enabled: true, ObserveOnly: true}, budget: availableBudget, wantAction: RecoveryDecisionObserveOnly},
		{name: "budget exhausted", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: RestartBudget{MaxAttempts: 2, Window: time.Hour, AttemptedAt: []time.Time{now.Add(-20 * time.Minute), now.Add(-10 * time.Minute)}}, wantAction: RecoveryDecisionBudgetExhausted},
		{name: "backoff active", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: RestartBudget{MaxAttempts: 3, Window: time.Hour, AttemptedAt: []time.Time{now.Add(-30 * time.Second)}}, wantAction: RecoveryDecisionWaitBackoff, wantNext: timePtr(now.Add(30 * time.Second))},
		{name: "backoff elapsed", desiredRunning: true, status: InstanceHealthStatusUnhealthy, policy: defaultPolicy, budget: RestartBudget{MaxAttempts: 3, Window: time.Hour, AttemptedAt: []time.Time{now.Add(-2 * time.Minute)}}, wantAction: RecoveryDecisionRestart},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observation := ManagedInstanceHealth{Status: tc.status, LastObservedAt: now}
			got := EvaluateRecovery(tc.desiredRunning, observation, tc.policy, tc.budget, tc.override)
			require.Equal(t, tc.wantAction, got.Action)
			require.NotEmpty(t, got.Reason)
			require.Equal(t, tc.wantNext, got.NextAllowedAt)
		})
	}
}

func TestSanitizeEvidence(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "nostr secret", input: "signer=nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"},
		{name: "bunker url", input: "connect bunker://pubkey?relay=wss://relay.example&secret=abc"},
		{name: "api key", input: "api_key=super-secret-value"},
		{name: "token", input: "token: ghp_secretvalue"},
		{name: "authorization", input: "Authorization: Bearer abc123"},
		{name: "environment secret key", input: "AWS_SECRET_ACCESS_KEY=aws-private-value"},
		{name: "private key", input: "SIGNING_PRIVATE_KEY=private-value"},
		{name: "docker socket path", input: "dial /var/run/docker.sock failed"},
		{name: "docker host", input: "DOCKER_HOST=tcp://private-host:2376"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeEvidence(tc.input)
			require.Contains(t, got, "[REDACTED]")
			require.NotContains(t, got, "super-secret")
			require.NotContains(t, got, "bunker://")
			require.NotContains(t, got, "nsec1")
			require.NotContains(t, got, "docker.sock")
			require.NotContains(t, got, "private-host")
			require.NotContains(t, got, "abc123")
			require.NotContains(t, got, "aws-private-value")
			require.NotContains(t, got, "private-value")
		})
	}
}

func TestSanitizeEvidenceCapsUnicodeLength(t *testing.T) {
	got := SanitizeEvidence(strings.Repeat("界", MaxSanitizedEvidenceLength+50))
	require.Len(t, []rune(got), MaxSanitizedEvidenceLength)
}

func timePtr(value time.Time) *time.Time { return &value }
