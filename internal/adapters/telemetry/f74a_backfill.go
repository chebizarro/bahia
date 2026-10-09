package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// F74aBackfillMetrics describes local staging progress, not relay ACKs.
type F74aBackfillMetrics struct {
	Phase                              string
	Visited, Staged, Failures, Retries uint64
	Pending                            int64
	Paused                             bool
	Generation                         uint64
	Completed                          bool
	UpdatedAt                          time.Time
}

func f74aPhaseIndex(phase string) int64 {
	switch phase {
	case "releases":
		return 0
	case "signatures":
		return 1
	case "sboms":
		return 2
	case "semantic_packages":
		return 3
	case "legacy_packages":
		return 4
	case "observations":
		return 5
	case "complete":
		return 6
	default:
		return -1
	}
}
func (m *Metrics) SetF74aBackfill(s F74aBackfillMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.F74aBackfill = s
	if m.otel == nil {
		return
	}
	ctx := context.Background()
	m.otel.f74aBackfillPhase.Record(ctx, f74aPhaseIndex(s.Phase))
	values := map[string]int64{"visited": int64(s.Visited), "staged": int64(s.Staged), "failures": int64(s.Failures), "retries": int64(s.Retries), "pending_outbox": s.Pending, "dirty_generation": int64(s.Generation), "completed": 0, "paused": 0, "cursor_age_seconds": 0}
	if s.Completed {
		values["completed"] = 1
	}
	if s.Paused {
		values["paused"] = 1
	}
	if !s.UpdatedAt.IsZero() {
		values["cursor_age_seconds"] = int64(time.Since(s.UpdatedAt).Seconds())
	}
	for name, value := range values {
		m.otel.f74aBackfill.Record(ctx, value, metric.WithAttributes(attribute.String("name", name)))
	}
}
func renderF74aBackfillMetrics(w *prometheusWriter, s F74aBackfillMetrics) {
	w.println("# HELP bahia_f74a_backfill_phase Backfill phase index; complete is 6")
	w.println("# TYPE bahia_f74a_backfill_phase gauge")
	w.printf("bahia_f74a_backfill_phase %d\n", f74aPhaseIndex(s.Phase))
	w.println("# HELP bahia_f74a_backfill Local staging progress; staged does not mean relay ACK")
	w.println("# TYPE bahia_f74a_backfill gauge")
	values := map[string]int64{"visited": int64(s.Visited), "staged": int64(s.Staged), "failures": int64(s.Failures), "retries": int64(s.Retries), "pending_outbox": s.Pending, "dirty_generation": int64(s.Generation), "completed": 0, "paused": 0, "cursor_age_seconds": 0}
	if s.Completed {
		values["completed"] = 1
	}
	if s.Paused {
		values["paused"] = 1
	}
	if !s.UpdatedAt.IsZero() {
		values["cursor_age_seconds"] = int64(time.Since(s.UpdatedAt).Seconds())
	}
	for _, name := range []string{"visited", "staged", "failures", "retries", "pending_outbox", "dirty_generation", "completed", "paused", "cursor_age_seconds"} {
		w.printf("bahia_f74a_backfill{name=%q} %d\n", name, values[name])
	}
}
