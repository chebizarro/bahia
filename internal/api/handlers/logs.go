package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LogHandler handles container log requests.
type LogHandler struct {
	logService *runtime.LogService
	resolver   runtime.RuntimeResolver
	runs       repository.DeploymentRunRepository
	services   repository.ServiceRepository
	envs       repository.EnvironmentRepository
	states     repository.EnvironmentServiceStateRepository
	logger     *zap.Logger
}

// NewLogHandler creates a new LogHandler.
func NewLogHandler(
	logService *runtime.LogService,
	runs repository.DeploymentRunRepository,
	services repository.ServiceRepository,
	envs repository.EnvironmentRepository,
	states repository.EnvironmentServiceStateRepository,
	logger *zap.Logger,
) *LogHandler {
	return NewLogHandlerWithResolver(logService, nil, runs, services, envs, states, logger)
}

// NewLogHandlerWithResolver creates a new LogHandler that resolves live-log
// runtime targets per service/environment while retaining LogService for stored run logs.
func NewLogHandlerWithResolver(
	logService *runtime.LogService,
	resolver runtime.RuntimeResolver,
	runs repository.DeploymentRunRepository,
	services repository.ServiceRepository,
	envs repository.EnvironmentRepository,
	states repository.EnvironmentServiceStateRepository,
	logger *zap.Logger,
) *LogHandler {
	return &LogHandler{
		logService: logService,
		resolver:   resolver,
		runs:       runs,
		services:   services,
		envs:       envs,
		states:     states,
		logger:     logger,
	}
}

// GetRunLogs retrieves logs for a completed deployment run.
// GET /deployments/runs/{id}/logs
// Query params:
//   - tail: number of lines from end (default: all)
//   - stream: "stdout", "stderr", or "merged" (default: merged)
func (h *LogHandler) GetRunLogs(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}

	run, err := h.runs.GetByID(r.Context(), runID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "deployment run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch run")
		return
	}

	// Check if run is completed
	if run.Status != domain.RunStatusSucceeded && run.Status != domain.RunStatusFailed && run.Status != domain.RunStatusCancelled {
		writeError(w, http.StatusBadRequest, "run is still in progress; use live streaming endpoint")
		return
	}

	logs, err := h.logService.FetchRunLogs(r.Context(), run)
	if err != nil {
		h.logger.Error("failed to fetch run logs",
			zap.String("run_id", runID.String()),
			zap.Error(err),
		)
		writeError(w, http.StatusInternalServerError, "failed to fetch logs")
		return
	}

	// Apply tail parameter
	tail := queryInt(r, "tail", 0)
	if tail > 0 {
		logs.Stdout = runtime.TailLogs(logs.Stdout, tail)
		logs.Stderr = runtime.TailLogs(logs.Stderr, tail)
	}

	// Handle stream parameter
	stream := r.URL.Query().Get("stream")
	switch stream {
	case "stdout":
		logs.Stderr = ""
	case "stderr":
		logs.Stdout = ""
	case "merged", "":
		// Return both (default)
	default:
		writeError(w, http.StatusBadRequest, "invalid stream parameter; use stdout, stderr, or merged")
		return
	}

	writeData(w, http.StatusOK, logs)
}

// StreamLiveLogs streams live container logs via SSE.
// GET /services/{id}/environments/{envId}/logs
// Query params:
//   - tail: number of historical lines (default: 100)
//   - follow: whether to stream continuously (default: false)
func (h *LogHandler) StreamLiveLogs(w http.ResponseWriter, r *http.Request) {
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	envID, err := uuid.Parse(chi.URLParam(r, "envId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment ID")
		return
	}

	// Verify service exists
	svc, err := h.services.GetByID(r.Context(), serviceID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch service")
		return
	}

	// Verify environment exists
	env, err := h.envs.GetByID(r.Context(), envID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch environment")
		return
	}

	// Check that we can flush (required for SSE)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Parse options
	tail := queryInt(r, "tail", 100)
	follow := r.URL.Query().Get("follow") == "true"

	opts := runtime.LiveLogOptions{
		ServiceName: svc.Name,
		ServiceID:   serviceID,
		EnvID:       envID,
		Tail:        tail,
		Follow:      follow,
	}

	// Start log stream. Prefer resolver-based live logs so runtime targeting follows
	// the requested environment; fall back to the legacy LogService runtime when no
	// resolver has been wired (primarily for existing tests and callers).
	var logChan <-chan runtime.LogEntry
	if h.resolver != nil {
		rt, err := h.resolver.Resolve(svc, env)
		if err != nil {
			h.logger.Error("failed to resolve runtime for log stream",
				zap.String("service", svc.Name),
				zap.String("environment", env.Name),
				zap.Error(err),
			)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to resolve runtime: %v", err))
			return
		}
		logChan, err = rt.StreamLogs(r.Context(), opts.ServiceName, runtime.LogOptions{
			Tail:   tail,
			Follow: follow,
		})
	} else if h.logService != nil {
		logChan, err = h.logService.StreamLiveLogs(r.Context(), opts)
	} else {
		err = fmt.Errorf("no runtime configured for live log streaming")
	}
	if err != nil {
		h.logger.Error("failed to start log stream",
			zap.String("service", svc.Name),
			zap.Error(err),
		)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to stream logs: %v", err))
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.logger.Info("SSE log stream started",
		zap.String("service", svc.Name),
		zap.String("service_id", serviceID.String()),
		zap.String("env_id", envID.String()),
	)

	// Heartbeat ticker
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("SSE log stream disconnected",
				zap.String("service", svc.Name),
			)
			return

		case entry, ok := <-logChan:
			if !ok {
				// Channel closed, stream ended
				h.logger.Info("SSE log stream ended",
					zap.String("service", svc.Name),
				)
				return
			}

			line := runtime.LogLine{
				Timestamp: entry.Timestamp,
				Stream:    entry.Stream,
				Message:   entry.Message,
				Service:   svc.Name,
			}

			data, err := json.Marshal(line)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// logsResponse wraps run logs for JSON response.
type logsResponse struct {
	RunID     string `json:"run_id"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Duration  string `json:"duration,omitempty"`
}
