// Package runtime provides runtime observation adapters.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// LogService provides unified log access for both completed runs (via Blossom)
// and live container streaming (via runtime adapters).
type LogService struct {
	blossom *blossom.Client
	runtime Runtime
	logger  *zap.Logger
}

// NewLogService creates a new LogService.
func NewLogService(blossomClient *blossom.Client, runtime Runtime, logger *zap.Logger) *LogService {
	return &LogService{
		blossom: blossomClient,
		runtime: runtime,
		logger:  logger,
	}
}

// RunLogs contains stdout and stderr logs from a completed deployment run.
type RunLogs struct {
	RunID     uuid.UUID `json:"run_id"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Duration  string    `json:"duration,omitempty"`
}

// FetchRunLogs retrieves logs for a completed deployment run from Blossom storage.
// It downloads the stdout and stderr blobs referenced in the DeploymentRun.
func (s *LogService) FetchRunLogs(ctx context.Context, run *domain.DeploymentRun) (*RunLogs, error) {
	result := &RunLogs{
		RunID:    run.ID,
		ExitCode: run.ExitCode,
	}

	if run.StartedAt != nil {
		result.StartedAt = *run.StartedAt
	}

	if run.FinishedAt != nil && run.StartedAt != nil {
		result.Duration = run.FinishedAt.Sub(*run.StartedAt).String()
	}

	// Fetch stdout if available
	if run.StdoutRef != "" {
		stdout, err := s.blossom.Download(ctx, run.StdoutRef)
		if err != nil {
			s.logger.Warn("failed to fetch stdout logs",
				zap.String("run_id", run.ID.String()),
				zap.String("stdout_ref", run.StdoutRef),
				zap.Error(err),
			)
			// Don't fail completely - stderr might still be available
		} else {
			result.Stdout = string(stdout)
		}
	}

	// Fetch stderr if available
	if run.StderrRef != "" {
		stderr, err := s.blossom.Download(ctx, run.StderrRef)
		if err != nil {
			s.logger.Warn("failed to fetch stderr logs",
				zap.String("run_id", run.ID.String()),
				zap.String("stderr_ref", run.StderrRef),
				zap.Error(err),
			)
		} else {
			result.Stderr = string(stderr)
		}
	}

	return result, nil
}

// LiveLogOptions configures live log streaming.
type LiveLogOptions struct {
	ServiceName string    // Name of the service to stream logs from
	ServiceID   uuid.UUID // Service ID for labeling
	EnvID       uuid.UUID // Environment ID for labeling
	Tail        int       // Number of lines to fetch from history (0 = default 100)
	Follow      bool      // Whether to continuously stream new logs
}

// StreamLiveLogs streams live container logs from the runtime.
// The returned channel receives log entries until the context is cancelled.
func (s *LogService) StreamLiveLogs(ctx context.Context, opts LiveLogOptions) (<-chan LogEntry, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("no runtime configured for live log streaming")
	}

	tail := opts.Tail
	if tail <= 0 {
		tail = 100 // Default to last 100 lines
	}

	s.logger.Info("starting live log stream",
		zap.String("service", opts.ServiceName),
		zap.String("service_id", opts.ServiceID.String()),
		zap.String("env_id", opts.EnvID.String()),
		zap.Int("tail", tail),
		zap.Bool("follow", opts.Follow),
	)

	return s.runtime.StreamLogs(ctx, opts.ServiceName, LogOptions{
		Tail:   tail,
		Follow: opts.Follow,
	})
}

// LogLine represents a formatted log line for SSE streaming.
type LogLine struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Message   string    `json:"message"`
	Service   string    `json:"service,omitempty"`
}

// ParseLogLines parses raw log output into structured LogLine entries.
// This handles both plain text and Docker multiplexed format.
func ParseLogLines(raw string, stream string) []LogLine {
	lines := strings.Split(raw, "\n")
	result := make([]LogLine, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		entry := LogLine{
			Timestamp: time.Now().UTC(),
			Stream:    stream,
			Message:   line,
		}

		// Try to parse timestamp if present (format: 2006-01-02T15:04:05.000000000Z)
		if len(line) > 30 && line[4] == '-' && line[10] == 'T' {
			if ts, err := time.Parse(time.RFC3339Nano, line[:30]); err == nil {
				entry.Timestamp = ts
				entry.Message = strings.TrimSpace(line[30:])
			}
		}

		result = append(result, entry)
	}

	return result
}

// TailLogs returns the last N lines from a log string.
func TailLogs(logs string, n int) string {
	if n <= 0 {
		return logs
	}

	lines := strings.Split(logs, "\n")

	// Remove trailing empty lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}

	return strings.Join(lines[len(lines)-n:], "\n")
}

// MergeLogs merges stdout and stderr into a single chronological stream.
// This is a simple interleaving - for proper ordering, logs should have timestamps.
func MergeLogs(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}

	// Simple concatenation with markers
	var sb strings.Builder
	if stdout != "" {
		sb.WriteString("=== STDOUT ===\n")
		sb.WriteString(stdout)
		sb.WriteString("\n")
	}
	if stderr != "" {
		sb.WriteString("=== STDERR ===\n")
		sb.WriteString(stderr)
	}
	return sb.String()
}
