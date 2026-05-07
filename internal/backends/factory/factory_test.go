package factory

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestBuildBackendRejectsUnwiredSecretRefs(t *testing.T) {
	_, err := BuildBackend(config.PackageBackendConfig{Type: "nexus", BaseURL: "https://nexus.example.com", AuthSecretRef: "secret/nexus"})
	if err == nil || !strings.Contains(err.Error(), "secrets resolver") {
		t.Fatalf("expected explicit secret resolver error, got %v", err)
	}
}
