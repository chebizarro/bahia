package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
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

// InitiationRecord captures a completed build initiation so that exact
// request replay (same source event) is idempotent: the recorded result is
// returned without re-mirroring or re-publishing.
type InitiationRecord struct {
	SourceEventID   string
	Result          controlplane.HiveCIBuildStartResult
	RunRequestID    string
	EvidenceEventID string
	CreatedAt       time.Time
}

// InitiationStore persists initiation records keyed by source event ID.
type InitiationStore interface {
	Get(ctx context.Context, sourceEventID string) (*InitiationRecord, error)
	Put(ctx context.Context, record InitiationRecord) error
}

// MemoryInitiationStore is a process-local InitiationStore.
type MemoryInitiationStore struct {
	mu      sync.Mutex
	records map[string]InitiationRecord
}

func NewMemoryInitiationStore() *MemoryInitiationStore {
	return &MemoryInitiationStore{records: make(map[string]InitiationRecord)}
}

func (s *MemoryInitiationStore) Get(_ context.Context, sourceEventID string) (*InitiationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.records[sourceEventID]; ok {
		copied := rec
		return &copied, nil
	}
	return nil, nil
}

func (s *MemoryInitiationStore) Put(_ context.Context, record InitiationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.SourceEventID] = record
	return nil
}

// InitiatorConfig configures the fleet Gitea mirror / Hive-CI initiator.
type InitiatorConfig struct {
	// MirrorOwner is the fleet Gitea organization or user owning private mirrors.
	MirrorOwner string
	// WorkflowPath is the Hive-CI workflow invoked for Arcana builds.
	WorkflowPath string
	// SourceCloneURL overrides the upstream clone URL. When empty it is
	// derived from the request's repository coordinate on github.com.
	SourceCloneURL string
	// RepoAnnouncementAddr optionally carries the NIP-34 kind-30617 address
	// ("30617:<pubkey>:<repo-id>") of the fleet mirror announcement for
	// canonical run-request correlation.
	RepoAnnouncementAddr string
	// RelayHint is included on published events so consumers can locate them.
	RelayHint string
	// RefResolveAttempts and RefResolveDelay bound the mirror-sync poll loop.
	RefResolveAttempts int
	RefResolveDelay    time.Duration
}

// Initiator implements controlplane.HiveCIBuildStarter against a fleet Gitea
// private mirror and the canonical ContextVM ci/workflow-run boundary.
type Initiator struct {
	client    MirrorClient
	secrets   SecretResolver
	publisher EventPublisher
	signer    nostr.Signer
	store     InitiationStore
	cfg       InitiatorConfig
	logger    *zap.Logger
	now       func() time.Time
	mu        sync.Mutex
}

func NewInitiator(client MirrorClient, secrets SecretResolver, publisher EventPublisher, signer nostr.Signer, store InitiationStore, cfg InitiatorConfig, logger *zap.Logger) *Initiator {
	if store == nil {
		store = NewMemoryInitiationStore()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.RefResolveAttempts <= 0 {
		cfg.RefResolveAttempts = 10
	}
	if cfg.RefResolveDelay <= 0 {
		cfg.RefResolveDelay = 3 * time.Second
	}
	return &Initiator{
		client: client, secrets: secrets, publisher: publisher, signer: signer,
		store: store, cfg: cfg, logger: logger.Named("gitea-hiveci-initiator"),
		now: func() time.Time { return time.Now().UTC() },
	}
}

var _ controlplane.HiveCIBuildStarter = (*Initiator)(nil)

// StartHiveCIBuild mirrors the private upstream repository into fleet Gitea,
// resolves the requested ref to an immutable commit, publishes the canonical
// ci/workflow-run request and addressed queued evidence, and returns the
// commit/run correlation. Exact replay of the same source event returns the
// original result without side effects.
func (i *Initiator) StartHiveCIBuild(ctx context.Context, req controlplane.HiveCIBuildStartRequest) (*controlplane.HiveCIBuildStartResult, error) {
	if i == nil || i.client == nil || i.secrets == nil || i.publisher == nil || i.signer == nil {
		return nil, fmt.Errorf("fleet Gitea mirror initiator is not fully configured")
	}
	if strings.TrimSpace(i.cfg.MirrorOwner) == "" || strings.TrimSpace(i.cfg.WorkflowPath) == "" {
		return nil, fmt.Errorf("fleet Gitea mirror initiator requires mirror owner and workflow path")
	}
	owner, name, err := splitRepositoryCoordinate(req.RepositoryCoordinate)
	if err != nil {
		return nil, err
	}
	gitRef := strings.TrimSpace(req.GitRef)
	if gitRef == "" {
		return nil, fmt.Errorf("git_ref is required")
	}

	// Exact request replay must be idempotent: one source event maps to one
	// mirror sync, one CI run request, and one recorded result.
	i.mu.Lock()
	defer i.mu.Unlock()
	idempotencyKey := strings.TrimSpace(req.SourceEventID)
	if idempotencyKey == "" {
		idempotencyKey = "build:" + req.BuildID.String()
	}
	if rec, err := i.store.Get(ctx, idempotencyKey); err != nil {
		return nil, fmt.Errorf("load build initiation record: %w", err)
	} else if rec != nil {
		result := rec.Result
		i.logger.Info("replayed build initiation resolved idempotently",
			zap.String("source_event_id", idempotencyKey),
			zap.String("git_sha", result.GitSHA),
			zap.String("ci_run_id", result.CIRunID),
		)
		return &result, nil
	}

	// Resolve the opaque credential reference server-side with audit. The
	// plaintext token stays in memory and is passed only inside the HTTPS
	// body of the Gitea migrate call.
	token, _, err := i.secrets.ResolveSecretWithAudit(ctx, req.CredentialRef.String(), domain.SecretResolveOptions{
		Operation: domain.SecretAccessOperationResolve,
		Actor:     req.RequesterPubkey,
		Reason:    "fleet gitea private-mirror build initiation",
		RequestID: idempotencyKey,
	})
	if err != nil {
		// The resolver can surface errors after producing a value (e.g. audit
		// persistence failures); scrub defensively.
		return nil, scrubSecrets(fmt.Errorf("resolve repository credential reference: %w", err), token)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("repository credential reference resolved to an empty credential")
	}

	cloneURL := strings.TrimSpace(i.cfg.SourceCloneURL)
	if cloneURL == "" {
		cloneURL = "https://github.com/" + owner + "/" + name + ".git"
	}

	repoInfo, err := i.client.GetRepo(ctx, i.cfg.MirrorOwner, name)
	if err != nil {
		return nil, scrubSecrets(fmt.Errorf("check fleet mirror: %w", err), token)
	}
	if repoInfo == nil {
		if err := i.client.MigrateMirror(ctx, MigrateMirrorRequest{
			Owner: i.cfg.MirrorOwner, Name: name, CloneAddr: cloneURL, AuthToken: token,
		}); err != nil {
			return nil, scrubSecrets(fmt.Errorf("create fleet private mirror: %w", err), token)
		}
		// Re-fetch to validate the mirror we (or a concurrent initiation)
		// created before trusting it.
		repoInfo, err = i.client.GetRepo(ctx, i.cfg.MirrorOwner, name)
		if err != nil || repoInfo == nil {
			return nil, scrubSecrets(fmt.Errorf("fleet private mirror is unavailable after migration"), token)
		}
		if err := i.validateMirror(repoInfo, cloneURL); err != nil {
			return nil, err
		}
	} else {
		// Never trust a pre-existing repository by name alone: it must be a
		// private mirror of the expected upstream, or we fail closed.
		if err := i.validateMirror(repoInfo, cloneURL); err != nil {
			return nil, err
		}
		if err := i.client.SyncMirror(ctx, i.cfg.MirrorOwner, name); err != nil {
			return nil, scrubSecrets(fmt.Errorf("sync fleet private mirror: %w", err), token)
		}
	}

	sha, err := i.resolveRefWithRetry(ctx, name, gitRef)
	if err != nil {
		return nil, scrubSecrets(err, token)
	}

	branch := ""
	if !isFullCommitSHA(gitRef) {
		branch = gitRef
	}
	runRequestID, runEventID, err := i.publishWorkflowRunRequest(ctx, req, name, sha, branch)
	if err != nil {
		return nil, scrubSecrets(fmt.Errorf("publish canonical ci/workflow-run request: %w", err), token)
	}

	result := controlplane.HiveCIBuildStartResult{GitSHA: sha, GitRef: gitRef, CIRunID: runEventID}

	evidenceEventID, err := i.publishQueuedEvidence(ctx, req, name, result, runRequestID)
	if err != nil {
		return nil, scrubSecrets(fmt.Errorf("publish queued build evidence: %w", err), token)
	}

	if err := i.store.Put(ctx, InitiationRecord{
		SourceEventID: idempotencyKey, Result: result,
		RunRequestID: runRequestID, EvidenceEventID: evidenceEventID,
		CreatedAt: i.now(),
	}); err != nil {
		return nil, fmt.Errorf("record build initiation: %w", err)
	}

	i.logger.Info("fleet gitea mirror build initiated",
		zap.String("build_id", req.BuildID.String()),
		zap.String("repo", i.cfg.MirrorOwner+"/"+name),
		zap.String("git_ref", gitRef),
		zap.String("git_sha", sha),
		zap.String("ci_run_id", runEventID),
		zap.String("evidence_event_id", evidenceEventID),
	)
	return &result, nil
}

// validateMirror fails closed unless the fleet repository is a private mirror
// of the expected upstream. Gitea strips embedded credentials from
// original_url, so this comparison never touches secret material.
func (i *Initiator) validateMirror(info *RepoInfo, expectedCloneURL string) error {
	if info == nil {
		return fmt.Errorf("fleet mirror metadata is unavailable")
	}
	if !info.Private || !info.Mirror {
		return fmt.Errorf("fleet repository exists but is not a private mirror; refusing to build from it")
	}
	if got := normalizeCloneURL(info.OriginalURL); got != "" && got != normalizeCloneURL(expectedCloneURL) {
		return fmt.Errorf("fleet mirror tracks an unexpected upstream; refusing to build from it")
	}
	return nil
}

func normalizeCloneURL(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimSuffix(v, ".git")
	return strings.TrimSuffix(v, "/")
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

// publishWorkflowRunRequest emits the canonical ContextVM ci/workflow-run
// request that hands the build to Hive-CI downstream of the fleet mirror.
// The event carries no credentials; build args are restricted upstream to
// allowlisted public VITE_* values and travel as public parameters.
func (i *Initiator) publishWorkflowRunRequest(ctx context.Context, req controlplane.HiveCIBuildStartRequest, name, sha, branch string) (string, string, error) {
	method, ok := cascadia.ContextVMMethods["ci/workflow-run"]
	if !ok {
		return "", "", fmt.Errorf("cascadia binding missing ci/workflow-run")
	}
	payload := cascadia.HiveCiWorkflowV1Payload{
		Workflow:    i.cfg.WorkflowPath,
		Commit:      sha,
		Branch:      branch,
		TriggeredBy: "push",
	}
	if err := payload.Validate(); err != nil {
		return "", "", fmt.Errorf("validate %s payload: %w", method.Schema, err)
	}
	requestID := "bahia:" + req.BuildID.String() + ":" + sha
	// Params carry the canonical hive.ci.workflow.v1 fields plus the
	// allowlisted public VITE_* build args (validated upstream; public by
	// contract, so safe on the wire).
	params := map[string]any{
		"workflow": payload.Workflow,
		"commit":   payload.Commit,
	}
	if payload.Branch != "" {
		params["branch"] = payload.Branch
	}
	if payload.TriggeredBy != "" {
		params["triggered_by"] = payload.TriggeredBy
	}
	if len(req.BuildArgs) > 0 {
		params["build_args"] = req.BuildArgs
	}
	rpc := struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      string         `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}{JSONRPC: "2.0", ID: requestID, Method: method.Name, Params: params}
	content, err := json.Marshal(rpc)
	if err != nil {
		return "", "", fmt.Errorf("marshal %s request: %w", method.Name, err)
	}
	tags := nostr.Tags{
		{"repo", i.cfg.MirrorOwner + "/" + name},
		{"commit", sha},
		{"build", req.BuildID.String()},
	}
	if addr := strings.TrimSpace(i.cfg.RepoAnnouncementAddr); addr != "" {
		tags = append(tags, nostr.Tag{"a", addr})
	}
	if relayHint := strings.TrimSpace(i.cfg.RelayHint); relayHint != "" {
		tags = append(tags, nostr.Tag{"relay", relayHint})
	}
	if sourceEventID := strings.TrimSpace(req.SourceEventID); sourceEventID != "" {
		tags = append(tags, nostr.Tag{"e", sourceEventID})
	}
	ev := &nostr.Event{
		Kind:      nostr.Kind(method.Kind),
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}
	eventID, err := i.signAndPublish(ctx, ev)
	if err != nil {
		return "", "", err
	}
	return requestID, eventID, nil
}

// publishQueuedEvidence publishes an addressed (kind 30900, latest-wins per
// d-tag) build-state projection so queued/running/success/failure evidence is
// verifiable and replay-safe.
func (i *Initiator) publishQueuedEvidence(ctx context.Context, req controlplane.HiveCIBuildStartRequest, name string, result controlplane.HiveCIBuildStartResult, runRequestID string) (string, error) {
	statePayload := map[string]any{
		"schema":           "bahia.hiveci.build-state.v1",
		"build_id":         req.BuildID.String(),
		"service_id":       req.ServiceID.String(),
		"status":           string(domain.BuildStatusQueued),
		"repo":             i.cfg.MirrorOwner + "/" + name,
		"upstream_repo":    req.RepositoryCoordinate,
		"git_ref":          result.GitRef,
		"git_sha":          result.GitSHA,
		"ci_run_id":        result.CIRunID,
		"ci_run_request":   runRequestID,
		"artifact_repo":    req.ArtifactRepo,
		"request_event_id": req.SourceEventID,
	}
	content, err := json.Marshal(statePayload)
	if err != nil {
		return "", fmt.Errorf("marshal build-state evidence: %w", err)
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
	return i.signAndPublish(ctx, ev)
}

func (i *Initiator) signAndPublish(ctx context.Context, ev *nostr.Event) (string, error) {
	if err := controlplane.SignGoNostrEvent(ctx, i.signer, ev); err != nil {
		return "", fmt.Errorf("sign event: %w", err)
	}
	published, err := i.publisher.Publish(ctx, *ev)
	if err != nil {
		return "", err
	}
	if published == 0 {
		return "", fmt.Errorf("no relay accepted the event; retry after relay reconnect")
	}
	return ev.ID.Hex(), nil
}

func splitRepositoryCoordinate(coordinate string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(coordinate), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repository coordinate must be owner/name")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}
