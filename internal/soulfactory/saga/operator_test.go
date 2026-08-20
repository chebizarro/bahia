package saga

import (
	"context"
	"testing"
)

func TestOperatorCommandsExposeInspectionAndDryRun(t *testing.T) {
	engine, _, store := fixtureEngine(t, nil)
	operator, err := NewOperator(engine)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := engine.Start(ctx, "operator-request", "operator-run", "operator-agent", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []OperatorCommand{CommandInspect, CommandRetry, CommandReconcile, CommandSafeAbort} {
		report, err := operator.Execute(ctx, Command{Operation: operation, RequestID: "operator-request", DryRun: true})
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if !report.DryRun {
			t.Fatalf("%s did not return dry-run report", operation)
		}
	}
	run, err := store.Load(ctx, "operator-request")
	if err != nil {
		t.Fatal(err)
	}
	if run.Stage != StageRequested || run.Version != 1 {
		t.Fatalf("operator dry-run mutated saga: stage=%s version=%d", run.Stage, run.Version)
	}
}
