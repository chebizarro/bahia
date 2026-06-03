package controlplane

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestControlPlaneDoesNotConstructLegacyObservableKinds(t *testing.T) {
	legacyKindConstructor := regexp.MustCompile(`Kind:\s*(KindDNSOperationStatus|KindDNS[A-Za-z]+Result|KindPackageStatus|KindPackageResult|KindPackageDriftEvent|KindPackage[A-Za-z]+Registry|KindWorkerStatus|KindWorkerResult|KindSystemDiscovery|KindWorkerState|KindWorkerAssignmentState|KindWorkerDrainStatus|KindWorkerEligibilityPreview|KindHeartbeatObservation)\b`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read controlplane dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || regexp.MustCompile(`_test\.go$`).MatchString(entry.Name()) {
			continue
		}
		content, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if match := legacyKindConstructor.Find(content); match != nil {
			t.Fatalf("%s constructs a legacy Nostr observable kind directly: %s", entry.Name(), string(match))
		}
	}
}
