package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// LLMHandler handles HTTP requests for LLM routes, releases, intents, runs, and state.
type LLMHandler struct {
	registry *service.LLMRegistryService
	workers  repository.WorkerRepository
}

func NewLLMHandler(registry *service.LLMRegistryService, workers ...repository.WorkerRepository) *LLMHandler {
	var workerRepo repository.WorkerRepository
	if len(workers) > 0 {
		workerRepo = workers[0]
	}
	return &LLMHandler{registry: registry, workers: workerRepo}
}

func (h *LLMHandler) ListRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := h.registry.ListRoutes(r.Context(), queryInt(r, "limit", 100), queryInt(r, "offset", 0))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: routes, Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0)})
}

func (h *LLMHandler) GetRoute(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	route, err := h.registry.GetRoute(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if route == nil {
		writeError(w, http.StatusNotFound, "LLM route not found")
		return
	}
	writeData(w, http.StatusOK, route)
}

func (h *LLMHandler) UpdateRoute(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	existing, err := h.registry.GetRoute(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "LLM route not found")
		return
	}
	var req dto.UpdateLLMRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.GatewayConfig != nil {
		existing.GatewayConfig = gatewayConfig(req.GatewayConfig)
	}
	if req.DefaultPlacementPolicy != nil {
		existing.DefaultPlacementPolicy = placementPolicy(req.DefaultPlacementPolicy)
	}
	if req.DefaultPromotionGate != nil {
		existing.DefaultPromotionGate = promotionGate(req.DefaultPromotionGate)
	}
	if req.Metadata != nil {
		existing.Metadata = *req.Metadata
	}
	if err := h.registry.UpdateRoute(r.Context(), existing); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, existing)
}

func (h *LLMHandler) RegisterHost(w http.ResponseWriter, r *http.Request) {
	if h.workers == nil {
		writeError(w, http.StatusNotFound, "worker repository unavailable")
		return
	}
	var req dto.RegisterLLMHostRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	worker := workerFromLLMHostRequest(req)
	if err := domain.ValidateRequiredString(worker.PubKey, "pubkey"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if worker.Name == "" {
		worker.Name = worker.PubKey
	}
	worker.LastAdvertisementAt = time.Now().UTC()
	worker.Status = domain.WorkerStatusOnline
	if worker.MaxConcurrentJobs <= 0 {
		worker.MaxConcurrentJobs = 1
	}
	if err := h.workers.Upsert(r.Context(), worker); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, worker)
}

func (h *LLMHandler) ListReleases(w http.ResponseWriter, r *http.Request) {
	routeID, err := uuidParam(r, "routeId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	limit, offset := queryInt(r, "limit", 100), queryInt(r, "offset", 0)
	releases, err := h.registry.ListReleases(r.Context(), routeID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: releases, Limit: limit, Offset: offset})
}

func (h *LLMHandler) GetRelease(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := h.registry.GetRelease(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if release == nil {
		writeError(w, http.StatusNotFound, "LLM release not found")
		return
	}
	writeData(w, http.StatusOK, release)
}

func (h *LLMHandler) CreateIntent(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateLLMDeploymentIntentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.RequestedBy = resolveActor(r, req.RequestedBy)
	intent := &domain.LLMDeploymentIntent{RouteID: req.RouteID, EnvironmentID: req.EnvironmentID, ReleaseID: req.ReleaseID, RequestedBy: req.RequestedBy, SourceKind: domain.SourceKind(req.SourceKind), Metadata: req.Metadata}
	if err := h.registry.CreateDeploymentIntent(r.Context(), intent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, intent)
}

func (h *LLMHandler) GetIntent(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	intent, err := h.registry.GetDeploymentIntent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if intent == nil {
		writeError(w, http.StatusNotFound, "LLM deployment intent not found")
		return
	}
	writeData(w, http.StatusOK, intent)
}

func (h *LLMHandler) ListIntents(w http.ResponseWriter, r *http.Request) {
	routeID, err := uuidParam(r, "routeId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	limit, offset := queryInt(r, "limit", 50), queryInt(r, "offset", 0)
	intents, err := h.registry.ListDeploymentIntents(r.Context(), routeID, envID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: intents, Limit: limit, Offset: offset})
}

func (h *LLMHandler) ApproveIntent(w http.ResponseWriter, r *http.Request) {
	h.intentApproval(w, r, true)
}
func (h *LLMHandler) RejectIntent(w http.ResponseWriter, r *http.Request) {
	h.intentApproval(w, r, false)
}

func (h *LLMHandler) intentApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	if approve {
		err = h.registry.ApproveDeploymentIntent(r.Context(), id)
	} else {
		err = h.registry.RejectDeploymentIntent(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "LLM deployment intent not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if approve {
		writeMessage(w, http.StatusOK, "LLM deployment intent approved")
	} else {
		writeMessage(w, http.StatusOK, "LLM deployment intent rejected")
	}
}

func (h *LLMHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := h.registry.GetDeploymentRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "LLM deployment run not found")
		return
	}
	writeData(w, http.StatusOK, run)
}

func (h *LLMHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	intentID, err := uuidParam(r, "intentId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	runs, err := h.registry.ListDeploymentRuns(r.Context(), intentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, runs)
}

func (h *LLMHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var req dto.RollbackLLMRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.RequestedBy = resolveActor(r, req.RequestedBy)
	intent, err := h.registry.Rollback(r.Context(), req.RouteID, req.EnvironmentID, req.RequestedBy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, intent)
}

func (h *LLMHandler) RecordObservation(w http.ResponseWriter, r *http.Request) {
	var req dto.RecordLLMRouteObservationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	obs := &domain.LLMRouteObservation{RouteID: req.RouteID, EnvironmentID: req.EnvironmentID, ObservedReleaseID: req.ObservedReleaseID, ObservedRunID: req.ObservedRunID, BackendKind: domain.LLMBackendKind(req.BackendKind), BackendEndpoint: req.BackendEndpoint, BackendHealth: domain.HealthStatus(req.BackendHealth), GatewayStatus: domain.GatewayRouteStatus(req.GatewayStatus), GatewayTarget: req.GatewayTarget, GatewayConfigHash: req.GatewayConfigHash, Source: req.Source, Metadata: req.Metadata}
	if err := domain.ValidateHealthStatus(obs.BackendHealth); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateGatewayRouteStatus(obs.GatewayStatus); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateLLMBackendKind(obs.BackendKind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.registry.RecordObservation(r.Context(), obs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, obs)
}

func (h *LLMHandler) ListAllState(w http.ResponseWriter, r *http.Request) {
	states, err := h.registry.ListAllRouteStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}
func (h *LLMHandler) ListDriftedState(w http.ResponseWriter, r *http.Request) {
	states, err := h.registry.ListDriftedRouteStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}
func (h *LLMHandler) ListEnvironmentState(w http.ResponseWriter, r *http.Request) {
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	states, err := h.registry.ListEnvironmentRouteStates(r.Context(), envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}
func (h *LLMHandler) GetState(w http.ResponseWriter, r *http.Request) {
	routeID, err := uuidParam(r, "routeId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	state, err := h.registry.GetRouteState(r.Context(), routeID, envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if state == nil {
		writeError(w, http.StatusNotFound, "LLM route state not found")
		return
	}
	writeData(w, http.StatusOK, state)
}

func gatewayConfig(req *dto.LLMGatewayConfigRequest) *domain.LLMGatewayRouteConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMGatewayRouteConfig{PublicModel: req.PublicModel, Path: req.Path, TimeoutSeconds: req.TimeoutSeconds, Headers: req.Headers}
}
func promotionGate(req *dto.LLMPromotionGateRequest) *domain.LLMPromotionGateConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMPromotionGateConfig{IntervalSeconds: req.IntervalSeconds, TimeoutSeconds: req.TimeoutSeconds, SuccessThreshold: req.SuccessThreshold, FailureThreshold: req.FailureThreshold}
}
func placementPolicy(req *dto.LLMPlacementPolicyRequest) *domain.LLMPlacementPolicy {
	if req == nil {
		return nil
	}
	return &domain.LLMPlacementPolicy{PreferredKinds: backendKinds(req.PreferredKinds), WorkerSelector: req.WorkerSelector, MinGPUCount: req.MinGPUCount, MinGPUMemoryGB: req.MinGPUMemoryGB, MinSystemMemoryGB: req.MinSystemMemoryGB, MaxPrice: req.MaxPrice, AllowExternal: req.AllowExternal}
}
func backendKinds(values []string) []domain.LLMBackendKind {
	out := make([]domain.LLMBackendKind, 0, len(values))
	for _, v := range values {
		out = append(out, domain.LLMBackendKind(v))
	}
	return out
}
func runtimeBackend(req *dto.LLMRuntimeManagedBackendRequest) *domain.LLMRuntimeManagedBackendConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMRuntimeManagedBackendConfig{Image: req.Image, Scheme: req.Scheme, ContainerPort: req.ContainerPort, HostPort: req.HostPort, HealthPath: req.HealthPath, Environment: req.Environment, Volumes: req.Volumes, Command: req.Command, Entrypoint: req.Entrypoint, WorkingDir: req.WorkingDir, NetworkMode: req.NetworkMode, PullAlways: req.PullAlways}
}
func externalBackend(req *dto.LLMExternalBackendRequest) *domain.LLMExternalBackendConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMExternalBackendConfig{BaseURL: req.BaseURL, HealthURL: req.HealthURL}
}

func workerFromLLMHostRequest(req dto.RegisterLLMHostRequest) *domain.Worker {
	w := &domain.Worker{PubKey: req.PubKey, Name: req.Name, Description: req.Description, Architecture: req.Architecture, MaxConcurrentJobs: req.MaxConcurrentJobs, CurrentQueueDepth: req.CurrentQueueDepth, MinDurationSecs: req.MinDurationSecs, MaxDurationSecs: req.MaxDurationSecs, Geohash: req.Geohash, PreferredRelays: req.PreferredRelays}
	for _, s := range req.Software {
		w.Software = append(w.Software, domain.WorkerSoftware{Name: s["name"], Version: s["version"], Path: s["path"]})
	}
	for _, p := range req.Pricing {
		w.Pricing = append(w.Pricing, domain.WorkerPricing{MintURL: stringAny(p["mint_url"]), PricePerSecond: intAny(p["price_per_second"]), Unit: stringAny(p["unit"])})
	}
	if req.Resources != nil {
		w.Resources = &domain.WorkerResources{CPUCores: req.Resources["cpu_cores"], MemoryGB: req.Resources["memory_gb"], DiskGB: req.Resources["disk_gb"]}
	}
	for _, a := range req.Accelerators {
		w.Accelerators = append(w.Accelerators, domain.WorkerAccelerator{Vendor: stringAny(a["vendor"]), Model: stringAny(a["model"]), Count: intAny(a["count"]), MemoryGB: intAny(a["memory_gb"]), Driver: stringAny(a["driver"])})
	}
	if req.RuntimeTarget != nil {
		w.RuntimeTarget = &domain.WorkerRuntimeTarget{Type: domain.RuntimeType(req.RuntimeTarget["type"]), EndpointRef: req.RuntimeTarget["endpoint_ref"], ComposeDir: req.RuntimeTarget["compose_dir"], KubeNamespace: req.RuntimeTarget["kube_namespace"], PublicBaseURL: req.RuntimeTarget["public_base_url"]}
	}
	return w
}
func stringAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func intAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}
