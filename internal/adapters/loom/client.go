// Package loom provides a client for interacting with Loom workers via Nostr.
//
// The Loom protocol defines five event kinds:
//
//	Kind 10100 – Worker Advertisement (Replaceable)
//	Kind 5100  – Job Request (Regular)
//	Kind 30100 – Job Status Update (Parameterized Replaceable)
//	Kind 5101  – Job Result (Regular)
//	Kind 5102  – Job Cancellation Request (Regular)
//
// See loom-protocol/SPECIFICATION.md for the full specification.
package loom

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
)

// Loom Protocol event kinds.
const (
	KindWorkerAd   = 10100 // Replaceable worker advertisement
	KindJobRequest = 5100  // Job request (subprocess)
	KindJobStatus  = 30100 // Parameterized replaceable status update
	KindJobResult  = 5101  // Final job result

	defaultJobSubscriptionBackoff = 250 * time.Millisecond
	maxJobSubscriptionBackoff     = 5 * time.Second
)

// Nostr tag keys shared by job producers (request/cancel) and consumers
// (status/result subscription filters + validation) so the two cannot drift.
const (
	tagJobEvent  = "e" // references the originating job-request event id
	tagJobPubkey = "p" // client or worker pubkey
	tagJobDedup  = "d" // parameterized-replaceable status identifier
)

// Job status values returned in Kind 30100 status tags.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusTimeout   = "timeout"
)

// BuildDependency is an immutable, fleet-authorized source tree staged by a
// worker before repository-controlled workflow code runs.
type BuildDependency struct {
	Name      string `json:"name"`
	CloneURL  string `json:"clone_url"`
	CommitSHA string `json:"commit_sha"`
}

// JobRequest represents a deploy job request sent to Loom workers.
type JobRequest struct {
	ID                   string            `json:"id"`
	ReferencedEventID    string            `json:"referenced_event_id,omitempty"` // originating event referenced by the 5100 e tag
	Type                 string            `json:"type"`                          // "deploy", "build"
	Image                string            `json:"image"`
	Digest               string            `json:"digest"`
	Environment          string            `json:"environment"`
	Service              string            `json:"service"`
	WorkerPubkey         string            `json:"worker_pubkey,omitempty"` // target a specific worker (auto-selected if empty)
	Cmd                  string            `json:"cmd,omitempty"`           // executable (default: "bash")
	Args                 []string          `json:"args,omitempty"`          // extra args appended after generated script
	Env                  map[string]string `json:"env,omitempty"`           // additional env vars
	Secrets              map[string]string `json:"secrets,omitempty"`       // NIP-44 encrypted secret env vars
	Params               map[string]string `json:"params,omitempty"`
	PaymentToken         string            `json:"payment_token,omitempty"` // Cashu payment token
	Timeout              time.Duration     `json:"timeout,omitempty"`
	RequiredSoftware     []string          `json:"required_software,omitempty"`
	RequiredArchitecture string            `json:"required_architecture,omitempty"`
	RequiredWorkloads    []string          `json:"required_workloads,omitempty"`
	RequiredFeatures     []string          `json:"required_features,omitempty"`
	AllowedWorkerPubkeys []string          `json:"allowed_worker_pubkeys,omitempty"`
	BuildDependencies    []BuildDependency `json:"build_dependencies,omitempty"`

	RequiredExecutionPlane *domain.ExecutionPlaneCapability `json:"required_execution_plane,omitempty"`
}

// JobStatus represents the current status of a Loom job.
type JobStatus struct {
	JobID        string `json:"job_id"`
	Status       string `json:"status"` // queued, running, completed, failed, cancelled, timeout
	Success      *bool  `json:"success,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Duration     *int   `json:"duration,omitempty"` // seconds
	WorkerPubkey string `json:"worker_pubkey,omitempty"`
	StdoutURL    string `json:"stdout_url,omitempty"` // Blossom URL
	StderrURL    string `json:"stderr_url,omitempty"` // Blossom URL
	ChangeToken  string `json:"change_token,omitempty"`
	Error        string `json:"error,omitempty"`
	LogOutput    string `json:"log_output,omitempty"` // content from status updates
}

// StatusCallback is called for each intermediate Kind 30100 status update
// received while waiting for a job result.
type StatusCallback func(status *JobStatus)

// Client interacts with Loom workers via the Loom Nostr protocol.
type loomRelayPool interface {
	Publish(context.Context, nostr.Event) (int, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error)
}

type loomRelayHealthRecorder interface {
	RecordRelayClosed(relayURL, reason string)
	RecordRelayReREQ()
}

type Client struct {
	pool            loomRelayPool
	workerRepo      repository.WorkerRepository
	signer          nostr.Keyer
	clientPubkey    string
	canonicalSigner CanonicalSigner

	planeCapabilities VerifiedPlaneCapabilitySource

	jobsMu           sync.RWMutex
	submittedWorkers map[string]string
	jobSubmitters    map[string]string

	jobTimeout             time.Duration
	jobSubscriptionBackoff time.Duration
	logger                 *zap.Logger
}

// NewClient creates a new Loom client. signer is the injected service Keyer:
// it signs kind-5100 job requests and NIP-44-encrypts job secrets to the
// worker. A nil signer leaves the client able to read but not dispatch.
// If pool is nil, a standalone pool is created from config relay URLs.
// workerRepo is optional; when non-nil, enables auto-selection of workers.
func NewClient(cfg config.LoomConfig, signer nostr.Keyer, pool *nostrAdapter.RelayPool, logger *zap.Logger, opts ...ClientOption) *Client {
	if pool == nil {
		var poolOpts []nostrAdapter.RelayPoolOption
		if signer != nil {
			// The pool answers relays' NIP-42 challenges as the client.
			poolOpts = append(poolOpts, nostrAdapter.WithAuthSigner(signer))
		}
		pool = nostrAdapter.NewRelayPool(cfg.Relays, logger, poolOpts...)
		pool.Connect(context.Background())
	}

	clientPubkey := ""
	if signer != nil {
		pubkey, err := signer.GetPublicKey(context.Background())
		if err == nil {
			clientPubkey = pubkey.Hex()
		} else {
			logger.Warn("failed to resolve Loom client pubkey for result validation", zap.Error(err))
		}
	}

	c := &Client{
		pool:                   pool,
		signer:                 signer,
		clientPubkey:           clientPubkey,
		submittedWorkers:       make(map[string]string),
		jobTimeout:             cfg.JobTimeout,
		jobSubscriptionBackoff: defaultJobSubscriptionBackoff,
		logger:                 logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithWorkerRepo enables worker auto-selection from the catalog.
func WithWorkerRepo(repo repository.WorkerRepository) ClientOption {
	return func(c *Client) { c.workerRepo = repo }
}

// VerifiedPlaneCapabilitySource is implemented by the live execution-plane
// reconciler, not the worker advertisement repository.
type VerifiedPlaneCapabilitySource interface {
	VerifiedCapabilities(context.Context, string, time.Time) ([]domain.VerifiedExecutionPlaneCapabilities, error)
}

func WithVerifiedPlaneCapabilities(source VerifiedPlaneCapabilitySource) ClientOption {
	return func(c *Client) { c.planeCapabilities = source }
}

// WithCanonicalSigner injects the signer used for canonical 30900 state and
// 4903 audit projections. Production callers should pass the configured
// Signet/NIP-46 client; raw-key signers are development-only compatibility.
func WithCanonicalSigner(signer CanonicalSigner) ClientOption {
	return func(c *Client) { c.canonicalSigner = signer }
}

// ProjectCanonicalStatus publishes the canonical projection for a Loom status
// event through the configured signer.
func (c *Client) ProjectCanonicalStatus(ctx context.Context, ev *nostr.Event) error {
	return ProjectCanonicalStatusWithSigner(ctx, c.pool, c.canonicalSigner, ev)
}

// ProjectCanonicalResult publishes the canonical projection for a Loom result
// event through the configured signer.
func (c *Client) ProjectCanonicalResult(ctx context.Context, ev *nostr.Event) error {
	return ProjectCanonicalResultWithSigner(ctx, c.pool, c.canonicalSigner, ev)
}

// ProjectCanonicalJobState publishes canonical state/audit for an already parsed
// Loom job status through the configured signer.
func (c *Client) ProjectCanonicalJobState(ctx context.Context, status *JobStatus, auditType string) error {
	return ProjectCanonicalJobStateWithSigner(ctx, c.pool, c.canonicalSigner, status, auditType)
}

// CanonicalProjectionReady reports whether terminal Loom work can be projected
// onto the fleet-canonical 30900 state and 4903 audit surface. ContextVM submit
// handlers use this as a fail-closed preflight before publishing kind 5100.
func (c *Client) CanonicalProjectionReady() bool {
	return c != nil && c.canonicalSigner != nil
}

// StartCanonicalProjection follows a submitted native Loom job in the
// background and projects every validated status plus the terminal result to
// the canonical state/audit surface. The await owns its context so returning the
// ContextVM response does not cancel result collection.
func (c *Client) StartCanonicalProjection(jobEventID string) {
	if !c.CanonicalProjectionReady() {
		c.logger.Error("cannot start Loom canonical projection: signer is not configured",
			zap.String("job_id", jobEventID))
		return
	}
	go func() {
		ctx := context.Background()
		var projectionErr error
		result, err := c.AwaitJobStatus(ctx, jobEventID, func(status *JobStatus) {
			if projectionErr != nil {
				return
			}
			projectionErr = c.ProjectCanonicalJobState(ctx, status, "loom.status")
		})
		if err != nil {
			c.logger.Error("Loom canonical projection await failed",
				zap.String("job_id", jobEventID), zap.Error(err))
			return
		}
		if projectionErr != nil {
			c.logger.Error("Loom canonical status projection failed",
				zap.String("job_id", jobEventID), zap.Error(projectionErr))
			return
		}
		if err := c.ProjectCanonicalJobState(ctx, result, "loom.result"); err != nil {
			c.logger.Error("Loom canonical result projection failed",
				zap.String("job_id", jobEventID), zap.Error(err))
			return
		}
		c.logger.Info("Loom terminal result projected to canonical state and audit",
			zap.String("job_id", jobEventID), zap.String("status", result.Status))
	}()
}

// SubmitJob submits a deployment job to Loom workers via a Kind 5100 job request event.
// If no WorkerPubkey is set and a workerRepo is available, auto-selects an online worker.
func (c *Client) SubmitJob(ctx context.Context, job JobRequest) (_ string, retErr error) {
	ctx, span := telemetry.StartOperation(ctx, "bahia.loom.dispatch",
		attribute.Int("nostr.kind", KindJobRequest), attribute.String("job.type", job.Type))
	defer func() {
		outcome := "success"
		if retErr != nil {
			outcome = "failure"
		}
		telemetry.RecordDispatch(ctx, KindJobRequest, outcome)
		telemetry.EndOperation(ctx, span, "bahia.loom.dispatch", outcome, retErr)
	}()
	if c.signer == nil {
		return "", fmt.Errorf("loom client signer not configured")
	}
	dependencyTags, err := validatedBuildDependencyTags(job.BuildDependencies)
	if err != nil {
		return "", err
	}
	if job.ReferencedEventID != "" {
		if _, err := nostr.IDFromHex(strings.TrimSpace(job.ReferencedEventID)); err != nil {
			return "", fmt.Errorf("invalid referenced event id: %w", err)
		}
		job.ReferencedEventID = strings.TrimSpace(job.ReferencedEventID)
	}

	// Auto-select worker if none specified.
	workerPubkey := job.WorkerPubkey
	if workerPubkey == "" && c.workerRepo != nil {
		selected, err := c.selectWorker(ctx, job)
		if err != nil {
			return "", fmt.Errorf("auto-selecting worker: %w", err)
		}
		workerPubkey = selected
		c.logger.Info("auto-selected worker", zap.String("pubkey", workerPubkey))
	}
	if requiresExecutionPlane(job) {
		if workerPubkey == "" || c.workerRepo == nil {
			return "", fmt.Errorf("verified execution plane requires a resolved worker catalog entry")
		}
		worker, err := c.workerRepo.GetByPubKey(ctx, workerPubkey)
		if err != nil || worker == nil || worker.PubKey != workerPubkey {
			return "", fmt.Errorf("execution-plane worker unavailable")
		}
		verified, err := c.workerWithVerifiedPlanes(ctx, *worker, job)
		if err != nil {
			return "", err
		}
		allowed := map[string]struct{}{}
		for _, key := range job.AllowedWorkerPubkeys {
			if key = strings.TrimSpace(key); key != "" {
				allowed[key] = struct{}{}
			}
		}
		if !workerMatchesJob(verified, job, allowed) {
			return "", fmt.Errorf("worker has no eligible verified execution plane")
		}
	}
	if workerPubkey == "" && (len(job.RequiredSoftware) > 0 || job.RequiredArchitecture != "" || len(job.RequiredWorkloads) > 0 || len(job.RequiredFeatures) > 0 || len(job.AllowedWorkerPubkeys) > 0) {
		return "", fmt.Errorf("cannot satisfy worker selection requirements without a worker repository or explicit worker pubkey")
	}

	if len(job.Secrets) > 0 && workerPubkey == "" {
		return "", fmt.Errorf("cannot deliver job secrets without a resolved worker pubkey")
	}
	if workerPubkey != "" {
		if _, err := nostrutil.PubKeyFromHex(workerPubkey); err != nil {
			return "", fmt.Errorf("invalid Loom worker pubkey: %w", err)
		}
	}

	// Build the command. Default to "bash -c <deploy-script>".
	cmd := job.Cmd
	if cmd == "" {
		cmd = "bash"
	}

	tags := nostr.Tags{
		{"cmd", cmd},
	}
	if job.ReferencedEventID != "" {
		tags = append(tags, nostr.Tag{tagJobEvent, job.ReferencedEventID})
	}

	// Build args: if caller supplied explicit args, use those.
	// Otherwise synthesize a deploy script from image/digest/service.
	args := job.Args
	if len(args) == 0 && job.Image != "" {
		script := buildDeployScript(job)
		args = []string{"-c", script}
	}
	if len(args) > 0 {
		argsTag := nostr.Tag{"args"}
		argsTag = append(argsTag, args...)
		tags = append(tags, argsTag)
	}

	// Project the bounded Loom profile parameters into request tags. ContextVM
	// callers use this for profile-specific inputs such as the Hive-CI repository
	// and workflow. Keep the accepted keys explicit so callers cannot forge
	// routing, payment, or secret tags through the generic Params map.
	allowedParams := map[string]struct{}{
		"actor": {}, "event": {}, "input": {}, "job": {}, "method": {},
		"ref": {}, "repo": {}, "run": {}, "workflow": {},
	}
	paramKeys := make([]string, 0, len(job.Params))
	for key := range job.Params {
		if _, ok := allowedParams[key]; ok {
			paramKeys = append(paramKeys, key)
		}
	}
	sort.Strings(paramKeys)
	for _, key := range paramKeys {
		if value := strings.TrimSpace(job.Params[key]); value != "" {
			tags = append(tags, nostr.Tag{key, value})
		}
	}
	tags = append(tags, dependencyTags...)

	// Target worker.
	if workerPubkey != "" {
		tags = append(tags, nostr.Tag{tagJobPubkey, workerPubkey})
	}

	// Payment token (required by spec, optional in Bahia until Cashu is wired).
	if job.PaymentToken != "" {
		tags = append(tags, nostr.Tag{"payment", job.PaymentToken})
	}

	// Standard env vars for the deploy context.
	envVars := map[string]string{
		"BAHIA_DEPLOY_SERVICE":     job.Service,
		"BAHIA_DEPLOY_ENVIRONMENT": job.Environment,
		"BAHIA_DEPLOY_IMAGE":       job.Image,
		"BAHIA_DEPLOY_DIGEST":      job.Digest,
		"BAHIA_DEPLOY_TYPE":        job.Type,
	}
	// Merge caller-supplied env vars (override defaults).
	for k, v := range job.Env {
		envVars[k] = v
	}
	for k, v := range envVars {
		if v != "" {
			tags = append(tags, nostr.Tag{"env", k, v})
		}
	}

	// NIP-44 encrypted secret env vars.
	if len(job.Secrets) > 0 && workerPubkey != "" {
		secretTags, err := c.encryptSecrets(ctx, job.Secrets, workerPubkey)
		if err != nil {
			return "", fmt.Errorf("encrypting secrets: %w", err)
		}
		tags = append(tags, secretTags...)
	}
	tags = telemetry.InjectTraceContext(ctx, tags)

	// Stdin content is empty for deployment jobs.
	ev := nostr.Event{
		Kind:      KindJobRequest,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
	}

	if err := c.signer.SignEvent(ctx, &ev); err != nil {
		return "", fmt.Errorf("signing event: %w", err)
	}

	published, err := c.pool.Publish(ctx, ev)
	if err != nil {
		return "", fmt.Errorf("publishing job request: %w", err)
	}
	if published == 0 {
		return "", fmt.Errorf("publishing job request: no relay accepted event")
	}
	eventID := nostrutil.EventIDHex(&ev)
	c.rememberSubmittedWorker(eventID, workerPubkey)

	c.logger.Info("loom job submitted",
		zap.String("event_id", eventID),
		zap.String("service", job.Service),
		zap.String("environment", job.Environment),
		zap.String("worker", workerPubkey),
		zap.Int("kind", KindJobRequest),
		zap.Int("relays", published),
	)

	return eventID, nil
}

var buildDependencyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validatedBuildDependencyTags renders the non-spec `dep` tags. Hive-CI
// dispatch passes dependencies as loom-ci `--dep` args instead (see
// HiveCIJobArgs); this remains for non-CI jobs that carry them.
func validatedBuildDependencyTags(dependencies []BuildDependency) (nostr.Tags, error) {
	ordered, err := validatedBuildDependencies(dependencies)
	if err != nil {
		return nil, err
	}
	tags := make(nostr.Tags, 0, len(ordered))
	for _, dependency := range ordered {
		tags = append(tags, nostr.Tag{"dep", dependency.Name, dependency.CloneURL, dependency.CommitSHA})
	}
	return tags, nil
}

func isCredentialFreeHTTPSCloneURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.IsAbs() && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Opaque == "" && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func isLowerFullCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// JobTimeout returns the maximum wall-clock duration allowed for a Loom job.
func (c *Client) JobTimeout() time.Duration {
	return c.jobTimeout
}

// AwaitJobStatus subscribes to Kind 30100 (status) and Kind 5101 (result) events
// for the given job event ID. It returns when a terminal result (Kind 5101) is
// received or the context expires. An optional StatusCallback is invoked for
// each intermediate status update.
func (c *Client) AwaitJobStatus(ctx context.Context, jobEventID string, callbacks ...StatusCallback) (*JobStatus, error) {
	return c.AwaitJobStatusFromWorker(ctx, jobEventID, "", callbacks...)
}

// AwaitJobStatusFromWorker is AwaitJobStatus scoped to an expected worker pubkey.
// Relay-provided status/result events must pass shared Nostr validation plus
// Loom-specific kind, tag, job-correlation, client, worker, and duplicate checks
// before they can drive callbacks or terminal completion.
func (c *Client) AwaitJobStatusFromWorker(ctx context.Context, jobEventID string, expectedWorkerPubkey string, callbacks ...StatusCallback) (*JobStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, c.jobTimeout)
	defer cancel()

	if expectedWorkerPubkey == "" {
		expectedWorkerPubkey = c.submittedWorker(jobEventID)
	}
	if expectedWorkerPubkey != "" {
		if _, err := nostrutil.PubKeyFromHex(expectedWorkerPubkey); err != nil {
			return nil, fmt.Errorf("invalid expected Loom worker pubkey: %w", err)
		}
	}
	filters := c.jobStatusFilters(jobEventID, expectedWorkerPubkey)

	// Track the latest status while waiting for a result.
	latest := &JobStatus{JobID: jobEventID, Status: StatusQueued}
	seen := nostrAdapter.NewEventDeduplicator(256)
	backoff := c.initialJobSubscriptionBackoff()
	subscribeAttempts := 0

resubscribe:
	if subscribeAttempts > 0 {
		c.recordRelayReREQ()
	}
	subscribeAttempts++
	sub, err := c.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		c.logger.Warn("failed to subscribe for Loom job status; retrying",
			zap.String("job_id", jobEventID),
			zap.Duration("backoff", backoff),
			zap.Error(err),
		)
		if err := waitForJobResubscribe(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = nextJobSubscriptionBackoff(backoff)
		goto resubscribe
	}

	for {
		select {
		case <-ctx.Done():
			sub.Close()
			return nil, ctx.Err()
		case eose, ok := <-sub.RelayEOSE:
			if ok {
				backoff = c.initialJobSubscriptionBackoff()
				c.logger.Debug("loom relay sent EOSE",
					zap.String("relay", eose.RelayURL),
					zap.String("subscription_id", eose.SubscriptionID),
					zap.String("job_id", jobEventID),
				)
			} else {
				sub.RelayEOSE = nil
			}
		case closed, ok := <-sub.Closed:
			if !ok {
				sub.Closed = nil
				continue
			}
			if err := c.handleJobSubscriptionClosed(closed, jobEventID); err != nil {
				sub.Close()
				return nil, err
			}
		case <-sub.EndOfStoredEvents:
			c.logger.Debug("loom job status subscription caught up",
				zap.String("job_id", jobEventID),
			)
			sub.EndOfStoredEvents = nil
		case ev, ok := <-sub.Events:
			if !ok {
				sub.Close()
				// Every REQ stopped. If the pool gave up on the relays (a
				// policy refusal, failed AUTH or an exhausted CLOSED retry
				// budget), resubscribing would only sidestep that give-up
				// with a fresh budget.
				if gaveUp := sub.GaveUp(); gaveUp != nil {
					return nil, fmt.Errorf("loom job status subscription for %s: %w", jobEventID, gaveUp)
				}
				c.logger.Warn("Loom job subscription ended before terminal result; resubscribing",
					zap.String("job_id", jobEventID),
					zap.Duration("backoff", backoff),
				)
				if err := waitForJobResubscribe(ctx, backoff); err != nil {
					return nil, err
				}
				backoff = nextJobSubscriptionBackoff(backoff)
				goto resubscribe
			}
			if ev == nil {
				c.logger.Warn("dropping nil Loom job event", zap.String("job_id", jobEventID))
				continue
			}
			backoff = c.initialJobSubscriptionBackoff()
			if err := c.validateJobEvent(ev, jobEventID, expectedWorkerPubkey); err != nil {
				c.logger.Warn("dropping invalid Loom job event",
					zap.String("job_id", jobEventID),
					zap.String("event_id", nostrutil.EventIDHex(ev)),
					zap.Int("kind", int(ev.Kind)),
					zap.Error(err),
				)
				continue
			}
			eventID := nostrutil.EventIDHex(ev)
			if seen.IsDuplicate(eventID) {
				c.logger.Debug("skipping duplicate Loom job event",
					zap.String("job_id", jobEventID),
					zap.String("event_id", eventID),
					zap.Int("kind", int(ev.Kind)),
				)
				continue
			}

			switch int(ev.Kind) {
			case KindJobStatus:
				// Intermediate status update — record it.
				status := getTagValue(ev.Tags, "status")
				latest.Status = status
				latest.WorkerPubkey = nostrutil.EventPubKeyHex(ev)
				if ev.Content != "" {
					latest.LogOutput = ev.Content
				}
				c.logger.Debug("loom job status update",
					zap.String("job_id", jobEventID),
					zap.String("status", latest.Status),
					zap.String("worker", latest.WorkerPubkey),
				)
				// Notify callbacks after validation and deduplication.
				for _, cb := range callbacks {
					cb(latest)
				}

			case KindJobResult:
				// Terminal result — parse tags and return only after validation/deduplication.
				sub.Close()
				return parseJobResult(ev, jobEventID), nil
			}
		}
	}
}

func (c *Client) initialJobSubscriptionBackoff() time.Duration {
	if c.jobSubscriptionBackoff > 0 {
		return c.jobSubscriptionBackoff
	}
	return defaultJobSubscriptionBackoff
}

func nextJobSubscriptionBackoff(current time.Duration) time.Duration {
	if current >= maxJobSubscriptionBackoff/2 {
		return maxJobSubscriptionBackoff
	}
	return current * 2
}

func (c *Client) recordRelayReREQ() {
	if recorder, ok := c.pool.(loomRelayHealthRecorder); ok {
		recorder.RecordRelayReREQ()
	}
}

func waitForJobResubscribe(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) jobStatusFilters(jobEventID string, expectedWorkerPubkey string) []nostr.Filter {
	statusFilter := nostr.Filter{
		Kinds: []nostr.Kind{KindJobStatus},
		Tags: nostr.TagMap{
			tagJobDedup: {jobEventID},
			tagJobEvent: {jobEventID},
		},
	}
	resultFilter := nostr.Filter{
		Kinds: []nostr.Kind{KindJobResult},
		Tags:  nostr.TagMap{tagJobEvent: {jobEventID}},
	}
	if c.clientPubkey != "" {
		statusFilter.Tags[tagJobPubkey] = []string{c.clientPubkey}
		resultFilter.Tags[tagJobPubkey] = []string{c.clientPubkey}
	}
	if expectedWorkerPubkey != "" {
		if pubkey, err := nostrutil.PubKeyFromHex(expectedWorkerPubkey); err == nil {
			statusFilter.Authors = []nostr.PubKey{pubkey}
			resultFilter.Authors = []nostr.PubKey{pubkey}
		}
	}
	return []nostr.Filter{statusFilter, resultFilter}
}

// handleJobSubscriptionClosed records a relay's CLOSED. The pool answers
// "auth-required:" itself (NIP-42 through its AuthHandler, then a re-REQ on
// that relay) and reissues retryable CLOSEDs, so only its terminal verdicts
// reach here as anything but telemetry. A terminal "auth-required:" means the
// pool could not authenticate; it fails the wait.
func (c *Client) handleJobSubscriptionClosed(closed nostrAdapter.RelayClosed, jobEventID string) error {
	if recorder, ok := c.pool.(loomRelayHealthRecorder); ok {
		recorder.RecordRelayClosed(closed.RelayURL, closed.Reason)
	}
	c.logger.Warn("loom job status subscription closed by relay",
		zap.String("relay", closed.RelayURL),
		zap.String("subscription_id", closed.SubscriptionID),
		zap.String("reason", closed.Reason),
		zap.Bool("terminal", closed.Terminal),
		zap.String("job_id", jobEventID),
	)
	if closed.Terminal && nostrAdapter.IsAuthRequiredReason(closed.Reason) {
		return fmt.Errorf("loom job status subscription auth failed on %s: %s", closed.RelayURL, closed.Reason)
	}
	return nil
}

func (c *Client) rememberSubmittedWorker(jobEventID string, workerPubkey string) {
	if jobEventID == "" || workerPubkey == "" {
		return
	}
	c.jobsMu.Lock()
	defer c.jobsMu.Unlock()
	if c.submittedWorkers == nil {
		c.submittedWorkers = make(map[string]string)
	}
	c.submittedWorkers[jobEventID] = workerPubkey
}

func (c *Client) submittedWorker(jobEventID string) string {
	if jobEventID == "" {
		return ""
	}
	c.jobsMu.RLock()
	defer c.jobsMu.RUnlock()
	return c.submittedWorkers[jobEventID]
}
func (c *Client) RememberJobSubmitter(jobEventID, submitter string) {
	if jobEventID == "" || submitter == "" {
		return
	}
	c.jobsMu.Lock()
	defer c.jobsMu.Unlock()
	if c.jobSubmitters == nil {
		c.jobSubmitters = make(map[string]string)
	}
	c.jobSubmitters[jobEventID] = submitter
}

func (c *Client) JobSubmitter(jobEventID string) string {
	if jobEventID == "" {
		return ""
	}
	c.jobsMu.RLock()
	defer c.jobsMu.RUnlock()
	return c.jobSubmitters[jobEventID]
}

func (c *Client) validateJobEvent(ev *nostr.Event, jobEventID string, expectedWorkerPubkey string) error {
	if err := nostrAdapter.ValidateInboundEvent(ev, time.Now().UTC(), nostrAdapter.InboundEventMaxFutureSkew); err != nil {
		return err
	}
	if int(ev.Kind) != KindJobStatus && int(ev.Kind) != KindJobResult {
		return fmt.Errorf("unexpected Loom job event kind %d", ev.Kind)
	}
	if expectedWorkerPubkey != "" && nostrutil.EventPubKeyHex(ev) != expectedWorkerPubkey {
		return fmt.Errorf("worker pubkey mismatch")
	}
	if c.clientPubkey != "" && getTagValue(ev.Tags, tagJobPubkey) != c.clientPubkey {
		return fmt.Errorf("client pubkey tag mismatch")
	}

	switch int(ev.Kind) {
	case KindJobStatus:
		if err := requireTagValue(ev.Tags, tagJobDedup, jobEventID); err != nil {
			return err
		}
		if err := requireTagValue(ev.Tags, tagJobEvent, jobEventID); err != nil {
			return err
		}
		if err := requireTagPresent(ev.Tags, tagJobPubkey); err != nil {
			return err
		}
		status := getTagValue(ev.Tags, "status")
		if !isValidJobStatus(status) {
			return fmt.Errorf("invalid status tag %q", status)
		}
	case KindJobResult:
		if err := requireTagValue(ev.Tags, tagJobEvent, jobEventID); err != nil {
			return err
		}
		for _, key := range []string{tagJobPubkey, "success", "exit_code", "duration"} {
			if err := requireTagPresent(ev.Tags, key); err != nil {
				return err
			}
		}
		success := getTagValue(ev.Tags, "success")
		if success != "true" && success != "false" {
			return fmt.Errorf("invalid success tag %q", success)
		}
		if _, err := strconv.Atoi(getTagValue(ev.Tags, "exit_code")); err != nil {
			return fmt.Errorf("invalid exit_code tag: %w", err)
		}
		if _, err := parseDurationSecondsTag(getTagValue(ev.Tags, "duration")); err != nil {
			return fmt.Errorf("invalid duration tag: %w", err)
		}
	}
	return nil
}

func isValidJobStatus(status string) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusCompleted, StatusFailed, StatusCancelled, StatusTimeout:
		return true
	default:
		return false
	}
}

func requireTagValue(tags nostr.Tags, key string, want string) error {
	got := getTagValue(tags, key)
	if got == "" {
		return fmt.Errorf("missing %q tag", key)
	}
	if got != want {
		return fmt.Errorf("%q tag mismatch", key)
	}
	return nil
}

func requireTagPresent(tags nostr.Tags, key string) error {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return nil
		}
	}
	return fmt.Errorf("missing %q tag", key)
}

// ---------------------------------------------------------------------------
// Worker selection
// ---------------------------------------------------------------------------

// selectWorker picks the best available online worker from the catalog.
// Selection is fail-closed: a worker must match allowlist, software,
// architecture, advertised workloads/features, scheduling state, and capacity
// before it can be chosen.
func (c *Client) selectWorker(ctx context.Context, job JobRequest) (string, error) {
	workers, err := c.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 50)
	if err != nil {
		return "", fmt.Errorf("listing online workers: %w", err)
	}
	if len(workers) == 0 {
		return "", fmt.Errorf("no online workers available")
	}
	allowed := make(map[string]struct{}, len(job.AllowedWorkerPubkeys))
	for _, pubkey := range job.AllowedWorkerPubkeys {
		pubkey = strings.TrimSpace(pubkey)
		if pubkey != "" {
			allowed[pubkey] = struct{}{}
		}
	}
	for _, worker := range workers {
		worker, err = c.workerWithVerifiedPlanes(ctx, worker, job)
		if err != nil {
			return "", err
		}
		if !workerMatchesJob(worker, job, allowed) {
			continue
		}
		return worker.PubKey, nil
	}
	return "", fmt.Errorf("no online Loom worker matches required software/arch/workloads/features/allowlist/capacity")
}

func workerMatchesJob(worker domain.Worker, job JobRequest, allowed map[string]struct{}) bool {
	if len(allowed) > 0 {
		if _, ok := allowed[worker.PubKey]; !ok {
			return false
		}
	}
	if worker.SchedulingState != "" && worker.SchedulingState != domain.WorkerSchedulingActive {
		return false
	}
	if worker.MaxConcurrentJobs > 0 && worker.CurrentQueueDepth >= worker.MaxConcurrentJobs {
		return false
	}
	if job.RequiredArchitecture != "" && worker.Architecture != job.RequiredArchitecture {
		return false
	}
	for _, software := range job.RequiredSoftware {
		software = strings.TrimSpace(software)
		if software == "" {
			continue
		}
		if !worker.HasSoftware(software) {
			return false
		}
	}
	if job.RequiredExecutionPlane != nil && !domain.HasVerifiedExecutionPlaneCapability(worker, *job.RequiredExecutionPlane, time.Now()) {
		return false
	}
	for _, workload := range job.RequiredWorkloads {
		class := domain.VMLifecycleClass(strings.ToLower(strings.TrimSpace(workload)))
		if class == domain.VMLifecycleLoomFirecracker || class == domain.VMLifecycleLoomQEMU {
			if !workerHasVerifiedClass(worker, class) {
				return false
			}
			continue
		}
		if !containsCapability(worker.Capabilities.WorkloadKinds, workload) {
			return false
		}
	}
	for _, feature := range job.RequiredFeatures {
		if !containsCapability(worker.Capabilities.Features, feature) {
			return false
		}
	}
	return true
}

func requiresExecutionPlane(job JobRequest) bool {
	if job.RequiredExecutionPlane != nil {
		return true
	}
	for _, value := range job.RequiredWorkloads {
		class := domain.VMLifecycleClass(strings.ToLower(strings.TrimSpace(value)))
		if class == domain.VMLifecycleLoomFirecracker || class == domain.VMLifecycleLoomQEMU {
			return true
		}
	}
	return false
}

func (c *Client) workerWithVerifiedPlanes(ctx context.Context, worker domain.Worker, job JobRequest) (domain.Worker, error) {
	if !requiresExecutionPlane(job) {
		return worker, nil
	}
	worker.VerifiedExecutionPlanes = nil
	if c.planeCapabilities == nil {
		return worker, fmt.Errorf("verified execution-plane source unavailable")
	}
	verified, err := c.planeCapabilities.VerifiedCapabilities(ctx, worker.PubKey, time.Now())
	if err != nil {
		return worker, fmt.Errorf("reading verified execution-plane capabilities: %w", err)
	}
	worker.VerifiedExecutionPlanes = verified
	return worker, nil
}

func workerHasVerifiedClass(worker domain.Worker, class domain.VMLifecycleClass) bool {
	now := time.Now()
	for _, verified := range domain.NormalizeVerifiedExecutionPlaneCapabilities(worker, now) {
		for _, capability := range verified.Capabilities {
			if capability.LifecycleClass == class && domain.HasVerifiedExecutionPlaneCapability(worker, capability, now) {
				return true
			}
		}
	}
	return false
}

func containsCapability(values []string, required string) bool {
	required = strings.ToLower(strings.TrimSpace(required))
	if required == "" {
		return true
	}
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == required {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Secret encryption
// ---------------------------------------------------------------------------

// encryptSecrets encrypts secret env vars using NIP-44 with the worker's pubkey.
func (c *Client) encryptSecrets(ctx context.Context, secrets map[string]string, workerPubkey string) (nostr.Tags, error) {
	worker, err := nostrutil.PubKeyFromHex(workerPubkey)
	if err != nil {
		return nil, fmt.Errorf("decode worker pubkey: %w", err)
	}

	var tags nostr.Tags
	for key, value := range secrets {
		encrypted, err := c.signer.Encrypt(ctx, value, worker)
		if err != nil {
			return nil, fmt.Errorf("encrypting secret %q: %w", key, err)
		}
		tags = append(tags, nostr.Tag{"secret", key, encrypted})
	}
	return tags, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildDeployScript generates a minimal bash script for a container deployment.
func buildDeployScript(job JobRequest) string {
	ref := job.Image
	if job.Digest != "" {
		ref += "@" + job.Digest
	}

	prepareImage := fmt.Sprintf("docker pull %s", ref)
	if isLocalImageRef(job.Image) {
		prepareImage = fmt.Sprintf("docker image inspect %s >/dev/null", shellQuote(job.Image))
	}

	return fmt.Sprintf(
		`set -e; echo "Deploying %s to %s/%s"; %s && docker stop %s 2>/dev/null || true && docker rm %s 2>/dev/null || true && docker run -d --name %s %s; echo "Deploy complete"`,
		ref, job.Environment, job.Service,
		prepareImage,
		job.Service, job.Service,
		job.Service, ref,
	)
}

func isLocalImageRef(image string) bool {
	return strings.HasPrefix(image, "local/")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// parseJobResult extracts a JobStatus from a Kind 5101 result event.
func parseJobResult(ev *nostr.Event, jobEventID string) *JobStatus {
	result := &JobStatus{
		JobID:        jobEventID,
		WorkerPubkey: nostrutil.EventPubKeyHex(ev),
	}

	if s := getTagValue(ev.Tags, "success"); s != "" {
		v := s == "true"
		result.Success = &v
		if v {
			result.Status = StatusCompleted
		} else {
			result.Status = StatusFailed
		}
	}
	if s := getTagValue(ev.Tags, "exit_code"); s != "" {
		if code, err := strconv.Atoi(s); err == nil {
			result.ExitCode = &code
		}
	}
	if s := getTagValue(ev.Tags, "duration"); s != "" {
		if dur, err := parseDurationSecondsTag(s); err == nil {
			result.Duration = &dur
		}
	}
	result.StdoutURL = getTagValue(ev.Tags, "stdout")
	result.StderrURL = getTagValue(ev.Tags, "stderr")
	result.ChangeToken = getTagValue(ev.Tags, "change")
	result.Error = getTagValue(ev.Tags, "error")

	return result
}

func parseDurationSecondsTag(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return seconds, nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if seconds < 0 {
		return 0, fmt.Errorf("negative duration")
	}
	return int(math.Ceil(seconds)), nil
}

// getTagValue returns the first value for the given tag key, or "".
func getTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
