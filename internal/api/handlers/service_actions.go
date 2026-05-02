package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type runtimeLifecycleService interface {
	Deploy(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*domain.RuntimeObservation, error)
	Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
	Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
}

type runtimeActionMetrics interface {
	RecordRuntimeAction(action, status string, duration time.Duration)
}

// ServiceActionHandler exposes direct runtime actions for services.
type ServiceActionHandler struct {
	lifecycle runtimeLifecycleService
	logger    *zap.Logger
	metrics   runtimeActionMetrics
}

// NewServiceActionHandler creates a ServiceActionHandler.
func NewServiceActionHandler(lifecycle runtimeLifecycleService, opts ...ServiceActionHandlerOption) *ServiceActionHandler {
	if isNilHandlerDependency(lifecycle) {
		lifecycle = nil
	}
	h := &ServiceActionHandler{lifecycle: lifecycle, logger: zap.NewNop()}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServiceActionHandlerOption configures operational dependencies for ServiceActionHandler.
type ServiceActionHandlerOption func(*ServiceActionHandler)

// WithServiceActionLogger enables structured direct-runtime audit logs.
func WithServiceActionLogger(logger *zap.Logger) ServiceActionHandlerOption {
	return func(h *ServiceActionHandler) {
		if logger != nil {
			h.logger = logger
		}
	}
}

// WithServiceActionMetrics enables direct-runtime operational metrics.
func WithServiceActionMetrics(metrics runtimeActionMetrics) ServiceActionHandlerOption {
	return func(h *ServiceActionHandler) {
		if !isNilHandlerDependency(metrics) {
			h.metrics = metrics
		}
	}
}

// Deploy deploys the desired or explicit artifact directly through the resolved runtime.
func (h *ServiceActionHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	serviceID, envID, ok := h.parseIDs(w, r)
	if !ok {
		h.recordRuntimeAction(r, "deploy", uuid.Nil, uuid.Nil, nil, start, "failed", "invalid service or environment id")
		return
	}
	req, ok := decodeDeployServiceActionRequest(w, r)
	if !ok {
		h.recordRuntimeAction(r, "deploy", serviceID, envID, nil, start, "failed", "invalid request body")
		return
	}
	obs, err := h.lifecycle.Deploy(r.Context(), serviceID, envID, req.ArtifactID)
	if err != nil {
		writeRuntimeLifecycleError(w, err)
		h.recordRuntimeAction(r, "deploy", serviceID, envID, req.ArtifactID, start, "failed", err.Error())
		return
	}
	h.recordRuntimeAction(r, "deploy", serviceID, envID, req.ArtifactID, start, "success", "")
	writeData(w, http.StatusOK, dto.RuntimeActionResponse{Action: "deploy", ServiceID: serviceID, EnvironmentID: envID, Observation: mapRuntimeObservationResponse(obs)})
}

// Restart restarts the service directly through the resolved runtime.
func (h *ServiceActionHandler) Restart(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	serviceID, envID, ok := h.parseIDs(w, r)
	if !ok {
		h.recordRuntimeAction(r, "restart", uuid.Nil, uuid.Nil, nil, start, "failed", "invalid service or environment id")
		return
	}
	obs, err := h.lifecycle.Restart(r.Context(), serviceID, envID)
	if err != nil {
		writeRuntimeLifecycleError(w, err)
		h.recordRuntimeAction(r, "restart", serviceID, envID, nil, start, "failed", err.Error())
		return
	}
	h.recordRuntimeAction(r, "restart", serviceID, envID, nil, start, "success", "")
	writeData(w, http.StatusOK, dto.RuntimeActionResponse{Action: "restart", ServiceID: serviceID, EnvironmentID: envID, Observation: mapRuntimeObservationResponse(obs)})
}

// Stop stops the service directly through the resolved runtime.
func (h *ServiceActionHandler) Stop(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	serviceID, envID, ok := h.parseIDs(w, r)
	if !ok {
		h.recordRuntimeAction(r, "stop", uuid.Nil, uuid.Nil, nil, start, "failed", "invalid service or environment id")
		return
	}
	obs, err := h.lifecycle.Stop(r.Context(), serviceID, envID)
	if err != nil {
		writeRuntimeLifecycleError(w, err)
		h.recordRuntimeAction(r, "stop", serviceID, envID, nil, start, "failed", err.Error())
		return
	}
	h.recordRuntimeAction(r, "stop", serviceID, envID, nil, start, "success", "")
	writeData(w, http.StatusOK, dto.RuntimeActionResponse{Action: "stop", ServiceID: serviceID, EnvironmentID: envID, Observation: mapRuntimeObservationResponse(obs)})
}

func (h *ServiceActionHandler) recordRuntimeAction(r *http.Request, action string, serviceID, envID uuid.UUID, artifactID *uuid.UUID, start time.Time, result, errMsg string) {
	duration := time.Since(start)
	if h.metrics != nil {
		h.metrics.RecordRuntimeAction(action, result, duration)
	}
	fields := requestActorLogFields(r)
	fields = append(fields,
		zap.String("request_id", chimiddleware.GetReqID(r.Context())),
		zap.String("action", action),
		zap.String("service_id", serviceID.String()),
		zap.String("environment_id", envID.String()),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.String("result", result),
	)
	if artifactID != nil {
		fields = append(fields, zap.String("artifact_id", artifactID.String()))
	}
	if errMsg != "" {
		fields = append(fields, zap.String("error", errMsg))
	}
	h.logger.Info("direct runtime action completed", fields...)
}

func (h *ServiceActionHandler) parseIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	if h.lifecycle == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime lifecycle service is not configured")
		return uuid.Nil, uuid.Nil, false
	}
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return uuid.Nil, uuid.Nil, false
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return uuid.Nil, uuid.Nil, false
	}
	return serviceID, envID, true
}

func writeRuntimeLifecycleError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		writeError(w, http.StatusNotFound, msg)
	case strings.Contains(msg, "no desired artifact"), strings.Contains(msg, "belongs to service"):
		writeError(w, http.StatusBadRequest, msg)
	case strings.Contains(msg, "does not support"), strings.Contains(msg, "adopted direct_runtime workloads"):
		writeError(w, http.StatusConflict, msg)
	default:
		writeError(w, http.StatusInternalServerError, msg)
	}
}

func decodeDeployServiceActionRequest(w http.ResponseWriter, r *http.Request) (dto.DeployServiceActionRequest, bool) {
	var req dto.DeployServiceActionRequest
	if r.Body == nil || r.Body == http.NoBody {
		return req, true
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return req, true
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return req, false
	}
	return req, true
}

func mapRuntimeObservationResponse(obs *domain.RuntimeObservation) *dto.RuntimeObservationResponse {
	if obs == nil {
		return nil
	}
	metadata := make(map[string]any, len(obs.Metadata))
	for k, v := range obs.Metadata {
		metadata[k] = v
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return &dto.RuntimeObservationResponse{
		ID:                  obs.ID,
		ServiceID:           obs.ServiceID,
		EnvironmentID:       obs.EnvironmentID,
		ObservedImageDigest: obs.ObservedImageDigest,
		ObservedImageRepo:   obs.ObservedImageRepo,
		ObservedContainerID: obs.ObservedContainerID,
		ObservedHost:        obs.ObservedHost,
		ObservedVersion:     obs.ObservedVersion,
		HealthStatus:        string(obs.HealthStatus),
		Source:              obs.Source,
		Metadata:            metadata,
		ObservedAt:          obs.ObservedAt,
	}
}
