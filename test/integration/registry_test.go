// Package integration contains integration tests that require a running PostgreSQL instance.
// Run with: go test -tags=integration ./test/integration/
package integration

import (
	"context"
	"os"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func skipIfNoDatabase(t *testing.T) {
	t.Helper()
	if os.Getenv("BAHIA_TEST_DB") == "" {
		t.Skip("skipping integration test: set BAHIA_TEST_DB=1 to run")
	}
}

func TestRegistryServiceIntegration(t *testing.T) {
	skipIfNoDatabase(t)

	ctx := context.Background()
	logger := zap.NewNop()

	cfg := config.Defaults()
	if dbURL := os.Getenv("BAHIA_TEST_DB_URL"); dbURL != "" {
		cfg.DB.Host = "localhost"
		cfg.DB.Port = 5432
		cfg.DB.User = "bahia"
		cfg.DB.Password = "bahia"
		cfg.DB.Name = "bahia_test"
	}

	pool, err := db.Connect(ctx, cfg.DB, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, logger); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	serviceRepo := repository.NewPgServiceRepository(pool)
	envRepo := repository.NewPgEnvironmentRepository(pool)
	buildRepo := repository.NewPgBuildRepository(pool)
	artifactRepo := repository.NewPgArtifactRepository(pool)
	intentRepo := repository.NewPgDeploymentIntentRepository(pool)
	runRepo := repository.NewPgDeploymentRunRepository(pool)
	obsRepo := repository.NewPgRuntimeObservationRepository(pool)
	stateRepo := repository.NewPgEnvironmentServiceStateRepository(pool)
	publisher := &events.NoopPublisher{}

	registry := service.NewRegistryService(
		serviceRepo, envRepo, buildRepo, artifactRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		nil, publisher, logger,
	)

	// Test service CRUD.
	svc := &domain.Service{
		Name:         "test-api",
		ArtifactRepo: "harbor.example.com/test/api",
	}

	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	got, err := registry.GetService(ctx, svc.ID)
	if err != nil {
		t.Fatalf("failed to get service: %v", err)
	}
	if got.Name != "test-api" {
		t.Errorf("expected service name test-api, got %s", got.Name)
	}
	if got.RuntimeConfig != nil {
		t.Errorf("expected nil runtime config for service created without one, got %#v", got.RuntimeConfig)
	}

	svc.RuntimeConfig = &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{
		TargetName:    "legacy-api",
		SourceRuntime: "docker",
		HostAlias:     "local",
		Environment:   map[string]string{"APP_ENV": "test"},
		Ports:         []string{"8080:80"},
		Volumes:       []string{"/host:/data:ro"},
		Restart:       "unless-stopped",
		Command:       []string{"serve"},
		Entrypoint:    []string{"/entrypoint.sh"},
		WorkingDir:    "/app",
		NetworkMode:   "host",
		Labels:        map[string]string{"tier": "api"},
	}}
	if err := registry.UpdateService(ctx, svc); err != nil {
		t.Fatalf("failed to update service runtime config: %v", err)
	}
	got, err = registry.GetService(ctx, svc.ID)
	if err != nil {
		t.Fatalf("failed to get service after runtime config update: %v", err)
	}
	if got.RuntimeConfig == nil || got.RuntimeConfig.Adopted == nil || got.RuntimeConfig.Adopted.TargetName != "legacy-api" || got.RuntimeConfig.Adopted.Environment["APP_ENV"] != "test" {
		t.Fatalf("runtime config did not round-trip: %#v", got.RuntimeConfig)
	}

	services, err := registry.ListServices(ctx)
	if err != nil {
		t.Fatalf("failed to list services: %v", err)
	}
	if len(services) == 0 {
		t.Error("expected at least one service")
	}

	// Cleanup.
	_ = registry.DeleteService(ctx, svc.ID, true)
}
