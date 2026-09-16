package loom

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

func TestSubmitJobCreatesSpanAndStampsTraceparent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	pool := &submitRelayPool{}
	client := &Client{
		pool: pool, privateKey: nostrutil.GeneratePrivateKeyHex(),
		submittedWorkers: make(map[string]string), logger: zap.NewNop(),
	}
	if _, err := client.SubmitJob(t.Context(), JobRequest{Type: "build", Service: "api"}); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if len(pool.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(pool.published))
	}
	traceparent := getTagValue(pool.published[0].Tags, "traceparent")
	if traceparent == "" {
		t.Fatalf("kind-5100 tags missing traceparent: %#v", pool.published[0].Tags)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "bahia.loom.dispatch" {
		t.Fatalf("ended spans = %#v, want one Loom dispatch span", spans)
	}
	if got := traceparent[3:35]; got != spans[0].SpanContext().TraceID().String() {
		t.Fatalf("traceparent trace id = %s, span trace id = %s", got, spans[0].SpanContext().TraceID())
	}
}
