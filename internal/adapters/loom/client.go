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
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Loom Protocol event kinds.
const (
	KindWorkerAd     = 10100 // Replaceable worker advertisement
	KindJobRequest   = 5100  // Job request (subprocess)
	KindJobStatus    = 30100 // Parameterized replaceable status update
	KindJobResult    = 5101  // Final job result
	KindJobCancelReq = 5102  // Cancellation request
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

// JobRequest represents a deploy job request sent to Loom workers.
type JobRequest struct {
	ID                   string            `json:"id"`
	Type                 string            `json:"type"` // "deploy", "build"
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
	AllowedWorkerPubkeys []string          `json:"allowed_worker_pubkeys,omitempty"`
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
	AuthenticateRelay(context.Context, string) error
}

type Client struct {
	pool            loomRelayPool
	workerRepo      repository.WorkerRepository
	privateKey      string
	clientPubkey    string
	canonicalSigner CanonicalSigner

	jobsMu           sync.RWMutex
	submittedWorkers map[string]string

	jobTimeout   time.Duration
	pollInterval time.Duration
	logger       *zap.Logger
}

// NewClient creates a new Loom client.
// If pool is nil, a standalone pool is created from config relay URLs.
// workerRepo is optional; when non-nil, enables auto-selection of workers.
func NewClient(cfg config.LoomConfig, nostrPrivateKey string, pool *nostrAdapter.RelayPool, logger *zap.Logger, opts ...ClientOption) *Client {
	if pool == nil {
		pool = nostrAdapter.NewRelayPool(cfg.Relays, logger)
		pool.Connect(context.Background())
	}

	clientPubkey := ""
	if nostrPrivateKey != "" {
		pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(nostrPrivateKey)
		if err == nil {
			clientPubkey = pubkey
		} else {
			logger.Warn("failed to derive Loom client pubkey for result validation", zap.Error(err))
		}
	}

	c := &Client{
		pool:             pool,
		privateKey:       nostrPrivateKey,
		clientPubkey:     clientPubkey,
		submittedWorkers: make(map[string]string),
		jobTimeout:       cfg.JobTimeout,
		pollInterval:     cfg.PollInterval,
		logger:           logger,
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
// the canonical state/audit surface. The poll owns its context so returning the
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
		result, err := c.PollJobStatus(ctx, jobEventID, func(status *JobStatus) {
			if projectionErr != nil {
				return
			}
			projectionErr = c.ProjectCanonicalJobState(ctx, status, "loom.status")
		})
		if err != nil {
			c.logger.Error("Loom canonical projection poll failed",
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
func (c *Client) SubmitJob(ctx context.Context, job JobRequest) (string, error) {
	if c.privateKey == "" {
		return "", fmt.Errorf("nostr private key not configured")
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
		secretTags, err := c.encryptSecrets(job.Secrets, workerPubkey)
		if err != nil {
			return "", fmt.Errorf("encrypting secrets: %w", err)
		}
		tags = append(tags, secretTags...)
	}

	// Stdin content is empty for deployment jobs.
	ev := nostr.Event{
		Kind:      KindJobRequest,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
	}

	if err := nostrutil.SignEventWithHexKey(&ev, c.privateKey); err != nil {
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

// PollJobStatus subscribes to Kind 30100 (status) and Kind 5101 (result) events
// for the given job event ID. It returns when a terminal result (Kind 5101) is
// received or the context expires. An optional StatusCallback is invoked for
// each intermediate status update.
func (c *Client) PollJobStatus(ctx context.Context, jobEventID string, callbacks ...StatusCallback) (*JobStatus, error) {
	return c.PollJobStatusFromWorker(ctx, jobEventID, "", callbacks...)
}

// PollJobStatusFromWorker is PollJobStatus scoped to an expected worker pubkey.
// Relay-provided status/result events must pass shared Nostr validation plus
// Loom-specific kind, tag, job-correlation, client, worker, and duplicate checks
// before they can drive callbacks or terminal completion.
func (c *Client) PollJobStatusFromWorker(ctx context.Context, jobEventID string, expectedWorkerPubkey string, callbacks ...StatusCallback) (*JobStatus, error) {
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
	authAttempted := make(map[string]struct{})

resubscribe:
	sub, err := c.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribing for job status: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			sub.Close()
			return nil, ctx.Err()
		case eose, ok := <-sub.RelayEOSE:
			if ok {
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
			retry, err := c.handleJobSubscriptionClosed(ctx, closed, authAttempted, jobEventID)
			if err != nil {
				sub.Close()
				return nil, err
			}
			if retry {
				sub.Close()
				goto resubscribe
			}
		case <-sub.EndOfStoredEvents:
			c.logger.Debug("loom job status subscription caught up",
				zap.String("job_id", jobEventID),
			)
			sub.EndOfStoredEvents = nil
		case ev, ok := <-sub.Events:
			if !ok {
				sub.Close()
				return nil, fmt.Errorf("subscription closed before terminal job result")
			}
			if ev == nil {
				c.logger.Warn("dropping nil Loom job event", zap.String("job_id", jobEventID))
				continue
			}
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

func (c *Client) handleJobSubscriptionClosed(ctx context.Context, closed nostrAdapter.RelayClosed, authAttempted map[string]struct{}, jobEventID string) (bool, error) {
	c.logger.Warn("loom job status subscription closed by relay",
		zap.String("relay", closed.RelayURL),
		zap.String("subscription_id", closed.SubscriptionID),
		zap.String("reason", closed.Reason),
		zap.String("job_id", jobEventID),
	)
	if nostrAdapter.IsAuthRequiredReason(closed.Reason) && closed.RelayURL != "" && c.pool != nil {
		if _, ok := authAttempted[closed.RelayURL]; ok {
			return false, nil
		}
		authAttempted[closed.RelayURL] = struct{}{}
		if err := c.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
			c.logger.Warn("loom job status subscription auth failed",
				zap.String("relay", closed.RelayURL),
				zap.String("reason", closed.Reason),
				zap.String("job_id", jobEventID),
				zap.Error(err),
			)
			return false, fmt.Errorf("loom job status subscription auth failed: %w", err)
		}
		return true, nil
	}
	return false, nil
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
		if _, err := strconv.Atoi(getTagValue(ev.Tags, "duration")); err != nil {
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

// CancelJob publishes a Kind 5102 cancellation request for the given job.
func (c *Client) CancelJob(ctx context.Context, jobEventID string, workerPubkey string) error {
	if c.privateKey == "" {
		return fmt.Errorf("nostr private key not configured")
	}

	tags := nostr.Tags{
		{tagJobEvent, jobEventID},
	}
	if workerPubkey != "" {
		tags = append(tags, nostr.Tag{tagJobPubkey, workerPubkey})
	}

	ev := nostr.Event{
		Kind:      KindJobCancelReq,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
	}

	if err := nostrutil.SignEventWithHexKey(&ev, c.privateKey); err != nil {
		return fmt.Errorf("signing cancellation event: %w", err)
	}

	published, err := c.pool.Publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publishing cancellation request: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publishing cancellation request: no relay accepted event")
	}

	c.logger.Info("loom job cancellation sent",
		zap.String("job_event_id", jobEventID),
		zap.Int("kind", KindJobCancelReq),
		zap.Int("relays", published),
	)

	return nil
}

// ---------------------------------------------------------------------------
// Worker selection
// ---------------------------------------------------------------------------

// selectWorker picks the best available online worker from the catalog.
// Selection is fail-closed: a worker must match allowlist, software,
// architecture, scheduling state, and capacity before it can be chosen.
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
		if !workerMatchesJob(worker, job, allowed) {
			continue
		}
		return worker.PubKey, nil
	}
	return "", fmt.Errorf("no online Loom worker matches required software/arch/allowlist/capacity")
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
	return true
}

// ---------------------------------------------------------------------------
// Secret encryption
// ---------------------------------------------------------------------------

// encryptSecrets encrypts secret env vars using NIP-44 with the worker's pubkey.
func (c *Client) encryptSecrets(secrets map[string]string, workerPubkey string) (nostr.Tags, error) {
	conversationKey, err := nostrutil.NIP44ConversationKey(workerPubkey, c.privateKey)
	if err != nil {
		return nil, fmt.Errorf("generating conversation key: %w", err)
	}

	var tags nostr.Tags
	for key, value := range secrets {
		encrypted, err := nip44.Encrypt(value, conversationKey)
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
		if dur, err := strconv.Atoi(s); err == nil {
			result.Duration = &dur
		}
	}
	result.StdoutURL = getTagValue(ev.Tags, "stdout")
	result.StderrURL = getTagValue(ev.Tags, "stderr")
	result.ChangeToken = getTagValue(ev.Tags, "change")
	result.Error = getTagValue(ev.Tags, "error")

	return result
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
