package telemetry

import (
	"testing"

	"go.opentelemetry.io/otel/trace"
)

const testReleaseTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestOCIManifestDigestAttributeMatchesLoomWorker(t *testing.T) {
	// loom-worker src/telemetry/job-lifecycle.ts:
	//   export const OCI_MANIFEST_DIGEST_ATTRIBUTE = "oci.manifest.digest";
	// The two must stay byte-identical for the build span and the promotion
	// span to join on the artifact.
	if OCIManifestDigestAttribute != "oci.manifest.digest" {
		t.Fatalf("OCIManifestDigestAttribute = %q, want oci.manifest.digest", OCIManifestDigestAttribute)
	}
	if got := OCIManifestDigest("  sha256:abc  "); got.Value.AsString() != "sha256:abc" {
		t.Fatalf("OCIManifestDigest trimmed value = %q, want sha256:abc", got.Value.AsString())
	}
}

func TestExtractTraceContextFromSignedEvent(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantTrace string
		wantState string
	}{
		{
			name: "stamped 5402 continues the trace",
			raw: `{"kind":5402,"tags":[["traceparent","00-` + testReleaseTraceID +
				`-00f067aa0ba902b7-01"],["tracestate","loom=worker-01"]]}`,
			wantTrace: testReleaseTraceID,
			wantState: "worker-01",
		},
		{name: "unstamped event is ignored", raw: `{"kind":5402,"tags":[["e","abc"]]}`},
		{name: "malformed traceparent is ignored", raw: `{"kind":5402,"tags":[["traceparent","nope"]]}`},
		{name: "malformed json is ignored", raw: `{`},
		{name: "empty event is ignored", raw: "   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ExtractTraceContextFromSignedEvent(t.Context(), tc.raw)
			sc := trace.SpanContextFromContext(ctx)
			if tc.wantTrace == "" {
				if sc.IsValid() {
					t.Fatalf("span context = %#v, want none", sc)
				}
				return
			}
			if got := sc.TraceID().String(); got != tc.wantTrace {
				t.Fatalf("trace id = %s, want %s", got, tc.wantTrace)
			}
			if got := sc.TraceState().Get("loom"); got != tc.wantState {
				t.Fatalf("tracestate loom = %q, want %q", got, tc.wantState)
			}
		})
	}
}

func TestTraceContextMetadataRoundTrip(t *testing.T) {
	ctx := ExtractTraceContextFromSignedEvent(t.Context(),
		`{"tags":[["traceparent","00-`+testReleaseTraceID+`-00f067aa0ba902b7-01"],["tracestate","loom=worker-01"]]}`)

	carrier := TraceContextMetadata(ctx)
	if carrier["traceparent"] == "" {
		t.Fatalf("TraceContextMetadata = %#v, want a traceparent", carrier)
	}
	if carrier["tracestate"] != "loom=worker-01" {
		t.Fatalf("TraceContextMetadata tracestate = %q, want loom=worker-01", carrier["tracestate"])
	}

	metadata := map[string]any{"image_digest": "sha256:abc"}
	for key, value := range carrier {
		metadata[key] = value
	}
	restored := trace.SpanContextFromContext(ExtractTraceContextFromMetadata(t.Context(), metadata))
	if got := restored.TraceID().String(); got != testReleaseTraceID {
		t.Fatalf("restored trace id = %s, want %s", got, testReleaseTraceID)
	}
}

func TestTraceContextMetadataIsNilWithoutContext(t *testing.T) {
	if got := TraceContextMetadata(t.Context()); got != nil {
		t.Fatalf("TraceContextMetadata = %#v, want nil without an active trace", got)
	}
	ctx := ExtractTraceContextFromMetadata(t.Context(), map[string]any{"image_digest": "sha256:abc"})
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatalf("metadata without trace fields must not produce a span context")
	}
}
