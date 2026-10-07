package repository_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func openIdentityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL create-identity test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	return pool
}

// client-minted ids are stored verbatim, and the real
// constraint names classify a reused id (ErrAlreadyExists, resolved by
// content upstream) apart from a taken name (ErrConflict).
func TestPgCreatePathsStoreClientIDsAndClassifyConflicts(t *testing.T) {
	pool := openIdentityTestPool(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	policies := repository.NewPgDeploymentPolicyRepository(pool)
	policy := &domain.DeploymentPolicy{ID: domain.NewEntityID(), Name: "identity-" + suffix, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, Enforcement: domain.PolicyEnforcementWarn, Enabled: true}
	if err := policies.Create(ctx, policy); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	t.Cleanup(func() { _ = policies.Delete(context.Background(), policy.ID) })
	stored, err := policies.GetByID(ctx, policy.ID)
	if err != nil || stored.ID != policy.ID {
		t.Fatalf("stored policy = %+v, err %v; want id %s verbatim", stored, err, policy.ID)
	}
	again := *policy
	if err := policies.Create(ctx, &again); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("same id again: err = %v, want ErrAlreadyExists", err)
	}
	sameName := *policy
	sameName.ID = domain.NewEntityID()
	if err := policies.Create(ctx, &sameName); !errors.Is(err, repository.ErrConflict) || errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("same name, new id: err = %v, want ErrConflict", err)
	}
	minted := &domain.DeploymentPolicy{Name: "identity-minted-" + suffix, Rules: policy.Rules, Enforcement: domain.PolicyEnforcementWarn}
	if err := policies.Create(ctx, minted); err != nil || minted.ID.Version() != 7 {
		t.Fatalf("create without id: id %s err %v, want a UUIDv7", minted.ID, err)
	}
	t.Cleanup(func() { _ = policies.Delete(context.Background(), minted.ID) })

	routes := repository.NewPgLLMRouteRepository(pool)
	route := &domain.LLMRoute{ID: domain.NewEntityID(), Name: "identity-route-" + suffix}
	if err := routes.Create(ctx, route); err != nil {
		t.Fatalf("create LLM route: %v", err)
	}
	t.Cleanup(func() { _ = routes.Delete(context.Background(), route.ID) })
	storedRoute, err := routes.GetByID(ctx, route.ID)
	if err != nil || storedRoute == nil || storedRoute.ID != route.ID {
		t.Fatalf("stored route = %+v, err %v; want id %s verbatim", storedRoute, err, route.ID)
	}
	againRoute := *route
	if err := routes.Create(ctx, &againRoute); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("same route id again: err = %v, want ErrAlreadyExists", err)
	}
	sameRouteName := *route
	sameRouteName.ID = domain.NewEntityID()
	if err := routes.Create(ctx, &sameRouteName); !errors.Is(err, repository.ErrConflict) || errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("same route name, new id: err = %v, want ErrConflict", err)
	}
}

// A service row with a NULL repo_url (nullable since 000001; fixtures and
// older writers leave it NULL) must not break reads, including the List the
// projector snapshots from.
func TestPgServiceReadsToleratesNullRepoURL(t *testing.T) {
	pool := openIdentityTestPool(t)
	ctx := context.Background()
	id := domain.NewEntityID()
	if _, err := pool.Exec(ctx, `INSERT INTO services (id, name, artifact_repo) VALUES ($1, $2, 'registry.example/null-repo')`, id, "null-repo-"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	services := repository.NewPgServiceRepository(pool)
	t.Cleanup(func() { _ = services.Delete(context.Background(), id) })
	svc, err := services.GetByID(ctx, id)
	if err != nil || svc == nil || svc.RepoURL != "" {
		t.Fatalf("GetByID with NULL repo_url = %+v, %v", svc, err)
	}
	if _, err := services.List(ctx); err != nil {
		t.Fatalf("List with a NULL repo_url row: %v", err)
	}
}
