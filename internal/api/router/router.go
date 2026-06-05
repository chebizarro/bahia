// Package router defines the HTTP routing for the Bahia API.
package router

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	userdocs "github.com/openagentsinc/bahia/internal/docs"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Version is set at build time.
var Version = "dev"

// New creates and configures the HTTP router.
// RouterDeps holds optional dependencies for the router.
type RouterDeps struct {
	Config           *config.Config
	AuthMiddleware   auth.MiddlewareConfig
	Workers          repository.WorkerRepository
	Builds           repository.BuildRepository
	Runs             repository.DeploymentRunRepository
	Services         repository.ServiceRepository
	Environments     repository.EnvironmentRepository
	EnvStates        repository.EnvironmentServiceStateRepository
	RuntimeResolver  runtimeadapter.RuntimeResolver
	Payments         *service.PaymentService
	SBOMs            repository.SBOMRepository
	Artifacts        repository.ArtifactRepository
	Signatures       repository.ArtifactSignatureRepository
	SignVerifier     SignatureVerifier
	Policies         *service.PolicyService
	Adoption         *service.AdoptionService
	RuntimeLifecycle *service.RuntimeLifecycleService
	Secrets          repository.SecretRepository
	Encryptor        *secrets.Encryptor
	Notifications    repository.NotificationRepository
	Dispatcher       *notifications.Dispatcher
	ToolProvisioning repository.ToolProvisioningRepository
	MCP              *handlers.MCPHandler
	HiveCI           repository.HiveCIRepository
	Blossom          *blossom.Client
	OCI              http.Handler
	Orgs             repository.OrganizationRepository
	OrgMembers       repository.OrgMemberRepository
	OrgInvites       repository.OrgInviteRepository
	RBAC             *auth.RBAC
	LLMRegistry      *service.LLMRegistryService
	MLRegistry       *service.MLRegistryService
	MLCommands       handlers.MLCommandPublisher
	HealthProvider   any
	ModePolicy       any
	Docs             *userdocs.Service
}

// SignatureVerifier is the interface for signature verification.
type SignatureVerifier interface {
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

func New(registry *service.RegistryService, logger *zap.Logger, corsCfg config.CORSConfig, telemetryProvider *telemetry.Provider, authCfg ...config.AuthConfig) http.Handler {
	return NewWithDeps(registry, logger, corsCfg, telemetryProvider, RouterDeps{}, authCfg...)
}

func NewWithDeps(registry *service.RegistryService, logger *zap.Logger, corsCfg config.CORSConfig, telemetryProvider *telemetry.Provider, deps RouterDeps, authCfg ...config.AuthConfig) http.Handler {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
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
	tier1Gate := routeTierGate(deps.ModePolicy, 1)
	tier2Gate := routeTierGate(deps.ModePolicy, 2)
	tier3Gate := routeTierGate(deps.ModePolicy, 3)
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
	svcH := handlers.NewServiceHandler(registry)
	envH := handlers.NewEnvironmentHandler(registry)
	buildH := handlers.NewBuildHandler(registry)
	artifactH := handlers.NewArtifactHandler(registry)
	deployH := handlers.NewDeploymentHandler(registry)
	stateH := handlers.NewStateHandler(registry)
	repoCIHandler := handlers.NewRepositoryCIHandler(deps.HiveCI)
	docsService := userdocs.New(userdocs.DefaultBasePath)
	if deps.Docs != nil {
		docsService = *deps.Docs
	}
	docsH := handlers.NewDocsHandler(docsService)
	var llmH *handlers.LLMHandler
	if deps.LLMRegistry != nil {
		llmH = handlers.NewLLMHandler(deps.LLMRegistry)
	}
	var mlH *handlers.MLHandler
	if deps.MLRegistry != nil || deps.MLCommands != nil {
		mlH = handlers.NewMLHandler(deps.MLRegistry, deps.MLCommands)
	}
	var logsH *handlers.LogHandler
	if deps.Runs != nil && deps.Services != nil && deps.Environments != nil {
		var logService *runtimeadapter.LogService
		if deps.Blossom != nil {
			logService = runtimeadapter.NewLogService(deps.Blossom, nil, logger)
		}
		logsH = handlers.NewLogHandlerWithResolver(logService, deps.RuntimeResolver, deps.Runs, deps.Services, deps.Environments, deps.EnvStates, logger)
	}

	var tenantH *handlers.TenantHandler
	if deps.Orgs != nil && deps.OrgMembers != nil && deps.OrgInvites != nil && deps.RBAC != nil {
		var bootstrapOwnerPubkeys []string
		if deps.Config != nil {
			bootstrapOwnerPubkeys = deps.Config.Auth.BootstrapOwnerPubkeys
		}
		tenantH = handlers.NewTenantHandler(deps.Orgs, deps.OrgMembers, deps.OrgInvites, deps.RBAC, bootstrapOwnerPubkeys, logger)
	}

	if deps.OCI != nil {
		r.With(tier3Gate).Mount("/v2", deps.OCI)
	}

	if deps.MCP != nil {
		r.With(tier3Gate, middleware.ContentType, auth.MiddlewareFromConfig(authMiddleware), middleware.RateLimit(writeLimiter)).Post("/mcp", deps.MCP.HandleJSONRPC)
	}

	// API v1 routes (authenticated when auth is enabled).
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.ContentType)
		r.Use(auth.MiddlewareFromConfig(authMiddleware))

		// Read routes: GET/list endpoints with read rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(readLimiter))

			// Documentation (read-only query surface)
			r.With(tier1Gate).Get("/docs", docsH.Catalog)
			r.With(tier1Gate).Get("/docs/{topic}", docsH.Read)

			// Tenant orgs (read)
			if tenantH != nil {
				r.With(tier2Gate).Get("/orgs", tenantH.ListOrgs)
				r.With(tier2Gate).Get("/orgs/{id}", tenantH.GetOrg)
				r.With(tier2Gate).Get("/orgs/{id}/members", tenantH.ListMembers)
				r.With(tier2Gate).Get("/orgs/{id}/invites", tenantH.ListInvites)
				r.With(tier2Gate).Get("/me/invites", tenantH.MyInvites)
			}

			// Services (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, nil, true)).Get("/services", svcH.List)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "id"), true)).Get("/services/{id}", svcH.Get)

			// Environments (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, nil, true)).Get("/environments", envH.List)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, environmentOrgResolver(deps.Environments, "id"), true)).Get("/environments/{id}", envH.Get)

			// Builds (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, buildOrgResolver(deps.Builds, deps.Services, "id"), true)).Get("/builds/{id}", buildH.Get)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "serviceId"), true)).Get("/services/{serviceId}/builds", buildH.ListByService)

			// Artifacts (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, artifactOrgResolver(deps.Artifacts, deps.Services, "id"), true)).Get("/artifacts/{id}", artifactH.Get)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "serviceId"), true)).Get("/services/{serviceId}/artifacts", artifactH.ListByService)

			// Deployment Intents (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, intentOrgResolver(registry, deps.Services, "id"), true)).Get("/deployments/intents/{id}", deployH.GetIntent)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, serviceEnvOrgResolver(deps.Services, deps.Environments, "serviceId", "envId"), true)).Get("/services/{serviceId}/environments/{envId}/intents", deployH.ListIntents)

			// Deployment Runs (read)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, runOrgResolver(registry, deps.Services, "id"), true)).Get("/deployments/runs/{id}", deployH.GetRun)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, intentOrgResolver(registry, deps.Services, "intentId"), true)).Get("/deployments/intents/{intentId}/runs", deployH.ListRuns)
			if logsH != nil && deps.Blossom != nil {
				r.With(tier2Gate).Get("/deployments/runs/{id}/logs", logsH.GetRunLogs)
			}

			// Live logs (read, SSE)
			if logsH != nil && deps.RuntimeResolver != nil {
				r.With(tier2Gate).Get("/services/{id}/environments/{envId}/logs", logsH.StreamLiveLogs)
			}

			// State (read)
			r.With(tier2Gate).Get("/state", stateH.ListAll)
			r.With(tier2Gate).Get("/state/drifted", stateH.ListDrifted)
			r.With(tier2Gate).Get("/environments/{envId}/state", stateH.ListByEnvironment)
			r.With(tier2Gate).Get("/services/{serviceId}/environments/{envId}/state", stateH.GetState)

			// Repository CI lookup (read)
			r.With(tier3Gate).Post("/repositories/ci/lookup", repoCIHandler.Lookup)

			// ML control plane (read)
			if mlH != nil {
				r.With(tier3Gate).Get("/ml/models", mlH.ListModels)
				r.With(tier3Gate).Get("/ml/models/{id}", mlH.GetModel)
				r.With(tier3Gate).Get("/ml/models/{modelId}/versions", mlH.ListModelVersions)
				r.With(tier3Gate).Get("/ml/model-versions/{id}", mlH.GetModelVersion)
				r.With(tier3Gate).Get("/ml/endpoints", mlH.ListEndpoints)
				r.With(tier3Gate).Get("/ml/endpoints/{id}", mlH.GetEndpoint)
				r.With(tier3Gate).Get("/ml/state", mlH.ListState)
				r.With(tier3Gate).Get("/ml/endpoints/{endpointId}/environments/{envId}/state", mlH.GetState)
				r.With(tier3Gate).Get("/ml/artifacts/{artifactId}/provenance", mlH.GetArtifactProvenance)
			}

			// LLM control plane (read)
			if llmH != nil {
				r.With(tier3Gate).Get("/llm/routes", llmH.ListRoutes)
				r.With(tier3Gate).Get("/llm/routes/{id}", llmH.GetRoute)
				r.With(tier3Gate).Get("/llm/routes/{routeId}/releases", llmH.ListReleases)
				r.With(tier3Gate).Get("/llm/releases/{id}", llmH.GetRelease)
				r.With(tier3Gate).Get("/llm/intents/{id}", llmH.GetIntent)
				r.With(tier3Gate).Get("/llm/routes/{routeId}/environments/{envId}/intents", llmH.ListIntents)
				r.With(tier3Gate).Get("/llm/runs/{id}", llmH.GetRun)
				r.With(tier3Gate).Get("/llm/intents/{intentId}/runs", llmH.ListRuns)
				r.With(tier3Gate).Get("/llm/state", llmH.ListAllState)
				r.With(tier3Gate).Get("/llm/state/drifted", llmH.ListDriftedState)
				r.With(tier3Gate).Get("/llm/environments/{envId}/state", llmH.ListEnvironmentState)
				r.With(tier3Gate).Get("/llm/routes/{routeId}/environments/{envId}/state", llmH.GetState)
			}

			// Workers (read)
			if deps.Workers != nil {
				workerH := handlers.NewWorkerHandler(deps.Workers)
				r.With(tier1Gate).Get("/workers", workerH.List)
				r.With(tier1Gate).Get("/workers/{pubkey}", workerH.Get)
				r.With(tier1Gate).Get("/workers/{pubkey}/pricing", workerH.Pricing)
			}

			// Payments (read)
			if deps.Payments != nil {
				payH := handlers.NewPaymentHandler(deps.Payments)
				r.With(tier2Gate).Get("/deployments/runs/{id}/cost", payH.GetRunCost)
				r.With(tier2Gate).Get("/payments/history", payH.GetPaymentHistory)
			}

			// Policies (read)
			if deps.Policies != nil {
				polH := handlers.NewPolicyHandler(deps.Policies)
				r.With(tier2Gate).Get("/policies", polH.List)
				r.With(tier2Gate).Get("/policies/{id}", polH.Get)
			}

			// SBOM (read)
			if deps.SBOMs != nil && deps.Artifacts != nil {
				sbomH := handlers.NewSBOMHandler(deps.SBOMs, deps.Artifacts)
				r.With(tier3Gate).Get("/artifacts/{id}/sbom", sbomH.GetSBOM)
				r.With(tier3Gate).Get("/artifacts/{id}/sbom/packages", sbomH.GetSBOMPackages)
				r.With(tier3Gate).Get("/sbom/search", sbomH.SearchPackages)
			}

			// Signatures (read)
			if deps.Signatures != nil && deps.Artifacts != nil && deps.SignVerifier != nil {
				sigH := handlers.NewSignatureHandler(deps.Signatures, deps.Artifacts, deps.SignVerifier)
				artifactRBAC := coreRBAC(deps, authMiddleware, artifactOrgResolver(deps.Artifacts, deps.Services, "id"), true)
				r.With(tier2Gate, artifactRBAC).Get("/artifacts/{id}/signatures", sigH.List)
				r.With(tier2Gate, artifactRBAC).Get("/artifacts/{id}/signatures/verified", sigH.ListVerified)
				r.With(tier2Gate, artifactRBAC).Get("/artifacts/{id}/signatures/check", sigH.HasVerified)
				r.With(tier2Gate, coreRBAC(deps, authMiddleware, signatureOrgResolver(deps.Signatures, deps.Artifacts, deps.Services, "id"), true)).Get("/signatures/{id}", sigH.Get)
			}

			// Secrets (read)
			if deps.Secrets != nil && deps.Encryptor != nil {
				secretH := handlers.NewSecretHandler(deps.Secrets, deps.Encryptor)
				r.With(tier2Gate, coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "id"), true)).Get("/services/{id}/secrets", secretH.List)
			}

			// Notifications (read)
			if deps.Notifications != nil && deps.Dispatcher != nil {
				notifH := handlers.NewNotificationHandler(deps.Notifications, deps.Dispatcher)
				r.With(tier2Gate).Get("/notifications/channels", notifH.ListChannels)
				r.With(tier2Gate).Get("/notifications/channels/{id}", notifH.GetChannel)
				r.With(tier2Gate).Get("/notifications/log", notifH.ListLogs)
			}

			// Tool provisioning (read)
			if deps.ToolProvisioning != nil {
				toolH := handlers.NewToolHandler(deps.ToolProvisioning)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, nil, true)).Get("/tools/pending", toolH.ListPending)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, toolIntentOrgResolver(deps.ToolProvisioning, deps.Services, "id"), true)).Get("/tools/{id}", toolH.GetIntent)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, nil, true)).Get("/tools/denylist", toolH.ListDenylist)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "id"), true)).Get("/services/{id}/tools", toolH.GetProfile)
			}

			// Blossom (read)
			if deps.Blossom != nil {
				blossomH := handlers.NewBlossomHandler(deps.Blossom)
				r.With(tier3Gate).Post("/blossom/list", blossomH.ListBlobs)
				r.With(tier3Gate).Get("/blossom/servers", blossomH.GetServers)
				r.With(tier3Gate).Get("/blossom/health", blossomH.HealthCheck)
				r.With(tier3Gate).Get("/blossom/stats", blossomH.GetStats)
			}
		})

		// Write routes: POST/PUT/PATCH/DELETE endpoints with stricter rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(writeLimiter))

			// Tenant orgs (write)
			if tenantH != nil {
				r.Post("/orgs", tenantH.CreateOrg)
				r.Put("/orgs/{id}", tenantH.UpdateOrg)
				r.Delete("/orgs/{id}", tenantH.DeleteOrg)
				r.Post("/orgs/{id}/members", tenantH.AddMember)
				r.Put("/orgs/{id}/members/{pubkey}", tenantH.UpdateMemberRole)
				r.Delete("/orgs/{id}/members/{pubkey}", tenantH.RemoveMember)
				r.Post("/orgs/{id}/invites", tenantH.CreateInvite)
				r.Delete("/orgs/{id}/invites/{inviteId}", tenantH.RevokeInvite)
			}

			// Builds (write)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, nil, true)).Post("/builds", buildH.Register)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, buildOrgResolver(deps.Builds, deps.Services, "id"), true)).Patch("/builds/{id}/status", buildH.UpdateStatus)

			// ML control plane (write compatibility actions publish Nostr commands)
			if mlH != nil {
				r.With(tier3Gate).Post("/ml/imports", mlH.ImportModel)
				r.With(tier3Gate).Post("/ml/recipes/runs", mlH.RunRecipe)
				r.With(tier3Gate).Post("/ml/deployments", mlH.Deploy)
				r.With(tier3Gate).Post("/ml/rollback", mlH.Rollback)
			}

			// LLM control plane (write): route updates remain REST-compatible;
			// route/release creation moved to signer-first Nostr commands.
			if llmH != nil {
				r.With(tier3Gate).Put("/llm/routes/{id}", llmH.UpdateRoute)
			}

			// Deployment Runs (write)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, nil, true)).Post("/deployments/runs", deployH.CreateRun)
			r.With(tier2Gate, coreRBAC(deps, authMiddleware, runOrgResolver(registry, deps.Services, "id"), true)).Post("/deployments/runs/{id}/complete", deployH.CompleteRun)

			// Payments (write)
			if deps.Payments != nil {
				payH := handlers.NewPaymentHandler(deps.Payments)
				r.With(tier2Gate).Post("/payments/estimate", payH.EstimateCost)
			}

			// SBOM (write)
			if deps.SBOMs != nil && deps.Artifacts != nil {
				sbomH := handlers.NewSBOMHandler(deps.SBOMs, deps.Artifacts)
				r.With(tier3Gate).Post("/artifacts/{id}/sbom", sbomH.IngestSBOM)
			}

			// Signatures (write)
			if deps.Signatures != nil && deps.Artifacts != nil && deps.SignVerifier != nil {
				sigH := handlers.NewSignatureHandler(deps.Signatures, deps.Artifacts, deps.SignVerifier)
				r.With(tier2Gate, coreRBAC(deps, authMiddleware, artifactOrgResolver(deps.Artifacts, deps.Services, "id"), true)).Post("/artifacts/{id}/signatures/verify", sigH.Verify)
			}

			// Deprecated policy REST mutations are intentionally not mounted.
			// Signer-first Nostr policy command kinds 5986-5989 are the supported replacement.

			// Secrets (write)
			if deps.Secrets != nil && deps.Encryptor != nil {
				secretH := handlers.NewSecretHandler(deps.Secrets, deps.Encryptor)
				secretRBAC := coreRBAC(deps, authMiddleware, serviceOrgResolver(deps.Services, "id"), true)
				r.With(secretRBAC).Post("/services/{id}/secrets", secretH.Create)
				r.With(secretRBAC).Put("/services/{id}/secrets/{secretId}", secretH.Update)
				r.With(secretRBAC).Delete("/services/{id}/secrets/{secretId}", secretH.Delete)
			}

			// Notifications (write)
			if deps.Notifications != nil && deps.Dispatcher != nil {
				notifH := handlers.NewNotificationHandler(deps.Notifications, deps.Dispatcher)
				r.With(tier2Gate).Post("/notifications/channels", notifH.CreateChannel)
				r.With(tier2Gate).Put("/notifications/channels/{id}", notifH.UpdateChannel)
				r.With(tier2Gate).Delete("/notifications/channels/{id}", notifH.DeleteChannel)
				r.With(tier2Gate).Post("/notifications/channels/{id}/test", notifH.TestChannel)
			}

			// Tool provisioning (write)
			if deps.ToolProvisioning != nil {
				toolH := handlers.NewToolHandler(deps.ToolProvisioning)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, nil, true)).Post("/tools/denylist", toolH.AddDenylist)
				r.With(tier3Gate, coreRBAC(deps, authMiddleware, nil, true)).Delete("/tools/denylist/{package}/{manager}", toolH.RemoveDenylist)
			}

			if deps.MCP != nil {
				r.With(tier3Gate).Post("/mcp", deps.MCP.HandleJSONRPC)
			}
		})

		// Deprecated LLM operational, adoption, and direct runtime REST mutations
		// are intentionally not mounted. Signer-first Nostr control-plane commands
		// are the supported replacement for these flows.
	})

	return r
}

func routeTierGate(policy any, tier int) func(http.Handler) http.Handler {
	if policy == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.TierGate(policy, tier)
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
		Status:        stringField(snapshot, "Status"),
		Version:       Version,
		Mode:          stringField(snapshot, "Mode"),
		RequestedTier: intField(snapshot, "RequestedTier"),
		ActiveTier:    intField(snapshot, "ActiveTier"),
		Ready:         boolField(snapshot, "Ready"),
		Checks:        healthChecksFromSnapshot(snapshot.FieldByName("Checks")),
		Runners:       runnerStatusesFromSnapshot(snapshot.FieldByName("RunnerSummary")),
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
			Tier:    intField(check, "Tier"),
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
			Tier:    intField(runner, "Tier"),
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

func coreRBAC(deps RouterDeps, authCfg auth.MiddlewareConfig, resolver middleware.ResourceOrgResolver, requireOrg bool) func(http.Handler) http.Handler {
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
	return middleware.RBAC(middleware.RBACConfig{
		RBAC:          deps.RBAC,
		Required:      true,
		RequireOrg:    requireOrg,
		OrgIDResolver: resolver,
	})
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

func toolIntentOrgResolver(tools repository.ToolProvisioningRepository, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		intent, err := tools.GetIntent(r.Context(), id)
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

func signatureOrgResolver(signatures repository.ArtifactSignatureRepository, artifacts repository.ArtifactRepository, services repository.ServiceRepository, param string) middleware.ResourceOrgResolver {
	return func(r *http.Request) (uuid.UUID, error) {
		id, err := parseRouteUUID(r, param)
		if err != nil {
			return uuid.Nil, err
		}
		sig, err := signatures.GetByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return uuid.Nil, middleware.ErrOrgContextNotFound
			}
			return uuid.Nil, err
		}
		if sig == nil {
			return uuid.Nil, middleware.ErrOrgContextNotFound
		}
		req := requestWithRouteParam(r, "artifactId", sig.ArtifactID.String())
		return artifactOrgResolver(artifacts, services, "artifactId")(req)
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

func adoptionEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Adoption.Enabled
}

func directRuntimeEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.DirectRuntime.Enabled
}

func operatorAccessMiddlewareConfig(cfg config.OperatorAccessConfig, resolver *auth.NIP05Resolver) middleware.OperatorAccessConfig {
	return middleware.OperatorAccessConfig{
		AllowedSubjects: cfg.AllowedSubjects,
		AllowedPubkeys:  cfg.AllowedPubkeys,
		AllowedEmails:   cfg.AllowedEmails,
		NIP05Resolver:   resolver,
	}
}
