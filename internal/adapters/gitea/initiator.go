package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/google/uuid"
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// SecretResolver resolves opaque server-side credential references to
// plaintext values with audit recording. Matches secrets.Resolver.
type SecretResolver interface {
	ResolveSecretWithAudit(ctx context.Context, ref string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error)
}

// MirrorClient is the fleet Gitea surface the initiator depends on.
type MirrorClient interface {
	GetRepo(ctx context.Context, owner, name string) (*RepoInfo, error)
	MigrateMirror(ctx context.Context, req MigrateMirrorRequest) error
	SyncMirror(ctx context.Context, owner, name string) error
	ResolveRef(ctx context.Context, owner, name, ref string) (string, error)
}

// EventPublisher publishes signed Nostr events to the control-plane relay set.
type EventPublisher interface {
	Publish(ctx context.Context, ev nostr.Event) (int, error)
}

// LoomJobSubmitter is the existing kind-5100 Loom client surface used after a
// self-issued workflow run has been accepted by the control-plane relays.
type LoomJobSubmitter interface {
	SubmitJob(ctx context.Context, job loomAdapter.JobRequest) (string, error)
}

// InitiatorConfig configures the fleet Gitea mirror / Hive-CI initiator.
type InitiatorConfig struct {
	// GiteaBaseURL is the trusted fleet Gitea origin. Mirror clone credentials
	// may be sent only to this origin.
	GiteaBaseURL string
	// MirrorOwner is the fleet Gitea organization or user owning private mirrors.
	MirrorOwner string
	// WorkflowPath is the Hive-CI workflow invoked for Arcana builds.
	WorkflowPath string
	// SourceProvider explicitly selects the upstream provider. Supported values
	// are "github" and "gitea"; it is never inferred from SourceCloneURL.
	SourceProvider string
	// SourceCloneURL identifies the upstream. It is optional for GitHub, where
	// the request repository coordinate is used, and required for Gitea.
	SourceCloneURL string
	// SourceAuthUsername is the non-secret username paired with the resolved
	// credential for a private Gitea source.
	SourceAuthUsername string
	// MirrorReadUsername is the non-secret fleet Gitea username used only to
	// clone the private mirror. Operators must grant this identity read-only
	// access to the configured mirror namespace.
	MirrorReadUsername string
	// MirrorReadCredentialRef is the opaque service-secret UUID containing the
	// read-only fleet Gitea password/token. It is resolved only when dispatching
	// to Loom and never placed in a plaintext event tag.
	MirrorReadCredentialRef string
	// RepoAnnouncementAddr carries the NIP-34 kind-30617 address
	// ("30617:<pubkey>:<repo-id>") of the fleet mirror announcement for
	// canonical run-request correlation.
	RepoAnnouncementAddr string
	// TrustedCIPubkeys is the operator-managed allowlist consumed by Bahia's
	// Hive-CI subscriber. It is inspected only to make untrusted self-dispatch
	// legible; the initiator never mutates or expands it.
	TrustedCIPubkeys []string
	// TrustedLoomWorkerPubkeys is both the dispatch allowlist and the result
	// signer allowlist. Keeping them identical prevents dispatching work whose
	// worker-signed 5402 Bahia would reject.
	TrustedLoomWorkerPubkeys []string
	// BuildDependencyAuthorizations are resolved from fleet-owned service
	// policies at startup. Repository-controlled workflow input cannot alter them.
	BuildDependencyAuthorizations []BuildDependencyAuthorization
	// RelayHint is included on published events so consumers can locate them.
	RelayHint string
	// RefResolveAttempts and RefResolveDelay bound the mirror-sync poll loop.
	RefResolveAttempts int
	RefResolveDelay    time.Duration
}

// BuildDependencyAuthorization binds one complete dependency set to a Bahia
// service. An empty set is still an explicit authorization.
type BuildDependencyAuthorization struct {
	ServiceID    uuid.UUID
	Dependencies []BuildDependencySpec
}

const (
	SourceProviderGitHub = "github"
	SourceProviderGitea  = "gitea"

	hiveCIGitUsernameSecretKey = "HIVE_CI_GIT_USERNAME"
	hiveCIGitPasswordSecretKey = "HIVE_CI_GIT_PASSWORD"
)

type sourceMirrorConfig struct {
	cloneURL     string
	service      string
	authUsername string
}

// Initiator implements controlplane.HiveCIBuildStarter against a fleet Gitea
// private mirror and the fleet-local Hive-CI workflow-run boundary.
type Initiator struct {
	client     MirrorClient
	secrets    SecretResolver
	publisher  EventPublisher
	signer     nostr.Signer
	store      InitiationStore
	runLookup  WorkflowRunLookup
	inspection PublicationInspector
	cfg        InitiatorConfig
	logger     *zap.Logger
	loom       LoomJobSubmitter
	mu         sync.Mutex
}

// ReloadMirrorReadCredentialRef atomically replaces the opaque secret reference
// used for private sibling repository staging. Initiate serializes on the same
// mutex, so a running build observes either the complete old value or the
// complete new value.
func (i *Initiator) ReloadMirrorReadCredentialRef(ref string) error {
	ref = strings.TrimSpace(ref)
	parsed, err := uuid.Parse(ref)
	if err != nil || parsed == uuid.Nil {
		return fmt.Errorf("mirror-read credential reference must be a non-zero UUID")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cfg.MirrorReadCredentialRef = parsed.String()
	return nil
}

type InitiatorOption func(*Initiator)

// WithLoomJobSubmitter enables kind-5100 submission after the signed kind-5401
// publication. Tests and disabled deployments may omit it.
// WorkflowRunLookup resolves an already-ingested trusted kind-5401 for the
// hive-ci-protocol identity tuple (repository coordinate, commit, workflow).
// Grasp publishes exactly one such run per accepted push; Bahia must reuse it
// rather than publish a competing run and a second Loom job.
type WorkflowRunLookup interface {
	FindWorkflowRun(ctx context.Context, repoCoordinate, commitSHA, workflowPath string) (*domain.HiveCIWorkflowRun, error)
}

// WithWorkflowRunLookup enables build de-duplication against runs ingested
// from trusted CI producers (grasp-gitea, or Bahia's own earlier dispatch).
func WithWorkflowRunLookup(lookup WorkflowRunLookup) InitiatorOption {
	return func(i *Initiator) { i.runLookup = lookup }
}

func WithLoomJobSubmitter(submitter LoomJobSubmitter) InitiatorOption {
	return func(i *Initiator) { i.loom = submitter }
}

func NewInitiator(client MirrorClient, secrets SecretResolver, publisher EventPublisher, signer nostr.Signer, store InitiationStore, cfg InitiatorConfig, logger *zap.Logger, opts ...InitiatorOption) *Initiator {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.RefResolveAttempts <= 0 {
		cfg.RefResolveAttempts = 10
	}
	if cfg.RefResolveDelay <= 0 {
		cfg.RefResolveDelay = 3 * time.Second
	}
	initiator := &Initiator{
		client: client, secrets: secrets, publisher: publisher, signer: signer,
		store: store, cfg: cfg, logger: logger.Named("gitea-hiveci-initiator"),
	}
	for _, opt := range opts {
		opt(initiator)
	}
	return initiator
}

var _ controlplane.HiveCIBuildStarter = (*Initiator)(nil)

// StartHiveCIBuild mirrors the private upstream repository into fleet Gitea,
// resolves the requested ref to an immutable commit, publishes the canonical
// ci/workflow-run request and addressed queued evidence, and returns the
// commit/run correlation. Exact replay of the same source event returns the
// original result without side effects.
func (i *Initiator) StartHiveCIBuild(ctx context.Context, req controlplane.HiveCIBuildStartRequest) (*controlplane.HiveCIBuildStartResult, error) {
	if i == nil || i.client == nil || i.secrets == nil || i.publisher == nil || i.signer == nil || i.store == nil {
		return nil, fmt.Errorf("fleet Gitea mirror initiator is not fully configured")
	}
	if strings.TrimSpace(i.cfg.MirrorOwner) == "" || strings.TrimSpace(i.cfg.WorkflowPath) == "" || strings.TrimSpace(i.cfg.RepoAnnouncementAddr) == "" {
		return nil, fmt.Errorf("fleet Gitea mirror initiator requires mirror owner, workflow path, and repository announcement address")
	}
	if err := validateRepoAnnouncementAddr(i.cfg.RepoAnnouncementAddr); err != nil {
		return nil, err
	}
	if len(req.BuildArgs) > 0 {
		return nil, fmt.Errorf("Hive-CI kind-5401 tag-only dispatch does not support build arguments")
	}
	_, _, err := splitRepositoryCoordinate(req.RepositoryCoordinate)
	if err != nil {
		return nil, err
	}
	gitRef := strings.TrimSpace(req.GitRef)
	if gitRef == "" {
		return nil, fmt.Errorf("git_ref is required")
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	record, _, err := i.store.Claim(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("claim build initiation: %w", err)
	}
	if record.Stage == StageClaimed {
		if err := i.prepareInitiation(ctx, record); err != nil {
			return nil, err
		}
	}
	return i.resumeInitiation(ctx, record)
}

func (i *Initiator) prepareInitiation(ctx context.Context, rec *InitiationRecord) error {
	req := rec.Request
	owner, name, err := splitRepositoryCoordinate(req.RepositoryCoordinate)
	if err != nil {
		return err
	}
	gitRef := strings.TrimSpace(req.GitRef)
	idempotencyKey := rec.SourceEventID
	source, err := i.sourceMirrorConfig(owner, name)
	if err != nil {
		return err
	}

	// Resolve the dedicated fleet-mirror read credential before any event is
	// published. Exact replay returned above never re-resolves or re-encrypts it.
	// The manifest check preserves the service-scoped secret boundary even
	// though the opaque reference is supplied by trusted server configuration.
	mirrorReadUsername := strings.TrimSpace(i.cfg.MirrorReadUsername)
	mirrorReadCredentialRef := strings.TrimSpace(i.cfg.MirrorReadCredentialRef)
	mirrorReadPassword := ""
	if i.loom != nil {
		if mirrorReadUsername == "" || strings.ContainsAny(mirrorReadUsername, "\x00\r\n") || mirrorReadCredentialRef == "" {
			return fmt.Errorf("loom Hive-CI dispatch requires a valid mirror-read username and credential reference")
		}
		mirrorReadSecretID, parseErr := uuid.Parse(mirrorReadCredentialRef)
		if parseErr != nil || mirrorReadSecretID == uuid.Nil {
			return fmt.Errorf("loom Hive-CI mirror-read credential reference must be a non-zero secret UUID")
		}
		var manifest domain.SecretAccessManifest
		mirrorReadPassword, manifest, err = i.secrets.ResolveSecretWithAudit(ctx, mirrorReadCredentialRef, domain.SecretResolveOptions{
			Operation: domain.SecretAccessOperationResolve,
			Actor:     req.RequesterPubkey,
			Reason:    "fleet gitea private-mirror read for Loom Hive-CI dispatch",
			RequestID: idempotencyKey,
		})
		if err != nil {
			return scrubSecrets(fmt.Errorf("resolve mirror-read credential reference: %w", err), mirrorReadPassword)
		}
		if strings.TrimSpace(mirrorReadPassword) == "" {
			return fmt.Errorf("mirror-read credential reference resolved to an empty credential")
		}
		if manifest.SecretID != mirrorReadSecretID || manifest.ServiceID != req.ServiceID {
			return scrubSecrets(fmt.Errorf("mirror-read credential reference must belong to the selected service"), mirrorReadPassword)
		}
	}

	// Resolve the opaque upstream credential reference server-side with audit.
	// The plaintext credential stays in memory and is passed only inside the
	// HTTPS body of the Gitea migrate call, in the provider-specific auth field.
	token, _, err := i.secrets.ResolveSecretWithAudit(ctx, req.CredentialRef.String(), domain.SecretResolveOptions{
		Operation: domain.SecretAccessOperationResolve,
		Actor:     req.RequesterPubkey,
		Reason:    "fleet gitea private-mirror build initiation",
		RequestID: idempotencyKey,
	})
	if err != nil {
		// The resolver can surface errors after producing a value (e.g. audit
		// persistence failures); scrub defensively.
		return scrubSecrets(fmt.Errorf("resolve repository credential reference: %w", err), token, mirrorReadPassword)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("repository credential reference resolved to an empty credential")
	}

	repoInfo, err := i.client.GetRepo(ctx, i.cfg.MirrorOwner, name)
	if err != nil {
		return scrubSecrets(fmt.Errorf("check fleet mirror: %w", err), token, mirrorReadPassword)
	}
	if repoInfo == nil {
		migration := MigrateMirrorRequest{
			Owner: i.cfg.MirrorOwner, Name: name, CloneAddr: source.cloneURL, Service: source.service,
		}
		if source.service == MigrationServiceGit {
			migration.AuthUsername = source.authUsername
			migration.AuthPassword = token
		} else {
			migration.AuthToken = token
		}
		if err := i.client.MigrateMirror(ctx, migration); err != nil {
			return scrubSecrets(fmt.Errorf("create fleet private mirror: %w", err), token, mirrorReadPassword)
		}
		// Re-fetch to validate the mirror we (or a concurrent initiation)
		// created before trusting it.
		repoInfo, err = i.client.GetRepo(ctx, i.cfg.MirrorOwner, name)
		if err != nil || repoInfo == nil {
			return scrubSecrets(fmt.Errorf("fleet private mirror is unavailable after migration"), token, mirrorReadPassword)
		}
		if err := i.validateMirror(repoInfo, source.cloneURL); err != nil {
			return err
		}
	} else {
		// Never trust a pre-existing repository by name alone: it must be a
		// private mirror of the expected upstream, or we fail closed.
		if err := i.validateMirror(repoInfo, source.cloneURL); err != nil {
			return err
		}
		if err := i.client.SyncMirror(ctx, i.cfg.MirrorOwner, name); err != nil {
			return scrubSecrets(fmt.Errorf("sync fleet private mirror: %w", err), token, mirrorReadPassword)
		}
	}

	sha, err := i.resolveRefWithRetry(ctx, name, gitRef)
	if err != nil {
		return scrubSecrets(err, token, mirrorReadPassword)
	}
	pinnedDependencies, err := i.resolveAuthorizedBuildDependencies(ctx, req.ServiceID)
	if err != nil {
		return scrubSecrets(err, token, mirrorReadPassword)
	}

	rec.Result.GitSHA, rec.Result.GitRef = sha, gitRef
	rec.RunRequestID = "bahia:" + req.BuildID.String() + ":" + sha
	rec.MirrorRepository = i.cfg.MirrorOwner + "/" + name
	rec.MirrorReadCredentialRef = mirrorReadCredentialRef
	rec.MirrorReadUsername = mirrorReadUsername
	if existing, reuseErr := i.existingWorkflowRun(ctx, sha); reuseErr != nil {
		return scrubSecrets(reuseErr, token, mirrorReadPassword)
	} else if existing != nil {
		rec.Result.CIRunID = existing.RunEventID
		i.logger.Info("adopted existing Hive-CI workflow run",
			zap.String("reason", "workflow_run_reused"), zap.String("ci_run_id", existing.RunEventID))
		return i.advance(ctx, rec, StageRequestPublished)
	}
	rec.RunEvent, rec.PublisherNsec, err = i.prepareWorkflowRunRequest(ctx, req, name, sha, gitRef)
	if err != nil {
		return scrubSecrets(err, token, mirrorReadPassword)
	}
	rec.Result.CIRunID = rec.RunEvent.ID.Hex()
	if i.loom != nil {
		rec.LoomJob, err = i.prepareLoomWorkflowJob(repoInfo, req, name, gitRef, rec.Result.CIRunID, pinnedDependencies)
		if err != nil {
			return scrubSecrets(err, token, mirrorReadPassword, rec.PublisherNsec)
		}
	}
	return i.advance(ctx, rec, StageRequestReady)
}

func (i *Initiator) sourceMirrorConfig(owner, name string) (sourceMirrorConfig, error) {
	provider := strings.ToLower(strings.TrimSpace(i.cfg.SourceProvider))
	cloneURL := strings.TrimSpace(i.cfg.SourceCloneURL)
	var source sourceMirrorConfig
	switch provider {
	case SourceProviderGitHub:
		if strings.TrimSpace(i.cfg.SourceAuthUsername) != "" {
			return sourceMirrorConfig{}, fmt.Errorf("source auth username is not valid for github token authentication")
		}
		if cloneURL == "" {
			cloneURL = "https://github.com/" + owner + "/" + name + ".git"
		}
		source = sourceMirrorConfig{cloneURL: cloneURL, service: MigrationServiceGitHub}
		if err := validateGitHubCloneURL(cloneURL); err != nil {
			return sourceMirrorConfig{}, err
		}
	case SourceProviderGitea:
		if cloneURL == "" {
			return sourceMirrorConfig{}, fmt.Errorf("source clone URL is required for gitea provider")
		}
		username := strings.TrimSpace(i.cfg.SourceAuthUsername)
		if username == "" {
			return sourceMirrorConfig{}, fmt.Errorf("source auth username is required for gitea provider")
		}
		source = sourceMirrorConfig{cloneURL: cloneURL, service: MigrationServiceGit, authUsername: username}
		if err := validateSourceCloneURL(cloneURL); err != nil {
			return sourceMirrorConfig{}, err
		}
	default:
		return sourceMirrorConfig{}, fmt.Errorf("source provider must be explicitly configured as github or gitea")
	}
	return source, nil
}

// prepareLoomWorkflowJob prepares the spec-shaped loom-protocol kind-5100 job:
// cmd=loom-ci with argv, plus NIP-44 secret tags for the mirror credential and
// the per-run kind-5401 publisher key. No non-spec selector tags are used.
func (i *Initiator) prepareLoomWorkflowJob(repoInfo *RepoInfo, req controlplane.HiveCIBuildStartRequest, name, ref, runEventID string, pinnedDependencies []PinnedBuildDependency) (*loomAdapter.JobRequest, error) {
	if len(i.cfg.TrustedLoomWorkerPubkeys) == 0 {
		return nil, fmt.Errorf("trusted Loom worker pubkey allowlist is empty")
	}
	repository := ""
	if repoInfo != nil {
		repository = strings.TrimSpace(repoInfo.CloneURL)
	}
	if repository == "" {
		return nil, fmt.Errorf("fleet mirror clone URL is unavailable")
	}
	if err := validateFleetMirrorCloneURL(repository, i.cfg.GiteaBaseURL, i.cfg.MirrorOwner, name); err != nil {
		return nil, err
	}
	dependencies := make([]loomAdapter.BuildDependency, 0, len(pinnedDependencies))
	for _, dependency := range pinnedDependencies {
		dependencies = append(dependencies, loomAdapter.BuildDependency{
			Name: dependency.Name, CloneURL: dependency.CloneURL, CommitSHA: dependency.CommitSHA,
		})
	}
	args, err := loomAdapter.HiveCIJobArgs(loomAdapter.HiveCIJobSpec{
		Repository: repository, Ref: ref, Workflow: i.cfg.WorkflowPath, Event: "push",
		Actor: req.RequesterPubkey, RunEventID: runEventID, Dependencies: dependencies,
	})
	if err != nil {
		return nil, err
	}
	return &loomAdapter.JobRequest{
		ID:                   runEventID,
		ReferencedEventID:    runEventID,
		Type:                 "build",
		Cmd:                  loomAdapter.LoomCICommand,
		Args:                 args,
		RequiredSoftware:     []string{"git", "act", "docker", loomAdapter.LoomCICommand},
		AllowedWorkerPubkeys: append([]string(nil), i.cfg.TrustedLoomWorkerPubkeys...),
	}, nil
}

func (i *Initiator) resolveAuthorizedBuildDependencies(ctx context.Context, serviceID uuid.UUID) ([]PinnedBuildDependency, error) {
	if i.loom == nil {
		return nil, nil
	}
	if len(i.cfg.BuildDependencyAuthorizations) == 0 {
		return nil, nil
	}
	for _, authorization := range i.cfg.BuildDependencyAuthorizations {
		if authorization.ServiceID != serviceID {
			continue
		}
		if len(authorization.Dependencies) == 0 {
			return nil, nil
		}
		resolver, err := NewBuildDependencyResolver(i.client, i.cfg.GiteaBaseURL)
		if err != nil {
			return nil, fmt.Errorf("configure build dependency resolver: %w", err)
		}
		resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		pins, err := resolver.Resolve(resolveCtx, authorization.Dependencies)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("pin fleet-authorized build dependencies: %w", err)
		}
		return pins, nil
	}
	return nil, fmt.Errorf("selected service has no build dependency authorization")
}

func validateFleetMirrorCloneURL(raw, giteaBaseURL, owner, name string) error {
	if err := validateSourceCloneURL(raw); err != nil {
		return fmt.Errorf("fleet mirror clone URL must be credential-free HTTPS: %w", err)
	}
	cloneURL, _ := url.Parse(strings.TrimSpace(raw))
	trustedOrigin, err := url.Parse(strings.TrimSpace(giteaBaseURL))
	if err != nil || trustedOrigin.Scheme != "https" || trustedOrigin.Host == "" || trustedOrigin.User != nil {
		return fmt.Errorf("trusted fleet Gitea origin is invalid")
	}
	if !strings.EqualFold(cloneURL.Scheme, trustedOrigin.Scheme) || !strings.EqualFold(cloneURL.Host, trustedOrigin.Host) {
		return fmt.Errorf("fleet mirror clone URL does not use the trusted Gitea origin")
	}
	expectedPath := "/" + url.PathEscape(strings.TrimSpace(owner)) + "/" + url.PathEscape(strings.TrimSpace(name)) + ".git"
	if cloneURL.EscapedPath() != expectedPath {
		return fmt.Errorf("fleet mirror clone URL does not identify the selected mirror")
	}
	return nil
}

// validateMirror fails closed unless the fleet repository is a private mirror
// of the expected upstream. Gitea persists the submitted credential-free
// clone_addr as original_url; auth_username and auth_password are separate
// migration fields and therefore never affect this comparison.
func (i *Initiator) validateMirror(info *RepoInfo, expectedCloneURL string) error {
	if info == nil {
		return fmt.Errorf("fleet mirror metadata is unavailable")
	}
	if !info.Private || !info.Mirror {
		return fmt.Errorf("fleet repository exists but is not a private mirror; refusing to build from it")
	}
	if got := normalizeCloneURL(info.OriginalURL); got == "" || got != normalizeCloneURL(expectedCloneURL) {
		return fmt.Errorf("fleet mirror tracks an unexpected upstream; refusing to build from it")
	}
	return nil
}

func normalizeCloneURL(v string) string {
	v = strings.TrimSpace(v)
	parsed, err := url.Parse(v)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSuffix(strings.TrimSuffix(v, "/"), ".git")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), ".git")
	parsed.RawPath = ""
	return parsed.String()
}

func (i *Initiator) resolveRefWithRetry(ctx context.Context, name, ref string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < i.cfg.RefResolveAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(i.cfg.RefResolveDelay):
			}
		}
		sha, err := i.client.ResolveRef(ctx, i.cfg.MirrorOwner, name, ref)
		if err == nil {
			if !isFullCommitSHA(sha) {
				return "", fmt.Errorf("mirror resolved %q to a non-immutable commit reference", ref)
			}
			return strings.ToLower(sha), nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("resolve ref %q on fleet mirror: %w", ref, lastErr)
}

// prepareWorkflowRunRequest signs the fleet-local Hive-CI kind-5401 workflow
// run consumed by Hive-CI and by Bahia's own subscriber. It intentionally uses
// the tag-only producer contract established by grasp-gitea; the inbound
// ContextVM build/request command remains the mutation boundary.
// It returns the signed event and the nsec of the
// per-run ephemeral publisher key that must be delivered to the worker as
// HIVE_CI_NSEC so the worker signs the 5402 with it (hive-ci-protocol).
func (i *Initiator) prepareWorkflowRunRequest(ctx context.Context, req controlplane.HiveCIBuildStartRequest, name, sha, branch string) (*nostr.Event, string, error) {
	issuer, err := i.signer.GetPublicKey(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Hive-CI workflow run issuer: %w", err)
	}
	issuerHex := issuer.Hex()
	if issuerHex == strings.Repeat("0", 64) {
		return nil, "", fmt.Errorf("resolve Hive-CI workflow run issuer: signer returned an empty pubkey")
	}
	ephemeral := nostr.Generate()
	publisherHex := ephemeral.Public().Hex()
	publisherNsec := nip19.EncodeNsec(ephemeral)
	triggeredBy := strings.TrimSpace(req.RequesterPubkey)
	if triggeredBy == "" {
		triggeredBy = issuerHex
	}
	payload := cascadia.HiveCiWorkflowV1Payload{
		Workflow:    i.cfg.WorkflowPath,
		Commit:      sha,
		Branch:      branch,
		TriggeredBy: triggeredBy,
	}
	if err := payload.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate Hive-CI workflow run payload: %w", err)
	}
	if !containsPubkey(i.cfg.TrustedCIPubkeys, issuerHex) {
		i.logger.Warn("self-issued Hive-CI workflow run will be ignored by Bahia's local subscriber",
			zap.String("reason", "self_issued_run_untrusted"),
			zap.Int("kind", kinds.HiveCIWorkflowRun),
			zap.String("service_pubkey", issuerHex),
		)
	}
	tags := nostr.Tags{
		{"a", strings.TrimSpace(i.cfg.RepoAnnouncementAddr)},
		{"commit", payload.Commit},
		{"branch", payload.Branch},
		{"trigger", "push"},
		{"triggered-by", payload.TriggeredBy},
		{"workflow", payload.Workflow},
		{"publisher", publisherHex},
		{"t", "hive-ci"},
		{"repo", i.cfg.MirrorOwner + "/" + name},
		{"build", req.BuildID.String()},
	}
	if relayHint := strings.TrimSpace(i.cfg.RelayHint); relayHint != "" {
		tags = append(tags, nostr.Tag{"relay", relayHint})
	}
	if sourceEventID := strings.TrimSpace(req.SourceEventID); sourceEventID != "" {
		tags = append(tags, nostr.Tag{"e", sourceEventID})
	}
	ev := &nostr.Event{
		Kind:      nostr.Kind(kinds.HiveCIWorkflowRun),
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   "",
	}
	if err := controlplane.SignGoNostrEvent(ctx, i.signer, ev); err != nil {
		return nil, "", fmt.Errorf("sign workflow run: %w", err)
	}
	return ev, publisherNsec, nil
}

// existingWorkflowRun returns an already-ingested trusted run for this
// initiator's repository coordinate, the resolved commit, and its workflow
// path. It never treats a lookup failure as "absent": a broken lookup must
// not silently allow a duplicate build.
func (i *Initiator) existingWorkflowRun(ctx context.Context, sha string) (*domain.HiveCIWorkflowRun, error) {
	if i.runLookup == nil {
		return nil, nil
	}
	run, err := i.runLookup.FindWorkflowRun(ctx, strings.TrimSpace(i.cfg.RepoAnnouncementAddr), sha, strings.TrimSpace(i.cfg.WorkflowPath))
	if err != nil {
		return nil, fmt.Errorf("check for an existing Hive-CI workflow run: %w", err)
	}
	if run == nil || strings.TrimSpace(run.RunEventID) == "" {
		return nil, nil
	}
	return run, nil
}

func validateRepoAnnouncementAddr(raw string) error {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 3)
	if len(parts) != 3 || parts[0] != "30617" || strings.TrimSpace(parts[2]) == "" || strings.ContainsAny(parts[2], "\x00\r\n\t") {
		return fmt.Errorf("repository announcement address must be 30617:<pubkey>:<repo-id>")
	}
	if _, err := nostr.PubKeyFromHex(parts[1]); err != nil {
		return fmt.Errorf("repository announcement address contains an invalid pubkey: %w", err)
	}
	return nil
}

func containsPubkey(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// prepareQueuedEvidence signs an addressed (kind 30900, latest-wins per
// d-tag) build-state projection so queued/running/success/failure evidence is
// verifiable and replay-safe.
func (i *Initiator) prepareQueuedEvidence(ctx context.Context, rec *InitiationRecord) (*nostr.Event, error) {
	req, result := rec.Request, rec.Result
	statePayload := map[string]any{
		"schema":           "bahia.hiveci.build-state.v1",
		"build_id":         req.BuildID.String(),
		"service_id":       req.ServiceID.String(),
		"status":           string(domain.BuildStatusQueued),
		"repo":             rec.MirrorRepository,
		"upstream_repo":    req.RepositoryCoordinate,
		"git_ref":          result.GitRef,
		"git_sha":          result.GitSHA,
		"ci_run_id":        result.CIRunID,
		"ci_run_request":   rec.RunRequestID,
		"loom_job_id":      rec.LoomJobID,
		"artifact_repo":    req.ArtifactRepo,
		"request_event_id": req.SourceEventID,
	}
	if credentialRef := strings.TrimSpace(rec.MirrorReadCredentialRef); credentialRef != "" {
		statePayload["mirror_read_credential_ref"] = credentialRef
	}
	content, err := json.Marshal(statePayload)
	if err != nil {
		return nil, fmt.Errorf("marshal build-state evidence: %w", err)
	}
	tags := nostr.Tags{
		{kinds.CASControlStateTagD, "hiveci-build:" + req.BuildID.String()},
		{kinds.CASControlStateTagDomain, "hiveci-build"},
		{kinds.CASControlStateTagSchema, "bahia.hiveci.build-state.v1"},
		{"status", string(domain.BuildStatusQueued)},
		{"commit", result.GitSHA},
	}
	if sourceEventID := strings.TrimSpace(req.SourceEventID); sourceEventID != "" {
		tags = append(tags, nostr.Tag{"e", sourceEventID})
	}
	ev := &nostr.Event{
		Kind:      nostr.Kind(kinds.CASControlState),
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}
	if err := controlplane.SignGoNostrEvent(ctx, i.signer, ev); err != nil {
		return nil, fmt.Errorf("sign queued evidence: %w", err)
	}
	return ev, nil
}

func splitRepositoryCoordinate(coordinate string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(coordinate), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repository coordinate must be owner/name")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}
