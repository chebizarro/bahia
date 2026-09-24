package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
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

// ServiceActionHandler retains shared direct-runtime helpers after REST action endpoint removal.
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
