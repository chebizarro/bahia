package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	EncryptedOperationServiceSecretsList   = "services.secrets.list"
	EncryptedOperationServiceSecretsCreate = "services.secrets.create"
	EncryptedOperationServiceSecretsUpdate = "services.secrets.update"
	EncryptedOperationServiceSecretsDelete = "services.secrets.delete"
	EncryptedOperationServiceSecretsReveal = "services.secrets.reveal"

	EncryptedOperationDeploymentRunLogsGet = "deployments.run_logs.get"

	EncryptedOperationArtifactSignaturesVerify = "artifacts.signatures.verify"
)

// RunLogFetcher is the encrypted request contract for stored deployment run logs.
type RunLogFetcher interface {
	FetchRunLogs(ctx context.Context, run *domain.DeploymentRun) (*adapterruntime.RunLogs, error)
}

// SignatureVerifier is the encrypted request contract for artifact signature verification.
type SignatureVerifier interface {
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
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

// NewEncryptedRouteHandlers adapts sensitive route-only actions onto encrypted
// signer-first encrypted request/result operations. Secrets, stored run logs, and
// signature verification results are never projected to the public sidecar.
func NewEncryptedRouteHandlers(cfg EncryptedRouteHandlersConfig) *EncryptedRouteHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedRouteHandlers{
		secrets:      cfg.Secrets,
		encryptor:    cfg.Encryptor,
		runs:         cfg.Runs,
		runLogs:      cfg.RunLogs,
		artifacts:    cfg.Artifacts,
		signatures:   cfg.Signatures,
		signVerifier: cfg.SignVerifier,
		services:     cfg.Services,
		intents:      cfg.Intents,
		rbac:         cfg.RBAC,
		logger:       logger.Named("encrypted-route-handlers"),
	}
}

func (h *EncryptedRouteHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	transport.RegisterHandler(EncryptedOperationServiceSecretsList, h.ListSecrets)
	transport.RegisterHandler(EncryptedOperationServiceSecretsCreate, h.CreateSecret)
	transport.RegisterHandler(EncryptedOperationServiceSecretsUpdate, h.UpdateSecret)
	transport.RegisterHandler(EncryptedOperationServiceSecretsDelete, h.DeleteSecret)
	transport.RegisterHandler(EncryptedOperationServiceSecretsReveal, h.RevealSecret)
	transport.RegisterHandler(EncryptedOperationDeploymentRunLogsGet, h.GetRunLogs)
	transport.RegisterHandler(EncryptedOperationArtifactSignaturesVerify, h.VerifyArtifactSignatures)
}

type encryptedSecretPayload struct {
	ServiceID        string `json:"service_id"`
	SecretID         string `json:"secret_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Value            string `json:"value,omitempty"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	EncryptionMethod string `json:"encryption_method,omitempty"`
}

func (h *EncryptedRouteHandlers) requireSecretDeps() error {
	if h.secrets == nil || h.encryptor == nil {
		return fmt.Errorf("encrypted secret request handling is not configured")
	}
	return nil
}

func (h *EncryptedRouteHandlers) ListSecrets(ctx context.Context, request EncryptedRequest) (any, error) {
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
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermReadSecrets); err != nil {
		return nil, err
	}
	secrets, err := h.secrets.ListByService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets")
	}
	refs := make([]domain.SecretRef, 0, len(secrets))
	for i := range secrets {
		refs = append(refs, secrets[i].ToRef())
	}
	return map[string]any{"secrets": refs, "total": len(refs)}, nil
}

func (h *EncryptedRouteHandlers) CreateSecret(ctx context.Context, request EncryptedRequest) (any, error) {
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
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if payload.Value == "" {
		return nil, fmt.Errorf("value is required")
	}
	method, err := parseSecretEncryptionMethod(payload.EncryptionMethod, domain.EncryptionNIP44)
	if err != nil {
		return nil, err
	}
	encrypted, err := h.encryptor.Encrypt(payload.Value, method)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret")
	}
	now := time.Now().UTC()
	secret := &domain.ServiceSecret{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		Name:             name,
		EncryptedValue:   encrypted,
		EncryptionMethod: method,
		Version:          1,
		CreatedBy:        normalizeEncryptedPubkey(request.Event.PubKey),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if strings.TrimSpace(payload.EnvironmentID) != "" {
		envID, err := parseEncryptedUUID(payload.EnvironmentID, "environment ID")
		if err != nil {
			return nil, err
		}
		secret.EnvironmentID = &envID
	}
	if err := h.secrets.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("failed to create secret")
	}
	return map[string]any{"secret": secret.ToRef(), "status": "created"}, nil
}

func (h *EncryptedRouteHandlers) UpdateSecret(ctx context.Context, request EncryptedRequest) (any, error) {
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
	if payload.Value == "" {
		return nil, fmt.Errorf("value is required")
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	secret, err := h.secretForService(ctx, serviceID, secretID)
	if err != nil {
		return nil, err
	}
	method, err := parseSecretEncryptionMethod(payload.EncryptionMethod, secret.EncryptionMethod)
	if err != nil {
		return nil, err
	}
	encrypted, err := h.encryptor.Encrypt(payload.Value, method)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret")
	}
	secret.EncryptedValue = encrypted
	secret.EncryptionMethod = method
	secret.UpdatedAt = time.Now().UTC()
	if err := h.secrets.Update(ctx, secret); err != nil {
		return nil, fmt.Errorf("failed to update secret")
	}
	if updated, _ := h.secrets.GetByID(ctx, secretID); updated != nil {
		secret = updated
	}
	return map[string]any{"secret": secret.ToRef(), "status": "updated"}, nil
}

func (h *EncryptedRouteHandlers) DeleteSecret(ctx context.Context, request EncryptedRequest) (any, error) {
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
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	if _, err := h.secretForService(ctx, serviceID, secretID); err != nil {
		return nil, err
	}
	if err := h.secrets.Delete(ctx, secretID); err != nil {
		return nil, fmt.Errorf("failed to delete secret")
	}
	return map[string]string{"status": "deleted", "secret_id": secretID.String()}, nil
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

func parseSecretEncryptionMethod(value string, fallback domain.EncryptionMethod) (domain.EncryptionMethod, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	method := domain.EncryptionMethod(strings.TrimSpace(value))
	switch method {
	case domain.EncryptionNIP44, domain.EncryptionAES256:
		return method, nil
	default:
		return "", fmt.Errorf("encryption_method must be 'nip44' or 'aes256gcm'")
	}
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
	if err := h.authorizeRunPermission(ctx, request, run, domain.PermReadLogs); err != nil {
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

func (h *EncryptedRouteHandlers) authorizeRunPermission(ctx context.Context, request EncryptedRequest, run *domain.DeploymentRun, permission domain.Permission) error {
	if h.intents == nil {
		return fmt.Errorf("deployment intent lookup is not configured")
	}
	intent, err := h.intents.GetByID(ctx, run.DeploymentIntentID)
	if err != nil {
		if err == repository.ErrNotFound {
			return fmt.Errorf("deployment intent not found")
		}
		return fmt.Errorf("failed to fetch deployment intent")
	}
	if intent == nil {
		return fmt.Errorf("deployment intent not found")
	}
	return h.authorizeServicePermission(ctx, request, intent.ServiceID, permission)
}

func isTerminalEncryptedRunStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func (h *EncryptedRouteHandlers) VerifyArtifactSignatures(ctx context.Context, request EncryptedRequest) (any, error) {
	if h.signatures == nil || h.artifacts == nil || h.signVerifier == nil {
		return nil, fmt.Errorf("encrypted artifact signature request handling is not configured")
	}
	var payload struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	artifactID, err := parseEncryptedUUID(payload.ArtifactID, "artifact ID")
	if err != nil {
		return nil, err
	}
	artifact, err := h.artifacts.GetByID(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("artifact not found")
		}
		return nil, fmt.Errorf("failed to fetch artifact")
	}
	if artifact == nil {
		return nil, fmt.Errorf("artifact not found")
	}
	if err := h.authorizeServicePermission(ctx, request, artifact.ServiceID, domain.PermWriteServices); err != nil {
		return nil, err
	}
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
