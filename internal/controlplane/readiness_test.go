package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadinessTracker_VacuouslyReadyWithNoFilters(t *testing.T) {
	r := NewReadinessTracker()
	assert.True(t, r.IsReady())
}

func TestReadinessTracker_NotReadyUntilAllFiltersReady(t *testing.T) {
	r := NewReadinessTracker()
	r.RegisterFilter("intent-30900")
	r.RegisterFilter("encrypted-1059")

	assert.False(t, r.IsReady())

	r.MarkFilterReady("intent-30900")
	assert.False(t, r.IsReady(), "not ready until all filters are ready")

	r.MarkFilterReady("encrypted-1059")
	assert.True(t, r.IsReady())
}

func TestReadinessTracker_Progress(t *testing.T) {
	r := NewReadinessTracker()
	r.RegisterFilter("intent-30900")

	p := r.Progress()
	assert.False(t, p.Ready)
	assert.Equal(t, false, p.Filters["intent-30900"])

	r.MarkFilterReady("intent-30900")
	p = r.Progress()
	assert.True(t, p.Ready)
	assert.Equal(t, true, p.Filters["intent-30900"])
}

func TestReadinessTracker_IdempotentMark(t *testing.T) {
	r := NewReadinessTracker()
	r.RegisterFilter("test")
	r.MarkFilterReady("test")
	r.MarkFilterReady("test") // idempotent
	assert.True(t, r.IsReady())
}
