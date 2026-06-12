package nostr

import (
	"context"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
)

const defaultReplayCursorOverlap = time.Second

// CursorSource provides the latest event timestamp available for a replay cursor.
type CursorSource interface {
	LatestEventTimestamp(ctx context.Context, kinds []int) (time.Time, error)
}

// ReplayCursorPlanner computes replay cursors from one or more timestamp sources.
type ReplayCursorPlanner struct {
	sources []CursorSource
	overlap time.Duration
}

// NewReplayCursorPlanner creates a cursor planner. A non-positive overlap uses the default one-second overlap.
func NewReplayCursorPlanner(overlap time.Duration, sources ...CursorSource) *ReplayCursorPlanner {
	if overlap <= 0 {
		overlap = defaultReplayCursorOverlap
	}
	return &ReplayCursorPlanner{
		sources: append([]CursorSource(nil), sources...),
		overlap: overlap,
	}
}

// ComputeSince returns the newest source timestamp minus overlap, or nil when no source has data.
func (p *ReplayCursorPlanner) ComputeSince(ctx context.Context, kinds []int) *gonostr.Timestamp {
	if p == nil {
		return nil
	}

	var latest time.Time
	for _, source := range p.sources {
		if source == nil {
			continue
		}
		timestamp, err := source.LatestEventTimestamp(ctx, kinds)
		if err != nil || timestamp.IsZero() {
			continue
		}
		if latest.IsZero() || timestamp.After(latest) {
			latest = timestamp
		}
	}
	if latest.IsZero() {
		return nil
	}

	since := latest.Add(-p.overlap).Unix()
	timestamp := gonostr.Timestamp(since)
	return &timestamp
}

// NostrEventRepositoryCursorSource adapts repository.NostrEventRepository to CursorSource.
type NostrEventRepositoryCursorSource struct {
	repo repository.NostrEventRepository
}

// NewNostrEventRepositoryCursorSource creates a cursor source backed by a NostrEventRepository.
func NewNostrEventRepositoryCursorSource(repo repository.NostrEventRepository) *NostrEventRepositoryCursorSource {
	return &NostrEventRepositoryCursorSource{repo: repo}
}

// LatestEventTimestamp returns the newest created_at timestamp for the requested kinds.
func (s *NostrEventRepositoryCursorSource) LatestEventTimestamp(ctx context.Context, kinds []int) (time.Time, error) {
	if s == nil || s.repo == nil {
		return time.Time{}, nil
	}
	latest, err := s.repo.LatestCreatedAtForKinds(ctx, kinds)
	if err != nil || latest == nil {
		return time.Time{}, err
	}
	return *latest, nil
}
