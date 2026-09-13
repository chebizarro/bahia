package reconcile

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// --- CD-5: Attributable regression rollback tests ---

func TestCD5_AttributableRegressionRollsBackToPriorDigest(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	currentArtifactID := uuid.New()
	priorArtifactID := uuid.New()
	intentID := uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "web"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback"}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		currentArtifactID: {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: "sha256:current"},
		priorArtifactID:   {ID: priorArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: "sha256:prior"},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID:                  intentID,
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ArtifactID:          currentArtifactID,
			SourceKind:          domain.SourceKindAutoPromote,
			PriorArtifactDigest: "sha256:prior",
			Status:              domain.IntentStatusDeployed,
			CreatedAt:           now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:broken"}
	pub := &mockPublisher{}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop(), WithRemediationIntentHistory(intentRepo))

	err := r.OnHealthFailure(t.Context(), serviceID, envID)
	if err != nil {
		t.Fatalf("OnHealthFailure() error = %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) != 1 || rt.deployed[0] != "web:registry.example/web@sha256:prior" {
		t.Fatalf("expected rollback to prior digest, got deployments = %#v", rt.deployed)
	}
	rt.mu.Unlock()

	state := stateRepo.states[stateMapKey(serviceID, envID)]
	if state.DesiredArtifactID == nil || *state.DesiredArtifactID != priorArtifactID {
		t.Fatalf("expected desired artifact to be prior artifact, got %v", state.DesiredArtifactID)
	}
	if state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("expected drift status in_sync after rollback, got %s", state.DriftStatus)
	}

	pub.mu.Lock()
	var foundAttributable bool
	for _, e := range pub.events {
		if e.Type == events.EventRollbackAttributableRollback {
			foundAttributable = true
			data, ok := e.Data.(map[string]string)
			if !ok {
				t.Fatal("attributable rollback event data is not map[string]string")
			}
			if data["target_image"] != "registry.example/web@sha256:prior" {
				t.Errorf("expected target_image to be prior digest, got %s", data["target_image"])
			}
			if data["source_kind"] != string(domain.SourceKindAutoPromote) {
				t.Errorf("expected source_kind auto_promote, got %s", data["source_kind"])
			}
		}
	}
	pub.mu.Unlock()
	if !foundAttributable {
		t.Fatal("expected EventRollbackAttributableRollback audit event")
	}
}

func TestCD5_ManualIntentDoesNotTriggerRollback(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	currentArtifactID := uuid.New()
	intentID := uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "web"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback"}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		currentArtifactID: {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: "sha256:current"},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID:            intentID,
			ServiceID:     serviceID,
			EnvironmentID: envID,
			ArtifactID:    currentArtifactID,
			SourceKind:    domain.SourceKindManual,
			Status:        domain.IntentStatusDeployed,
			CreatedAt:     now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:implicit"}
	pub := &mockPublisher{}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop(), WithRemediationIntentHistory(intentRepo))

	err := r.OnHealthFailure(t.Context(), serviceID, envID)
	if err != nil {
		t.Fatalf("OnHealthFailure() error = %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) > 0 {
		t.Fatalf("expected no deployments for manual intent, got %#v", rt.deployed)
	}
	rt.mu.Unlock()

	state := stateRepo.states[stateMapKey(serviceID, envID)]
	if state.DesiredArtifactID == nil || *state.DesiredArtifactID != currentArtifactID {
		t.Fatalf("expected desired artifact unchanged, got %v", state.DesiredArtifactID)
	}

	pub.mu.Lock()
	var foundNotAttributable bool
	for _, e := range pub.events {
		if e.Type == events.EventRollbackNotAttributable {
			foundNotAttributable = true
		}
	}
	pub.mu.Unlock()
	if !foundNotAttributable {
		t.Fatal("expected EventRollbackNotAttributable audit event")
	}
}

func TestCD5_UnrelatedOutageNoRollback(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	currentArtifactID := uuid.New()
	intentID := uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "worker"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback"}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		currentArtifactID: {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/worker", ImageDigest: "sha256:current"},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID:                  intentID,
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ArtifactID:          currentArtifactID,
			SourceKind:          domain.SourceKindAutoPromote,
			PriorArtifactDigest: "",
			Status:              domain.IntentStatusDeployed,
			CreatedAt:           now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:implicit"}
	pub := &mockPublisher{}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop(), WithRemediationIntentHistory(intentRepo))

	err := r.OnHealthFailure(t.Context(), serviceID, envID)
	if err != nil {
		t.Fatalf("OnHealthFailure() error = %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) > 0 {
		t.Fatalf("expected no deployments for unrelated outage (no prior digest), got %#v", rt.deployed)
	}
	rt.mu.Unlock()

	pub.mu.Lock()
	var foundNotAttributable bool
	for _, e := range pub.events {
		if e.Type == events.EventRollbackNotAttributable {
			foundNotAttributable = true
			break
		}
	}
	pub.mu.Unlock()
	if !foundNotAttributable {
		t.Fatal("expected EventRollbackNotAttributable audit event for unrelated outage")
	}
}

func TestCD5_AlreadyRolledBackSuppressesDoubleRollback(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	currentArtifactID := uuid.New()
	priorArtifactID := uuid.New()
	intentID := uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "web"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback"}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		currentArtifactID: {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: "sha256:current"},
		priorArtifactID:   {ID: priorArtifactID, ServiceID: serviceID, ImageRepo: "registry.example/web", ImageDigest: "sha256:prior"},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID:                  intentID,
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ArtifactID:          currentArtifactID,
			SourceKind:          domain.SourceKindAutoPromote,
			PriorArtifactDigest: "sha256:prior",
			Status:              domain.IntentStatusDeployed,
			CreatedAt:           now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:broken"}
	pub := &mockPublisher{}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop(), WithRemediationIntentHistory(intentRepo))

	// First call: should roll back.
	if err := r.OnHealthFailure(t.Context(), serviceID, envID); err != nil {
		t.Fatalf("first OnHealthFailure() error = %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) != 1 {
		t.Fatalf("expected 1 deployment on first call, got %d", len(rt.deployed))
	}
	rt.mu.Unlock()

	// Manually mark the intent as already rolled back to simulate a rollback that occurred.
	_ = intentRepo.UpdateStatus(t.Context(), intentID, domain.IntentStatusRolledBack)

	// Reset runtime so we can check it does NOT deploy again.
	rt.mu.Lock()
	rt.deployed = nil
	rt.undeployed = nil
	rt.mu.Unlock()

	// Second call: should suppress.
	if err := r.OnHealthFailure(t.Context(), serviceID, envID); err != nil {
		t.Fatalf("second OnHealthFailure() error = %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) > 0 {
		t.Fatalf("expected no deployments on second call, got %#v", rt.deployed)
	}
	rt.mu.Unlock()

	pub.mu.Lock()
	var foundSuppressed bool
	for _, e := range pub.events {
		if e.Type == events.EventRollbackSuppressedDouble {
			foundSuppressed = true
			break
		}
	}
	pub.mu.Unlock()
	if !foundSuppressed {
		t.Fatal("expected EventRollbackSuppressedDouble audit event")
	}
}

func TestCD5_AuditEventsEmittedForAllOutcomes(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	now := time.Now().UTC()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "audit-me"},
	}}
	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, Name: "prod", RuntimeConfig: map[string]any{"auto_remediation": map[string]any{"enabled": true, "cooldown_seconds": float64(0), "on_health_failure": "rollback"}}},
	}}
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		artifactID: {ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/audit", ImageDigest: "sha256:current"},
	}}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &artifactID,
			DesiredIntentID:   &intentID,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {
			ID:            intentID,
			ServiceID:     serviceID,
			EnvironmentID: envID,
			ArtifactID:    artifactID,
			SourceKind:    domain.SourceKindScheduled,
			Status:        domain.IntentStatusDeployed,
			CreatedAt:     now,
		},
	}}
	rt := &mockRuntime{observeDigest: "sha256:implicit"}
	pub := &mockPublisher{}
	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop(), WithRemediationIntentHistory(intentRepo))

	_ = r.OnHealthFailure(t.Context(), serviceID, envID)

	pub.mu.Lock()
	var hasNotAttributable bool
	var hasSuppressed bool
	var hasAttributableRollback bool
	var hasStarted bool
	var hasCompleted bool
	var hasFailed bool
	for _, e := range pub.events {
		switch e.Type {
		case events.EventRollbackNotAttributable:
			hasNotAttributable = true
		case events.EventRollbackSuppressedDouble:
			hasSuppressed = true
		case events.EventRollbackAttributableRollback:
			hasAttributableRollback = true
		case "remediation.started":
			hasStarted = true
		case "remediation.completed":
			hasCompleted = true
		case "remediation.failed":
			hasFailed = true
		}
	}
	pub.mu.Unlock()

	if !hasStarted {
		t.Error("expected remediation.started event")
	}
	if !hasCompleted {
		t.Error("expected remediation.completed event")
	}
	if hasFailed {
		t.Error("unexpected remediation.failed event")
	}
	if !hasNotAttributable {
		t.Error("expected EventRollbackNotAttributable audit event (scheduled source kind)")
	}
	if hasAttributableRollback {
		t.Error("unexpected EventRollbackAttributableRollback for non-promoted intent")
	}
	if hasSuppressed {
		t.Error("unexpected EventRollbackSuppressedDouble for first attempt")
	}
}
