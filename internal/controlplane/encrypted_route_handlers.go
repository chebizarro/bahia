package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	ContextVMMethodServiceSecretsReveal = "services/secrets-reveal"
	ContextVMMethodDeploymentRunLogsGet = "deployments/run-logs-get"
)

type RunLogFetcher interface {
	FetchRunLogs(context.Context, *domain.DeploymentRun) (*adapterruntime.RunLogs, error)
}

type SignatureVerifier interface {
	VerifySignatures(context.Context, *domain.Artifact) ([]domain.ArtifactSignature, error)
}

type secretVersionHistoryLister interface {
	ListVersions(context.Context, uuid.UUID) ([]domain.SecretVersion, error)
}

type EncryptedRouteHandlersConfig struct {
	Secrets      repository.SecretRepository
	Encryptor    *secrets.Encryptor
	Runs         repository.DeploymentRunRepository
	RunLogs      RunLogFetcher
	Artifacts    repository.ArtifactRepository
	Signatures   repository.ArtifactSignatureRepository
	SignVerifier SignatureVerifier
	Services     repository.ServiceRepository
	Intents      repository.DeploymentIntentRepository
	RBAC         *auth.RBAC
	Logger       *zap.Logger
}

type EncryptedRouteHandlers struct {
	secrets      repository.SecretRepository
	encryptor    *secrets.Encryptor
	runs         repository.DeploymentRunRepository
	runLogs      RunLogFetcher
	artifacts    repository.ArtifactRepository
	signatures   repository.ArtifactSignatureRepository
	signVerifier SignatureVerifier
	services     repository.ServiceRepository
	intents      repository.DeploymentIntentRepository
	rbac         *auth.RBAC
	logger       *zap.Logger
}

func NewEncryptedRouteHandlers(cfg EncryptedRouteHandlersConfig) *EncryptedRouteHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedRouteHandlers{secrets: cfg.Secrets, encryptor: cfg.Encryptor,
		runs: cfg.Runs, runLogs: cfg.RunLogs, artifacts: cfg.Artifacts,
		signatures: cfg.Signatures, signVerifier: cfg.SignVerifier,
		services: cfg.Services, intents: cfg.Intents, rbac: cfg.RBAC,
		logger: logger.Named("encrypted-route-handlers")}
}

// Register exposes only interactive secret reveal and stored run-log fetch.
func (h *EncryptedRouteHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	register := func(method string, handler EncryptedRequestHandler) {
		transport.RegisterContextVMHandler(method, func(ctx context.Context, request ContextVMRequest) (any, error) {
			return handler(ctx, EncryptedRequest{Event: request.Event,
				Envelope: EncryptedRequestEnvelope{Version: ContextVMWireVersion,
					Operation: request.RPC.Method, RequesterPubkey: request.Event.PubKey.Hex(), Payload: request.RPC.Params}})
		})
	}
	register(ContextVMMethodServiceSecretsReveal, h.RevealSecret)
	register(ContextVMMethodDeploymentRunLogsGet, h.GetRunLogs)
}

type encryptedSecretPayload struct {
	ServiceID string `json:"service_id"`
	SecretID  string `json:"secret_id,omitempty"`
}

func (h *EncryptedRouteHandlers) requireSecretDeps() error {
	if h.secrets == nil || h.encryptor == nil {
		return fmt.Errorf("encrypted secret request handling is not configured")
	}
	return nil
}

func (h *EncryptedRouteHandlers) RevealSecret(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	secretID, err := parseEncryptedUUID(payload.SecretID, "secret ID")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermReadSecrets); err != nil {
		return nil, err
	}
	secret, err := h.secretForService(ctx, serviceID, secretID)
	if err != nil {
		return nil, err
	}
	value, err := h.encryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt secret")
	}
	return map[string]any{"secret": secret.ToRef(), "value": value}, nil
}

func (h *EncryptedRouteHandlers) secretForService(ctx context.Context, serviceID, secretID uuid.UUID) (*domain.ServiceSecret, error) {
	secret, err := h.secrets.GetByID(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("failed to look up secret")
	}
	if secret == nil {
		return nil, fmt.Errorf("secret not found")
	}
	if secret.ServiceID != serviceID {
		return nil, fmt.Errorf("secret does not belong to service")
	}
	return secret, nil
}

func (h *EncryptedRouteHandlers) authorizeServicePermission(ctx context.Context, request EncryptedRequest, serviceID uuid.UUID, permission domain.Permission) error {
	if h.services == nil || h.rbac == nil {
		return fmt.Errorf("encrypted route RBAC is not configured")
	}
	service, err := h.services.GetByID(ctx, serviceID)
	if err != nil {
		if err == repository.ErrNotFound {
			return fmt.Errorf("service not found")
		}
		return fmt.Errorf("failed to fetch service")
	}
	if service == nil {
		return fmt.Errorf("service not found")
	}
	if service.OrgID == uuid.Nil {
		return fmt.Errorf("service organization is not configured")
	}
	return h.rbac.CheckPermission(ctx, requestPrincipal(request), service.OrgID, permission)
}

func (h *EncryptedRouteHandlers) GetRunLogs(ctx context.Context, request EncryptedRequest) (any, error) {
	if h.runs == nil || h.runLogs == nil {
		return nil, fmt.Errorf("encrypted deployment run log retrieval is not configured")
	}
	var payload struct {
		RunID  string `json:"run_id"`
		Tail   int    `json:"tail,omitempty"`
		Stream string `json:"stream,omitempty"`
	}
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	runID, err := parseEncryptedUUID(payload.RunID, "run ID")
	if err != nil {
		return nil, err
	}
	run, err := h.runs.GetByID(ctx, runID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("deployment run not found")
		}
		return nil, fmt.Errorf("failed to fetch run")
	}
	if run == nil {
		return nil, fmt.Errorf("deployment run not found")
	}
	intent, err := h.authorizeRunPermission(ctx, request, run, domain.PermReadLogs)
	if err != nil {
		return nil, err
	}
	if !isTerminalEncryptedRunStatus(run.Status) {
		return nil, fmt.Errorf("run is still in progress; stored logs are available after completion")
	}
	logs, err := h.runLogs.FetchRunLogs(ctx, run)
	if err != nil {
		h.logger.Error("failed to fetch encrypted request run logs", zap.String("run_id", runID.String()), zap.Error(err))
		return nil, fmt.Errorf("failed to fetch logs")
	}
	if logs == nil {
		logs = &adapterruntime.RunLogs{RunID: runID}
	}
	if err := h.redactRunLogSecrets(ctx, intent, logs); err != nil {
		h.logger.Error("failed to redact encrypted request run logs", zap.String("run_id", runID.String()), zap.Error(err))
		return nil, fmt.Errorf("failed to safely prepare logs")
	}
	if payload.Tail > 0 {
		logs.Stdout = adapterruntime.TailLogs(logs.Stdout, payload.Tail)
		logs.Stderr = adapterruntime.TailLogs(logs.Stderr, payload.Tail)
	}
	stream := strings.TrimSpace(payload.Stream)
	if stream == "" {
		stream = "merged"
	}
	switch stream {
	case "stdout":
		logs.Stderr = ""
	case "stderr":
		logs.Stdout = ""
	case "merged":
		// keep both streams for tabbed UI callers
	default:
		return nil, fmt.Errorf("invalid stream parameter; use stdout, stderr, or merged")
	}
	return map[string]any{"logs": logs, "stream": stream}, nil
}

func (h *EncryptedRouteHandlers) authorizeRunPermission(ctx context.Context, request EncryptedRequest, run *domain.DeploymentRun, permission domain.Permission) (*domain.DeploymentIntent, error) {
	if h.intents == nil {
		return nil, fmt.Errorf("deployment intent lookup is not configured")
	}
	intent, err := h.intents.GetByID(ctx, run.DeploymentIntentID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("deployment intent not found")
		}
		return nil, fmt.Errorf("failed to fetch deployment intent")
	}
	if intent == nil {
		return nil, fmt.Errorf("deployment intent not found")
	}
	if err := h.authorizeServicePermission(ctx, request, intent.ServiceID, permission); err != nil {
		return nil, err
	}
	return intent, nil
}

func (h *EncryptedRouteHandlers) redactRunLogSecrets(ctx context.Context, intent *domain.DeploymentIntent, logs *adapterruntime.RunLogs) error {
	if intent == nil || intent.DesiredState == nil || len(intent.DesiredState.SecretRefs) == 0 {
		return nil
	}
	if h.secrets == nil || h.encryptor == nil {
		return fmt.Errorf("secret redaction dependencies are not configured")
	}
	values := make([]string, 0, len(intent.DesiredState.SecretRefs))
	seen := make(map[uuid.UUID]struct{}, len(intent.DesiredState.SecretRefs))
	for _, ref := range intent.DesiredState.SecretRefs {
		if ref.SecretID == uuid.Nil {
			return fmt.Errorf("desired secret reference is incomplete")
		}
		if _, ok := seen[ref.SecretID]; ok {
			continue
		}
		seen[ref.SecretID] = struct{}{}
		history, ok := h.secrets.(secretVersionHistoryLister)
		if !ok {
			return fmt.Errorf("referenced secret version history is unavailable")
		}
		versions, err := history.ListVersions(ctx, ref.SecretID)
		if err != nil {
			return fmt.Errorf("load referenced secret history")
		}
		if len(versions) == 0 {
			return fmt.Errorf("referenced secret has no retained versions")
		}
		for i := range versions {
			value, err := h.encryptor.Decrypt(versions[i].EncryptedValue, versions[i].EncryptionMethod)
			if err != nil {
				return fmt.Errorf("decrypt referenced secret")
			}
			if value != "" {
				values = append(values, value)
			}
		}
	}
	// Longest first prevents a short secret that is a substring of another from
	// leaving a partially-redacted value behind.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	redact := func(input string) string {
		for _, value := range values {
			input = strings.ReplaceAll(input, value, "[REDACTED]")
			if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
				input = strings.ReplaceAll(input, string(encoded[1:len(encoded)-1]), "[REDACTED]")
			}
		}
		return input
	}
	logs.Stdout = redact(logs.Stdout)
	logs.Stderr = redact(logs.Stderr)
	return nil
}

func isTerminalEncryptedRunStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func (h *EncryptedRouteHandlers) verifyArtifactSignaturesDirect(ctx context.Context, artifact *domain.Artifact) (map[string]any, error) {
	artifactID := artifact.ID
	sigs, err := h.signVerifier.VerifySignatures(ctx, artifact)
	if err != nil {
		return nil, fmt.Errorf("verifying signatures: %w", err)
	}
	var stored int
	counts := map[domain.SignatureVerificationStatus]int{
		domain.SignatureStatusVerified:   0,
		domain.SignatureStatusDiscovered: 0,
		domain.SignatureStatusRejected:   0,
		domain.SignatureStatusError:      0,
	}
	for i := range sigs {
		sig := &sigs[i]
		if sig.ID == uuid.Nil {
			sig.ID = uuid.New()
		}
		if sig.ArtifactID == uuid.Nil {
			sig.ArtifactID = artifactID
		}
		sig.NormalizeVerificationStatus()
		counts[sig.VerificationStatus]++
		if err := h.signatures.Create(ctx, sig); err != nil {
			h.logger.Warn("failed to store signature record", zap.String("artifact_id", artifactID.String()), zap.String("signature_id", sig.ID.String()), zap.Error(err))
			continue
		}
		stored++
	}
	return map[string]any{
		"artifact_id": artifactID.String(),
		"found":       len(sigs),
		"stored":      stored,
		"verified":    counts[domain.SignatureStatusVerified],
		"discovered":  counts[domain.SignatureStatusDiscovered],
		"rejected":    counts[domain.SignatureStatusRejected],
		"errors":      counts[domain.SignatureStatusError],
		"signatures":  sigs,
	}, nil
}
