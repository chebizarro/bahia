package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

var systemdUnitPattern = regexp.MustCompile(`^[A-Za-z0-9_.@:-]+$`)

// SystemdCommandExecutor is the command side-effect boundary used by SystemdObserver.
type SystemdCommandExecutor interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
}

type execSystemdCommandExecutor struct{}

func (execSystemdCommandExecutor) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// SystemdObserver observes and controls system or user systemd units.
type SystemdObserver struct {
	supervisorType domain.InstanceSupervisorType
	executor       SystemdCommandExecutor
}

// NewSystemdObserver creates a systemd observer using the host command executor.
func NewSystemdObserver(supervisorType domain.InstanceSupervisorType) (*SystemdObserver, error) {
	return NewSystemdObserverWithExecutor(supervisorType, execSystemdCommandExecutor{})
}

// NewSystemdObserverWithExecutor creates a systemd observer with an injectable executor.
func NewSystemdObserverWithExecutor(supervisorType domain.InstanceSupervisorType, executor SystemdCommandExecutor) (*SystemdObserver, error) {
	if supervisorType != domain.InstanceSupervisorSystemd && supervisorType != domain.InstanceSupervisorUserSystemd {
		return nil, fmt.Errorf("unsupported systemd supervisor type %q", supervisorType)
	}
	if executor == nil {
		return nil, fmt.Errorf("systemd executor is required")
	}
	return &SystemdObserver{supervisorType: supervisorType, executor: executor}, nil
}

// ObserveInstance reads detailed state for exactly one validated unit.
func (o *SystemdObserver) ObserveInstance(ctx context.Context, key domain.ManagedInstanceKey) (*InstanceObservation, error) {
	unit, err := validateSystemdUnit(key.RuntimeTargetName)
	if err != nil {
		return nil, err
	}
	properties := "ActiveState,SubState,Result,NRestarts,ExecMainStatus,MemoryCurrent,MemoryPeak,MemoryMax,ActiveEnterTimestamp,InactiveEnterTimestamp"
	args := o.systemctlArgs("show", "--property="+properties, "--no-pager", "--", unit)
	stdout, stderr, err := o.executor.Run(ctx, "systemctl", args...)
	if err != nil {
		return nil, fmt.Errorf("systemctl show %s: %w: %s", unit, err, domain.SanitizeEvidence(stderr))
	}
	values := parseSystemdProperties(stdout)
	observation := &InstanceObservation{
		Key:                key,
		Status:             mapSystemdInstanceStatus(values["ActiveState"], values["SubState"], values["Result"]),
		RawStatus:          systemdRawStatus(values),
		HealthStatus:       values["Result"],
		OOMKilled:          strings.EqualFold(values["Result"], "oom-kill"),
		ExitCode:           parseSystemdInt(values["ExecMainStatus"]),
		RestartCount:       parseSystemdInt(values["NRestarts"]),
		StartedAt:          parseSystemdTimestamp(values["ActiveEnterTimestamp"]),
		FinishedAt:         parseSystemdTimestamp(values["InactiveEnterTimestamp"]),
		MemoryCurrentBytes: parseSystemdBytes(values["MemoryCurrent"]),
		MemoryPeakBytes:    parseSystemdBytes(values["MemoryPeak"]),
		MemoryLimitBytes:   parseSystemdBytes(values["MemoryMax"]),
		ObservedAt:         time.Now().UTC(),
	}
	journalArgs := o.journalctlArgs(unit)
	journal, _, journalErr := o.executor.Run(ctx, "journalctl", journalArgs...)
	if journalErr == nil {
		observation.Detail = domain.SanitizeEvidence(journal)
	}
	return observation, nil
}

// Restart restarts exactly one validated systemd unit.
func (o *SystemdObserver) Restart(ctx context.Context, unit string) error {
	return o.control(ctx, "restart", unit)
}

// Stop stops exactly one validated systemd unit.
func (o *SystemdObserver) Stop(ctx context.Context, unit string) error {
	return o.control(ctx, "stop", unit)
}

// RestartInstance restarts the unit named by the managed instance key.
func (o *SystemdObserver) RestartInstance(ctx context.Context, key domain.ManagedInstanceKey) error {
	return o.Restart(ctx, key.RuntimeTargetName)
}

// StopInstance stops the unit named by the managed instance key.
func (o *SystemdObserver) StopInstance(ctx context.Context, key domain.ManagedInstanceKey) error {
	return o.Stop(ctx, key.RuntimeTargetName)
}

func (o *SystemdObserver) control(ctx context.Context, action, rawUnit string) error {
	unit, err := validateSystemdUnit(rawUnit)
	if err != nil {
		return err
	}
	_, stderr, err := o.executor.Run(ctx, "systemctl", o.systemctlArgs(action, "--", unit)...)
	if err != nil {
		return fmt.Errorf("systemctl %s %s: %w: %s", action, unit, err, domain.SanitizeEvidence(stderr))
	}
	return nil
}

func (o *SystemdObserver) systemctlArgs(args ...string) []string {
	if o.supervisorType == domain.InstanceSupervisorUserSystemd {
		return append([]string{"--user"}, args...)
	}
	return args
}

func (o *SystemdObserver) journalctlArgs(unit string) []string {
	args := []string{}
	if o.supervisorType == domain.InstanceSupervisorUserSystemd {
		args = append(args, "--user")
	}
	return append(args, "-u", unit, "-n", "20", "--no-pager", "-o", "cat")
}

func validateSystemdUnit(unit string) (string, error) {
	unit = strings.TrimSpace(unit)
	if unit == "" || strings.HasPrefix(unit, "-") || !systemdUnitPattern.MatchString(unit) {
		return "", fmt.Errorf("invalid systemd unit name %q", unit)
	}
	return unit, nil
}

func parseSystemdProperties(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

func mapSystemdInstanceStatus(active, sub, result string) domain.InstanceHealthStatus {
	if strings.EqualFold(result, "oom-kill") {
		return domain.InstanceHealthStatusOOMKilled
	}
	switch strings.ToLower(active) {
	case "active":
		if strings.EqualFold(sub, "failed") {
			return domain.InstanceHealthStatusUnhealthy
		}
		return domain.InstanceHealthStatusRunning
	case "failed":
		return domain.InstanceHealthStatusUnhealthy
	case "inactive":
		return domain.InstanceHealthStatusStopped
	case "activating", "deactivating", "reloading":
		return domain.InstanceHealthStatusDegraded
	default:
		return domain.InstanceHealthStatusUnknown
	}
}

func systemdRawStatus(values map[string]string) string {
	parts := []string{values["ActiveState"]}
	if values["SubState"] != "" {
		parts = append(parts, values["SubState"])
	}
	if result := values["Result"]; result != "" {
		parts = append(parts, "result="+result)
	}
	return strings.Join(parts, "/")
}

func parseSystemdInt(value string) int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0
	}
	return int(parsed)
}

func parseSystemdBytes(value string) int64 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 63)
	if err != nil {
		return 0
	}
	return int64(parsed)
}

func parseSystemdTimestamp(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" || value == "n/a" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

var (
	_ HealthObserver            = (*SystemdObserver)(nil)
	_ ManagedInstanceController = (*SystemdObserver)(nil)
)
