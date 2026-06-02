package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestAdoptionTargetValidationRejectsDuplicateTargetsAfterNormalization(t *testing.T) {
	err := validateAdoptionTargets([]dto.AdoptionTargetRequest{
		{Name: "Local", DockerHost: "unix:///docker.sock"},
		{Name: "local", DockerHost: "tcp://docker.example:2376"},
	})
	if err == nil || !strings.Contains(err.Error(), "normalization") {
		t.Fatalf("validateAdoptionTargets() error = %v, want duplicate normalization error", err)
	}
}

func TestAdoptionSelectionValidationRequiresTargetAndContainer(t *testing.T) {
	tests := []struct {
		name       string
		selection  dto.AdoptionSelectionRequest
		wantErrMsg string
	}{
		{name: "target", selection: dto.AdoptionSelectionRequest{ContainerID: "abc123"}, wantErrMsg: "target_name"},
		{name: "container", selection: dto.AdoptionSelectionRequest{TargetName: "local"}, wantErrMsg: "container_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAdoptionSelections([]dto.AdoptionSelectionRequest{tt.selection})
			if err == nil || !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Fatalf("validateAdoptionSelections() error = %v, want %q", err, tt.wantErrMsg)
			}
		})
	}
}

func TestAdoptionMappingNormalizesAndTrimsInputs(t *testing.T) {
	targets := mapAdoptionTargets([]dto.AdoptionTargetRequest{{Name: " Prod API ", EndpointRef: " prod-docker ", EnvironmentName: " Production "}})
	if len(targets) != 1 || targets[0].Name != "prod-api" || targets[0].EndpointRef != "prod-docker" || targets[0].EnvironmentName != "production" {
		t.Fatalf("mapAdoptionTargets() = %#v, want normalized target", targets)
	}

	selections := mapAdoptionSelections([]dto.AdoptionSelectionRequest{{TargetName: " Prod API ", ContainerID: " abc123 ", ServiceNameOverride: " Legacy API "}})
	if len(selections) != 1 || selections[0].TargetName != "prod-api" || selections[0].ContainerID != "abc123" || selections[0].ServiceNameOverride != "legacy-api" {
		t.Fatalf("mapAdoptionSelections() = %#v, want normalized selection", selections)
	}
}

func TestAdoptionStatsCountCandidatesFailuresAndRedactions(t *testing.T) {
	candidateCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionPreviewStats([]service.AdoptionPreview{{
		Containers: []service.AdoptionPreviewContainer{{RedactedEnvironmentKeys: []string{"DB_PASSWORD"}, RedactedLabelKeys: []string{"secret-token"}}},
	}})
	if candidateCount != 1 || redactedEnvKeyCount != 1 || redactedLabelKeyCount != 1 {
		t.Fatalf("adoptionPreviewStats() = (%d,%d,%d), want (1,1,1)", candidateCount, redactedEnvKeyCount, redactedLabelKeyCount)
	}

	successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount := adoptionImportStats([]service.AdoptionImportResult{
		{Status: "created", RedactedEnvironmentKeys: []string{"DB_PASSWORD"}},
		{Status: "failed", Error: "container unavailable", RedactedLabelKeys: []string{"secret-token"}},
	})
	if successCount != 1 || failureCount != 1 || redactedEnvKeyCount != 1 || redactedLabelKeyCount != 1 {
		t.Fatalf("adoptionImportStats() = (%d,%d,%d,%d), want (1,1,1,1)", successCount, failureCount, redactedEnvKeyCount, redactedLabelKeyCount)
	}
}

func TestRuntimeLifecycleErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: errors.New("service 123 not found"), status: http.StatusNotFound},
		{name: "bad request", err: errors.New("no desired artifact for service"), status: http.StatusBadRequest},
		{name: "conflict", err: errors.New("runtime docker does not support restart"), status: http.StatusConflict},
		{name: "direct runtime guardrail conflict", err: errors.New("direct runtime actions are only allowed for adopted direct_runtime workloads"), status: http.StatusConflict},
		{name: "internal", err: errors.New("docker daemon unavailable"), status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeRuntimeLifecycleError(w, tt.err)
			assertStatus(t, w, tt.status)
		})
	}
}
