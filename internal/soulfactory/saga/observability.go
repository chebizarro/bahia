package saga

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

type MonitorConfig struct {
	Store    Store
	Instance string
	Build    string
	Logger   *slog.Logger
	Now      func() time.Time
}

type Monitor struct {
	store    Store
	instance string
	build    string
	logger   *slog.Logger
	now      func() time.Time
}

type RunObservation struct {
	RequestID             string
	RunID                 string
	AgentID               string
	Stage                 Stage
	StageAge              time.Duration
	StageDurations        map[Stage]time.Duration
	ProgressRatio         float64
	RetryCount            int
	ReconciliationCount   int
	RollbackCount         int
	SigningReady          bool
	RelayReady            bool
	DMGatePassed          bool
	TerminalProjected     bool
	FalseRunning          bool
	OrphanCandidates      int
	SignetUnauthorized    int
	CorrelationMismatches int
}

type ObservabilitySnapshot struct {
	Instance string
	Build    string
	Runs     []RunObservation
}

func NewMonitor(config MonitorConfig) (*Monitor, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("saga monitor store is required")
	}
	instance := strings.TrimSpace(config.Instance)
	build := strings.TrimSpace(config.Build)
	if instance == "" || build == "" {
		return nil, fmt.Errorf("saga monitor instance and build identity are required")
	}
	if containsSecretMarker(instance) || containsSecretMarker(build) {
		return nil, fmt.Errorf("saga monitor identity contains secret-shaped data")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Monitor{store: config.Store, instance: instance, build: build, logger: config.Logger, now: config.Now}, nil
}

// Collect inspects durable checkpoints without mutating or contacting any external system.
func (m *Monitor) Collect(ctx context.Context) (ObservabilitySnapshot, error) {
	runs, err := m.store.List(ctx)
	if err != nil {
		return ObservabilitySnapshot{}, err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RequestID < runs[j].RequestID })
	snapshot := ObservabilitySnapshot{Instance: m.instance, Build: m.build, Runs: make([]RunObservation, 0, len(runs))}
	now := m.now().UTC()
	for _, run := range runs {
		observation := observeRun(run, now)
		snapshot.Runs = append(snapshot.Runs, observation)
		m.logger.InfoContext(ctx, "openclaw provisioning run observed",
			"instance", m.instance, "build", m.build,
			"request_id", observation.RequestID, "run_id", observation.RunID,
			"agent_id", observation.AgentID, "stage", observation.Stage,
			"stage_age_seconds", observation.StageAge.Seconds(),
			"stage_durations", observation.StageDurations,
			"retry_count", observation.RetryCount,
			"reconciliation_count", observation.ReconciliationCount,
			"rollback_count", observation.RollbackCount,
			"signing_ready", observation.SigningReady,
			"relay_ready", observation.RelayReady,
			"dm_gate_passed", observation.DMGatePassed,
			"terminal_projected", observation.TerminalProjected,
			"false_running", observation.FalseRunning,
			"orphan_candidates", observation.OrphanCandidates,
			"signet_unauthorized_responses", observation.SignetUnauthorized,
			"correlation_mismatches", observation.CorrelationMismatches,
		)
	}
	return snapshot, nil
}

func observeRun(run *Run, now time.Time) RunObservation {
	stageAt := run.CreatedAt
	previousAt := run.CreatedAt
	completed := map[Stage]bool{}
	stageDurations := make(map[Stage]time.Duration)
	rollbackCount := 0
	for _, transition := range run.Transitions {
		completed[transition.To] = true
		if isForwardStage(transition.To) {
			stageDurations[transition.To] = maxDuration(0, transition.At.Sub(previousAt))
		}
		previousAt = transition.At
		if transition.To == run.Stage {
			stageAt = transition.At
		}
		if transition.To == StageRollbackPending || transition.To == StageRolledBack {
			rollbackCount++
		}
	}
	terminal := hasTerminalProjection(run.Resources, run, run.Stage)
	retryCount, unauthorized := 0, 0
	for _, failure := range run.Failures {
		if failure.Retryable {
			retryCount++
		}
		if failure.Stage == StageSignerEnrolled && failure.Code == "policy_denied" {
			unauthorized++
		}
	}
	correlationMismatches := 0
	for _, resource := range run.Resources {
		if resource.Conflict || (!resource.Conflict && (resource.CorrelationID != run.RequestID || resource.SpecHash != run.SpecHash)) {
			correlationMismatches++
		}
	}
	orphanCandidates := 0
	if run.Stage == StageRolledBack || run.Stage == StageFailedTerminal {
		for _, resource := range run.Resources {
			if resource.Ownership == OwnershipCreated && resource.OwnerRunID == run.RunID && !compensated(run, resource.key()) && resource.System != SystemBahiaProjection {
				orphanCandidates++
			}
		}
	}
	progress := 0
	for _, stage := range forwardStages {
		if completed[stage] || run.Stage == stage {
			progress++
		}
	}
	dmPassed := completed[StageDMVerified] || run.Stage == StageDMVerified || run.Stage == StageRunning
	signingReady := completed[StageSignerEnrolled] || run.Stage == StageSignerEnrolled || stageIndex(run.Stage) > stageIndex(StageSignerEnrolled)
	relayReady := completed[StageNostrConfigured] || run.Stage == StageNostrConfigured || stageIndex(run.Stage) > stageIndex(StageNostrConfigured)
	return RunObservation{
		RequestID: run.RequestID, RunID: run.RunID, AgentID: run.AgentID, Stage: run.Stage,
		StageAge: maxDuration(0, now.Sub(stageAt)), ProgressRatio: float64(progress) / float64(len(forwardStages)),
		StageDurations: stageDurations,
		RetryCount:     retryCount, ReconciliationCount: len(run.Transitions), RollbackCount: rollbackCount,
		SigningReady: signingReady, RelayReady: relayReady, DMGatePassed: dmPassed,
		TerminalProjected: terminal, FalseRunning: run.Stage == StageRunning && (!dmPassed || !terminal),
		OrphanCandidates: orphanCandidates, SignetUnauthorized: unauthorized,
		CorrelationMismatches: correlationMismatches,
	}
}

func stageIndex(stage Stage) int {
	for i, candidate := range forwardStages {
		if candidate == stage {
			return i
		}
	}
	return -1
}

func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}

// WritePrometheus writes the monitor's Prometheus text exposition.
func (m *Monitor) WritePrometheus(ctx context.Context, w io.Writer) error {
	snapshot, err := m.Collect(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "# HELP bahia_openclaw_provisioning_build_info OpenClaw provisioning monitor build identity")
	fmt.Fprintln(w, "# TYPE bahia_openclaw_provisioning_build_info gauge")
	fmt.Fprintf(w, "bahia_openclaw_provisioning_build_info{instance=%s,build=%s} 1\n", quoteLabel(snapshot.Instance), quoteLabel(snapshot.Build))
	for _, definition := range []struct{ name, help string }{
		{"bahia_openclaw_provisioning_stage", "Current durable stage for an OpenClaw provisioning run"},
		{"bahia_openclaw_provisioning_stage_age_seconds", "Seconds since the current durable stage began"},
		{"bahia_openclaw_provisioning_stage_duration_seconds", "Durable elapsed time to complete a forward stage"},
		{"bahia_openclaw_provisioning_progress_ratio", "Completed forward stages divided by all required forward stages"},
		{"bahia_openclaw_provisioning_retries", "Durable retryable failures recorded for a run"},
		{"bahia_openclaw_provisioning_reconciliations", "Durable stage transitions recorded for a run"},
		{"bahia_openclaw_provisioning_rollbacks", "Durable rollback transitions recorded for a run"},
		{"bahia_openclaw_provisioning_signing_ready", "Whether Signet enrollment has completed"},
		{"bahia_openclaw_provisioning_relay_ready", "Whether Nostr configuration has completed"},
		{"bahia_openclaw_provisioning_dm_gate", "Whether the independent DM verification gate has completed"},
		{"bahia_openclaw_provisioning_terminal_projection", "Whether correlated terminal 7950 and 31951 lineage is durable"},
		{"bahia_openclaw_provisioning_false_running", "Running checkpoints missing DM or terminal evidence"},
		{"bahia_openclaw_provisioning_orphan_candidates", "Owned resources left after terminal compensation"},
		{"bahia_openclaw_provisioning_signet_unauthorized_responses", "Durable Signet policy denials during enrollment"},
		{"bahia_openclaw_provisioning_correlation_mismatches", "Durable resource correlation conflicts"},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", definition.name, definition.help, definition.name)
	}
	for _, run := range snapshot.Runs {
		labels := fmt.Sprintf("instance=%s,build=%s,request_id=%s,run_id=%s,agent_id=%s,stage=%s",
			quoteLabel(snapshot.Instance), quoteLabel(snapshot.Build), quoteLabel(run.RequestID),
			quoteLabel(run.RunID), quoteLabel(run.AgentID), quoteLabel(string(run.Stage)))
		values := []struct{ name, value string }{
			{"bahia_openclaw_provisioning_stage", "1"},
			{"bahia_openclaw_provisioning_stage_age_seconds", strconv.FormatFloat(run.StageAge.Seconds(), 'f', 6, 64)},
			{"bahia_openclaw_provisioning_progress_ratio", strconv.FormatFloat(run.ProgressRatio, 'f', 6, 64)},
			{"bahia_openclaw_provisioning_retries", strconv.Itoa(run.RetryCount)},
			{"bahia_openclaw_provisioning_reconciliations", strconv.Itoa(run.ReconciliationCount)},
			{"bahia_openclaw_provisioning_rollbacks", strconv.Itoa(run.RollbackCount)},
			{"bahia_openclaw_provisioning_signing_ready", boolMetric(run.SigningReady)},
			{"bahia_openclaw_provisioning_relay_ready", boolMetric(run.RelayReady)},
			{"bahia_openclaw_provisioning_dm_gate", boolMetric(run.DMGatePassed)},
			{"bahia_openclaw_provisioning_terminal_projection", boolMetric(run.TerminalProjected)},
			{"bahia_openclaw_provisioning_false_running", boolMetric(run.FalseRunning)},
			{"bahia_openclaw_provisioning_orphan_candidates", strconv.Itoa(run.OrphanCandidates)},
			{"bahia_openclaw_provisioning_signet_unauthorized_responses", strconv.Itoa(run.SignetUnauthorized)},
			{"bahia_openclaw_provisioning_correlation_mismatches", strconv.Itoa(run.CorrelationMismatches)},
		}
		for _, value := range values {
			fmt.Fprintf(w, "%s{%s} %s\n", value.name, labels, value.value)
		}
		for _, stage := range forwardStages {
			if duration, ok := run.StageDurations[stage]; ok {
				fmt.Fprintf(w, "bahia_openclaw_provisioning_stage_duration_seconds{%s,observed_stage=%s} %.6f\n", labels, quoteLabel(string(stage)), duration.Seconds())
			}
		}
	}
	return nil
}

func quoteLabel(value string) string { return strconv.Quote(strings.ReplaceAll(value, "\n", " ")) }

func boolMetric(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
