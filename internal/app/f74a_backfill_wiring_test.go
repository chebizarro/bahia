package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestF74aPublisherFailureBridgeReopensCompletedV2(t *testing.T) {
	store, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	complete := service.F74aBackfillProgress{Phase: "complete", Completed: true, Generation: 7, PassGeneration: 7}
	raw, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutControlRecord("bootstrap", "f74a-canonical-v2", raw); err != nil {
		t.Fatal(err)
	}
	runner := service.NewF74aBackfillRunner(service.F74aBackfillConfig{Marker: store})
	bridge := &f74aDirtyMarkerBridge{runner: runner}
	if err := bridge.PutControlRecord("bootstrap", "f74a-canonical-v1", []byte("dirty")); err != nil {
		t.Fatal(err)
	}
	raw, err = store.GetControlRecord("bootstrap", "f74a-canonical-v2")
	if err != nil {
		t.Fatal(err)
	}
	var progress service.F74aBackfillProgress
	if err := json.Unmarshal(raw, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Completed || progress.Phase != "releases" || progress.Generation != 8 {
		t.Fatalf("failure left v2 completed: %+v", progress)
	}
}
func TestF74aBackfillHealthCompletionMeansStagedNotRelayAck(t *testing.T) {
	provider := NewHealthProvider(nil, nil)
	runner := service.NewF74aBackfillRunner(service.F74aBackfillConfig{})
	registerF74aBackfillHealthCheck(provider, runner)
	check := provider.Readiness().Checks[len(provider.Readiness().Checks)-1]
	if check.Status != HealthStatusWarn || check.Details["completed"] != "false" {
		t.Fatalf("pending check %+v", check)
	}
}
