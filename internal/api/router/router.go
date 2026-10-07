// Package router defines the HTTP routing for the Bahia API.
package router

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/version"
	"go.uber.org/zap"
)

// Version is set at build time and defaults to the shared Bahia source version.
var Version = version.Semantic()

// New creates and configures the HTTP router.
// RouterDeps holds optional dependencies for the router.
type RouterDeps struct {
	Virtualization            repository.VirtualizationRepository
	Config                    *config.Config
	AuthMiddleware            auth.MiddlewareConfig
	Builds                    repository.BuildRepository
	Runs                      repository.DeploymentRunRepository
	Services                  repository.ServiceRepository
	Environments              repository.EnvironmentRepository
	EnvStates                 repository.EnvironmentServiceStateRepository
	InstanceOperator          handlers.InstanceMaintenanceOperator
	RuntimeResolver           runtimeadapter.RuntimeResolver
	Payments                  *service.PaymentService
	SBOMs                     repository.SBOMRepository
	SBOMImporter              *service.SBOMOrchestrator
	Artifacts                 repository.ArtifactRepository
	Signatures                repository.ArtifactSignatureRepository
	Adoption                  *service.AdoptionService
	RuntimeLifecycle          *service.RuntimeLifecycleService
	LegacyAgentReconciliation handlers.LegacyAgentReconciliationController
	Notifications             repository.NotificationRepository
	Dispatcher                *notifications.Dispatcher
	ToolProvisioning          repository.ToolProvisioningRepository
	MCP                       *handlers.MCPHandler
	Blossom                   *blossom.Client
	OCI                       http.Handler
	RBAC                      *auth.RBAC
	ConfigFabric              *service.ConfigFabricService
	HealthProvider            any
}

func New(registry *service.RegistryService, logger *zap.Logger, corsCfg config.CORSConfig, telemetryProvider *telemetry.Provider, authCfg ...config.AuthConfig) http.Handler {
	return NewWithDeps(registry, logger, corsCfg, telemetryProvider, RouterDeps{}, authCfg...)
}

func NewWithDeps(registry *service.RegistryService, logger *zap.Logger, corsCfg config.CORSConfig, telemetryProvider *telemetry.Provider, deps RouterDeps, authCfg ...config.AuthConfig) http.Handler {
	// Auto-populate Services from registry when the caller omits it.
	// This lets router.New (used by tests) inherit the registry's repo,
	// while NewWithDeps callers can still override with an explicit nil
	// to model a DB-less daemon.
	if deps.Services == nil && registry != nil {
		deps.Services = registry.ServiceRepository()
	}

	r := chi.NewRouter()

	// Global middleware.
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.RequestLogger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.CORS(middleware.NewCORSConfig(corsCfg.AllowedOrigins)))
	if telemetryProvider != nil {
		r.Use(middleware.Metrics(telemetryProvider.GetMetrics()))
	}

	// Per-IP rate limiting: 100 requests/minute for reads, 30/minute for writes.
	readLimiter := middleware.NewIPRateLimiter(middleware.RateLimiterConfig{
		Rate:     100,
		Interval: time.Minute,
	})
	writeLimiter := middleware.NewIPRateLimiter(middleware.RateLimiterConfig{
		Rate:     30,
		Interval: time.Minute,
	})

	// Auth middleware (applied to API routes, not health checks).
	authMiddleware := routeAuthConfig(deps, authCfg...)
	// Dependency gates replace the old tier model (§6). Routes whose
	// backing repository is nil (e.g. no Postgres) return 503.
	dbGate := middleware.RequireRepo(deps.Services)
	platformAdminGate := platformRoleRBAC(deps, authMiddleware, domain.RoleAdmin)
	// Health, readiness, and metrics (unauthenticated).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if deps.HealthProvider == nil {
			handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ok", Version: Version})
			return
		}
		resp, ok := healthResponseFromProvider(deps.HealthProvider, "Liveness")
		if !ok {
			handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ok", Version: Version})
			return
		}
		handlers.WriteHealthJSON(w, http.StatusOK, resp)
	})
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		if deps.HealthProvider == nil {
			handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ready", Version: Version})
			return
		}
		resp, ok := healthResponseFromProvider(deps.HealthProvider, "Readiness")
		if !ok {
			handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ready", Version: Version})
			return
		}
		status := http.StatusOK
		if !resp.Ready {
			status = http.StatusServiceUnavailable
		}
		handlers.WriteHealthJSON(w, status, resp)
	})
	if telemetryProvider != nil {
		metricsHandler := telemetryProvider.MetricsHandler()
		if authMiddleware.Enabled {
			r.With(auth.MiddlewareFromConfig(authMiddleware)).Get("/metrics", metricsHandler)
		} else {
			r.Get("/metrics", metricsHandler)
		}
	}

	// Create handlers.
	var legacyReconciliationH *handlers.LegacyAgentReconciliationHandler
	if deps.LegacyAgentReconciliation != nil {
		legacyReconciliationH = handlers.NewLegacyAgentReconciliationHandler(deps.LegacyAgentReconciliation)
	}
	var instanceHealthH *handlers.InstanceHealthHandler
	if deps.InstanceOperator != nil && deps.Services != nil && deps.Environments != nil {
		instanceHealthH = handlers.NewInstanceHealthHandler(deps.InstanceOperator)
	}
	var logsH *handlers.LogHandler
	if deps.Runs != nil && deps.Services != nil && deps.Environments != nil {
		var logService *runtimeadapter.LogService
		if deps.Blossom != nil {
			logService = runtimeadapter.NewLogService(deps.Blossom, nil, logger)
		}
		logsH = handlers.NewLogHandlerWithResolver(logService, deps.RuntimeResolver, deps.Runs, deps.Services, deps.Environments, deps.EnvStates, logger)
	}

	if deps.OCI != nil {
		r.With(dbGate).Mount("/v2", deps.OCI)
	}

	if deps.MCP != nil {
		r.With(dbGate, middleware.ContentType, auth.MiddlewareFromConfig(authMiddleware), platformAdminGate, middleware.RateLimit(writeLimiter)).Post("/mcp", deps.MCP.HandleJSONRPC)
	}

	// API v1 routes (authenticated when auth is enabled).
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.ContentType)
		r.Use(auth.MiddlewareFromConfig(authMiddleware))
		r.Use(platformRBAC(deps, authMiddleware))
		// Removed REST methods return 404 even where a surviving read owns the path.
		r.MethodNotAllowed(http.NotFound)

		// Read routes: GET/list endpoints with read rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(readLimiter))
			RegisterVirtualizationRoutes(r, deps, dbGate)

			// Deployment run logs remain HTTP-native.
			if logsH != nil && deps.Blossom != nil {
				r.With(dbGate, coreRBAC(deps, authMiddleware, runOrgResolver(registry, deps.Services, "id"), true)).Get("/deployments/runs/{id}/logs", logsH.GetRunLogs)
			}

			// Live logs (read, SSE)
			if logsH != nil && deps.RuntimeResolver != nil {
				r.With(dbGate, coreRBAC(deps, authMiddleware, serviceEnvOrgResolver(deps.Services, deps.Environments, "id", "envId"), true)).Get("/services/{id}/environments/{envId}/logs", logsH.StreamLiveLogs)
			}

			// Payment records are canonical cp-state read from the local event
			// store (audit B-31), so these reads need no PostgreSQL gate: the
			// service answers from the local store alone (bahia-u5whr).
			if deps.Payments != nil {
				payH := handlers.NewPaymentHandler(deps.Payments)
				r.Get("/deployments/runs/{id}/cost", payH.GetRunCost)
				r.Get("/payments/history", payH.GetPaymentHistory)
			}

			// Config fabric desired/applied drift (read)
			if deps.ConfigFabric != nil {
				configFabricH := handlers.NewConfigFabricHandler(deps.ConfigFabric)
				r.With(dbGate, platformAdminGate).Get("/config-fabric/drift", configFabricH.ListDrift)
			}

			// Blossom blob fetch stays HTTP-native for browser content-addressed downloads.
			if deps.Blossom != nil {
				blossomH := handlers.NewBlossomHandler(deps.Blossom)
				// Blob download is unauthenticated: content-addressable blobs are
				// publicly verifiable by SHA-256 hash and the Blossom server itself
				// may be HTTP-only, requiring this HTTPS proxy to avoid mixed-content.
				r.Get("/blossom/blob/{hash}", blossomH.DownloadBlob)
			}
		})

		// Write routes: POST/PUT/PATCH/DELETE endpoints with stricter rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(writeLimiter))

			// Tenant orgs (write) — Phase 3 O1 (B-26): REST mutation routes
			// deleted. Org/member/invite mutations now go through the
			// encrypted ContextVM path (dual dispatch to intent processor).
			// Signer-first intent consumers no longer require those REST reads.

			// Phase 5 F1: retained — web/src/routes/instance-health/+page.svelte; replaced by bahia-irsry.11.19.
			// Managed instance maintenance (write)
			if instanceHealthH != nil {
				instanceRBAC := coreRBAC(deps, authMiddleware, serviceEnvOrgResolver(deps.Services, deps.Environments, "serviceId", "envId"), true)
				r.With(dbGate, instanceRBAC).Post("/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance", instanceHealthH.SetMaintenance)
				r.With(dbGate, instanceRBAC).Delete("/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance", instanceHealthH.ClearMaintenance)
			}

			// Phase 5 F1: retained — docs/user-guide/features/artifacts.md curl import; replaced by bahia-irsry.11.19.
			// SBOM (write compatibility import)
			if deps.SBOMs != nil && deps.Artifacts != nil && deps.SBOMImporter != nil {
				sbomH := handlers.NewSBOMHandler(deps.SBOMs, deps.Artifacts, deps.SBOMImporter)
				r.With(dbGate, coreRBAC(deps, authMiddleware, artifactOrgResolver(deps.Artifacts, deps.Services, "id"), true, domain.PermWriteServices)).Post("/artifacts/{id}/sbom", sbomH.IngestSBOM)
			}

			// Deprecated policy REST mutations are intentionally not mounted.
			// Signer-first Nostr policy command kinds retired-kind-retired-kind are the supported replacement.

			// Secrets (write): deleted in Phase 3 N1.
			// Secret create/update/delete now go through intent publishing (30900).

			// Phase 5 F1: retained — docs/user-guide/features/souls.md migration workflow; replaced by bahia-irsry.11.19.
			// Legacy Soul reconciliation is authenticated and dry-run-first.
			if legacyReconciliationH != nil {
				r.With(dbGate, platformAdminGate).Post("/soulfactory/legacy-reconciliation/preview", legacyReconciliationH.Preview)
				r.With(dbGate, platformAdminGate).Post("/soulfactory/legacy-reconciliation/apply", legacyReconciliationH.Apply)
			}

		})

		// Deprecated LLM operational, adoption, and direct runtime REST mutations
		// are intentionally not mounted. Signer-first Nostr control-plane commands
		// are the supported replacement for these flows.
	})

	return r
}

func healthResponseFromProvider(provider any, methodName string) (dto.HealthResponse, bool) {
	value := reflect.ValueOf(provider)
	if !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return dto.HealthResponse{}, false
	}
	method := value.MethodByName(methodName)
	if !method.IsValid() || method.Type().NumIn() != 0 || method.Type().NumOut() != 1 {
		return dto.HealthResponse{}, false
	}
	out := method.Call(nil)
	if len(out) != 1 {
		return dto.HealthResponse{}, false
	}
	return healthResponseFromSnapshotValue(out[0]), true
}

func healthResponseFromSnapshotValue(snapshot reflect.Value) dto.HealthResponse {
	if !snapshot.IsValid() {
		return dto.HealthResponse{Version: Version}
	}
	if snapshot.Kind() == reflect.Pointer {
		if snapshot.IsNil() {
			return dto.HealthResponse{Version: Version}
		}
		snapshot = snapshot.Elem()
	}
	if snapshot.Kind() != reflect.Struct {
		return dto.HealthResponse{Version: Version}
	}

	return dto.HealthResponse{
		Status:  stringField(snapshot, "Status"),
		Version: Version,
		Ready:   boolField(snapshot, "Ready"),
		Checks:  healthChecksFromSnapshot(snapshot.FieldByName("Checks")),
		Runners: runnerStatusesFromSnapshot(snapshot.FieldByName("RunnerSummary")),
	}
}

func healthChecksFromSnapshot(checksValue reflect.Value) []dto.HealthCheckDTO {
	if !checksValue.IsValid() || checksValue.Kind() != reflect.Slice {
		return nil
	}
	checks := make([]dto.HealthCheckDTO, 0, checksValue.Len())
	for i := 0; i < checksValue.Len(); i++ {
		check := checksValue.Index(i)
		checks = append(checks, dto.HealthCheckDTO{
			Name:    stringField(check, "Name"),
			Status:  stringField(check, "Status"),
			Message: stringField(check, "Message"),
			Details: stringMapField(check, "Details"),
		})
	}
	return checks
}

func runnerStatusesFromSnapshot(runnersValue reflect.Value) []dto.RunnerStatusDTO {
	if !runnersValue.IsValid() || runnersValue.Kind() != reflect.Slice {
		return nil
	}
	runners := make([]dto.RunnerStatusDTO, 0, runnersValue.Len())
	for i := 0; i < runnersValue.Len(); i++ {
		runner := runnersValue.Index(i)
		runners = append(runners, dto.RunnerStatusDTO{
			Name:    stringField(runner, "Name"),
			Running: boolField(runner, "Running"),
		})
	}
	return runners
}

func stringField(value reflect.Value, name string) string {
	field := exportedField(value, name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func stringMapField(value reflect.Value, name string) map[string]string {
	field := exportedField(value, name)
	if !field.IsValid() || field.Kind() != reflect.Map || field.IsNil() {
		return nil
	}
	result := make(map[string]string, field.Len())
	iter := field.MapRange()
	for iter.Next() {
		if iter.Key().Kind() == reflect.String && iter.Value().Kind() == reflect.String {
			result[iter.Key().String()] = iter.Value().String()
		}
	}
	return result
}

func intField(value reflect.Value, name string) int {
	field := exportedField(value, name)
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(field.Int())
	default:
		return 0
	}
}

func boolField(value reflect.Value, name string) bool {
	field := exportedField(value, name)
	if !field.IsValid() || field.Kind() != reflect.Bool {
		return false
	}
	return field.Bool()
}

func exportedField(value reflect.Value, name string) reflect.Value {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return value.FieldByName(name)
}

func coreRBACEnabled(deps RouterDeps, authCfg auth.MiddlewareConfig) bool {
	return authCfg.Enabled && deps.RBAC != nil
}

func platformRBAC(deps RouterDeps, authCfg auth.MiddlewareConfig) func(http.Handler) http.Handler {
	return platformRoleRBAC(deps, authCfg, "")
}

func platformRoleRBAC(deps RouterDeps, authCfg auth.MiddlewareConfig, minimumRole domain.Role) func(http.Handler) http.Handler {
	if !authCfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}
	var bootstrapOwners []string
	if deps.Config != nil {
		bootstrapOwners = deps.Config.Auth.BootstrapOwnerPubkeys
	}
	return middleware.PlatformAccess(middleware.PlatformAccessConfig{
		RBAC:                  deps.RBAC,
		BootstrapOwnerPubkeys: bootstrapOwners,
		MinimumRole:           minimumRole,
		AllowUnaffiliated:     isUnaffiliatedOnboardingRoute,
	})
}

func isUnaffiliatedOnboardingRoute(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodPost {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) == 5 &&
		parts[0] == "api" &&
		parts[1] == "v1" &&
		parts[2] == "invites" &&
		parts[3] != "" &&
		parts[4] == "accept"
}

func coreRBAC(deps RouterDeps, authCfg auth.MiddlewareConfig, resolver middleware.ResourceOrgResolver, requireOrg bool, permissions ...domain.Permission) func(http.Handler) http.Handler {
	if authCfg.Enabled && deps.RBAC == nil {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"authorization not configured"}`, http.StatusInternalServerError)
			})
		}
	}
	if !coreRBACEnabled(deps, authCfg) {
		return func(next http.Handler) http.Handler { return next }
	}
	loadAuthz := middleware.RBAC(middleware.RBACConfig{
		RBAC:          deps.RBAC,
		Required:      true,
		RequireOrg:    requireOrg,
		OrgIDResolver: resolver,
	})
	return func(next http.Handler) http.Handler {
		guarded := middleware.RequireMember()(next)
		for i := len(permissions) - 1; i >= 0; i-- {
			guarded = middleware.RequirePermission(permissions[i])(guarded)
		}
		return loadAuthz(guarded)
	}
}

func serviceOrgResolver(services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		svc, err := services.GetByID(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if svc == nil || svc.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return svc.OrgID, nil
	}
}

func environmentOrgResolver(environments repository.EnvironmentRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		env, err := environments.GetByID(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if env == nil || env.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return env.OrgID, nil
	}
}

func buildOrgResolver(builds repository.BuildRepository, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		build, err := builds.GetByID(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if build == nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		svc, err := services.GetByID(r.Context(), build.ServiceID)
		if err != nil {
			return uuid.Nil, err
		}
		if svc == nil || svc.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return svc.OrgID, nil
	}
}

func artifactOrgResolver(artifacts repository.ArtifactRepository, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		artifact, err := artifacts.GetByID(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if artifact == nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		svc, err := services.GetByID(r.Context(), artifact.ServiceID)
		if err != nil {
			return uuid.Nil, err
		}
		if svc == nil || svc.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return svc.OrgID, nil
	}
}

func notificationChannelOrgResolver(notifications repository.NotificationRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		channel, err := notifications.GetChannelByID(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if channel == nil || channel.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return channel.OrgID, nil
	}
}

func serviceEnvOrgResolver(services repository.ServiceRepository, environments repository.EnvironmentRepository, serviceParam, envParam string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		svcOrg, err := serviceOrgResolver(services, serviceParam)(r)
		if err != nil {
			return uuid.Nil, err
		}
		envOrg, err := environmentOrgResolver(environments, envParam)(r)
		if err != nil {
			return uuid.Nil, err
		}
		if svcOrg != envOrg {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return svcOrg, nil
	}
}

func intentOrgResolver(registry *service.RegistryService, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		intent, err := registry.GetDeploymentIntent(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if intent == nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		svc, err := services.GetByID(r.Context(), intent.ServiceID)
		if err != nil {
			return uuid.Nil, err
		}
		if svc == nil || svc.OrgID == uuid.Nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		return svc.OrgID, nil
	}
}

func runOrgResolver(registry *service.RegistryService, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		run, err := registry.GetDeploymentRun(r.Context(), id)
		if err != nil {
			return uuid.Nil, err
		}
		if run == nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		req := requestWithRouteParam(r, "intentId", run.DeploymentIntentID.String())
		return intentOrgResolver(registry, services, "intentId")(req)
	}
}

func parseRouteUUID(r *http.Request, param string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		return uuid.Nil, middleware.ErrInvalidOrgID
	}
	return id, nil
}

func requestWithRouteParam(r *http.Request, name, value string) *http.Request {
	rctx := chi.RouteContext(r.Context())
	copyCtx := chi.NewRouteContext()
	if rctx != nil {
		*copyCtx = *rctx
	}
	copyCtx.URLParams.Add(name, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, copyCtx))
}

func routeAuthConfig(deps RouterDeps, authCfg ...config.AuthConfig) auth.MiddlewareConfig {
	if deps.AuthMiddleware.Enabled || deps.AuthMiddleware.NIP98Validator != nil || deps.AuthMiddleware.NIP05Resolver != nil {
		return deps.AuthMiddleware
	}
	if len(authCfg) > 0 {
		return middlewareAuthConfig(authCfg[0])
	}
	if deps.Config != nil {
		return middlewareAuthConfig(deps.Config.Auth)
	}
	return auth.MiddlewareConfig{}
}

func middlewareAuthConfig(cfg config.AuthConfig) auth.MiddlewareConfig {
	out := auth.MiddlewareConfig{
		Enabled: cfg.Enabled,
	}
	if cfg.Enabled {
		out.NIP98Validator = auth.NewNIP98Validator(auth.DefaultNIP98Config())
	}
	return out
}
