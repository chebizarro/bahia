package controlplane

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	ContextVMMethodBuildRequest                = "build/request"
	ContextVMMethodArtifactRegisterBuildResult = "artifact/register-build-result"

	ArcanaRepositoryCoordinate = "chebizarro/living-library-forge"
	ArcanaRepositoryURL        = "https://github.com/chebizarro/living-library-forge"
)

var fullGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

var ArcanaPublicBuildArgNames = []string{
	"VITE_ARCANA_READ_RELAYS",
	"VITE_ARCANA_WRITE_RELAYS",
	"VITE_ARCANA_SIGNER_MODE",
	"VITE_ARCANA_SEARCH_DVM_PUBKEY",
	"VITE_BLOSSOM_URL",
	"VITE_ARCANA_INFERENCE_URL",
	"VITE_ARCANA_WORKFLOW_API_URL",
	"VITE_SUPABASE_URL",
	"VITE_SUPABASE_PUBLISHABLE_KEY",
}

var arcanaPublicBuildArgs = func() map[string]struct{} {
	allowed := make(map[string]struct{}, len(ArcanaPublicBuildArgNames))
	for _, name := range ArcanaPublicBuildArgNames {
		allowed[name] = struct{}{}
	}
	return allowed
}()

// ArcanaBuildRequest is the browser-facing, signed ContextVM build contract.
// repository_credential_ref is an opaque server-side secret ID. The secret value
// is never accepted in this payload and build_args are restricted to public
// compile-time values.
type ArcanaBuildRequest struct {
	ServiceID               uuid.UUID         `json:"service_id"`
	GitRef                  string            `json:"git_ref"`
	RepositoryCredentialRef uuid.UUID         `json:"repository_credential_ref"`
	ArtifactRepo            string            `json:"artifact_repo"`
	BuildArgs               map[string]string `json:"build_args"`
}

// HiveCIBuildStartRequest is passed to the server-side Gitea mirror/HiveCI
// initiation adapter. CredentialRef remains opaque; the adapter must resolve it
// from protected server storage and must never encode credentials in Nostr.
type HiveCIBuildStartRequest struct {
	BuildID              uuid.UUID
	ServiceID            uuid.UUID
	RepositoryCoordinate string
	GitRef               string
	CredentialRef        uuid.UUID
	ArtifactRepo         string
	BuildArgs            map[string]string
	RequesterPubkey      string
	SourceEventID        string
}

type HiveCIBuildStartResult struct {
	GitSHA  string
	GitRef  string
	CIRunID string
}

// HiveCIBuildStarter is deliberately not implemented by a direct GitHub fetcher.
// The production implementation belongs at the fleet Gitea mirror boundary.
type HiveCIBuildStarter interface {
	StartHiveCIBuild(context.Context, HiveCIBuildStartRequest) (*HiveCIBuildStartResult, error)
}

type BuildRegistry interface {
	RegisterBuild(context.Context, *domain.Build) error
}

type BuildResultLoader interface {
	GetByID(context.Context, uuid.UUID) (*domain.Build, error)
}

type BuildResultArtifactRegistrar interface {
	RegisterBuildResult(context.Context, uuid.UUID) (*domain.Artifact, error)
}

// BuildCredentialReferenceLoader intentionally exposes lookup only. Build
// initiation cannot list, reveal, update, or delete protected credentials.
type BuildCredentialReferenceLoader interface {
	GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error)
}

type EncryptedBuildHandlersConfig struct {
	Starter           HiveCIBuildStarter
	Registry          BuildRegistry
	Builds            BuildResultLoader
	ArtifactRegistrar BuildResultArtifactRegistrar
	Services          encryptedServiceLoader
	Secrets           BuildCredentialReferenceLoader
	RBAC              *auth.RBAC
}

type EncryptedBuildHandlers struct {
	starter           HiveCIBuildStarter
	registry          BuildRegistry
	builds            BuildResultLoader
	artifactRegistrar BuildResultArtifactRegistrar
	services          encryptedServiceLoader
	secrets           BuildCredentialReferenceLoader
	rbac              *auth.RBAC
}

func NewEncryptedBuildHandlers(cfg EncryptedBuildHandlersConfig) *EncryptedBuildHandlers {
	return &EncryptedBuildHandlers{
		starter: cfg.Starter, registry: cfg.Registry, builds: cfg.Builds,
		artifactRegistrar: cfg.ArtifactRegistrar, services: cfg.Services,
		secrets: cfg.Secrets, rbac: cfg.RBAC,
	}
}

func (h *EncryptedBuildHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	transport.RegisterContextVMHandler(ContextVMMethodBuildRequest, h.RequestBuild)
	transport.RegisterContextVMHandler(ContextVMMethodArtifactRegisterBuildResult, h.RegisterBuildResult)
}

func (h *EncryptedBuildHandlers) RequestBuild(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload ArcanaBuildRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode build/request params: %w", err)
	}
	if err := validateArcanaBuildRequest(payload); err != nil {
		return nil, err
	}
	if h == nil || h.starter == nil {
		return nil, fmt.Errorf("Gitea mirror and HiveCI build initiation are not configured")
	}
	if h.registry == nil || h.services == nil || h.secrets == nil {
		return nil, fmt.Errorf("build request handling is not configured")
	}

	authorizer := encryptedTenantAuthorizer{services: h.services, rbac: h.rbac}
	svc, err := authorizer.authorizeService(ctx, request.Event, payload.ServiceID, domain.PermWriteServices)
	if err != nil {
		return nil, err
	}
	if !isArcanaService(svc) {
		return nil, fmt.Errorf("service repository must be %s", ArcanaRepositoryCoordinate)
	}
	if strings.TrimSpace(svc.ArtifactRepo) != payload.ArtifactRepo {
		return nil, fmt.Errorf("artifact_repo must match the service artifact repository")
	}

	secret, err := h.secrets.GetByID(ctx, payload.RepositoryCredentialRef)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("repository credential reference not found")
		}
		return nil, fmt.Errorf("fetch repository credential reference")
	}
	if secret == nil || secret.ServiceID != payload.ServiceID {
		return nil, fmt.Errorf("repository credential reference must belong to the selected service")
	}

	buildID := uuid.New()
	sourceEventID := ""
	requesterPubkey := ""
	if request.Event != nil {
		sourceEventID = request.Event.ID.Hex()
		requesterPubkey = request.Event.PubKey.Hex()
	}
	result, err := h.starter.StartHiveCIBuild(ctx, HiveCIBuildStartRequest{
		BuildID: buildID, ServiceID: payload.ServiceID,
		RepositoryCoordinate: ArcanaRepositoryCoordinate,
		GitRef:               payload.GitRef, CredentialRef: payload.RepositoryCredentialRef,
		ArtifactRepo: payload.ArtifactRepo, BuildArgs: cloneStringMap(payload.BuildArgs),
		RequesterPubkey: requesterPubkey, SourceEventID: sourceEventID,
	})
	if err != nil {
		return nil, fmt.Errorf("request Gitea mirror/HiveCI build: %w", err)
	}
	if result == nil || !fullGitSHA.MatchString(strings.TrimSpace(result.GitSHA)) || strings.TrimSpace(result.CIRunID) == "" {
		return nil, fmt.Errorf("build initiator did not return an immutable commit and CI run ID")
	}
	resolvedRef := strings.TrimSpace(result.GitRef)
	if resolvedRef == "" {
		resolvedRef = payload.GitRef
	}
	build := &domain.Build{
		ID: buildID, ServiceID: payload.ServiceID,
		GitSHA: strings.ToLower(strings.TrimSpace(result.GitSHA)), GitRef: resolvedRef,
		CISystem: domain.CISystemHiveCI, CIRunID: strings.TrimSpace(result.CIRunID),
		Status: domain.BuildStatusQueued, SourceEventID: sourceEventID,
		Metadata: map[string]any{
			"repository_coordinate": ArcanaRepositoryCoordinate,
			"artifact_repo":         payload.ArtifactRepo,
			"build_args":            cloneStringMap(payload.BuildArgs),
			"evidence":              map[string]any{"request_event_id": sourceEventID},
		},
	}
	if err := h.registry.RegisterBuild(ctx, build); err != nil {
		return nil, fmt.Errorf("register queued build: %w", err)
	}
	return map[string]any{
		"build_id": build.ID, "status": build.Status, "git_sha": build.GitSHA,
		"git_ref": build.GitRef, "ci_system": build.CISystem, "ci_run_id": build.CIRunID,
	}, nil
}

func (h *EncryptedBuildHandlers) RegisterBuildResult(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload struct {
		BuildID uuid.UUID `json:"build_id"`
	}
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode artifact/register-build-result params: %w", err)
	}
	if payload.BuildID == uuid.Nil {
		return nil, fmt.Errorf("build_id is required")
	}
	if h == nil || h.builds == nil || h.artifactRegistrar == nil || h.services == nil {
		return nil, fmt.Errorf("build-result artifact registration is not configured")
	}
	build, err := h.builds.GetByID(ctx, payload.BuildID)
	if err != nil {
		return nil, fmt.Errorf("fetch build: %w", err)
	}
	if build == nil {
		return nil, fmt.Errorf("build %s not found", payload.BuildID)
	}
	authorizer := encryptedTenantAuthorizer{services: h.services, rbac: h.rbac}
	if _, err := authorizer.authorizeService(ctx, request.Event, build.ServiceID, domain.PermWriteServices); err != nil {
		return nil, err
	}
	if build.Status != domain.BuildStatusSucceeded {
		return nil, fmt.Errorf("only a successful build result can register an artifact")
	}
	artifact, err := h.artifactRegistrar.RegisterBuildResult(ctx, build.ID)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, fmt.Errorf("build result did not produce a verified artifact")
	}
	return map[string]any{
		"artifact_id":         artifact.ID,
		"build_id":            artifact.BuildID,
		"service_id":          artifact.ServiceID,
		"image_repo":          artifact.ImageRepo,
		"image_tag":           artifact.ImageTag,
		"manifest_digest":     artifact.ImageDigest,
		"manifest_media_type": artifact.ManifestMediaType,
		"scan_status":         artifact.ScanStatus,
		"signature_ref":       artifact.SignatureRef,
		"sbom_url":            artifact.SBOMURL,
		"metadata":            artifact.Metadata,
	}, nil
}

func validateArcanaBuildRequest(payload ArcanaBuildRequest) error {
	if payload.ServiceID == uuid.Nil {
		return fmt.Errorf("service_id is required")
	}
	ref := strings.TrimSpace(payload.GitRef)
	if ref == "" || len(ref) > 255 {
		return fmt.Errorf("git_ref is required and must be at most 255 characters")
	}
	if strings.ContainsAny(ref, "\x00\r\n") {
		return fmt.Errorf("git_ref contains invalid control characters")
	}
	if payload.RepositoryCredentialRef == uuid.Nil {
		return fmt.Errorf("repository_credential_ref is required")
	}
	if strings.TrimSpace(payload.ArtifactRepo) == "" {
		return fmt.Errorf("artifact_repo is required")
	}
	if len(payload.BuildArgs) > len(arcanaPublicBuildArgs) {
		return fmt.Errorf("build_args contains too many values")
	}
	for key, value := range payload.BuildArgs {
		if _, ok := arcanaPublicBuildArgs[key]; !ok {
			return fmt.Errorf("build arg %q is not an approved public Arcana Vite setting", key)
		}
		if len(value) > 2048 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("build arg %q contains an invalid value", key)
		}
	}
	if mode := strings.TrimSpace(payload.BuildArgs["VITE_ARCANA_SIGNER_MODE"]); mode != "" && mode != "nip07" && mode != "nip46" {
		return fmt.Errorf("VITE_ARCANA_SIGNER_MODE must be nip07 or nip46")
	}
	return nil
}

func isArcanaService(svc *domain.Service) bool {
	if svc == nil {
		return false
	}
	if svc.Repository != nil && strings.EqualFold(strings.TrimSpace(svc.Repository.RepoCoordinate), ArcanaRepositoryCoordinate) {
		return true
	}
	url := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(svc.RepoURL)), ".git")
	return url == strings.ToLower(ArcanaRepositoryURL)
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
