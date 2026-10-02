package controlplane

import (
	"sync"
)

// ReadinessTracker tracks whether the daemon's intent subscription filters
// have completed their first catch-up. Readiness means: for every required
// filter, at least one relay has delivered EOSE and reconciliation has
// completed.
//
// This is additive to the existing tier model (ModePolicy/bootstrapper): the
// readiness endpoint can check both. The tier model is deleted in Wave 6 (T1)
// when all domains have migrated.
//
// See design §6.2.
type ReadinessTracker struct {
	mu      sync.RWMutex
	filters map[string]bool // filter-key → ready
}

// NewReadinessTracker creates a tracker. Register filters with RegisterFilter
// before any MarkFilterReady calls.
func NewReadinessTracker() *ReadinessTracker {
	return &ReadinessTracker{
		filters: make(map[string]bool),
	}
}

// RegisterFilter declares a filter that must be ready before the tracker
// reports overall readiness.
func (r *ReadinessTracker) RegisterFilter(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.filters[key]; !ok {
		r.filters[key] = false
	}
}

// MarkFilterReady records that a filter's first catch-up is complete.
func (r *ReadinessTracker) MarkFilterReady(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.filters[key] = true
}

// IsReady reports whether all registered filters have caught up. Returns true
// when no filters are registered (vacuously ready — no intent domains are
// enabled).
func (r *ReadinessTracker) IsReady() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ready := range r.filters {
		if !ready {
			return false
		}
	}
	return true
}

// Progress returns the readiness state for health endpoints.
func (r *ReadinessTracker) Progress() ReadinessProgress {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := ReadinessProgress{
		Ready:   true,
		Filters: make(map[string]bool, len(r.filters)),
	}
	for key, ready := range r.filters {
		p.Filters[key] = ready
		if !ready {
			p.Ready = false
		}
	}
	return p
}

// ReadinessProgress is the serializable readiness state.
type ReadinessProgress struct {
	Ready   bool            `json:"ready"`
	Filters map[string]bool `json:"filters"`
}
