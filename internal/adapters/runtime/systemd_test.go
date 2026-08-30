package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeSystemdExecutor struct {
	calls []string
	show  string
}

func (f *fakeSystemdExecutor) Run(_ context.Context, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "systemctl" && containsArg(args, "show") {
		return f.show, "", nil
	}
	if name == "journalctl" {
		return "service failed token=secret-value\nsecond line", "", nil
	}
	return "", "", nil
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestSystemdObserverParsesDetailedState(t *testing.T) {
	tests := []struct {
		name       string
		active     string
		sub        string
		result     string
		wantStatus domain.InstanceHealthStatus
	}{
		{name: "active", active: "active", sub: "running", result: "success", wantStatus: domain.InstanceHealthStatusRunning},
		{name: "failed", active: "failed", sub: "failed", result: "exit-code", wantStatus: domain.InstanceHealthStatusUnhealthy},
		{name: "oom", active: "failed", sub: "failed", result: "oom-kill", wantStatus: domain.InstanceHealthStatusOOMKilled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeSystemdExecutor{show: fmt.Sprintf("ActiveState=%s\nSubState=%s\nResult=%s\nNRestarts=9\nExecMainStatus=137\nMemoryCurrent=1000\nMemoryPeak=2000\nMemoryMax=3000\nActiveEnterTimestamp=2026-08-29T10:00:00Z\nInactiveEnterTimestamp=2026-08-29T11:00:00Z\n", test.active, test.sub, test.result)}
			observer, err := NewSystemdObserverWithExecutor(domain.InstanceSupervisorUserSystemd, executor)
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			observation, err := observer.ObserveInstance(context.Background(), domain.ManagedInstanceKey{RuntimeTargetName: "bahia-api.service"})
			if err != nil {
				t.Fatalf("ObserveInstance() error = %v", err)
			}
			if observation.Status != test.wantStatus || observation.RestartCount != 9 || observation.ExitCode != 137 {
				t.Fatalf("observation = %+v", observation)
			}
			if observation.MemoryCurrentBytes != 1000 || observation.MemoryPeakBytes != 2000 || observation.MemoryLimitBytes != 3000 {
				t.Fatalf("memory observation = %+v", observation)
			}
			if observation.StartedAt == nil || observation.FinishedAt == nil {
				t.Fatalf("timestamps not parsed: %+v", observation)
			}
			if observation.OOMKilled != (test.result == "oom-kill") {
				t.Fatalf("OOMKilled = %v", observation.OOMKilled)
			}
			if strings.Contains(observation.Detail, "secret-value") {
				t.Fatalf("journal detail was not sanitized: %q", observation.Detail)
			}
			if !strings.Contains(executor.calls[0], "systemctl --user show --property=ActiveState,SubState,Result,NRestarts,ExecMainStatus,MemoryCurrent,MemoryPeak,MemoryMax,ActiveEnterTimestamp,InactiveEnterTimestamp --no-pager -- bahia-api.service") {
				t.Fatalf("show call = %q", executor.calls[0])
			}
		})
	}
}

func TestSystemdObserverControlsValidatedUnit(t *testing.T) {
	executor := &fakeSystemdExecutor{}
	observer, err := NewSystemdObserverWithExecutor(domain.InstanceSupervisorSystemd, executor)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Restart(context.Background(), "bahia-api.service"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if got := executor.calls[0]; got != "systemctl restart -- bahia-api.service" {
		t.Fatalf("call = %q", got)
	}
	if err := observer.Stop(context.Background(), "bad;unit.service"); err == nil {
		t.Fatal("Stop() accepted unsafe unit name")
	}
	if err := observer.Stop(context.Background(), "-evil.service"); err == nil {
		t.Fatal("Stop() accepted option-shaped unit name")
	}
	if len(executor.calls) != 1 {
		t.Fatalf("unsafe unit executed a command: %v", executor.calls)
	}
}
