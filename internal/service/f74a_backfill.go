package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	f74aBackfillPageSize = repository.F74aPageLimit
	f74aProgressFamily   = "bootstrap"
	f74aProgressID       = "f74a-canonical-v2"
	f74aDefaultRate      = 25
	f74aHighWater        = 2000
	f74aLowWater         = 1500
)

// F74aBackfillMarker remains the legacy get/put contract used by other backfills.
type F74aBackfillMarker interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}

// F74aProgressStore serializes v2 cursor and dirty-generation mutations.
type F74aProgressStore interface {
	GetControlRecord(family, id string) ([]byte, error)
	UpdateControlRecord(family, id string, update func([]byte) ([]byte, error)) ([]byte, error)
}

type F74aBackfillSource interface {
	ListReleasesAfter(context.Context, uuid.UUID, int) ([]domain.LLMRelease, error)
	ListSignaturesAfter(context.Context, uuid.UUID, int) ([]domain.ArtifactSignature, error)
	ListSBOMsAfter(context.Context, uuid.UUID, int) ([]domain.ArtifactSBOM, error)
	ListSemanticPackagesAfter(context.Context, uuid.UUID, int) ([]domain.SBOMPackage, error)
	ListLegacyPackagesAfter(context.Context, uuid.UUID, int) ([]domain.SBOMPackage, error)
	ListLinkedObservationsAfter(context.Context, repository.F74aStateCursor, int) ([]domain.RuntimeObservation, error)
}

type F74aBackfillPublisher interface {
	PublishLLMRelease(context.Context, *domain.LLMRelease) error
	PublishArtifactSignature(context.Context, *domain.ArtifactSignature) error
	PublishArtifactSBOM(context.Context, *domain.ArtifactSBOM) error
	PublishSBOMPackage(context.Context, *domain.SBOMPackage) error
	PublishLegacySBOMPackageTombstone(context.Context, *domain.SBOMPackage) error
	PublishRuntimeObservation(context.Context, *domain.RuntimeObservation) error
}

// F74aOutboxCountFunc adapts the durable outbox's count result without coupling service to bbolt.
type F74aOutboxCountFunc func(context.Context) (int64, error)

// F74aSemanticDeliveryProof must prove the current semantic coordinate was
// accepted by the relay publish quorum. Missing or pruned local evidence is
// not proof; an operator must sync retained relay state before retrying.
type F74aSemanticDeliveryProof func(context.Context, *domain.SBOMPackage) (bool, error)

type F74aBackfillConfig struct {
	Marker            F74aProgressStore
	Source            F74aBackfillSource
	Publisher         F74aBackfillPublisher
	Pending           F74aOutboxCountFunc
	SemanticDelivered F74aSemanticDeliveryProof
	Ready             <-chan struct{}
	Rate              int
	HighWater         int64
	LowWater          int64
}

type F74aBackfillProgress struct {
	Phase          string                     `json:"phase"`
	Cursor         uuid.UUID                  `json:"cursor"`
	StateCursor    repository.F74aStateCursor `json:"state_cursor"`
	Generation     uint64                     `json:"generation"`
	PassGeneration uint64                     `json:"pass_generation"`
	Completed      bool                       `json:"completed"`
	RelayVerified  bool                       `json:"relay_verified"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

var f74aPhases = [...]string{"releases", "signatures", "sboms", "semantic_packages", "legacy_packages", "observations", "complete"}

func decodeF74aProgress(raw []byte) (F74aBackfillProgress, error) {
	if len(raw) == 0 {
		return F74aBackfillProgress{Phase: f74aPhases[0]}, nil
	}
	var p F74aBackfillProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("decode F74a progress: %w", err)
	}
	valid := false
	for _, phase := range f74aPhases {
		if p.Phase == phase {
			valid = true
			break
		}
	}
	if !valid {
		return p, fmt.Errorf("invalid F74a phase %q", p.Phase)
	}
	// Markers written before relay-visibility gating are not completion proof.
	if p.Completed && !p.RelayVerified {
		p.Completed = false
		p.Phase = "semantic_packages"
		p.Cursor = uuid.Nil
		p.StateCursor = repository.F74aStateCursor{}
		p.PassGeneration = p.Generation
	}
	if p.Completed && p.Phase != "complete" {
		return p, errors.New("completed F74a progress has incomplete phase")
	}
	return p, nil
}

// F74aBackfillRunner owns a single bounded worker. A nil publisher return
// means locally staged or deduplicated, never a relay acknowledgment.
type F74aBackfillRunner struct {
	cfg      F74aBackfillConfig
	wake     chan struct{}
	mu       sync.RWMutex
	snapshot F74aBackfillSnapshot
}
type F74aBackfillSnapshot struct {
	Phase      string
	Cursor     string
	Visited    uint64
	Processed  uint64
	Failures   uint64
	Retries    uint64
	Pending    int64
	Paused     bool
	Generation uint64
	Completed  bool
	UpdatedAt  time.Time
	LastError  string
}

func NewF74aBackfillRunner(cfg F74aBackfillConfig) *F74aBackfillRunner {
	if cfg.Rate <= 0 {
		cfg.Rate = f74aDefaultRate
	}
	if cfg.HighWater <= 0 {
		cfg.HighWater = f74aHighWater
	}
	if cfg.LowWater <= 0 || cfg.LowWater >= cfg.HighWater {
		cfg.LowWater = f74aLowWater
		if cfg.LowWater >= cfg.HighWater {
			cfg.LowWater = cfg.HighWater - 1
		}
	}
	return &F74aBackfillRunner{cfg: cfg, wake: make(chan struct{}, 1), snapshot: F74aBackfillSnapshot{Phase: "pending"}}
}
func (r *F74aBackfillRunner) Name() string { return "f74a_backfill" }
func (r *F74aBackfillRunner) Snapshot() F74aBackfillSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot
}
func (r *F74aBackfillRunner) setProgress(p F74aBackfillProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot.Phase = p.Phase
	r.snapshot.Cursor = p.Cursor.String()
	if p.Phase == "observations" {
		r.snapshot.Cursor = p.StateCursor.ServiceID.String() + "/" + p.StateCursor.EnvironmentID.String()
	}
	r.snapshot.Generation = p.Generation
	r.snapshot.Completed = p.Completed
	r.snapshot.UpdatedAt = p.UpdatedAt
}
func (r *F74aBackfillRunner) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot.Failures++
	r.snapshot.LastError = err.Error()
}
func (r *F74aBackfillRunner) recordVisit() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot.Visited++
	r.snapshot.Processed++
	r.snapshot.LastError = ""
}

func (r *F74aBackfillRunner) setAdmission(pending int64, paused bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.snapshot.Pending = pending
	}
	r.snapshot.Paused = paused || err != nil
	if err != nil {
		r.snapshot.LastError = "outbox count: " + err.Error()
		r.snapshot.Failures++
	}
}

func (r *F74aBackfillRunner) trigger() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *F74aBackfillRunner) update(fn func(*F74aBackfillProgress) error) (F74aBackfillProgress, error) {
	var next F74aBackfillProgress
	_, err := r.cfg.Marker.UpdateControlRecord(f74aProgressFamily, f74aProgressID, func(raw []byte) ([]byte, error) {
		p, err := decodeF74aProgress(raw)
		if err != nil {
			return nil, err
		}
		if err = fn(&p); err != nil {
			return nil, err
		}
		p.UpdatedAt = time.Now().UTC()
		encoded, err := json.Marshal(p)
		if err == nil {
			next = p
		}
		return encoded, err
	})
	if err == nil {
		r.setProgress(next)
	}
	return next, err
}
func (r *F74aBackfillRunner) load() (F74aBackfillProgress, error) {
	raw, err := r.cfg.Marker.GetControlRecord(f74aProgressFamily, f74aProgressID)
	if err != nil {
		return F74aBackfillProgress{}, err
	}
	p, err := decodeF74aProgress(raw)
	if err == nil {
		r.setProgress(p)
	}
	return p, err
}

// MarkDirty invalidates a pass if an operator detects a concurrent legacy write.
// Normal daemon writes never call this migration-only method.
func (r *F74aBackfillRunner) markDirty(force bool) error {
	_, err := r.update(func(p *F74aBackfillProgress) error {
		if !p.Completed || force {
			p.Generation++
			p.Completed = false
			if p.Phase == "complete" {
				p.Phase = f74aPhases[0]
				p.Cursor = uuid.Nil
				p.StateCursor = repository.F74aStateCursor{}
				p.PassGeneration = p.Generation
			}
		}
		return nil
	})
	if err == nil {
		r.trigger()
	}
	return err
}

// Run keeps the repair worker alive for post-completion live-publication
// failures. Cancellation stops at an item boundary; progress remains durable.
// RunMigration executes one explicit, resumable operator pass and propagates
// errors. It never runs on normal daemon startup.
func (r *F74aBackfillRunner) RunMigration(ctx context.Context) error {
	if r == nil || r.cfg.Marker == nil || r.cfg.Source == nil || r.cfg.Publisher == nil || r.cfg.Pending == nil || r.cfg.SemanticDelivered == nil {
		return errors.New("F74a migration requires marker, source, publisher, outbox count and semantic delivery proof")
	}
	p, err := r.load()
	if err != nil || p.Completed {
		return err
	}
	if err = r.runPass(ctx); err != nil {
		r.setError(err)
		return err
	}
	return nil
}

func (r *F74aBackfillRunner) Run(ctx context.Context) error {
	if r == nil || r.cfg.Marker == nil || r.cfg.Source == nil || r.cfg.Publisher == nil || r.cfg.Pending == nil {
		return errors.New("F74a runner requires marker, source, publisher and outbox count")
	}
	if r.cfg.Ready != nil {
		select {
		case <-r.cfg.Ready:
		case <-ctx.Done():
			return nil
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		p, err := r.load()
		if err == nil && !p.Completed {
			err = r.runPass(ctx)
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.setError(err)
			r.mu.Lock()
			r.snapshot.Retries++
			r.mu.Unlock()
			if !waitF74a(ctx, backoff) {
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		}
	}
	return nil
}
func waitF74a(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *F74aBackfillRunner) admit(ctx context.Context, last *time.Time, paused *bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending, err := r.cfg.Pending(ctx)
		if err == nil {
			if *paused {
				if pending < r.cfg.LowWater {
					*paused = false
				}
			} else if pending >= r.cfg.HighWater {
				*paused = true
			}
		}
		r.setAdmission(pending, *paused, err)
		if err == nil && !*paused {
			break
		}
		if !waitF74a(ctx, time.Second) {
			return ctx.Err()
		}
	}
	interval := time.Second / time.Duration(r.cfg.Rate)
	if interval > 0 && !last.IsZero() {
		if delay := time.Until(last.Add(interval)); delay > 0 && !waitF74a(ctx, delay) {
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*last = time.Now()
	return nil
}
func (r *F74aBackfillRunner) runPass(ctx context.Context) error {
	var last time.Time
	paused := false
	for ctx.Err() == nil {
		p, err := r.load()
		if err != nil {
			return err
		}
		if p.Completed {
			return nil
		}
		if p.Phase == "complete" {
			if err := r.verifySemanticPackages(ctx); err != nil {
				return err
			}
			next, err := r.update(func(current *F74aBackfillProgress) error {
				if current.Phase != "complete" {
					return nil
				}
				if current.Generation != current.PassGeneration {
					current.Phase = f74aPhases[0]
					current.Cursor = uuid.Nil
					current.StateCursor = repository.F74aStateCursor{}
					current.PassGeneration = current.Generation
					return nil
				}
				current.Completed = true
				current.RelayVerified = true
				return nil
			})
			if err != nil {
				return err
			}
			if next.Completed {
				return nil
			}
			continue
		}
		n, err := r.page(ctx, p, &last, &paused)
		if err != nil {
			return err
		}
		if n < f74aBackfillPageSize {
			_, err = r.update(func(current *F74aBackfillProgress) error {
				if current.Phase != p.Phase {
					return errors.New("F74a phase changed during page")
				}
				for i, phase := range f74aPhases {
					if phase == p.Phase {
						current.Phase = f74aPhases[i+1]
						break
					}
				}
				current.Cursor = uuid.Nil
				current.StateCursor = repository.F74aStateCursor{}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
func (r *F74aBackfillRunner) page(ctx context.Context, p F74aBackfillProgress, last *time.Time, paused *bool) (int, error) {
	publish := func(id uuid.UUID, state repository.F74aStateCursor, fn func() error) error {
		if err := r.admit(ctx, last, paused); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
		_, err := r.update(func(current *F74aBackfillProgress) error {
			if current.Phase != p.Phase {
				return errors.New("F74a phase changed during publish")
			}
			current.Cursor = id
			current.StateCursor = state
			return nil
		})
		if err == nil {
			r.recordVisit()
		}
		return err
	}
	switch p.Phase {
	case "releases":
		items, err := r.cfg.Source.ListReleasesAfter(ctx, p.Cursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			if err := publish(item.ID, repository.F74aStateCursor{}, func() error { return r.cfg.Publisher.PublishLLMRelease(ctx, item) }); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	case "signatures":
		items, err := r.cfg.Source.ListSignaturesAfter(ctx, p.Cursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			if err := publish(item.ID, repository.F74aStateCursor{}, func() error { return r.cfg.Publisher.PublishArtifactSignature(ctx, item) }); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	case "sboms":
		items, err := r.cfg.Source.ListSBOMsAfter(ctx, p.Cursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			if err := publish(item.ID, repository.F74aStateCursor{}, func() error { return r.cfg.Publisher.PublishArtifactSBOM(ctx, item) }); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	case "semantic_packages":
		items, err := r.cfg.Source.ListSemanticPackagesAfter(ctx, p.Cursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			if err := publish(item.ID, repository.F74aStateCursor{}, func() error { return r.cfg.Publisher.PublishSBOMPackage(ctx, item) }); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	case "legacy_packages":
		items, err := r.cfg.Source.ListLegacyPackagesAfter(ctx, p.Cursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			if err := publish(item.ID, repository.F74aStateCursor{}, func() error {
				if err := r.requireSemanticDelivery(ctx, item); err != nil {
					return err
				}
				return r.cfg.Publisher.PublishLegacySBOMPackageTombstone(ctx, item)
			}); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	case "observations":
		items, err := r.cfg.Source.ListLinkedObservationsAfter(ctx, p.StateCursor, f74aBackfillPageSize)
		if err != nil {
			return 0, err
		}
		for i := range items {
			item := &items[i]
			state := repository.F74aStateCursor{ServiceID: item.ServiceID, EnvironmentID: item.EnvironmentID}
			if err := publish(uuid.Nil, state, func() error { return r.cfg.Publisher.PublishRuntimeObservation(ctx, item) }); err != nil {
				return len(items), err
			}
		}
		return len(items), nil
	default:
		return 0, fmt.Errorf("invalid F74a phase %q", p.Phase)
	}
}

// requireSemanticDelivery never treats local staging, a pending attempt, or a
// late OK as a relay-visible replacement for a legacy UUID coordinate.
func (r *F74aBackfillRunner) requireSemanticDelivery(ctx context.Context, pkg *domain.SBOMPackage) error {
	if r.cfg.SemanticDelivered == nil {
		return errors.New("F74a semantic relay delivery proof is not configured")
	}
	accepted, err := r.cfg.SemanticDelivered(ctx, pkg)
	if err != nil {
		return err
	}
	if !accepted {
		return fmt.Errorf("F74a semantic package %s lacks relay delivery proof", pkg.ID)
	}
	return nil
}

// verifySemanticPackages also covers migrations without legacy UUID records.
// A pruned outbox and lost cache cannot establish completion without an EOSE
// relay sync, so an absent proof fails closed.
func (r *F74aBackfillRunner) verifySemanticPackages(ctx context.Context) error {
	var cursor uuid.UUID
	for {
		items, err := r.cfg.Source.ListSemanticPackagesAfter(ctx, cursor, f74aBackfillPageSize)
		if err != nil {
			return err
		}
		for i := range items {
			if err := r.requireSemanticDelivery(ctx, &items[i]); err != nil {
				return err
			}
			cursor = items[i].ID
		}
		if len(items) < f74aBackfillPageSize {
			return nil
		}
	}
}
