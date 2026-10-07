package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// promotionTraceID is the trace id an upstream 5402 carries into Bahia.
const promotionTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// promotionTestSignedResult renders the ingested terminal 5402 exactly as
// Bahia persists it (domain.HiveCIAcceptedRelease.SignedEvent), carrying the
// W3C trace context loom-worker stamped on the result it signed.
func promotionTestSignedResult(traceparent string) string {
	return `{"kind":5402,"id":"` + promotionTestDigest("d")[7:] + `","tags":[["traceparent","` + traceparent +
		`"],["tracestate","loom=worker-01"]],"content":"{}"}`
}

func promotionTestTraceparent() string {
	return "00-" + promotionTraceID + "-00f067aa0ba902b7-01"
}

func newPromotionSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	return recorder
}

func newPromotionOTelFixture(t *testing.T) (*AgentRuntimePromotionService, *recordingPromotionIntentSink, uuid.UUID) {
	t.Helper()
	orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{
		serviceID: {ID: serviceID, OrgID: orgID, Name: "agent-scout"},
	}}
	sink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, environmentID)}},
		sink,
	)
	return svc, sink, orgID
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

func TestRuntimePromotionCreatesSpan(t *testing.T) {
	recorder := newPromotionSpanRecorder(t)
	svc, _, orgID := newPromotionOTelFixture(t)

	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "bahia.release.promotion" {
		t.Fatalf("ended spans = %#v, want one promotion span", spans)
	}
}

// TestRuntimePromotionContinuesTraceFromSignedResult is the release-spine
// stitch: the promotion span must land in the trace the 5402 carried, not in a
// fresh trace of its own.
func TestRuntimePromotionContinuesTraceFromSignedResult(t *testing.T) {
	recorder := newPromotionSpanRecorder(t)
	svc, _, orgID := newPromotionOTelFixture(t)

	release := promotionTestAcceptedRelease()
	release.SignedEvent = promotionTestSignedResult(promotionTestTraceparent())

	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: release,
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if got := span.SpanContext().TraceID().String(); got != promotionTraceID {
		t.Fatalf("promotion trace id = %s, want %s (trace did not continue from the 5402)", got, promotionTraceID)
	}
	if parent := span.Parent(); !parent.IsRemote() || !parent.IsValid() {
		t.Fatalf("promotion span parent = %#v, want the remote 5402 span context", parent)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Fatalf("promotion parent span id = %s, want the 5402 traceparent span id", got)
	}
	if got := span.SpanContext().TraceState().Get("loom"); got != "worker-01" {
		t.Fatalf("promotion tracestate loom = %q, want worker-01", got)
	}
}

// TestRuntimePromotionSpanCarriesManifestDigest pins the artifact join key. The
// attribute name must stay identical to loom-worker's
// OCI_MANIFEST_DIGEST_ATTRIBUTE or the build span and promotion span no longer
// join on the artifact.
func TestRuntimePromotionSpanCarriesManifestDigest(t *testing.T) {
	recorder := newPromotionSpanRecorder(t)
	svc, _, orgID := newPromotionOTelFixture(t)

	release := promotionTestAcceptedRelease()
	release.SignedEvent = promotionTestSignedResult(promotionTestTraceparent())
	want := release.Result.Manifest.Digest

	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: release,
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if telemetry.OCIManifestDigestAttribute != "oci.manifest.digest" {
		t.Fatalf("manifest digest attribute = %s, want loom-worker's oci.manifest.digest",
			telemetry.OCIManifestDigestAttribute)
	}
	got, ok := spanAttribute(spans[0], telemetry.OCIManifestDigestAttribute)
	if !ok {
		t.Fatalf("promotion span attributes = %#v, want %s", spans[0].Attributes(), telemetry.OCIManifestDigestAttribute)
	}
	if got != want {
		t.Fatalf("%s = %s, want the promoted manifest digest %s", telemetry.OCIManifestDigestAttribute, got, want)
	}
}

// TestRuntimePromotionIntentCarriesTraceContext proves the durable hand-off a
// later rollback depends on.
func TestRuntimePromotionIntentCarriesTraceContext(t *testing.T) {
	newPromotionSpanRecorder(t)
	svc, sink, orgID := newPromotionOTelFixture(t)

	release := promotionTestAcceptedRelease()
	release.SignedEvent = promotionTestSignedResult(promotionTestTraceparent())

	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: release,
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}
	if len(sink.intents) != 1 {
		t.Fatalf("submitted intents = %d, want 1", len(sink.intents))
	}
	traceparent, _ := sink.intents[0].Metadata["traceparent"].(string)
	if !strings.Contains(traceparent, promotionTraceID) {
		t.Fatalf("intent traceparent = %q, want the %s trace", traceparent, promotionTraceID)
	}
	if state, _ := sink.intents[0].Metadata["tracestate"].(string); state != "loom=worker-01" {
		t.Fatalf("intent tracestate = %q, want loom=worker-01", state)
	}
	if digest, _ := sink.intents[0].Metadata["image_digest"].(string); digest != release.Result.Manifest.Digest {
		t.Fatalf("intent image_digest = %q, want %q", digest, release.Result.Manifest.Digest)
	}
}

// TestRuntimePromotionWithoutTraceContextStartsNewTrace keeps the change
// additive: an unstamped 5402 must not break promotion.
func TestRuntimePromotionWithoutTraceContextStartsNewTrace(t *testing.T) {
	recorder := newPromotionSpanRecorder(t)
	svc, sink, orgID := newPromotionOTelFixture(t)

	release := promotionTestAcceptedRelease()
	release.SignedEvent = `{"kind":5402,"tags":[["e","abc"]],"content":"{}"}`

	if _, err := svc.PromoteSubscribedSouls(t.Context(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: release,
	}); err != nil {
		t.Fatalf("PromoteSubscribedSouls: %v", err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Fatalf("promotion span parent = %#v, want a root span", spans[0].Parent())
	}
	if _, ok := sink.intents[0].Metadata["traceparent"]; ok {
		// A local (non-remote) trace context is still valid to carry forward,
		// so only assert that nothing bogus was written.
		if tp, _ := sink.intents[0].Metadata["traceparent"].(string); strings.Contains(tp, promotionTraceID) {
			t.Fatalf("intent traceparent = %q, want no upstream trace", tp)
		}
	}
}
