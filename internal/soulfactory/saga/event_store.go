package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// RecordSchema is the schema tag and content discriminator of the canonical
// saga-run record (audit C-45).
const RecordSchema = "bahia.state.soulfactory-saga-run.v1"

const (
	recordDomain = "soul-factory"
	recordEntity = "saga-run"
	// recordHistoryLimit bounds the audit history a record carries. Resume
	// needs the stage, the resume stage, the resource lineage, the
	// compensations already made and the current failure; transitions and
	// past failures are operator history, so a record keeps only the newest
	// ones and the totals. The record is replaced per run, so canonical state
	// grows with the number of runs, never with the number of retries.
	recordHistoryLimit = 16
)

// EventReader is the read surface of the daemon's local event store.
type EventReader interface {
	QueryEvents(gonostr.Filter) iter.Seq[gonostr.Event]
}

// EventPublisher signs a record with the daemon's service key and publishes
// it outbox-first.
type EventPublisher interface {
	PublishSignedEvent(context.Context, *gonostr.Event) error
}

// EventStoreConfig wires an EventStore.
type EventStoreConfig struct {
	Reader    EventReader
	Publisher EventPublisher
	// ServicePubkey (hex) is the author every record is read under.
	ServicePubkey string
	// CacheDir optionally keeps the on-disk JSON checkpoint per run that
	// FileStore writes. It is a fast local cache and the journal a daemon
	// upgraded in place resumes from once; it is never the only authority.
	CacheDir string
	Logger   *slog.Logger
	Now      func() time.Time
}

// EventStore is the Store whose authority is the daemon's canonical saga-run
// record: one replaceable cp-state record per run in the local event store
// (and on the relays, through the outbox), published before a checkpoint is
// reported durable. Precedence on read is canonical record → the copy this
// process published → the file cache, the highest version winning and the
// record winning a tie; a run whose file is absent (fresh host) resumes from
// the record alone. The engine's optimistic versioning and append-only
// lineage checks are enforced here exactly as FileStore enforces them.
type EventStore struct {
	reader    EventReader
	publisher EventPublisher
	author    gonostr.PubKey
	cacheDir  string
	logger    *slog.Logger
	now       func() time.Time

	// mu serializes every read-modify-publish, as FileStore's lock does.
	mu   sync.Mutex
	runs map[string]cachedRun
}

// cachedRun is the record this process last published for one request.
type cachedRun struct {
	run       *Run
	createdAt gonostr.Timestamp
	deleted   bool
}

// NewEventStore returns the canonical saga store.
func NewEventStore(cfg EventStoreConfig) (*EventStore, error) {
	if cfg.Reader == nil || cfg.Publisher == nil {
		return nil, errors.New("saga event store requires an event reader and a publisher")
	}
	author, err := gonostr.PubKeyFromHex(strings.TrimSpace(cfg.ServicePubkey))
	if err != nil {
		return nil, fmt.Errorf("saga event store requires the service pubkey: %w", err)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	cacheDir := strings.TrimSpace(cfg.CacheDir)
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("create saga cache directory: %w", err)
		}
		if err := os.Chmod(cacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("secure saga cache directory: %w", err)
		}
	}
	return &EventStore{reader: cfg.Reader, publisher: cfg.Publisher, author: author, cacheDir: cacheDir, logger: logger.With("component", "saga_event_store"), now: now, runs: map[string]cachedRun{}}, nil
}

// Record is the wire content of one canonical saga-run record. It carries
// everything resume needs and bounded audit history; it never carries
// secrets, because a Run holds only one-way resource references and
// sanitized public failures (Run.validate).
type Record struct {
	Schema            string         `json:"schema"`
	RequestID         string         `json:"request_id"`
	RunID             string         `json:"run_id"`
	RootKey           string         `json:"root_key"`
	AgentID           string         `json:"agent_id"`
	SpecHash          string         `json:"spec_hash"`
	Stage             Stage          `json:"stage"`
	ResumeStage       Stage          `json:"resume_stage,omitempty"`
	Version           uint64         `json:"version"`
	Resources         []Resource     `json:"resources,omitempty"`
	Compensations     []Compensation `json:"compensations,omitempty"`
	Failure           *Failure       `json:"failure,omitempty"`
	RecentTransitions []Transition   `json:"recent_transitions,omitempty"`
	TransitionCount   int            `json:"transition_count"`
	RecentFailures    []Failure      `json:"recent_failures,omitempty"`
	FailureCount      int            `json:"failure_count"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	RetainUntil       *time.Time     `json:"retain_until,omitempty"`
	Deleted           bool           `json:"deleted,omitempty"`
}

// RecordDTag is the coordinate of the request's saga-run record.
func RecordDTag(requestID string) string {
	return recordDomain + ":" + recordEntity + ":" + strings.TrimPrefix(DeriveKey(strings.TrimSpace(requestID), "record"), "ocw-saga:")
}

// recordFromRun is the bounded record of run.
func recordFromRun(run *Run) Record {
	record := Record{
		Schema: RecordSchema, RequestID: run.RequestID, RunID: run.RunID, RootKey: run.RootKey, AgentID: run.AgentID, SpecHash: run.SpecHash,
		Stage: run.Stage, ResumeStage: run.ResumeStage, Version: run.Version,
		Resources:       append([]Resource(nil), run.Resources...),
		Compensations:   append([]Compensation(nil), run.Compensations...),
		TransitionCount: len(run.Transitions), FailureCount: len(run.Failures),
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
	if run.Failure != nil {
		failure := *run.Failure
		record.Failure = &failure
	}
	if run.RetainUntil != nil {
		until := *run.RetainUntil
		record.RetainUntil = &until
	}
	if n := len(run.Transitions); n > 0 {
		record.RecentTransitions = append([]Transition(nil), run.Transitions[max(0, n-recordHistoryLimit):]...)
	}
	if n := len(run.Failures); n > 0 {
		record.RecentFailures = append([]Failure(nil), run.Failures[max(0, n-recordHistoryLimit):]...)
	}
	return record
}

// run rebuilds the Run a record carries. Its transitions and failures are
// the record's recent history.
func (r Record) run() (*Run, error) {
	if r.Schema != RecordSchema {
		return nil, fmt.Errorf("saga record schema %q is not %s", r.Schema, RecordSchema)
	}
	run := &Run{
		RequestID: r.RequestID, RunID: r.RunID, RootKey: r.RootKey, AgentID: r.AgentID, SpecHash: r.SpecHash,
		Stage: r.Stage, ResumeStage: r.ResumeStage, Version: r.Version,
		Resources: append([]Resource(nil), r.Resources...), Compensations: append([]Compensation(nil), r.Compensations...),
		Transitions: append([]Transition(nil), r.RecentTransitions...), Failures: append([]Failure(nil), r.RecentFailures...),
		Failure: r.Failure, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RetainUntil: r.RetainUntil,
	}
	if err := run.validate(); err != nil {
		return nil, fmt.Errorf("validate saga record: %w", err)
	}
	return run.clone(), nil
}

// decodeRecord decodes a canonical saga-run record authored by the daemon.
// It reports false for events of another family or author.
func decodeRecord(ev gonostr.Event, author gonostr.PubKey) (Record, bool) {
	if ev.PubKey != author || int(ev.Kind) != kinds.CASControlState || tagValue(ev.Tags, kinds.CASControlStateTagSchema) != RecordSchema {
		return Record{}, false
	}
	var record Record
	if json.Unmarshal([]byte(ev.Content), &record) != nil || record.Schema != RecordSchema {
		return Record{}, false
	}
	if tagValue(ev.Tags, kinds.CASControlStateTagDeleted) == "true" {
		record.Deleted = true
	}
	return record, true
}

func tagValue(tags gonostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// Create publishes the run's first record. A request that already has a
// live record, file or in-process copy is a conflict.
func (s *EventStore) Create(ctx context.Context, run *Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := run.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.current(ctx, run.RequestID)
	if err != nil {
		return err
	}
	if current != nil {
		return ErrConflict
	}
	return s.publish(ctx, run, replaces, false)
}

// Load returns the request's run from whichever authority holds the highest
// version.
func (s *EventStore) Load(ctx context.Context, requestID string) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, _, err := s.current(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrNotFound
	}
	return current.clone(), nil
}

// Save publishes the next version of the run's record. The checkpoint is
// durable only once the publisher accepted or durably queued the record.
func (s *EventStore) Save(ctx context.Context, run *Run, expectedVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := run.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.current(ctx, run.RequestID)
	if err != nil {
		return err
	}
	if current == nil {
		return ErrNotFound
	}
	if current.Version != expectedVersion || run.Version != expectedVersion+1 {
		return ErrConflict
	}
	if err := validateImmutableUpdate(current, run); err != nil {
		return err
	}
	return s.publish(ctx, run, replaces, false)
}

// List returns every live run the local event store, this process or the
// file cache knows, ordered by request id.
func (s *EventStore) List(ctx context.Context) ([]*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := map[string]struct{}{}
	for ev := range s.reader.QueryEvents(gonostr.Filter{
		Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Authors: []gonostr.PubKey{s.author},
		Tags: gonostr.TagMap{"t": {kinds.CPStateTopicSoulFactorySagaRun}},
	}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if record, ok := decodeRecord(ev, s.author); ok {
			ids[record.RequestID] = struct{}{}
		}
	}
	for requestID := range s.runs {
		ids[requestID] = struct{}{}
	}
	if s.cacheDir != "" {
		entries, err := os.ReadDir(s.cacheDir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			run, err := readRun(filepath.Join(s.cacheDir, entry.Name()))
			if err != nil {
				return nil, err
			}
			ids[run.RequestID] = struct{}{}
		}
	}
	out := make([]*Run, 0, len(ids))
	for requestID := range ids {
		current, _, err := s.current(ctx, requestID)
		if err != nil {
			return nil, err
		}
		if current != nil {
			out = append(out, current.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out, nil
}

// Delete publishes the run's tombstone and drops its file.
func (s *EventStore) Delete(ctx context.Context, requestID string, expectedVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.current(ctx, requestID)
	if err != nil {
		return err
	}
	if current == nil {
		return ErrNotFound
	}
	if current.Version != expectedVersion {
		return ErrConflict
	}
	return s.publish(ctx, current, replaces, true)
}

// current returns the request's live run from the highest-version authority
// and the created_at of the newest record known, which the next publish must
// replace. On a version tie the copy this process published wins, then the
// canonical record, then the file cache: the process copy carries the full
// history the engine appends to, the record carries the bounded history a
// restarted or moved daemon resumes from, and a file is only ever as new as
// the record that was published before it was written. A tombstone is the
// coordinate's latest record until a new run is created there; a file left
// behind at or below the tombstoned version cannot resurrect the run.
func (s *EventStore) current(ctx context.Context, requestID string) (*Run, gonostr.Timestamp, error) {
	var (
		best       *Run
		replaces   gonostr.Timestamp
		tombstoned uint64
	)
	consider := func(run *Run, deleted bool) {
		if deleted {
			tombstoned = max(tombstoned, run.Version)
			return
		}
		if best == nil || run.Version > best.Version {
			best = run
		}
	}
	if written, ok := s.runs[requestID]; ok {
		replaces = written.createdAt
		consider(written.run, written.deleted)
	}
	record, err := s.record(ctx, requestID)
	if err != nil {
		return nil, 0, err
	}
	if record != nil {
		replaces = max(replaces, record.createdAt)
		consider(record.run, record.deleted)
	}
	if s.cacheDir != "" {
		cached, err := readRun(s.cachePath(requestID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, 0, err
		}
		if cached != nil && cached.Version > tombstoned {
			consider(cached, false)
		}
	}
	return best, replaces, nil
}

// record reads the request's canonical record from the local event store.
func (s *EventStore) record(ctx context.Context, requestID string) (*cachedRun, error) {
	var latest *gonostr.Event
	for ev := range s.reader.QueryEvents(gonostr.Filter{
		Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Authors: []gonostr.PubKey{s.author},
		Tags: gonostr.TagMap{kinds.CASControlStateTagD: {RecordDTag(requestID)}},
	}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != s.author || int(ev.Kind) != kinds.CASControlState {
			continue
		}
		if latest == nil || ev.CreatedAt > latest.CreatedAt {
			copyEvent := ev
			latest = &copyEvent
		}
	}
	if latest == nil {
		return nil, nil
	}
	record, ok := decodeRecord(*latest, s.author)
	if !ok || record.RequestID != requestID {
		return nil, fmt.Errorf("saga record at %s is not a %s record of request %s", RecordDTag(requestID), RecordSchema, requestID)
	}
	run, err := record.run()
	if err != nil {
		return nil, err
	}
	return &cachedRun{run: run, createdAt: latest.CreatedAt, deleted: record.Deleted}, nil
}

// publish signs and publishes the run's record (or tombstone), then updates
// this process's copy and the file cache. Its created_at is strictly after
// the record it replaces, so a checkpoint rewritten within one second still
// replaces its predecessor. A publish the outbox keeps for retry counts as
// published; a cache write failure is logged, never reported, because the
// canonical record is already durable.
func (s *EventStore) publish(ctx context.Context, run *Run, replaces gonostr.Timestamp, deleted bool) error {
	record := recordFromRun(run)
	record.Deleted = deleted
	content, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode saga record: %w", err)
	}
	createdAt := gonostr.Timestamp(s.now().Unix())
	if createdAt <= replaces {
		createdAt = replaces + 1
	}
	event := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Content: string(content), Tags: gonostr.Tags{
		{kinds.CASControlStateTagD, RecordDTag(run.RequestID)},
		{kinds.CASControlStateTagDomain, recordDomain},
		{kinds.CASControlStateTagSchema, RecordSchema},
		{kinds.CASControlStateTagEntity, recordEntity},
		{"t", kinds.CPStateTopicSoulFactorySagaRun},
		{kinds.CASControlStateTagLegacyKind, kinds.CPStateFamilySoulFactorySagaRun.TagValue()},
		{kinds.CASControlStateTagDeleted, fmt.Sprint(deleted)},
		{"stage", string(run.Stage)},
	}}
	if err := s.publisher.PublishSignedEvent(ctx, &event); err != nil && !nostrutil.IsPublishQueued(err) {
		return fmt.Errorf("publish saga record for %s: %w", run.RequestID, err)
	}
	s.runs[run.RequestID] = cachedRun{run: run.clone(), createdAt: createdAt, deleted: deleted}
	if s.cacheDir == "" {
		return nil
	}
	path := s.cachePath(run.RequestID)
	if deleted {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("saga checkpoint cache remove failed", "request_id", run.RequestID, "error", err)
		}
		return nil
	}
	if err := writeAtomic(path, run); err != nil {
		s.logger.Warn("saga checkpoint cache write failed", "request_id", run.RequestID, "error", err)
	}
	return nil
}

// cachePath is the FileStore path of the request's checkpoint, so a
// directory FileStore wrote before the canonical record existed serves as
// the cache.
func (s *EventStore) cachePath(requestID string) string {
	return filepath.Join(s.cacheDir, DeriveKey(requestID, "file")+".json")
}

var _ Store = (*EventStore)(nil)
