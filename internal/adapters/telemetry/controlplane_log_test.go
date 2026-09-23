package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

type lifecycleLogExporter struct{ records []sdklog.Record }

func (e *lifecycleLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (*lifecycleLogExporter) Shutdown(context.Context) error   { return nil }
func (*lifecycleLogExporter) ForceFlush(context.Context) error { return nil }

func TestEmitLifecyclePreservesLogContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		outcome  string
		severity otellog.Severity
	}{
		{name: "success", outcome: "success", severity: otellog.SeverityInfo},
		{name: "failure", err: errors.New("private failure detail"), outcome: "failure", severity: otellog.SeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := &lifecycleLogExporter{}
			provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
			previous := global.GetLoggerProvider()
			global.SetLoggerProvider(provider)
			t.Cleanup(func() {
				global.SetLoggerProvider(previous)
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Errorf("shutdown: %v", err)
				}
			})

			EmitLifecycle(context.Background(), "deployment.completed", "success", tc.err,
				attribute.String("service.id", "service-1"), attribute.Bool("managed", true))
			if len(exporter.records) != 1 {
				t.Fatalf("exported %d records, want 1", len(exporter.records))
			}
			record := exporter.records[0]
			if record.EventName() != "deployment.completed" || record.Body().AsString() != "deployment.completed" {
				t.Fatalf("event/body changed: %q / %v", record.EventName(), record.Body())
			}
			if record.Severity() != tc.severity {
				t.Fatalf("severity = %v, want %v", record.Severity(), tc.severity)
			}
			attrs := map[string]string{}
			record.WalkAttributes(func(kv attribute.KeyValue) bool {
				if kv.Value.Type() != attribute.STRING {
					t.Errorf("attribute %s changed type: %v", kv.Key, kv.Value.Type())
				}
				attrs[string(kv.Key)] = kv.Value.AsString()
				return true
			})
			if attrs["outcome"] != tc.outcome || attrs["service.id"] != "service-1" || attrs["managed"] != "true" {
				t.Fatalf("attributes changed: %#v", attrs)
			}
			if tc.err != nil && attrs["error.type"] != "*errors.errorString" {
				t.Fatalf("error type = %q", attrs["error.type"])
			}
			wantAttrs := 3
			if tc.err != nil {
				wantAttrs++
			}
			if len(attrs) != wantAttrs {
				t.Fatalf("unexpected log attributes: %#v", attrs)
			}
		})
	}
}
