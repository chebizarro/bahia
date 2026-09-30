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
			"bahia_relay_sidecar_subscriber_queue_size %d\n",
		s.fanout.OverflowCloses(), s.fanout.subscriberQueueSize()); err != nil {
		return fmt.Errorf("write relay sidecar metrics: %w", err)
	}
	return nil
}

func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.WritePrometheus(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
