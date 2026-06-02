package nostrmigration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type EventRepository interface {
	Record(context.Context, *repository.NostrEventRecord) (bool, error)
	ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error)
	FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error)
}

type RelaySubscriber interface {
	SubscribeAllWithEOSE(context.Context, []gonostr.Filter) (*nostrAdapter.MergedSubscription, error)
}

type PublishOutcome struct {
	RelayURL string
	Accepted bool
	Reason   string
	Error    error
}

type EventPublisher interface {
	PublishMigrationEvent(context.Context, gonostr.Event) ([]PublishOutcome, error)
}

type Config struct {
	PrivateKey      string
	LocalBatchLimit int
	RelayBackfill   bool
	DryRun          bool
	BackfillSince   *time.Time
	BackfillUntil   *time.Time
	BackfillLimit   int
	BackfillTimeout time.Duration
}

type Runner struct {
	repo       EventRepository
	publisher  EventPublisher
	subscriber RelaySubscriber
	config     Config
	logger     *zap.Logger
}

type Summary struct {
	LocalScanned     int
	RelayScanned     int
	Migrated         int
	SkippedExisting  int
	DryRun           int
	PublishDuplicate int
	ByLegacyKind     map[int]int
}

func NewRunner(repo EventRepository, publisher EventPublisher, subscriber RelaySubscriber, cfg Config, logger *zap.Logger) *Runner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.LocalBatchLimit <= 0 {
		cfg.LocalBatchLimit = 1000
	}
	if cfg.BackfillLimit <= 0 {
		cfg.BackfillLimit = 500
	}
	if cfg.BackfillTimeout <= 0 {
		cfg.BackfillTimeout = 30 * time.Second
	}
	return &Runner{repo: repo, publisher: publisher, subscriber: subscriber, config: cfg, logger: logger.Named("nostr-migration")}
}

func (r *Runner) Name() string { return "nostr-migration" }

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.repo == nil {
		return nil
	}
	summary := Summary{ByLegacyKind: map[int]int{}}
	if err := r.migrateLocal(ctx, &summary); err != nil {
		return err
	}
	if r.config.RelayBackfill && r.subscriber != nil {
		if err := r.migrateRelayBackfill(ctx, &summary); err != nil {
			return err
		}
	}
	r.logger.Info("nostr native migration complete",
		zap.Int("local_scanned", summary.LocalScanned),
		zap.Int("relay_scanned", summary.RelayScanned),
		zap.Int("migrated", summary.Migrated),
		zap.Int("skipped_existing", summary.SkippedExisting),
		zap.Int("dry_run", summary.DryRun),
		zap.Int("publish_duplicate", summary.PublishDuplicate),
		zap.Any("by_legacy_kind", summary.ByLegacyKind),
	)
	return nil
}

func (r *Runner) migrateLocal(ctx context.Context, summary *Summary) error {
	records, err := r.repo.ListByKinds(ctx, LegacyKinds(), r.config.LocalBatchLimit)
	if err != nil {
		return fmt.Errorf("list local legacy nostr records: %w", err)
	}
	for i := range records {
		summary.LocalScanned++
		if err := r.migrateRecord(ctx, records[i], summary); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) migrateRelayBackfill(ctx context.Context, summary *Summary) error {
	filter := gonostr.Filter{Kinds: LegacyKinds(), Limit: r.config.BackfillLimit}
	if r.config.BackfillSince != nil {
		since := gonostr.Timestamp(r.config.BackfillSince.Unix())
		filter.Since = &since
	}
	if r.config.BackfillUntil != nil {
		until := gonostr.Timestamp(r.config.BackfillUntil.Unix())
		filter.Until = &until
	}
	backfillCtx, cancel := context.WithTimeout(ctx, r.config.BackfillTimeout)
	defer cancel()
	merged, err := r.subscriber.SubscribeAllWithEOSE(backfillCtx, []gonostr.Filter{filter})
	if err != nil {
		return fmt.Errorf("subscribe relay migration backfill: %w", err)
	}
	defer merged.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-backfillCtx.Done():
			return fmt.Errorf("relay migration backfill did not reach EOSE: %w", backfillCtx.Err())
		case <-merged.EndOfStoredEvents:
			return nil
		case closed, ok := <-merged.Closed:
			if ok {
				return fmt.Errorf("relay migration backfill closed by %s subscription %s: %s", closed.RelayURL, closed.SubscriptionID, closed.Reason)
			}
		case ev, ok := <-merged.Events:
			if !ok {
				continue
			}
			if ev == nil {
				continue
			}
			rec, err := recordFromEvent(ev)
			if err != nil {
				return err
			}
			if _, err := r.repo.Record(ctx, rec); err != nil {
				return fmt.Errorf("record relay legacy event %s: %w", ev.ID, err)
			}
			summary.RelayScanned++
			if err := r.migrateRecord(ctx, *rec, summary); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) migrateRecord(ctx context.Context, rec repository.NostrEventRecord, summary *Summary) error {
	disp, ok := ResolveDisposition(rec.Kind, rec.Tags, rec.Content)
	if !ok {
		return nil
	}
	existing, err := r.repo.FindByTag(ctx, "migrated-from", rec.ID, []int{disp.CanonicalKind}, 1)
	if err != nil {
		return fmt.Errorf("detect existing migration output for %s: %w", rec.ID, err)
	}
	if len(existing) > 0 {
		summary.SkippedExisting++
		return nil
	}
	ev, err := BuildCanonicalEvent(rec, disp)
	if err != nil {
		return err
	}
	if r.config.DryRun {
		summary.DryRun++
		return nil
	}
	if r.publisher == nil || r.config.PrivateKey == "" {
		return fmt.Errorf("nostr migration publisher and private key are required")
	}
	if err := ev.Sign(r.config.PrivateKey); err != nil {
		return fmt.Errorf("sign migration event for %s: %w", rec.ID, err)
	}
	outcomes, err := r.publisher.PublishMigrationEvent(ctx, *ev)
	if err != nil {
		return fmt.Errorf("publish migration event for %s: %w", rec.ID, err)
	}
	accepted, duplicate := verifyPublish(outcomes)
	if !accepted && !duplicate {
		return fmt.Errorf("migration event %s had no accepted or duplicate relay OK", ev.ID)
	}
	if duplicate {
		summary.PublishDuplicate++
	}
	canon, err := recordFromEvent(ev)
	if err != nil {
		return err
	}
	if _, err := r.repo.Record(ctx, canon); err != nil {
		return fmt.Errorf("record canonical migration event %s: %w", ev.ID, err)
	}
	summary.Migrated++
	summary.ByLegacyKind[rec.Kind]++
	return nil
}

func BuildCanonicalEvent(rec repository.NostrEventRecord, disp Disposition) (*gonostr.Event, error) {
	payload := map[string]any{
		"migration": "bahia-nostr-native-v1",
		"legacy_event": map[string]any{
			"id":         rec.ID,
			"kind":       rec.Kind,
			"pubkey":     rec.PubKey,
			"content":    rec.Content,
			"created_at": rec.CreatedAt.UTC().Format(time.RFC3339),
		},
	}
	if disp.Method != "" {
		payload["jsonrpc"] = "2.0"
		payload["id"] = "migration-" + rec.ID
		payload["method"] = disp.Method
		payload["params"] = map[string]any{"legacy_event_id": rec.ID, "legacy_kind": rec.Kind, "content": rec.Content, "_meta": map[string]any{"progressToken": "migration-" + rec.ID}}
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal migration payload for %s: %w", rec.ID, err)
	}
	tags := disp.Tags(rec.ID)
	if len(rec.Tags) > 0 {
		var legacyTags [][]string
		if err := json.Unmarshal(rec.Tags, &legacyTags); err == nil {
			for _, tag := range legacyTags {
				if len(tag) >= 2 {
					tags = append(tags, []string{"legacy-tag", tag[0], strings.Join(tag[1:], ":")})
				}
			}
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return strings.Join(tags[i], "\x00") < strings.Join(tags[j], "\x00") })
	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	eventTags := make(gonostr.Tags, 0, len(tags))
	for _, tag := range tags {
		eventTags = append(eventTags, gonostr.Tag(tag))
	}
	return &gonostr.Event{Kind: disp.CanonicalKind, CreatedAt: gonostr.Timestamp(createdAt.Unix()), Tags: eventTags, Content: string(content)}, nil
}

func verifyPublish(outcomes []PublishOutcome) (accepted bool, duplicate bool) {
	for _, outcome := range outcomes {
		if outcome.Accepted {
			accepted = true
		}
		if nostrAdapter.IsDuplicateReason(outcome.Reason) {
			duplicate = true
		}
	}
	return accepted, duplicate
}

func recordFromEvent(ev *gonostr.Event) (*repository.NostrEventRecord, error) {
	if ev == nil {
		return nil, fmt.Errorf("nostr event is nil")
	}
	tags, err := json.Marshal(ev.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal event tags %s: %w", ev.ID, err)
	}
	return &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tags, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time().UTC(), ReceivedAt: time.Now().UTC(), EntityType: "nostr_migration"}, nil
}
