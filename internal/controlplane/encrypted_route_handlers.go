package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/secretstore"
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

// RunLogSecretValues resolves the secrets a run references for log
// redaction (secretstore.Resolver).
type RunLogSecretValues interface {
	ResolveAll(ctx context.Context, secretIDs []uuid.UUID) ([]secretstore.Resolved, error)
}

type EncryptedRouteHandlersConfig struct {
	// SecretRecords and SecretStore reveal secrets: the canonical record
	// names the credential, the store releases it.
	SecretRecords SecretRecordView
	SecretStore   secretstore.SecretStore
	// SecretValues resolves referenced secrets to redact run logs.
	SecretValues RunLogSecretValues
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
	secretRecords SecretRecordView
	secretStore   secretstore.SecretStore
	secretValues  RunLogSecretValues
	runs          repository.DeploymentRunRepository
	runLogs       RunLogFetcher
	artifacts     repository.ArtifactRepository
	signatures    repository.ArtifactSignatureRepository
	signVerifier  SignatureVerifier
	services      repository.ServiceRepository
	intents       repository.DeploymentIntentRepository
	rbac          *auth.RBAC
	logger        *zap.Logger
}

func NewEncryptedRouteHandlers(cfg EncryptedRouteHandlersConfig) *EncryptedRouteHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedRouteHandlers{
		secretRecords: cfg.SecretRecords, secretStore: cfg.SecretStore, secretValues: cfg.SecretValues,
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

func (h *EncryptedRouteHandlers) RevealSecret(ctx context.Context, request EncryptedRequest) (any, error) {
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
	if h.secretRecords == nil {
		return nil, fmt.Errorf("secret reveal needs the canonical secret records, which are not configured")
	}
	// The canonical service record is the authority for the org: it
	// authorizes the reveal and scopes the store, and a daemon without
	// PostgreSQL can reveal.
	org, err := h.canonicalServiceOrg(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if h.rbac == nil {
		return nil, fmt.Errorf("encrypted route RBAC is not configured")
	}
	if err := h.rbac.CheckPermission(ctx, requestPrincipal(request), org, domain.PermReadSecrets); err != nil {
		return nil, err
	}
	ref, err := h.secretRecords.CurrentSecretRef(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("failed to look up secret")
	}
	if ref == nil {
		return nil, fmt.Errorf("secret not found")
	}
	if ref.ServiceID != serviceID {
		return nil, fmt.Errorf("secret does not belong to service")
	}
	if ref.CredentialRef == nil {
		// Bahia never decrypts a value it sealed with the retired key.
		return nil, domain.LegacySecretValueError(ref.ID, ref.Name)
	}
	return h.revealStoredSecret(ctx, serviceID, org, ref)
}

// revealStoredSecret resolves a secret whose value lives in the external
// secret store. The value is fetched for this request only and the buffer
// Bahia holds is zeroized once the response is built.
func (h *EncryptedRouteHandlers) canonicalServiceOrg(ctx context.Context, serviceID uuid.UUID) (uuid.UUID, error) {
	orgID, err := h.secretRecords.ServiceOrgID(ctx, serviceID)
	if err != nil {
		h.logger.Warn("secret reveal: no canonical service organization", zap.String("service_id", serviceID.String()), zap.Error(err))
		return uuid.Nil, fmt.Errorf("service organization is not available: %w", err)
	}
	org, err := uuid.Parse(orgID)
	if err != nil || org == uuid.Nil {
		return uuid.Nil, fmt.Errorf("service organization is not available")
	}
	return org, nil
}

func (h *EncryptedRouteHandlers) revealStoredSecret(ctx context.Context, serviceID, org uuid.UUID, ref *domain.SecretRef) (any, error) {
	if ref.ServiceID != serviceID {
		return nil, fmt.Errorf("secret does not belong to service")
	}
	if h.secretStore == nil {
		return nil, fmt.Errorf("secret %s is held by the secret store, which is not configured", ref.ID)
	}
	// The credential must belong to this secret's org and service; the
	// store checks the credential's label against that scope.
	value, err := h.secretStore.Resolve(ctx, *ref.CredentialRef, secretstore.Scope{OrgID: org, ServiceID: serviceID})
	if err != nil {
		h.logger.Warn("secret store refused reveal", zap.String("secret_id", ref.ID.String()), zap.Error(err))
		if errors.Is(err, secretstore.ErrNotFound) || errors.Is(err, secretstore.ErrDenied) {
			return nil, fmt.Errorf("secret store could not release secret %s: credential is not available to this org and service", ref.ID)
		}
		return nil, fmt.Errorf("secret store could not release secret %s: %w", ref.ID, err)
	}
	defer value.Zeroize()
	if !time.Now().Before(value.LeaseExpiresAt()) {
		return nil, fmt.Errorf("secret store lease for secret %s expired before it could be revealed", ref.ID)
	}
	return map[string]any{"secret": ref, "value": string(value.Bytes())}, nil
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
	if err := h.redactRunLogSecrets(ctx, run, intent, logs); err != nil {
		h.logger.Error("failed to redact encrypted request run logs", zap.String("run_id", runID.String()), zap.Error(err))
		// The reason names secrets, never values; the caller may read logs.
		return nil, fmt.Errorf("logs withheld because they cannot be safely redacted: %w", err)
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

// redactRunLogSecrets redacts the value of every secret the run's desired
// state references. Values resolve through the secret store, which releases
// only the version a secret is pinned to now: when a secret was re-pointed
// after the run began, or still holds a legacy value, the run may contain a
// value redaction cannot know, so the logs are withheld rather than returned
// unredacted.
func (h *EncryptedRouteHandlers) redactRunLogSecrets(ctx context.Context, run *domain.DeploymentRun, intent *domain.DeploymentIntent, logs *adapterruntime.RunLogs) error {
	if intent == nil || intent.DesiredState == nil || len(intent.DesiredState.SecretRefs) == 0 {
		return nil
	}
	if h.secretRecords == nil || h.secretValues == nil {
		return fmt.Errorf("secret redaction dependencies are not configured")
	}
	ids := make([]uuid.UUID, 0, len(intent.DesiredState.SecretRefs))
	seen := make(map[uuid.UUID]struct{}, len(intent.DesiredState.SecretRefs))
	for _, ref := range intent.DesiredState.SecretRefs {
		if ref.SecretID == uuid.Nil {
			return fmt.Errorf("desired secret reference is incomplete")
		}
		if _, ok := seen[ref.SecretID]; ok {
			continue
		}
		seen[ref.SecretID] = struct{}{}
		current, err := h.secretRecords.CurrentSecretRef(ctx, ref.SecretID)
		if err != nil {
			return fmt.Errorf("load referenced secret %s: %w", ref.SecretID, err)
		}
		if current == nil {
			return fmt.Errorf("referenced secret %s: %w", ref.SecretID, secretstore.ErrNoRecord)
		}
		if current.CredentialRef == nil {
			return domain.LegacySecretValueError(current.ID, current.Name)
		}
		if run != nil && current.UpdatedAt.After(run.CreatedAt) {
			return fmt.Errorf("%w: secret %s (%s) was re-pointed after this run began; the store no longer releases the value the run used, so its logs are withheld", ErrRunLogsUnredactable, current.ID, current.Name)
		}
		ids = append(ids, ref.SecretID)
	}
	resolved, err := h.secretValues.ResolveAll(ctx, ids)
	if err != nil {
		return err
	}
	defer secretstore.ZeroizeResolved(resolved)
	values := make([]string, 0, len(resolved))
	for _, r := range resolved {
		if value := string(r.Value.Bytes()); value != "" {
			values = append(values, value)
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

// ErrRunLogsUnredactable: a run's logs may contain a secret value redaction
// cannot resolve, so they are withheld.
var ErrRunLogsUnredactable = errors.New("run logs cannot be safely redacted")

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
