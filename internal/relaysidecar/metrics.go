package relaysidecar

import (
	"fmt"
	"io"
	"net/http"
)

// metricsPath serves the sidecar's own Prometheus text exposition. Like
// internal/soulfactory/saga, the sidecar runs outside the daemon's
// internal/adapters/telemetry registry, so it exposes its metrics itself.
const metricsPath = "/metrics"

// WritePrometheus writes the sidecar's Prometheus text exposition.
func (s *Server) WritePrometheus(w io.Writer) error {
	if _, err := fmt.Fprintf(w,
		"# HELP bahia_relay_sidecar_subscription_overflow_closes_total Subscriptions CLOSED because their connection's live delivery queue overflowed\n"+
			"# TYPE bahia_relay_sidecar_subscription_overflow_closes_total counter\n"+
			"bahia_relay_sidecar_subscription_overflow_closes_total %d\n"+
			"# HELP bahia_relay_sidecar_subscriber_queue_size Configured per-connection live delivery queue size\n"+
			"# TYPE bahia_relay_sidecar_subscriber_queue_size gauge\n"+
			"bahia_relay_sidecar_subscriber_queue_size %d\n"+
			"# HELP bahia_relay_sidecar_retention_deleted_events_total Events deleted by retention sweeps, by cause\n"+
			"# TYPE bahia_relay_sidecar_retention_deleted_events_total counter\n"+
			"bahia_relay_sidecar_retention_deleted_events_total{cause=\"nip40_expired\"} %d\n"+
			"bahia_relay_sidecar_retention_deleted_events_total{cause=\"request_retention\"} %d\n"+
			"bahia_relay_sidecar_retention_deleted_events_total{cause=\"event_retention\"} %d\n",
		s.fanout.OverflowCloses(), s.fanout.subscriberQueueSize(),
		s.swept.expired.Load(), s.swept.request.Load(), s.swept.regular.Load()); err != nil {
		return fmt.Errorf("write relay sidecar metrics: %w", err)
	}
	if s.mirror != nil {
		status, err := s.mirror.Status()
		if err != nil {
			return fmt.Errorf("read relay sidecar mirror metrics: %w", err)
		}
		if _, err := fmt.Fprint(w,
			"# HELP bahia_relay_sidecar_mirror_backlog_events Accepted events not yet acknowledged by an external relay\n"+
				"# TYPE bahia_relay_sidecar_mirror_backlog_events gauge\n"+
				"# HELP bahia_relay_sidecar_mirror_skipped_events_total Stored events unavailable or refused by current posting policy\n"+
				"# TYPE bahia_relay_sidecar_mirror_skipped_events_total counter\n"+
				"# HELP bahia_relay_sidecar_mirror_failure Mirror destination currently failing (1) or healthy (0)\n"+
				"# TYPE bahia_relay_sidecar_mirror_failure gauge\n"); err != nil {
			return err
		}
		for _, relay := range status {
			failed := 0
			if relay.Failure != "" {
				failed = 1
			}
			if _, err := fmt.Fprintf(w,
				"bahia_relay_sidecar_mirror_backlog_events{relay=%q} %d\n"+
					"bahia_relay_sidecar_mirror_skipped_events_total{relay=%q} %d\n"+
					"bahia_relay_sidecar_mirror_failure{relay=%q} %d\n",
				relay.RelayURL, relay.Backlog, relay.RelayURL, relay.Skipped, relay.RelayURL, failed); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.WritePrometheus(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
