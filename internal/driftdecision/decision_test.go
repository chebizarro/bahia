package driftdecision

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLogUsesConsistentTrimmedPrefixes(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	input := LogInput{
		Service: "api", Environment: "prod", ServiceID: uuid.New(), EnvironmentID: uuid.New(),
		Status: domain.DriftStatusDrifted, PreviousStatus: domain.DriftStatusInSync, Branch: "digest-fallback",
		DesiredHash: "  sha256:desired-state  ", ObservedHash: "  sha256:observed-state  ",
		DesiredDigest:  "  sha256:" + strings.Repeat("a", 64) + "  ",
		ObservedDigest: "  sha256:" + strings.Repeat("b", 64) + "  ",
		Health:         domain.HealthStatusStopped, ObservationID: uuid.New(), Source: "docker",
	}
	Log(zap.New(core), input)
	entries := logs.FilterMessage("runtime drift decision changed from in_sync").All()
	if len(entries) != 1 || entries[0].Level != zap.WarnLevel {
		t.Fatalf("warn decision logs = %#v", entries)
	}
	fields := entries[0].ContextMap()
	for field, want := range map[string]any{
		"desired_hash_prefix": "sha256:desir", "observed_hash_prefix": "sha256:obser",
		"desired_digest_prefix": "sha256:aaaaa", "observed_digest_prefix": "sha256:bbbbb",
		"desired_hash_present": true, "observed_hash_present": true,
	} {
		if fields[field] != want {
			t.Fatalf("%s = %#v, want %#v; fields=%#v", field, fields[field], want, fields)
		}
	}
}
