package hiveci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Compatibility names remain package-local for existing adapter tests; their
// values come from Bahia's centralized kind catalog.
const (
	kindWorkflowRun    = kinds.HiveCIWorkflowRun
	kindWorkflowResult = kinds.HiveCIWorkflowResult
)

// ResultConsumer is invoked after a valid workflow result has been persisted.
type ResultConsumer func(ctx context.Context, resultEventID string)

type WorkflowRunDispatch struct {
	RunEventID string
	Repository string
	Ref        string
	Workflow   string
	CommitSHA  string
	Release    bool
}

type RunConsumer func(ctx context.Context, run WorkflowRunDispatch)

// AcceptedReleaseConsumer is invoked once after a new RELEASE result crosses
// validation and durable replay protection. Exact relay replays do not invoke it.
type AcceptedReleaseConsumer func(ctx context.Context, commit domain.HiveCIReleaseCommitResult)

type ReleaseIngestAuditor interface {
	AuditReleaseRejection(context.Context, *nostr.Event, error) error
}

type relaySubscriber interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error)
	AuthenticateRelay(context.Context, string) error
}

// Subscriber ingests Hive-CI 5401/5402 events from relays and persists parsed records.
type Subscriber struct {
	pool           relaySubscriber
	repo           repository.HiveCIRepository
	logger         *zap.Logger
	trusted        map[string]struct{}
	onResult       ResultConsumer
	onRun          RunConsumer
	releases       *ReleaseIngestor
	onRelease      AcceptedReleaseConsumer
	releaseAuditor ReleaseIngestAuditor
	evidenceEvents repository.NostrEventRepository
	now            func() time.Time
}

func (s *Subscriber) SetRunConsumer(consumer RunConsumer) { s.onRun = consumer }

func (s *Subscriber) SetReleaseIngestor(ingestor *ReleaseIngestor, consumer AcceptedReleaseConsumer) {
	s.releases = ingestor
	s.onRelease = consumer
}

func (s *Subscriber) SetReleaseAuditor(auditor ReleaseIngestAuditor) { s.releaseAuditor = auditor }

func (s *Subscriber) SetReleaseEvidenceRecorder(events repository.NostrEventRepository) {
	s.evidenceEvents = events
}

func NewSubscriber(pool *nostrAdapter.RelayPool, repo repository.HiveCIRepository, trustedCIPubkeys []string, logger *zap.Logger, onResult ResultConsumer) *Subscriber {
	trusted := make(map[string]struct{}, len(trustedCIPubkeys))
	for _, pk := range trustedCIPubkeys {
		pk = strings.TrimSpace(pk)
		if pk != "" {
			trusted[pk] = struct{}{}
		}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Subscriber{
		pool:     pool,
		repo:     repo,
		logger:   logger.Named("hiveci-subscriber"),
		trusted:  trusted,
		onResult: onResult,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *Subscriber) Name() string { return "hiveci-subscriber" }

func (s *Subscriber) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := s.subscribe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		s.logger.Warn("hiveci subscription ended, reconnecting", zap.Error(err), zap.Duration("delay", backoff))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (s *Subscriber) subscribe(ctx context.Context) error {
	filters := []nostr.Filter{{Kinds: []nostr.Kind{kinds.HiveCIWorkflowRun, kinds.HiveCIWorkflowResult}}}
	authAttempted := make(map[string]struct{})
	for {
		if s.pool == nil {
			return fmt.Errorf("hiveci relay pool is not configured")
		}
		merged, err := s.pool.SubscribeAllWithEOSE(ctx, filters)
		if err != nil {
			return err
		}
		retry, err := s.consumeSubscription(ctx, merged, authAttempted)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
	}
}

func (s *Subscriber) consumeSubscription(ctx context.Context, merged *nostrAdapter.MergedSubscription, authAttempted map[string]struct{}) (bool, error) {
	if merged == nil {
		return false, nil
	}
	for merged.Events != nil || merged.EndOfStoredEvents != nil || merged.RelayEOSE != nil || merged.Closed != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				s.handleRelayEOSE(eose)
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if s.handleRelayClosed(ctx, closed, authAttempted) {
					merged.Close()
					return true, nil
				}
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			s.handleEOSE()
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				return false, nil
			}
			s.handleEvent(ctx, ev)
		}
	}
	return false, nil
}

func (s *Subscriber) handleRelayEOSE(eose nostrAdapter.RelayEOSE) {
	s.logger.Debug("hiveci relay sent EOSE",
		zap.String("relay", eose.RelayURL),
		zap.String("subscription_id", eose.SubscriptionID),
	)
}

func (s *Subscriber) handleEOSE() {
	s.logger.Info("hiveci EOSE received: caught up with stored workflow events")
}

func (s *Subscriber) handleRelayClosed(ctx context.Context, closed nostrAdapter.RelayClosed, authAttempted map[string]struct{}) bool {
	s.logger.Warn("hiveci relay closed subscription",
		zap.String("relay", closed.RelayURL),
		zap.String("subscription_id", closed.SubscriptionID),
		zap.String("reason", closed.Reason),
	)
	if !nostrAdapter.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || s.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := s.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		s.logger.Warn("hiveci relay subscription auth failed",
			zap.String("relay", closed.RelayURL),
			zap.String("reason", closed.Reason),
			zap.Error(err),
		)
		return false
	}
	return true
}

func (s *Subscriber) handleEvent(ctx context.Context, ev *nostr.Event) {
	if err := nostrAdapter.ValidateInboundEvent(ev, s.now(), nostrAdapter.InboundEventMaxFutureSkew); err != nil {
		eventID := ""
		kind := 0
		if ev != nil {
			eventID = nostrutil.EventIDHex(ev)
			kind = int(ev.Kind)
		}
		s.logger.Warn("dropping invalid hiveci event before persistence",
			zap.String("event_id", eventID),
			zap.Int("kind", kind),
			zap.Error(err),
		)
		if IsReleaseCandidate(ev) && s.releaseAuditor != nil {
			if auditErr := s.releaseAuditor.AuditReleaseRejection(ctx, ev, err); auditErr != nil {
				s.logger.Error("failed to persist invalid Hive-CI release audit", zap.Error(auditErr))
			}
		}
		return
	}

	switch int(ev.Kind) {
	case kinds.HiveCIWorkflowRun:
		s.handleWorkflowRun(ctx, ev)
	case kinds.HiveCIWorkflowResult:
		s.handleWorkflowResult(ctx, ev)
	}
}

func (s *Subscriber) handleWorkflowRun(ctx context.Context, ev *nostr.Event) {
	eventID := nostrutil.EventIDHex(ev)
	pubkey := nostrutil.EventPubKeyHex(ev)
	if _, ok := s.trusted[pubkey]; !ok {
		s.logger.Debug("ignoring untrusted hiveci workflow run", zap.String("event_id", eventID), zap.String("pubkey", pubkey))
		return
	}
	existing, err := s.repo.GetRunByEventID(ctx, eventID)
	if err != nil {
		s.logger.Warn("failed to load existing hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}

	repoCoordinate, err := requiredTag(ev, "a")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	commit, err := requiredTag(ev, "commit")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	branch, err := requiredTag(ev, "branch")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	workflow, err := requiredTag(ev, "workflow")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	triggeredBy, err := requiredTag(ev, "triggered-by")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	publisher, err := requiredTag(ev, "publisher")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}

	run := domain.HiveCIWorkflowRun{
		RunEventID:      eventID,
		RepoCoordinate:  repoCoordinate,
		CommitSHA:       commit,
		Branch:          branch,
		WorkflowPath:    workflow,
		TriggerType:     optionalTag(ev, "trigger"),
		TriggeredBy:     triggeredBy,
		PublisherPubkey: publisher,
		EventCreatedAt:  ev.CreatedAt.Time(),
		ProcessingState: domain.HiveCIProcessingStatePendingResult,
	}
	if s.evidenceEvents != nil {
		tagsJSON, marshalErr := json.Marshal(ev.Tags)
		if marshalErr != nil {
			s.logger.Warn("failed to encode signed Hive-CI workflow evidence", zap.Error(marshalErr))
			return
		}
		if _, recordErr := s.evidenceEvents.Record(ctx, &repository.NostrEventRecord{
			ID: eventID, Kind: int(ev.Kind), PubKey: pubkey, Content: ev.Content, Tags: tagsJSON,
			Sig: hex.EncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: s.now(),
			EntityType: "hiveci_workflow_run", PublishState: repository.NostrPublishStateNotApplicable,
		}); recordErr != nil {
			s.logger.Warn("failed to persist signed Hive-CI workflow evidence", zap.Error(recordErr))
			return
		}
	}
	if err := s.repo.UpsertWorkflowRun(ctx, run); err != nil {
		s.logger.Warn("failed to persist hiveci workflow run", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	// A merged relay subscription can deliver the same signed event once per
	// relay. Persisting is idempotent, and dispatch must be as well: otherwise
	// one workflow event can fan out into multiple Loom jobs and competing 5402
	// results for the same run.
	if existing == nil && s.onRun != nil {
		s.onRun(ctx, WorkflowRunDispatch{
			RunEventID: eventID,
			Repository: optionalTag(ev, "repo"),
			Ref:        optionalTag(ev, "ref"),
			Workflow:   workflow,
			CommitSHA:  commit,
			Release:    optionalTag(ev, "release") == "true",
		})
	}
	s.logger.Info("hiveci workflow run ingested", zap.String("run_event_id", eventID), zap.String("workflow", workflow), zap.String("commit", commit))

	// Check for any orphaned results that arrived before this run.
	s.processOrphanedResults(ctx, eventID)
	s.processOrphanedReleases(ctx, eventID)
}

func (s *Subscriber) processOrphanedResults(ctx context.Context, runEventID string) {
	orphans, err := s.repo.ListOrphanedResultsByRun(ctx, runEventID)
	if err != nil {
		s.logger.Warn("failed to list orphaned results", zap.String("run_event_id", runEventID), zap.Error(err))
		return
	}
	for _, result := range orphans {
		if _, trusted := s.trusted[result.PublisherPubkey]; !trusted {
			s.logger.Warn("orphaned result publisher mismatch, rejecting",
				zap.String("run_event_id", runEventID),
				zap.String("result_event_id", result.ResultEventID),
				zap.String("expected", "trusted CI publisher"),
				zap.String("actual", result.PublisherPubkey))
			_ = s.repo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected)
			continue
		}
		// Transition to pending_result so it can be picked up
		if err := s.repo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStatePendingResult); err != nil {
			s.logger.Warn("failed to transition orphaned result", zap.String("result_event_id", result.ResultEventID), zap.Error(err))
			continue
		}
		s.logger.Info("orphaned result matched to run", zap.String("run_event_id", runEventID), zap.String("result_event_id", result.ResultEventID))
		// Invoke callback to process the result
		if s.onResult != nil {
			s.onResult(ctx, result.ResultEventID)
		}
	}
}

func (s *Subscriber) recordReleaseCandidate(ctx context.Context, ev *nostr.Event) error {
	if s.evidenceEvents == nil {
		return fmt.Errorf("release evidence repository is not configured")
	}
	tagsJSON, err := json.Marshal(ev.Tags)
	if err != nil {
		return fmt.Errorf("encode signed release candidate tags: %w", err)
	}
	receivedAt := time.Now().UTC()
	if s.now != nil {
		receivedAt = s.now().UTC()
	}
	_, err = s.evidenceEvents.Record(ctx, &repository.NostrEventRecord{
		ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON,
		Sig: hex.EncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: receivedAt,
		EntityType: "hiveci_release_candidate", PublishState: repository.NostrPublishStateNotApplicable,
	})
	if err != nil {
		return fmt.Errorf("persist signed release candidate: %w", err)
	}
	return nil
}

func (s *Subscriber) processOrphanedReleases(ctx context.Context, runEventID string) {
	if s.releases == nil || s.evidenceEvents == nil {
		return
	}
	records, err := s.evidenceEvents.FindByTag(
		ctx, "e", runEventID, []int{kinds.HiveCIWorkflowResult}, 1000,
	)
	if err != nil {
		s.logger.Warn("failed to list orphaned Hive-CI RELEASE candidates",
			zap.String("run_event_id", runEventID), zap.Error(err))
		return
	}
	for index := range records {
		record := &records[index]
		if record.EntityType != "hiveci_release_candidate" {
			continue
		}
		event, decodeErr := signedEventFromRecord(record)
		if decodeErr != nil {
			s.logger.Warn("failed to decode orphaned Hive-CI RELEASE candidate",
				zap.String("event_id", record.ID), zap.Error(decodeErr))
			continue
		}
		if !IsReleaseCandidate(event) {
			continue
		}
		s.handleWorkflowResult(ctx, event)
	}
}

func (s *Subscriber) handleWorkflowResult(ctx context.Context, ev *nostr.Event) {
	eventID := nostrutil.EventIDHex(ev)
	if IsReleaseCandidate(ev) {
		if err := s.recordReleaseCandidate(ctx, ev); err != nil {
			s.logger.Warn("failed to durably retain Hive-CI RELEASE candidate", zap.String("event_id", eventID), zap.Error(err))
			if s.releaseAuditor != nil {
				_ = s.releaseAuditor.AuditReleaseRejection(ctx, ev, err)
			}
			return
		}
		if s.releases == nil {
			err := fmt.Errorf("release ingestor is not configured")
			s.logger.Warn("dropping Hive-CI RELEASE result", zap.String("event_id", eventID), zap.Error(err))
			if s.releaseAuditor != nil {
				_ = s.releaseAuditor.AuditReleaseRejection(ctx, ev, err)
			}
			return
		}
		commit, err := s.releases.Ingest(ctx, ev)
		if errors.Is(err, ErrReleaseLineagePending) {
			s.logger.Info("retaining orphaned Hive-CI RELEASE until signed workflow lineage arrives",
				zap.String("event_id", eventID), zap.Error(err))
			return
		}
		if err != nil {
			s.logger.Warn("rejecting Hive-CI RELEASE result", zap.String("event_id", eventID), zap.Error(err))
			if s.releaseAuditor != nil {
				if auditErr := s.releaseAuditor.AuditReleaseRejection(ctx, ev, err); auditErr != nil {
					s.logger.Error("failed to persist Hive-CI release rejection audit", zap.Error(auditErr))
				}
			}
			return
		}
		if s.onRelease != nil {
			s.onRelease(ctx, commit)
		}
		s.logger.Info("Hive-CI RELEASE result accepted", zap.String("event_id", eventID), zap.Bool("replay", commit.Replay))
		return
	}
	existing, err := s.repo.GetResultByEventID(ctx, eventID)
	if err != nil {
		s.logger.Warn("failed to load existing hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	if existing != nil && isTerminalHiveCIResultState(existing.ProcessingState) {
		s.logger.Debug("ignoring replayed terminal hiveci workflow result",
			zap.String("event_id", eventID),
			zap.String("processing_state", string(existing.ProcessingState)),
		)
		return
	}
	pubkey := nostrutil.EventPubKeyHex(ev)
	if _, trusted := s.trusted[pubkey]; !trusted {
		s.logger.Debug("ignoring untrusted hiveci workflow result", zap.String("event_id", eventID), zap.String("pubkey", pubkey))
		return
	}
	runEventID, err := requiredTag(ev, "e")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	logURL, err := requiredTag(ev, "log_url")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	status, err := requiredTag(ev, "status")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	if status != "success" && status != "failure" {
		s.logger.Warn("invalid hiveci workflow result status", zap.String("event_id", eventID), zap.String("status", status))
		return
	}
	exitCodeStr, err := requiredTag(ev, "exit_code")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	exitCode, err := strconv.Atoi(exitCodeStr)
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result exit_code", zap.String("event_id", eventID), zap.String("exit_code", exitCodeStr))
		return
	}
	durationStr, err := requiredTag(ev, "duration")
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	duration, err := strconv.Atoi(durationStr)
	if err != nil {
		s.logger.Warn("invalid hiveci workflow result duration", zap.String("event_id", eventID), zap.String("duration", durationStr))
		return
	}

	run, err := s.repo.GetRunByEventID(ctx, runEventID)
	if err != nil {
		s.logger.Warn("failed to load hiveci workflow run for result", zap.String("run_event_id", runEventID), zap.Error(err))
		return
	}

	// Determine processing state: pending_run if run hasn't arrived yet, pending_result otherwise
	processingState := domain.HiveCIProcessingStatePendingResult
	if run == nil {
		// Result arrived before run - store as orphan to be processed when run arrives
		processingState = domain.HiveCIProcessingStatePendingRun
		s.logger.Info("hiveci workflow result arrived before run, storing as orphan", zap.String("run_event_id", runEventID), zap.String("result_event_id", eventID))
	}

	type workflowResultContent struct {
		ImageRepo   string `json:"image_repo"`
		ImageTag    string `json:"image_tag"`
		ImageDigest string `json:"image_digest"`
	}
	var content workflowResultContent
	if strings.TrimSpace(ev.Content) != "" {
		_ = json.Unmarshal([]byte(ev.Content), &content)
	}
	imageRepo := optionalTag(ev, "image_repo")
	if imageRepo == "" {
		imageRepo = content.ImageRepo
	}
	imageTag := optionalTag(ev, "image_tag")
	if imageTag == "" {
		imageTag = content.ImageTag
	}
	imageDigest := optionalTag(ev, "image_digest")
	if imageDigest == "" {
		imageDigest = content.ImageDigest
	}

	result := domain.HiveCIWorkflowResult{
		ResultEventID:   eventID,
		RunEventID:      runEventID,
		Status:          status,
		ExitCode:        exitCode,
		DurationSeconds: duration,
		LogURL:          logURL,
		Error:           optionalTag(ev, "error"),
		ImageRepo:       imageRepo,
		ImageTag:        imageTag,
		ImageDigest:     imageDigest,
		PublisherPubkey: pubkey,
		EventCreatedAt:  ev.CreatedAt.Time(),
		ProcessingState: processingState,
	}

	if err := s.repo.UpsertWorkflowResult(ctx, result); err != nil {
		s.logger.Warn("failed to persist hiveci workflow result", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	// Only invoke callback if we have the run (otherwise wait for run to arrive)
	if run != nil && s.onResult != nil {
		s.onResult(ctx, result.ResultEventID)
	}
	s.logger.Info("hiveci workflow result ingested", zap.String("run_event_id", runEventID), zap.String("result_event_id", eventID), zap.String("status", status), zap.String("processing_state", string(processingState)))
}

func isTerminalHiveCIResultState(state domain.HiveCIProcessingState) bool {
	switch state {
	case domain.HiveCIProcessingStateProcessed,
		domain.HiveCIProcessingStateRejected,
		domain.HiveCIProcessingStateFailed:
		return true
	default:
		return false
	}
}

func requiredTag(ev *nostr.Event, key string) (string, error) {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			v := strings.TrimSpace(tag[1])
			if v == "" {
				return "", fmt.Errorf("required tag %q is empty", key)
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("missing required tag %q", key)
}

func optionalTag(ev *nostr.Event, key string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}
