package controlplane

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	ArcanaRepositoryCoordinate = "chebizarro/living-library-forge"
	ArcanaRepositoryURL        = "https://github.com/chebizarro/living-library-forge"
)

var (
	fullGitSHA           = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	buildArgNamePattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	repositoryURLPattern = regexp.MustCompile(`(?i)(?:^|[:/])([^/:]+)/([^/]+?)(?:\.git)?/?$`)
	// Frozen namespace for build IDs derived from signed request intent events.
	buildIntentRequestNamespace = uuid.MustParse("24ac457f-f5f6-4eb2-bdd5-d67ca47b8b45")
)

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

// ArcanaBuildRequest is the operator-signed build intent contract.
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
	BuildID uuid.UUID
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
	ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error)
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
	Starter  HiveCIBuildStarter
	Registry BuildRegistry
	Builds   BuildResultLoader
	Services encryptedServiceLoader
	Secrets  BuildCredentialReferenceLoader
	RBAC     *auth.RBAC
}

type EncryptedBuildHandlers struct {
	starter  HiveCIBuildStarter
	registry BuildRegistry
	builds   BuildResultLoader
	services encryptedServiceLoader
	secrets  BuildCredentialReferenceLoader
	rbac     *auth.RBAC
}

func NewEncryptedBuildHandlers(cfg EncryptedBuildHandlersConfig) *EncryptedBuildHandlers {
	return &EncryptedBuildHandlers{
		starter: cfg.Starter, registry: cfg.Registry, builds: cfg.Builds,
		services: cfg.Services,
		secrets:  cfg.Secrets, rbac: cfg.RBAC,
	}
}

func (h *EncryptedBuildHandlers) requestBuild(ctx context.Context, event *nostr.Event, payload ArcanaBuildRequest) (map[string]any, error) {
	if err := validateBuildRequest(payload); err != nil {
		return nil, err
	}
	if h == nil || h.starter == nil {
		return nil, fmt.Errorf("gitea mirror and HiveCI build initiation are not configured")
	}
	if h.registry == nil || h.builds == nil || h.services == nil || h.secrets == nil {
		return nil, fmt.Errorf("build request handling is not configured")
	}

	authorizer := encryptedTenantAuthorizer{services: h.services, rbac: h.rbac}
	svc, err := authorizer.authorizeService(ctx, event, payload.ServiceID, domain.PermWriteServices)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(svc.ArtifactRepo) != payload.ArtifactRepo {
		return nil, fmt.Errorf("artifact_repo must match the service artifact repository")
	}
	if err := validateServiceBuildArgs(svc, payload.BuildArgs); err != nil {
		return nil, err
	}
	repositoryCoordinate, err := serviceRepositoryCoordinate(svc)
	if err != nil {
		return nil, err
	}
	sourceEventID := ""
	requesterPubkey := ""
	if event != nil {
		if event.ID == (nostr.ID{}) {
			return nil, fmt.Errorf("build/request requires a signed source event ID")
		}
		sourceEventID = event.ID.Hex()
		requesterPubkey = event.PubKey.Hex()
	}
	buildID := buildIDFromIntentEvent(sourceEventID)
	existing, err := h.builds.GetByID(ctx, buildID)
	if err != nil {
		return nil, fmt.Errorf("load canonical build: %w", err)
	}
	if existing != nil {
		if err := validateCanonicalBuildIdentity(existing, buildID, payload.ServiceID, sourceEventID); err != nil {
			return nil, err
		}
		return buildRequestResult(existing), nil
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

	result, err := h.starter.StartHiveCIBuild(ctx, HiveCIBuildStartRequest{
		BuildID: buildID, ServiceID: payload.ServiceID,
		RepositoryCoordinate: repositoryCoordinate,
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
	if result.BuildID != uuid.Nil && result.BuildID != buildID {
		return nil, fmt.Errorf("build initiator returned a conflicting canonical build ID")
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
			"repository_coordinate": repositoryCoordinate,
			"artifact_repo":         payload.ArtifactRepo,
			"build_args":            cloneStringMap(payload.BuildArgs),
			"evidence":              map[string]any{"request_event_id": sourceEventID},
		},
	}
	existing, err = h.builds.GetByID(ctx, build.ID)
	if err != nil {
		return nil, fmt.Errorf("load canonical build: %w", err)
	}
	if existing != nil {
		if err := validateCanonicalBuild(existing, build); err != nil {
			return nil, err
		}
		build = existing
	} else if err := h.registry.RegisterBuild(ctx, build); err != nil {
		// PgBuildRepository intentionally uses a plain INSERT. A concurrent exact
		// replay can therefore lose the insert race; accept that error only when
		// the canonical deterministic row is now present and matches this request.
		existing, loadErr := h.builds.GetByID(ctx, build.ID)
		if loadErr != nil {
			return nil, fmt.Errorf("register queued build: %v; reload canonical build: %w", err, loadErr)
		}
		if existing == nil {
			return nil, fmt.Errorf("register queued build: %w", err)
		}
		if conflictErr := validateCanonicalBuild(existing, build); conflictErr != nil {
			return nil, fmt.Errorf("register queued build: %v; %w", err, conflictErr)
		}
		build = existing
	}
	return buildRequestResult(build), nil
}

func buildIDFromIntentEvent(sourceEventID string) uuid.UUID {
	return BuildIDForSourceEvent(sourceEventID)
}

// BuildIDForSourceEvent is the canonical build identity of a signed
// build/request: the request intent event id, namespaced. Every daemon, and
// every replay, derives the same id from the same signed request, so the
// build needs no claim to be named (audit C-49).
func BuildIDForSourceEvent(sourceEventID string) uuid.UUID {
	return uuid.NewSHA1(buildIntentRequestNamespace, []byte(strings.TrimSpace(sourceEventID)))
}

func validateCanonicalBuildIdentity(existing *domain.Build, buildID, serviceID uuid.UUID, sourceEventID string) error {
	if existing == nil || existing.ID != buildID || existing.ServiceID != serviceID ||
		existing.CISystem != domain.CISystemHiveCI || strings.TrimSpace(existing.CIRunID) == "" ||
		existing.SourceEventID != sourceEventID {
		return &intentStateConflictError{message: fmt.Sprintf("canonical build %s conflicts with replayed build request", buildID)}
	}
	return nil
}

func validateCanonicalBuild(existing, requested *domain.Build) error {
	if existing == nil || requested == nil {
		return fmt.Errorf("canonical build replay is incomplete")
	}
	if existing.ID != requested.ID ||
		existing.ServiceID != requested.ServiceID ||
		!strings.EqualFold(strings.TrimSpace(existing.GitSHA), strings.TrimSpace(requested.GitSHA)) ||
		strings.TrimSpace(existing.GitRef) != strings.TrimSpace(requested.GitRef) ||
		existing.CISystem != requested.CISystem || existing.CIRunID != requested.CIRunID ||
		existing.SourceEventID != requested.SourceEventID {
		return &intentStateConflictError{message: fmt.Sprintf("canonical build %s conflicts with replayed build request", requested.ID)}
	}
	return nil
}

func buildRequestResult(build *domain.Build) map[string]any {
	return map[string]any{
		"build_id": build.ID, "status": build.Status, "git_sha": build.GitSHA,
		"git_ref": build.GitRef, "ci_system": build.CISystem, "ci_run_id": build.CIRunID,
	}
}

func validateBuildRequest(payload ArcanaBuildRequest) error {
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
	return validateGenericBuildArgs(payload.BuildArgs)
}

func validateGenericBuildArgs(buildArgs map[string]string) error {
	if len(buildArgs) > 64 {
		return fmt.Errorf("build_args contains too many values")
	}
	for key, value := range buildArgs {
		if !buildArgNamePattern.MatchString(key) {
			return fmt.Errorf("build arg %q is invalid", key)
		}
		if len(value) > 2048 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("build arg %q contains an invalid value", key)
		}
	}
	return nil
}

func validateServiceBuildArgs(svc *domain.Service, buildArgs map[string]string) error {
	if len(buildArgs) == 0 {
		return nil
	}
	if !isArcanaService(svc) {
		return fmt.Errorf("build_args are only available for services with an approved public build argument allowlist")
	}
	for key := range buildArgs {
		if _, ok := arcanaPublicBuildArgs[key]; !ok {
			return fmt.Errorf("build arg %q is not an approved public Arcana Vite setting", key)
		}
	}
	if mode := strings.TrimSpace(buildArgs["VITE_ARCANA_SIGNER_MODE"]); mode != "" && mode != "nip07" && mode != "nip46" {
		return fmt.Errorf("VITE_ARCANA_SIGNER_MODE must be nip07 or nip46")
	}
	return nil
}

func serviceRepositoryCoordinate(svc *domain.Service) (string, error) {
	if svc == nil {
		return "", fmt.Errorf("service is required")
	}
	if svc.Repository != nil {
		if coordinate := strings.TrimSpace(svc.Repository.RepoCoordinate); coordinate != "" {
			return coordinate, nil
		}
	}
	if coordinate := repositoryCoordinateFromURL(svc.RepoURL); coordinate != "" {
		return coordinate, nil
	}
	return "", fmt.Errorf("service repository coordinate is required for HiveCI build routing")
}

func repositoryCoordinateFromURL(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return ""
	}
	matches := repositoryURLPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return ""
	}
	owner := strings.TrimSpace(matches[1])
	repo := strings.TrimSuffix(strings.TrimSpace(matches[2]), ".git")
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
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
