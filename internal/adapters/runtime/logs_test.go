package runtime

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// testSlogLogger returns an slog logger for blossom client tests
func testSlogLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTailLogs(t *testing.T) {
	tests := []struct {
		name     string
		logs     string
		n        int
		expected string
	}{
		{
			name:     "tail last 3 lines",
			logs:     "line1\nline2\nline3\nline4\nline5",
			n:        3,
			expected: "line3\nline4\nline5",
		},
		{
			name:     "tail more than available",
			logs:     "line1\nline2",
			n:        10,
			expected: "line1\nline2",
		},
		{
			name:     "tail zero returns all",
			logs:     "line1\nline2\nline3",
			n:        0,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "tail with trailing newlines",
			logs:     "line1\nline2\nline3\n\n",
			n:        2,
			expected: "line2\nline3",
		},
		{
			name:     "empty logs",
			logs:     "",
			n:        5,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TailLogs(tt.logs, tt.n)
			if result != tt.expected {
				t.Errorf("TailLogs() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestMergeLogs(t *testing.T) {
	tests := []struct {
		name           string
		stdout         string
		stderr         string
		containsStdout bool
		containsStderr bool
	}{
		{
			name:           "both streams",
			stdout:         "stdout content",
			stderr:         "stderr content",
			containsStdout: true,
			containsStderr: true,
		},
		{
			name:           "only stdout",
			stdout:         "stdout only",
			stderr:         "",
			containsStdout: true,
			containsStderr: false,
		},
		{
			name:           "only stderr",
			stdout:         "",
			stderr:         "stderr only",
			containsStdout: false,
			containsStderr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeLogs(tt.stdout, tt.stderr)

			if tt.containsStdout && !strings.Contains(result, tt.stdout) {
				t.Errorf("MergeLogs() should contain stdout: %s", tt.stdout)
			}
			if tt.containsStderr && !strings.Contains(result, tt.stderr) {
				t.Errorf("MergeLogs() should contain stderr: %s", tt.stderr)
			}
		})
	}
}

func TestParseLogLines(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		stream   string
		expected int // number of lines
	}{
		{
			name:     "simple lines",
			raw:      "line1\nline2\nline3",
			stream:   "stdout",
			expected: 3,
		},
		{
			name:     "empty lines filtered",
			raw:      "line1\n\n\nline2",
			stream:   "stdout",
			expected: 2,
		},
		{
			name:     "empty input",
			raw:      "",
			stream:   "stderr",
			expected: 0,
		},
		{
			name:     "whitespace only",
			raw:      "   \n\t\n  ",
			stream:   "stdout",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseLogLines(tt.raw, tt.stream)
			if len(result) != tt.expected {
				t.Errorf("ParseLogLines() returned %d lines, want %d", len(result), tt.expected)
			}
			for _, line := range result {
				if line.Stream != tt.stream {
					t.Errorf("ParseLogLines() line stream = %s, want %s", line.Stream, tt.stream)
				}
			}
		})
	}
}

func TestLogService_FetchRunLogs(t *testing.T) {
	// Create mock Blossom server
	stdoutContent := "application started\nprocessing request\ndone"
	stderrContent := "warning: deprecated API used"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, "stdout") {
			w.Write([]byte(stdoutContent))
		} else if strings.Contains(path, "stderr") {
			w.Write([]byte(stderrContent))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Blossom URLs use SHA-256 hashes - create valid-looking ones
	stdoutHash := blossom.ComputeSHA256([]byte(stdoutContent))
	stderrHash := blossom.ComputeSHA256([]byte(stderrContent))

	// Create a test server that returns the content for the hash URLs
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, stdoutHash) {
			w.Write([]byte(stdoutContent))
		} else if strings.Contains(path, stderrHash) {
			w.Write([]byte(stderrContent))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()

	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{testServer.URL},
	}, testSlogLogger())

	logService := NewLogService(blossomClient, nil, zap.NewNop())

	startedAt := time.Now().Add(-1 * time.Minute)
	finishedAt := time.Now()
	exitCode := 0

	run := &domain.DeploymentRun{
		ID:         uuid.New(),
		StdoutRef:  testServer.URL + "/" + stdoutHash,
		StderrRef:  testServer.URL + "/" + stderrHash,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		ExitCode:   &exitCode,
	}

	logs, err := logService.FetchRunLogs(context.Background(), run)
	if err != nil {
		t.Fatalf("FetchRunLogs() error = %v", err)
	}

	if logs.Stdout != stdoutContent {
		t.Errorf("FetchRunLogs() Stdout = %q, want %q", logs.Stdout, stdoutContent)
	}
	if logs.Stderr != stderrContent {
		t.Errorf("FetchRunLogs() Stderr = %q, want %q", logs.Stderr, stderrContent)
	}
	if logs.ExitCode == nil || *logs.ExitCode != 0 {
		t.Errorf("FetchRunLogs() ExitCode = %v, want 0", logs.ExitCode)
	}
	if logs.Duration == "" {
		t.Error("FetchRunLogs() Duration should be set")
	}
}

func TestLogService_FetchRunLogs_MissingRefs(t *testing.T) {
	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{"http://localhost:9999"}, // Non-existent server
	}, testSlogLogger())

	logService := NewLogService(blossomClient, nil, zap.NewNop())

	run := &domain.DeploymentRun{
		ID:        uuid.New(),
		StdoutRef: "", // No refs
		StderrRef: "",
	}

	logs, err := logService.FetchRunLogs(context.Background(), run)
	if err != nil {
		t.Fatalf("FetchRunLogs() should not error with missing refs: %v", err)
	}

	if logs.Stdout != "" {
		t.Errorf("FetchRunLogs() Stdout should be empty, got %q", logs.Stdout)
	}
	if logs.Stderr != "" {
		t.Errorf("FetchRunLogs() Stderr should be empty, got %q", logs.Stderr)
	}
}

func TestNewLogService(t *testing.T) {
	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{"http://example.com"},
	}, testSlogLogger())

	logService := NewLogService(blossomClient, nil, zap.NewNop())

	if logService == nil {
		t.Error("NewLogService() returned nil")
	}
	if logService.blossom != blossomClient {
		t.Error("NewLogService() did not set blossom client")
	}
}

func TestLiveLogOptions(t *testing.T) {
	opts := LiveLogOptions{
		ServiceName: "test-service",
		ServiceID:   uuid.New(),
		EnvID:       uuid.New(),
		Tail:        50,
		Follow:      true,
	}

	if opts.ServiceName != "test-service" {
		t.Errorf("ServiceName = %s, want test-service", opts.ServiceName)
	}
	if opts.Tail != 50 {
		t.Errorf("Tail = %d, want 50", opts.Tail)
	}
	if !opts.Follow {
		t.Error("Follow should be true")
	}
}

func TestLogService_StreamLiveLogs_NoRuntime(t *testing.T) {
	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{"http://example.com"},
	}, testSlogLogger())

	logService := NewLogService(blossomClient, nil, zap.NewNop())

	_, err := logService.StreamLiveLogs(context.Background(), LiveLogOptions{
		ServiceName: "test",
	})

	if err == nil {
		t.Error("StreamLiveLogs() should error when runtime is nil")
	}
	if !strings.Contains(err.Error(), "no runtime configured") {
		t.Errorf("StreamLiveLogs() error = %v, want 'no runtime configured'", err)
	}
}
