package nostrmigration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type EventRepository interface {
	Record(context.Context, *repository.NostrEventRecord) (bool, error)
	ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error)
	ListByKindsPage(context.Context, []int, *repository.NostrMigrationCursor, int) ([]repository.NostrEventRecord, error)
	GetMigrationCursor(context.Context, string) (*repository.NostrMigrationCursor, error)
	SaveMigrationCursor(context.Context, repository.NostrMigrationCursor) error
	FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error)
}

const (
	localCursorName = "bahia-nostr-native-v2-local"
	migrationID     = "bahia-nostr-native-v2"
)

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
	cursor, err := r.repo.GetMigrationCursor(ctx, localCursorName)
	if err != nil {
		return fmt.Errorf("load local nostr migration cursor: %w", err)
	}
	for {
		records, err := r.repo.ListByKindsPage(ctx, LegacyKinds(), cursor, r.config.LocalBatchLimit)
		if err != nil {
			return fmt.Errorf("list local legacy nostr records page: %w", err)
		}
		if len(records) == 0 {
			return nil
		}
		for i := range records {
			summary.LocalScanned++
			if err := r.migrateRecord(ctx, records[i], summary); err != nil {
				return err
			}
			cursor = &repository.NostrMigrationCursor{Name: localCursorName, CreatedAt: records[i].CreatedAt, EventID: records[i].ID}
			if !r.config.DryRun {
				if err := r.repo.SaveMigrationCursor(ctx, *cursor); err != nil {
					return fmt.Errorf("save local nostr migration cursor after %s: %w", records[i].ID, err)
				}
			}
		}
	}
}

func (r *Runner) migrateRelayBackfill(ctx context.Context, summary *Summary) error {
	until := r.config.BackfillUntil
	for {
		count, oldest, err := r.migrateRelayPage(ctx, summary, until)
		if err != nil {
			return err
		}
		if count < r.config.BackfillLimit || oldest == nil {
			return nil
		}
		nextUntil := oldest.Add(-time.Second)
		if r.config.BackfillSince != nil && nextUntil.Before(r.config.BackfillSince.UTC()) {
			return nil
		}
		until = &nextUntil
	}
}

func (r *Runner) migrateRelayPage(ctx context.Context, summary *Summary, until *time.Time) (int, *time.Time, error) {
	filter := gonostr.Filter{Kinds: nostrutil.KindsFromInts(LegacyKinds()), Limit: r.config.BackfillLimit}
	if r.config.BackfillSince != nil {
		filter.Since = gonostr.Timestamp(r.config.BackfillSince.Unix())
	}
	if until != nil {
		filter.Until = gonostr.Timestamp(until.Unix())
	}
	backfillCtx, cancel := context.WithTimeout(ctx, r.config.BackfillTimeout)
	defer cancel()
	merged, err := r.subscriber.SubscribeAllWithEOSE(backfillCtx, []gonostr.Filter{filter})
	if err != nil {
		return 0, nil, fmt.Errorf("subscribe relay migration backfill: %w", err)
	}
	defer merged.Close()
	count := 0
	var oldest *time.Time
	for {
		select {
		case <-ctx.Done():
			return count, oldest, ctx.Err()
		case <-backfillCtx.Done():
			return count, oldest, fmt.Errorf("relay migration backfill did not reach EOSE: %w", backfillCtx.Err())
		case <-merged.EndOfStoredEvents:
			return count, oldest, nil
		case closed, ok := <-merged.Closed:
			if ok {
				return count, oldest, fmt.Errorf("relay migration backfill closed by %s subscription %s: %s", closed.RelayURL, closed.SubscriptionID, closed.Reason)
			}
		case ev, ok := <-merged.Events:
			if !ok {
				continue
			}
			if ev == nil {
				continue
			}
			if err := validateRelayBackfillEvent(ev, time.Now().UTC(), r.config.BackfillSince, until); err != nil {
				r.logger.Warn("skipping invalid relay legacy event during migration backfill", zap.String("event_id", nostrutil.EventIDHex(ev)), zap.Error(err))
				continue
			}
			count++
			createdAt := ev.CreatedAt.Time().UTC()
			if oldest == nil || createdAt.Before(*oldest) {
				oldest = &createdAt
			}
			rec, err := recordFromEvent(ev)
			if err != nil {
				return count, oldest, err
			}
			if _, err := r.repo.Record(ctx, rec); err != nil {
				return count, oldest, fmt.Errorf("record relay legacy event %s: %w", nostrutil.EventIDHex(ev), err)
			}
			summary.RelayScanned++
			if err := r.migrateRecord(ctx, *rec, summary); err != nil {
				return count, oldest, err
			}
		}
	}
}

func (r *Runner) migrateRecord(ctx context.Context, rec repository.NostrEventRecord, summary *Summary) error {
	if recordHasCurrentMigration(rec) {
		summary.SkippedExisting++
		return nil
	}
	disp, ok := ResolveDisposition(rec.Kind, rec.Tags, rec.Content)
	if !ok {
		return nil
	}
	existing, err := r.repo.FindByTag(ctx, "migrated-from", rec.ID, []int{disp.CanonicalKind}, 50)
	if err != nil {
		return fmt.Errorf("detect existing migration output for %s: %w", rec.ID, err)
	}
	if hasCurrentMigration(existing) {
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
	if err := nostrutil.SignEventWithHexKey(ev, r.config.PrivateKey); err != nil {
		return fmt.Errorf("sign migration event for %s: %w", rec.ID, err)
	}
	outcomes, err := r.publisher.PublishMigrationEvent(ctx, *ev)
	if err != nil {
		return fmt.Errorf("publish migration event for %s: %w", rec.ID, err)
	}
	accepted, duplicate := verifyPublish(outcomes)
	if !accepted && !duplicate {
		return fmt.Errorf("migration event %s had no accepted or duplicate relay OK", nostrutil.EventIDHex(ev))
	}
	if duplicate {
		summary.PublishDuplicate++
	}
	canon, err := recordFromEvent(ev)
	if err != nil {
		return err
	}
	if _, err := r.repo.Record(ctx, canon); err != nil {
		return fmt.Errorf("record canonical migration event %s: %w", nostrutil.EventIDHex(ev), err)
	}
	summary.Migrated++
	summary.ByLegacyKind[rec.Kind]++
	return nil
}

func BuildCanonicalEvent(rec repository.NostrEventRecord, disp Disposition) (*gonostr.Event, error) {
	translated, err := translatedLegacyContent(rec)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{"id": rec.ID, "kind": rec.Kind, "pubkey": rec.PubKey, "created_at": rec.CreatedAt.UTC().Format(time.RFC3339), "version": migrationID}
	payload := translatedObject(translated)
	payload["schema"] = disp.Schema
	payload["_migration"] = metadata
	if disp.Method != "" {
		params := translatedObject(translated)
		params["legacy_event_id"] = rec.ID
		params["legacy_kind"] = rec.Kind
		params["_meta"] = map[string]any{"progressToken": "migration-" + rec.ID, "migration": metadata, "schema": disp.Schema}
		payload = map[string]any{}
		payload["jsonrpc"] = "2.0"
		payload["id"] = "migration-" + rec.ID
		payload["method"] = disp.Method
		payload["params"] = params
	} else if disp.CanonicalKind == CanonicalContextVMMessage && disp.Operation == "result" {
		result := translatedObject(translated)
		result["legacy_event_id"] = rec.ID
		result["legacy_kind"] = rec.Kind
		result["_migration"] = metadata
		payload = map[string]any{
			"jsonrpc": "2.0",
			"id":      "migration-" + rec.ID,
			"result":  result,
		}
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
	return &gonostr.Event{Kind: gonostr.Kind(disp.CanonicalKind), CreatedAt: gonostr.Timestamp(createdAt.Unix()), Tags: eventTags, Content: string(content)}, nil
}

func translatedLegacyContent(rec repository.NostrEventRecord) (any, error) {
	var translated any
	decoder := json.NewDecoder(strings.NewReader(rec.Content))
	decoder.UseNumber()
	if err := decoder.Decode(&translated); err != nil {
		return nil, fmt.Errorf("translate legacy content for %s: %w", rec.ID, err)
	}
	return translated, nil
}

func translatedObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(object)+2)
		for key, item := range object {
			out[key] = item
		}
		return out
	}
	return map[string]any{"data": value}
}

func hasCurrentMigration(records []repository.NostrEventRecord) bool {
	for _, rec := range records {
		if recordHasCurrentMigration(rec) {
			return true
		}
	}
	return false
}

func recordHasCurrentMigration(rec repository.NostrEventRecord) bool {
	var tags [][]string
	if json.Unmarshal(rec.Tags, &tags) != nil {
		return false
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "migration" && tag[1] == migrationID {
			return true
		}
	}
	return false
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

func validateRelayBackfillEvent(ev *gonostr.Event, now time.Time, since, until *time.Time) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ev.CreatedAt <= 0 {
		return fmt.Errorf("created_at is required")
	}
	createdAt := ev.CreatedAt.Time()
	if createdAt.After(now.Add(nostrAdapter.InboundEventMaxFutureSkew)) {
		return fmt.Errorf("created_at too far in future")
	}
	if since != nil && createdAt.Before(since.UTC()) {
		return fmt.Errorf("created_at before relay backfill since bound")
	}
	if until != nil && createdAt.After(until.UTC()) {
		return fmt.Errorf("created_at after relay backfill until bound")
	}
	for i, tag := range ev.Tags {
		if tag == nil {
			return fmt.Errorf("tag %d is nil", i)
		}
		if len(tag) == 0 {
			return fmt.Errorf("tag %d is empty", i)
		}
		if tag[0] == "" {
			return fmt.Errorf("tag %d has empty key", i)
		}
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	if !ev.VerifySignature() {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func recordFromEvent(ev *gonostr.Event) (*repository.NostrEventRecord, error) {
	if ev == nil {
		return nil, fmt.Errorf("nostr event is nil")
	}
	tags, err := json.Marshal(ev.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal event tags %s: %w", nostrutil.EventIDHex(ev), err)
	}
	return &repository.NostrEventRecord{ID: nostrutil.EventIDHex(ev), Kind: int(ev.Kind), PubKey: nostrutil.EventPubKeyHex(ev), Content: ev.Content, Tags: tags, Sig: nostrutil.EventSignatureHex(ev), CreatedAt: ev.CreatedAt.Time().UTC(), ReceivedAt: time.Now().UTC(), EntityType: "nostr_migration"}, nil
}
