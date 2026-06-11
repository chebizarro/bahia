package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

func TestRunReadsInvocationAndWritesOutcome(t *testing.T) {
	root := t.TempDir()
	invocation := soulfactory.OpenClawControlInvocation{
		Envelope: soulfactory.RuntimeControlEnvelope{
			Schema:         "soulfactory-runtime-control/v1",
			Method:         soulfactory.RuntimeMethodProvision,
			IdempotencyKey: "idem-cli",
			RequestedAt:    1715700000,
			Operator:       soulfactory.RuntimeOperatorRef{Pubkey: "operator", RequestEvent: "operator-request"},
			Controller:     soulfactory.RuntimeControllerRef{Pubkey: "controller"},
			Target:         soulfactory.RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime", AgentID: "agent-cli"},
			Soul:           soulfactory.RuntimeSoulRef{ID: "soul-cli", SpecHash: "sha256:cli"},
			Params:         cliProvisionParams(),
		},
		Method:   soulfactory.RuntimeMethodProvision,
		AgentID:  "agent-cli",
		SoulID:   "soul-cli",
		SpecHash: "sha256:cli",
		Params:   cliProvisionParams(),
	}
	payload, err := json.Marshal(invocation)
	if err != nil {
		t.Fatalf("marshal invocation: %v", err)
	}
	var stdout bytes.Buffer
	code := run(context.Background(), bytes.NewReader(payload), &stdout, func(key string) string {
		switch key {
		case "OPENCLAW_SOULFACTORY_ROOT":
			return root
		case "OPENCLAW_SOULFACTORY_DRY_RUN":
			return "1"
		case "OPENCLAW_SOULFACTORY_RUNTIME_MODE":
			return "existing-container"
		case "OPENCLAW_SOULFACTORY_CONTAINER":
			return "openclaw-gateway"
		default:
			return ""
		}
	})
	if code != 0 {
		t.Fatalf("run exit code = %d", code)
	}
	var outcome soulfactory.OpenClawControlOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v", stdout.String(), err)
	}
	if outcome.Status != "success" || outcome.Result["agent_id"] != "agent-cli" || outcome.Result["state"] != "running" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if workspace, ok := outcome.Result["workspace"].(string); !ok || workspace != filepath.Join(root, "agents", "agent-cli", "workspace") {
		t.Fatalf("workspace result = %#v", outcome.Result["workspace"])
	}
}

func TestRunRejectsTrailingJSONDocument(t *testing.T) {
	root := t.TempDir()
	payload := []byte(`{"method":"soulfactory.provision","agent_id":"agent-cli","soul_id":"soul-cli","spec_hash":"sha256:cli","params":{"identity":{},"runtime":{},"permissions":{},"relay_policy":{},"workspace":{},"assets":{}}} {}`)
	var stdout bytes.Buffer
	code := run(context.Background(), bytes.NewReader(payload), &stdout, func(key string) string {
		if key == "OPENCLAW_SOULFACTORY_ROOT" {
			return root
		}
		if key == "OPENCLAW_SOULFACTORY_DRY_RUN" {
			return "1"
		}
		return ""
	})
	if code != 0 {
		t.Fatalf("run exit code = %d", code)
	}
	var outcome soulfactory.OpenClawControlOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v", stdout.String(), err)
	}
	if outcome.Status != "failed" || outcome.Error == nil || outcome.Error.Code != "execution_failed" {
		t.Fatalf("trailing document outcome = %+v", outcome)
	}
}

func TestRunMalformedJSONReturnsStructuredFailure(t *testing.T) {
	var stdout bytes.Buffer
	code := run(context.Background(), bytes.NewReader([]byte(`{"method"`)), &stdout, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("run exit code = %d", code)
	}
	var outcome soulfactory.OpenClawControlOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v", stdout.String(), err)
	}
	if outcome.Status != "failed" || outcome.Error == nil || outcome.Error.Code != "execution_failed" {
		t.Fatalf("malformed JSON outcome = %+v", outcome)
	}
}

func cliProvisionParams() map[string]interface{} {
	return map[string]interface{}{
		"identity":     map[string]interface{}{"name": "CLI Agent"},
		"runtime":      map[string]interface{}{"target": "openclaw"},
		"permissions":  map[string]interface{}{},
		"relay_policy": map[string]interface{}{},
		"workspace":    map[string]interface{}{},
		"assets":       map[string]interface{}{},
	}
}
