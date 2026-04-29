// Package router defines the HTTP routing for the Bahia API.
package router

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
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
	"go.uber.org/zap"
)

// Version is set at build time.
var Version = "dev"

// New creates and configures the HTTP router.
// RouterDeps holds optional dependencies for the router.
type RouterDeps struct {
	Workers       repository.WorkerRepository
	Payments      *service.PaymentService
	SBOMs         repository.SBOMRepository
	Artifacts     repository.ArtifactRepository
	Signatures    repository.ArtifactSignatureRepository
	SignVerifier  SignatureVerifier
	EventHub      *handlers.EventStreamHub
	Policies      *service.PolicyService
	Secrets       repository.SecretRepository
	Encryptor     *secrets.Encryptor
	Notifications repository.NotificationRepository
	Dispatcher    *notifications.Dispatcher
	MCP           *handlers.MCPHandler
	OCI           http.Handler
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
	var authEnabled bool
	var jwtSecret string
	if len(authCfg) > 0 {
		authEnabled = authCfg[0].Enabled
		jwtSecret = authCfg[0].JWTSecret
	}

	// Health, readiness, and metrics (unauthenticated).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ok", Version: Version})
	})
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteHealthJSON(w, http.StatusOK, dto.HealthResponse{Status: "ready", Version: Version})
	})
	if telemetryProvider != nil {
		r.Get("/metrics", telemetryProvider.MetricsHandler())
	}

	// Create handlers.
	svcH := handlers.NewServiceHandler(registry)
	envH := handlers.NewEnvironmentHandler(registry)
	buildH := handlers.NewBuildHandler(registry)
	artifactH := handlers.NewArtifactHandler(registry)
	deployH := handlers.NewDeploymentHandler(registry)
	stateH := handlers.NewStateHandler(registry)

	if deps.OCI != nil {
		r.Mount("/v2", deps.OCI)
	}

	// Public auth exchange endpoint (unauthenticated)
	// This must be registered BEFORE the authenticated routes to allow JWT acquisition
	if jwtSecret != "" {
		authExchangeH := handlers.NewAuthExchangeHandler(jwtSecret)
		r.Post("/api/v1/auth/nostr", authExchangeH.Exchange)
	}

	// API v1 routes (authenticated when auth is enabled).
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.ContentType)
		r.Use(auth.Middleware(authEnabled, jwtSecret))

		// Read routes: GET/list endpoints with read rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(readLimiter))

			// Services (read)
			r.Get("/services", svcH.List)
			r.Get("/services/{id}", svcH.Get)

			// Environments (read)
			r.Get("/environments", envH.List)
			r.Get("/environments/{id}", envH.Get)

			// Builds (read)
			r.Get("/builds/{id}", buildH.Get)
			r.Get("/services/{serviceId}/builds", buildH.ListByService)

			// Artifacts (read)
			r.Get("/artifacts/{id}", artifactH.Get)
			r.Get("/services/{serviceId}/artifacts", artifactH.ListByService)

			// Deployment Intents (read)
			r.Get("/deployments/intents/{id}", deployH.GetIntent)
			r.Get("/services/{serviceId}/environments/{envId}/intents", deployH.ListIntents)

			// Deployment Runs (read)
			r.Get("/deployments/runs/{id}", deployH.GetRun)
			r.Get("/deployments/intents/{intentId}/runs", deployH.ListRuns)

			// State (read)
			r.Get("/state", stateH.ListAll)
			r.Get("/state/drifted", stateH.ListDrifted)
			r.Get("/environments/{envId}/state", stateH.ListByEnvironment)
			r.Get("/services/{serviceId}/environments/{envId}/state", stateH.GetState)

			// Workers (read)
			if deps.Workers != nil {
				workerH := handlers.NewWorkerHandler(deps.Workers)
				r.Get("/workers", workerH.List)
				r.Get("/workers/{pubkey}", workerH.Get)
				r.Get("/workers/{pubkey}/pricing", workerH.Pricing)
			}

			// Payments (read)
			if deps.Payments != nil {
				payH := handlers.NewPaymentHandler(deps.Payments)
				r.Get("/deployments/runs/{id}/cost", payH.GetRunCost)
				r.Get("/payments/history", payH.GetPaymentHistory)
			}

			// Event stream (SSE)
			if deps.EventHub != nil {
				r.Get("/events/stream", deps.EventHub.StreamSSE)
			}

			// Policies (read)
			if deps.Policies != nil {
				polH := handlers.NewPolicyHandler(deps.Policies)
				r.Get("/policies", polH.List)
				r.Get("/policies/{id}", polH.Get)
			}

			// SBOM (read)
			if deps.SBOMs != nil && deps.Artifacts != nil {
				sbomH := handlers.NewSBOMHandler(deps.SBOMs, deps.Artifacts)
				r.Get("/artifacts/{id}/sbom", sbomH.GetSBOM)
				r.Get("/artifacts/{id}/sbom/packages", sbomH.GetSBOMPackages)
				r.Get("/sbom/search", sbomH.SearchPackages)
			}

			// Signatures (read)
			if deps.Signatures != nil && deps.Artifacts != nil && deps.SignVerifier != nil {
				sigH := handlers.NewSignatureHandler(deps.Signatures, deps.Artifacts, deps.SignVerifier)
				r.Get("/artifacts/{id}/signatures", sigH.List)
				r.Get("/artifacts/{id}/signatures/verified", sigH.ListVerified)
				r.Get("/artifacts/{id}/signatures/check", sigH.HasVerified)
				r.Get("/signatures/{id}", sigH.Get)
			}

			// Secrets (read)
			if deps.Secrets != nil && deps.Encryptor != nil {
				secretH := handlers.NewSecretHandler(deps.Secrets, deps.Encryptor)
				r.Get("/services/{id}/secrets", secretH.List)
			}

			// Notifications (read)
			if deps.Notifications != nil && deps.Dispatcher != nil {
				notifH := handlers.NewNotificationHandler(deps.Notifications, deps.Dispatcher)
				r.Get("/notifications/channels", notifH.ListChannels)
				r.Get("/notifications/channels/{id}", notifH.GetChannel)
				r.Get("/notifications/log", notifH.ListLogs)
			}
		})

		// Write routes: POST/PUT/PATCH/DELETE endpoints with stricter rate limit.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(writeLimiter))

			// Services (write)
			r.Post("/services", svcH.Create)
			r.Put("/services/{id}", svcH.Update)
			r.Delete("/services/{id}", svcH.Delete)

			// Environments (write)
			r.Post("/environments", envH.Create)
			r.Put("/environments/{id}", envH.Update)
			r.Delete("/environments/{id}", envH.Delete)

			// Builds (write)
			r.Post("/builds", buildH.Register)
			r.Patch("/builds/{id}/status", buildH.UpdateStatus)

			// Artifacts (write)
			r.Post("/artifacts", artifactH.Register)

			// Deployment Intents (write)
			r.Post("/deployments/intents", deployH.CreateIntent)
			r.Post("/deployments/intents/{id}/approve", deployH.ApproveIntent)
			r.Post("/deployments/intents/{id}/reject", deployH.RejectIntent)

			// Deployment Runs (write)
			r.Post("/deployments/runs", deployH.CreateRun)
			r.Post("/deployments/runs/{id}/complete", deployH.CompleteRun)

			// Rollback
			r.Post("/rollback", deployH.Rollback)

			// Runtime Observations (write)
			r.Post("/observations", stateH.RecordObservation)

			// Payments (write)
			if deps.Payments != nil {
				payH := handlers.NewPaymentHandler(deps.Payments)
				r.Post("/payments/estimate", payH.EstimateCost)
			}

			// SBOM (write)
			if deps.SBOMs != nil && deps.Artifacts != nil {
				sbomH := handlers.NewSBOMHandler(deps.SBOMs, deps.Artifacts)
				r.Post("/artifacts/{id}/sbom", sbomH.IngestSBOM)
			}

			// Signatures (write)
			if deps.Signatures != nil && deps.Artifacts != nil && deps.SignVerifier != nil {
				sigH := handlers.NewSignatureHandler(deps.Signatures, deps.Artifacts, deps.SignVerifier)
				r.Post("/artifacts/{id}/signatures/verify", sigH.Verify)
			}

			// Policies (write)
			if deps.Policies != nil {
				polH := handlers.NewPolicyHandler(deps.Policies)
				r.Post("/policies", polH.Create)
				r.Put("/policies/{id}", polH.Update)
				r.Delete("/policies/{id}", polH.Delete)
				r.Post("/policies/evaluate", polH.Evaluate)
			}

			// Secrets (write)
			if deps.Secrets != nil && deps.Encryptor != nil {
				secretH := handlers.NewSecretHandler(deps.Secrets, deps.Encryptor)
				r.Post("/services/{id}/secrets", secretH.Create)
				r.Put("/services/{id}/secrets/{secretId}", secretH.Update)
				r.Delete("/services/{id}/secrets/{secretId}", secretH.Delete)
			}

			// Notifications (write)
			if deps.Notifications != nil && deps.Dispatcher != nil {
				notifH := handlers.NewNotificationHandler(deps.Notifications, deps.Dispatcher)
				r.Post("/notifications/channels", notifH.CreateChannel)
				r.Put("/notifications/channels/{id}", notifH.UpdateChannel)
				r.Delete("/notifications/channels/{id}", notifH.DeleteChannel)
				r.Post("/notifications/channels/{id}/test", notifH.TestChannel)
			}

			// Agent Tools API (inspired by MCP, but HTTP-based for direct AI agent access)
			// NOTE: This is NOT a true MCP implementation (no JSON-RPC 2.0, no SSE transport).
			// It exposes deployment registry operations as callable tools for AI agents.
			// For real MCP integration, see internal/mcp/server.go's GetTools/CallTool methods.
			if deps.MCP != nil {
				r.Get("/agent/info", deps.MCP.GetServerInfo)
				r.Post("/agent/tools/list", deps.MCP.ListTools)
				r.Post("/agent/tools/call", deps.MCP.CallTool)
			}
		})
	})

	return r
}
