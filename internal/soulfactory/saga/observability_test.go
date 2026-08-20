package saga

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestMonitorExportsSecretFreeRunStageReadinessAndTerminalMetrics(t *testing.T) {
	engine, _, store := fixtureEngine(t, nil)
	if _, err := engine.Start(context.Background(), "request-monitor", "run-monitor", "agent-monitor", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(context.Background(), "request-monitor", false); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	monitor, err := NewMonitor(MonitorConfig{
		Store: store, Instance: "bahia-canary-a", Build: "f88ec8fa418485670803c3ee72e2dd10d7de601e",
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var metrics bytes.Buffer
	if err := monitor.WritePrometheus(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"bahia_openclaw_provisioning_build_info{instance=\"bahia-canary-a\",build=\"f88ec8fa418485670803c3ee72e2dd10d7de601e\"} 1",
		"bahia_openclaw_provisioning_signing_ready{",
		"bahia_openclaw_provisioning_relay_ready{",
		"bahia_openclaw_provisioning_dm_gate{",
		"bahia_openclaw_provisioning_terminal_projection{",
		"request_id=\"request-monitor\"",
		"run_id=\"run-monitor\"",
	} {
		if !strings.Contains(metrics.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, metrics.String())
		}
	}
	if strings.Contains(metrics.String()+logs.String(), "content") || strings.Contains(metrics.String()+logs.String(), "bunker://") {
		t.Fatal("monitor exposed payload or secret material")
	}
}

func TestMonitorRetainsRetryRollbackAndSignetDenialEvidence(t *testing.T) {
	engine, _, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageSignerEnrolled].applyErr = &SafeError{Code: "policy_denied", Retryable: false}
		ds[StageSignerEnrolled].applyLeavesAbsent = true
	})
	ctx := context.Background()
	if _, err := engine.Start(ctx, "request-denied", "run-denied", "agent-denied", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(ctx, "request-denied", false); err == nil {
		t.Fatal("expected Signet denial")
	}
	run, err := store.Load(ctx, "request-denied")
	if err != nil {
		t.Fatal(err)
	}
	if run.Stage != StageRolledBack || len(run.Failures) != 1 || run.Failures[0].Code != "policy_denied" {
		t.Fatalf("durable failure history = %#v", run)
	}
	monitor, err := NewMonitor(MonitorConfig{
		Store: store, Instance: "test", Build: "build",
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := monitor.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Runs[0]; got.SignetUnauthorized != 1 || got.RollbackCount == 0 || got.OrphanCandidates != 0 {
		t.Fatalf("observation = %#v", got)
	}
}

func TestStoreRejectsFailureHistoryRewrite(t *testing.T) {
	engine, _, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageNostrConfigured].inspectErr = &SafeError{Code: "response_lost", Retryable: true}
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-history", "run-history", "agent-history", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-history", false)
	run, err := store.Load(ctx, "request-history")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Failures) != 1 {
		t.Fatalf("failure history = %#v", run.Failures)
	}
	expected := run.Version
	run.Failures[0].Code = "policy_denied"
	run.Version++
	if err := store.Save(ctx, run, expected); err == nil {
		t.Fatal("rewritten failure history was accepted")
	}
}

func TestStoreRejectsUnsanitizedFailureHistory(t *testing.T) {
	_, _, store := fixtureEngine(t, nil)
	ctx := context.Background()
	run, err := NewRun("request-secret-history", "run-secret-history", "agent-secret-history", "sha256:spec", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	expected := run.Version
	run.Failures = append(run.Failures, Failure{Stage: StageSignerEnrolled, Code: "policy_denied", Message: "token=unsafe", At: time.Now()})
	run.Version++
	if err := store.Save(ctx, run, expected); err == nil {
		t.Fatal("unsanitized failure history was accepted")
	}
}
