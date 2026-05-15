package soulfactory

import (
	"reflect"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseConfigReloadRequestParsesTargetFieldsPatchAndResolvedSpec(t *testing.T) {
	params := map[string]interface{}{
		"schema":        SoulFactoryConfigReloadSchema,
		"target":        []interface{}{"voice", "memory", "unsupported"},
		"patch":         map[string]interface{}{"voice": map[string]interface{}{"persona_id": "scout-v2"}},
		"resolved_spec": map[string]interface{}{"schema": domain.SoulFactoryDraftSchemaV2, "memory": map[string]interface{}{"embedding_provider": "voyage", "embedding_model": "voyage-3"}},
		"new_spec_hash": "sha256:new",
	}

	req, err := ParseConfigReloadRequest(params)
	if err != nil {
		t.Fatalf("ParseConfigReloadRequest error = %v", err)
	}
	if req.Schema != SoulFactoryConfigReloadSchema || req.NewSpecHash != "sha256:new" {
		t.Fatalf("request metadata = %+v", req)
	}
	if !reflect.DeepEqual(req.TargetFields, []string{"voice", "memory"}) {
		t.Fatalf("target fields = %#v", req.TargetFields)
	}
	if req.ResolvedSpec == nil || req.ResolvedSpec.Memory.EmbeddingProvider != "voyage" {
		t.Fatalf("resolved spec = %+v", req.ResolvedSpec)
	}
}

func TestParseConfigReloadRequestInfersTargetsAndRejectsEmptyPayload(t *testing.T) {
	req, err := ParseConfigReloadRequest(map[string]interface{}{
		"patch": map[string]interface{}{
			"persona": map[string]interface{}{"tone": "friendly"},
			"avatar":  map[string]interface{}{"current": "generated"},
		},
	})
	if err != nil {
		t.Fatalf("ParseConfigReloadRequest inferred targets error = %v", err)
	}
	if !reflect.DeepEqual(req.TargetFields, []string{"avatar", "persona"}) {
		t.Fatalf("inferred target fields = %#v", req.TargetFields)
	}

	if _, err := ParseConfigReloadRequest(map[string]interface{}{}); err == nil {
		t.Fatal("ParseConfigReloadRequest empty payload error = nil")
	}
}

func TestOpenClawSidecarDispatchesConfigReloadAndPublishes38386Result(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{methods: []string{RuntimeMethodConfigReload}, outcome: &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"reloaded": []string{"voice"}, "restart": false}}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodConfigReload, map[string]interface{}{
		"target_fields": []string{"voice"},
		"patch":         map[string]interface{}{"voice": map[string]interface{}{"persona_id": "scout-v2"}},
		"new_spec_hash": "sha256:reload",
	}, nil)

	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent config reload error = %v", err)
	}
	if len(driver.calls) != 1 || driver.calls[0].Method != RuntimeMethodConfigReload {
		t.Fatalf("driver calls = %+v", driver.calls)
	}
	parsed, err := ParseConfigReloadRequest(driver.calls[0].Params)
	if err != nil {
		t.Fatalf("driver params did not parse: %v", err)
	}
	if !reflect.DeepEqual(parsed.TargetFields, []string{"voice"}) {
		t.Fatalf("driver target fields = %#v", parsed.TargetFields)
	}
	if result.Status != "success" || result.Method != RuntimeMethodConfigReload || result.Result["restart"] != false {
		t.Fatalf("unexpected reload result = %+v", result)
	}
	if len(transport.published) != 1 || transport.published[0].Kind != domain.KindRuntimeControlResult {
		t.Fatalf("published events = %+v, want one 38386 result", transport.published)
	}
}
