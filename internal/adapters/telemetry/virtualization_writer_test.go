package telemetry

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type failingVirtualizationWriter struct {
	*httptest.ResponseRecorder
	prefix             string
	err                error
	failed             bool
	writesAfterFailure int
}

func (w *failingVirtualizationWriter) Write(b []byte) (int, error) {
	if w.failed {
		w.writesAfterFailure++
		return 0, w.err
	}
	if strings.HasPrefix(string(b), w.prefix) {
		w.failed = true
		return 0, w.err
	}
	return w.ResponseRecorder.Write(b)
}

func TestVirtualizationRenderStopsOnWriteFailure(t *testing.T) {
	for _, prefix := range []string{"# TYPE", "bahia_virtualization_capacity{", "bahia_virtualization_operation_duration_seconds_sum{"} {
		t.Run(prefix, func(t *testing.T) {
			m := NewMetrics()
			m.recordVirtualization("capacity", VirtualizationLabels{}, 2, "gauge")
			m.recordVirtualization("operation_duration_seconds", VirtualizationLabels{}, 3, "histogram")
			failure := errors.New("scrape disconnected")
			w := &failingVirtualizationWriter{ResponseRecorder: httptest.NewRecorder(), prefix: prefix, err: failure}
			if err := m.renderVirtualization(w); !errors.Is(err, failure) {
				t.Fatalf("write failure was swallowed: %v", err)
			}
			if !w.failed || w.writesAfterFailure != 0 {
				t.Fatal("renderer continued after a failed write")
			}
		})
	}
}

func TestMetricsHandlerStopsAfterVirtualizationWriteFailure(t *testing.T) {
	p := Setup(Config{}, zap.NewNop())
	defer func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	w := &failingVirtualizationWriter{ResponseRecorder: httptest.NewRecorder(), prefix: "# TYPE", err: errors.New("scrape disconnected")}
	p.MetricsHandler()(w, httptest.NewRequest("GET", "/metrics", nil))
	if !w.failed || w.writesAfterFailure != 0 {
		t.Fatalf("handler continued after a failed write: %d further writes", w.writesAfterFailure)
	}
}
