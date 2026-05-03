package dto

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestAdoptionPreviewResponsesFromServiceUsesSafePayloads(t *testing.T) {
	serviceID := uuid.New()
	previews := []service.AdoptionPreview{{
		Target: service.AdoptionTarget{
			Name:            "local",
			EndpointRef:     "managed-docker",
			DockerHost:      "tcp://secret-docker.internal:2376",
			EnvironmentName: "prod",
		},
		Containers: []service.AdoptionPreviewContainer{{
			Discovered: runtime.DiscoveredContainer{
				TargetName:      "local",
				EnvironmentName: "prod",
				ContainerID:     "abc123",
				ContainerName:   "legacy-api",
				ImageRef:        "ghcr.io/org/api:v1",
				ImageRepo:       "ghcr.io/org/api",
				ImageTag:        "v1",
				Environment:     map[string]string{"DB_PASSWORD": "secret"},
				Labels:          map[string]string{"secret-token": "secret"},
				Compose:         &domain.ComposeMetadata{ProjectName: "legacy", ConfigFiles: []string{"compose.yml"}},
				HealthStatus:    domain.HealthStatusHealthy,
				Adoptable:       true,
			},
			SafeEnvironment:         map[string]string{"APP_ENV": "prod"},
			SafeLabels:              map[string]string{"public": "label"},
			RedactedEnvironmentKeys: []string{"DB_PASSWORD"},
			RedactedLabelKeys:       []string{"secret-token"},
			ProposedServiceName:     "legacy-api",
			ExistingServiceID:       &serviceID,
			WillUpdate:              true,
			Adoptable:               true,
		}},
	}}

	mapped := AdoptionPreviewResponsesFromService(previews)
	if len(mapped) != 1 || len(mapped[0].Containers) != 1 {
		t.Fatalf("unexpected mapped previews: %#v", mapped)
	}
	if mapped[0].Target.EndpointRef != "managed-docker" || mapped[0].Target.DockerHost != "" {
		t.Fatalf("managed endpoint response leaked docker_host: %#v", mapped[0].Target)
	}
	container := mapped[0].Containers[0]
	if container.ExistingServiceID == nil || *container.ExistingServiceID != serviceID || !container.WillUpdate {
		t.Fatalf("preview metadata not mapped: %#v", container)
	}
	if got := container.Discovered.Environment["APP_ENV"]; got != "prod" {
		t.Fatalf("safe environment missing: %#v", container.Discovered.Environment)
	}
	if _, ok := container.Discovered.Environment["DB_PASSWORD"]; ok {
		t.Fatalf("raw environment leaked: %#v", container.Discovered.Environment)
	}
	if got := container.Discovered.Labels["public"]; got != "label" {
		t.Fatalf("safe labels missing: %#v", container.Discovered.Labels)
	}
	if _, ok := container.Discovered.Labels["secret-token"]; ok {
		t.Fatalf("raw labels leaked: %#v", container.Discovered.Labels)
	}
	if len(container.Discovered.RedactedEnvironmentKeys) != 1 || container.Discovered.RedactedEnvironmentKeys[0] != "DB_PASSWORD" {
		t.Fatalf("redacted env keys not mapped: %#v", container.Discovered.RedactedEnvironmentKeys)
	}
	if container.Discovered.Compose == nil || container.Discovered.Compose.ProjectName != "legacy" || len(container.Discovered.Compose.ConfigFiles) != 1 {
		t.Fatalf("compose metadata not mapped: %#v", container.Discovered.Compose)
	}
}

func TestRuntimeActionResponseFromDomainCopiesObservation(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	obsID := uuid.New()
	observedAt := time.Now().UTC().Truncate(time.Second)
	metadata := map[string]any{"container": "abc123"}
	obs := &domain.RuntimeObservation{
		ID:                  obsID,
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:abc",
		ObservedImageRepo:   "ghcr.io/org/api",
		ObservedContainerID: "abc123",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "direct_runtime",
		Metadata:            metadata,
		ObservedAt:          observedAt,
	}

	mapped := RuntimeActionResponseFromDomain("deploy", serviceID, envID, obs)
	if mapped.Action != "deploy" || mapped.ServiceID != serviceID || mapped.EnvironmentID != envID {
		t.Fatalf("action response context not mapped: %#v", mapped)
	}
	if mapped.Observation == nil || mapped.Observation.ID != obsID || mapped.Observation.HealthStatus != string(domain.HealthStatusHealthy) {
		t.Fatalf("observation not mapped: %#v", mapped.Observation)
	}
	metadata["container"] = "mutated"
	if mapped.Observation.Metadata["container"] != "abc123" {
		t.Fatalf("observation metadata was not copied: %#v", mapped.Observation.Metadata)
	}
}
