package controlplane

import (
	"testing"

	"fiatjaf.com/nostr"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestContextVMDispatchCreatesSpanAndStampsTraceparent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	traceID, _ := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	spanID, _ := trace.SpanIDFromHex("0011223344556677")
	traceState, _ := trace.ParseTraceState("vendor=state")
	ctx := trace.ContextWithRemoteSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, TraceState: traceState, Remote: true,
	}))
	event, _, _, err := publishContextVMCommand(
		ctx, capture, signer, "service/deploy", "otel-contextvm-1", "", nil,
		map[string]any{"service_id": "svc"}, "test",
	)
	if err != nil {
		t.Fatalf("publish ContextVM command: %v", err)
	}
	traceparent := tagValueNostr(event.Tags, "traceparent")
	if traceparent == "" {
		t.Fatalf("kind-25910 tags missing traceparent: %#v", event.Tags)
	}
	if got := tagValueNostr(event.Tags, "tracestate"); got != "vendor=state" {
		t.Fatalf("kind-25910 tracestate = %q, want vendor=state", got)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "bahia.contextvm.dispatch" {
		t.Fatalf("ended spans = %#v, want one ContextVM dispatch span", spans)
	}
	if !spans[0].SpanContext().IsValid() || !spans[0].SpanContext().IsSampled() {
		t.Fatalf("dispatch span context is invalid: %v", spans[0].SpanContext())
	}
	if got := traceparent[3:35]; got != spans[0].SpanContext().TraceID().String() {
		t.Fatalf("traceparent trace id = %s, span trace id = %s", got, spans[0].SpanContext().TraceID())
	}
}
