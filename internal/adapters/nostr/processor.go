package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Loom protocol kind constants (duplicated here to avoid import cycle with
// the loom package, which imports the nostr package for RelayPool).
const (
	kindLoomWorkerAd  = 10100
	kindLoomJobStatus = 30100
	kindLoomJobResult = 5101
)

// Processor maps ingested Nostr events to domain commands.
// It is designed to be called from the Subscriber as an EventHandler.
type Processor struct {
	registry   *service.RegistryService
	workerRepo repository.WorkerRepository
	logger     *zap.Logger
}

// NewProcessor creates a new event processor.
// workerRepo is optional; when nil, Kind 10100 events are logged but not persisted.
func NewProcessor(registry *service.RegistryService, workerRepo repository.WorkerRepository, logger *zap.Logger) *Processor {
	return &Processor{
		registry:   registry,
		workerRepo: workerRepo,
		logger:     logger.Named("nostr-processor"),
	}
}

// Handle implements EventHandler. It routes each event to the appropriate
// domain method based on event kind.
func (p *Processor) Handle(ctx context.Context, ev *gonostr.Event) {
	if ev == nil {
		return
	}

	var err error
	switch ev.Kind {
	// --- Bahia command kinds (inbound) ---
	case KindCmdBuildRegister:
		err = p.handleBuildRegister(ctx, ev)
	case KindCmdArtifactRegister:
		err = p.handleArtifactRegister(ctx, ev)
	case KindCmdIntentCreate:
		err = p.handleIntentCreate(ctx, ev)
	case KindCmdIntentApprove:
		err = p.handleIntentApprove(ctx, ev)
	case KindCmdIntentReject:
		err = p.handleIntentReject(ctx, ev)
	case KindCmdRollbackRequest:
		err = p.handleRollback(ctx, ev)

	// --- Loom protocol kinds ---
	case kindLoomWorkerAd:
		err = p.handleWorkerAdvertisement(ctx, ev)
	case kindLoomJobStatus:
		err = p.handleLoomStatusUpdate(ctx, ev)
	case kindLoomJobResult:
		err = p.handleLoomResult(ctx, ev)

	default:
		// Unhandled kinds are silently ignored (subscriber persists them regardless).
		return
	}

	if err != nil {
		p.logger.Warn("event processing failed",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
			zap.Error(err),
		)
	} else {
		p.logger.Info("event processed",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
		)
	}
}

// ---------------------------------------------------------------------------
// Bahia command handlers
// ---------------------------------------------------------------------------

func (p *Processor) handleBuildRegister(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		ServiceID     uuid.UUID      `json:"service_id"`
		GitSHA        string         `json:"git_sha"`
		GitRef        string         `json:"git_ref"`
		CISystem      string         `json:"ci_system"`
		CIRunID       string         `json:"ci_run_id"`
		LoomJobID     string         `json:"loom_job_id"`
		Status        string         `json:"status"`
		SourceEventID string         `json:"source_event_id"`
		Metadata      map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing build.register content: %w", err)
	}

	status := domain.BuildStatus(cmd.Status)
	if status == "" {
		status = domain.BuildStatusQueued
	}

	sourceEventID := cmd.SourceEventID
	if sourceEventID == "" {
		sourceEventID = ev.ID // link back to the Nostr event
	}

	build := &domain.Build{
		ServiceID:     cmd.ServiceID,
		GitSHA:        cmd.GitSHA,
		GitRef:        cmd.GitRef,
		CISystem:      cmd.CISystem,
		CIRunID:       cmd.CIRunID,
		LoomJobID:     cmd.LoomJobID,
		Status:        status,
		SourceEventID: sourceEventID,
		Metadata:      cmd.Metadata,
	}
	return p.registry.RegisterBuild(ctx, build)
}

func (p *Processor) handleArtifactRegister(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		BuildID           uuid.UUID      `json:"build_id"`
		ServiceID         uuid.UUID      `json:"service_id"`
		ImageRepo         string         `json:"image_repo"`
		ImageTag          string         `json:"image_tag"`
		ImageDigest       string         `json:"image_digest"`
		ManifestMediaType string         `json:"manifest_media_type"`
		SizeBytes         *int64         `json:"size_bytes"`
		SBOMURL           string         `json:"sbom_url"`
		SignatureRef      string         `json:"signature_ref"`
		ScanStatus        string         `json:"scan_status"`
		Metadata          map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing artifact.register content: %w", err)
	}

	artifact := &domain.Artifact{
		BuildID:           cmd.BuildID,
		ServiceID:         cmd.ServiceID,
		ImageRepo:         cmd.ImageRepo,
		ImageTag:          cmd.ImageTag,
		ImageDigest:       cmd.ImageDigest,
		ManifestMediaType: cmd.ManifestMediaType,
		SizeBytes:         cmd.SizeBytes,
		SBOMURL:           cmd.SBOMURL,
		SignatureRef:      cmd.SignatureRef,
		ScanStatus:        domain.ScanStatus(cmd.ScanStatus),
		Metadata:          cmd.Metadata,
	}
	return p.registry.RegisterArtifact(ctx, artifact)
}

func (p *Processor) handleIntentCreate(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		ServiceID     uuid.UUID      `json:"service_id"`
		EnvironmentID uuid.UUID      `json:"environment_id"`
		ArtifactID    uuid.UUID      `json:"artifact_id"`
		RequestedBy   string         `json:"requested_by"`
		SourceKind    string         `json:"source_kind"`
		Metadata      map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing deployment.intent.create content: %w", err)
	}

	// Use event author as actor (same as REST resolveActor).
	requestedBy := cmd.RequestedBy
	if ev.PubKey != "" {
		requestedBy = ev.PubKey
	}

	sourceKind := domain.SourceKind(cmd.SourceKind)
	if sourceKind == "" {
		sourceKind = domain.SourceKindEventTriggered
	}

	intent := &domain.DeploymentIntent{
		ServiceID:     cmd.ServiceID,
		EnvironmentID: cmd.EnvironmentID,
		ArtifactID:    cmd.ArtifactID,
		RequestedBy:   requestedBy,
		SourceKind:    sourceKind,
		Metadata:      cmd.Metadata,
	}
	return p.registry.CreateDeploymentIntent(ctx, intent)
}

func (p *Processor) handleIntentApprove(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		IntentID uuid.UUID `json:"intent_id"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing deployment.intent.approve content: %w", err)
	}
	if cmd.IntentID == uuid.Nil {
		return fmt.Errorf("intent_id is required")
	}
	return p.registry.ApproveDeploymentIntent(ctx, cmd.IntentID)
}

func (p *Processor) handleIntentReject(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		IntentID uuid.UUID `json:"intent_id"`
		Reason   string    `json:"reason"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing deployment.intent.reject content: %w", err)
	}
	if cmd.IntentID == uuid.Nil {
		return fmt.Errorf("intent_id is required")
	}
	return p.registry.RejectDeploymentIntent(ctx, cmd.IntentID)
}

func (p *Processor) handleRollback(ctx context.Context, ev *gonostr.Event) error {
	var cmd struct {
		ServiceID     uuid.UUID `json:"service_id"`
		EnvironmentID uuid.UUID `json:"environment_id"`
		RequestedBy   string    `json:"requested_by"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &cmd); err != nil {
		return fmt.Errorf("parsing rollback.request content: %w", err)
	}

	requestedBy := cmd.RequestedBy
	if ev.PubKey != "" {
		requestedBy = ev.PubKey
	}

	_, err := p.registry.Rollback(ctx, cmd.ServiceID, cmd.EnvironmentID, requestedBy)
	return err
}

// ---------------------------------------------------------------------------
// Loom protocol handlers
// ---------------------------------------------------------------------------

func (p *Processor) handleWorkerAdvertisement(ctx context.Context, ev *gonostr.Event) error {
	if p.workerRepo == nil {
		p.logger.Debug("worker advertisement ignored (no worker repo configured)",
			zap.String("pubkey", ev.PubKey))
		return nil
	}

	// Parse content JSON for name/description/queue info.
	var content struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		MaxConcurrentJobs int    `json:"max_concurrent_jobs"`
		CurrentQueueDepth int    `json:"current_queue_depth"`
	}
	if ev.Content != "" {
		_ = json.Unmarshal([]byte(ev.Content), &content)
	}

	w := &domain.Worker{
		PubKey:            ev.PubKey,
		Name:              content.Name,
		Description:       content.Description,
		MaxConcurrentJobs: content.MaxConcurrentJobs,
		CurrentQueueDepth: content.CurrentQueueDepth,
		LastAdvertisementAt: ev.CreatedAt.Time(),
		Status:            domain.WorkerStatusOnline,
	}

	// Parse tags.
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "S":
			sw := domain.WorkerSoftware{Name: tag[1]}
			if len(tag) >= 3 {
				sw.Version = tag[2]
			}
			if len(tag) >= 4 {
				sw.Path = tag[3]
			}
			w.Software = append(w.Software, sw)
		case "A":
			w.Architecture = tag[1]
		case "price":
			if len(tag) >= 4 {
				price, _ := strconv.Atoi(tag[2])
				w.Pricing = append(w.Pricing, domain.WorkerPricing{
					MintURL:        tag[1],
					PricePerSecond: price,
					Unit:           tag[3],
				})
			}
		case "min_duration":
			w.MinDurationSecs, _ = strconv.Atoi(tag[1])
		case "max_duration":
			w.MaxDurationSecs, _ = strconv.Atoi(tag[1])
		case "g":
			w.Geohash = tag[1]
		case "relay":
			w.PreferredRelays = append(w.PreferredRelays, tag[1])
		}
	}

	if err := p.workerRepo.Upsert(ctx, w); err != nil {
		return fmt.Errorf("upserting worker %s: %w", ev.PubKey, err)
	}

	p.logger.Info("worker advertisement processed",
		zap.String("pubkey", ev.PubKey),
		zap.String("name", w.Name),
		zap.Int("software_count", len(w.Software)),
	)
	return nil
}

func (p *Processor) handleLoomStatusUpdate(ctx context.Context, ev *gonostr.Event) error {
	// Kind 30100 status updates are informational — logged by subscriber.
	// The workflow coordinator's PollJobStatus already handles these via
	// direct subscription. This handler is a fallback for events received
	// through the general subscriber.
	status := tagValue(ev.Tags, "status")
	jobID := tagValue(ev.Tags, "e")
	p.logger.Debug("loom status update received via subscriber",
		zap.String("job_id", jobID),
		zap.String("status", status),
	)
	return nil
}

func (p *Processor) handleLoomResult(ctx context.Context, ev *gonostr.Event) error {
	// Kind 5101 results. The workflow coordinator normally handles these
	// through PollJobStatus, but if a result arrives on the general subscriber
	// (e.g. after a reconnect), log it for observability.
	jobID := tagValue(ev.Tags, "e")
	success := tagValue(ev.Tags, "success")
	exitCode := tagValue(ev.Tags, "exit_code")
	p.logger.Info("loom result received via subscriber",
		zap.String("job_id", jobID),
		zap.String("success", success),
		zap.String("exit_code", exitCode),
	)
	return nil
}

// tagValue returns the first value for the given tag key.
func tagValue(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
