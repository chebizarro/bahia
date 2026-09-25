//go:build integration

package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Like the VM integration fixtures, each test owns a disposable schema and
// fails (rather than skips) when explicitly run without a PostgreSQL URL.
// Run integration packages with -p 1: pgcrypto creation is database-wide.
func hiveCIPostgres(t *testing.T) (*PgInitiationStore, func() *PgInitiationStore) {
	t.Helper()
	dsn := os.Getenv("BAHIA_HIVECI_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	}
	require.NotEmpty(t, dsn, "integration requires BAHIA_HIVECI_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "hiveci_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
		admin.Close()
	})
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	cipher, err := secretsAdapter.NewEncryptor(strings.Repeat("71", 32))
	require.NoError(t, err)
	store := NewPgInitiationStore(pool, cipher)
	return store, func() *PgInitiationStore {
		pool.Close()
		var err error
		pool, err = pgxpool.NewWithConfig(ctx, cfg.Copy())
		require.NoError(t, err)
		cipher, err := secretsAdapter.NewEncryptor(strings.Repeat("71", 32))
		require.NoError(t, err)
		return NewPgInitiationStore(pool, cipher)
	}
}

func TestPostgresInitiationAtomicClaimAndCAS(t *testing.T) {
	store, restart := hiveCIPostgres(t)
	ctx := context.Background()
	req := arcanaStartRequest(uuid.New())
	const contenders = 32
	var claims atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errorsCh := make(chan error, contenders)
	ids := make(chan uuid.UUID, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			candidate := req
			candidate.BuildID = uuid.New()
			record, claimed, err := NewPgInitiationStore(store.pool, store.cipher).Claim(ctx, candidate)
			if err != nil {
				errorsCh <- err
				return
			}
			if claimed {
				claims.Add(1)
			}
			ids <- record.Result.BuildID
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, claims.Load())
	close(ids)
	canonical := <-ids
	for id := range ids {
		require.Equal(t, canonical, id)
	}
	record, err := store.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	record.Stage, record.PublisherNsec = StageRequestReady, "sensitive-per-run-key"
	require.NoError(t, store.Advance(ctx, StageClaimed, record))
	var raw []byte
	require.NoError(t, store.pool.QueryRow(ctx, `SELECT document FROM hiveci_initiations WHERE source_event_id=$1`, req.SourceEventID).Scan(&raw))
	require.False(t, bytes.Contains(raw, []byte(record.PublisherNsec)))
	store = restart()
	record, err = store.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.Equal(t, canonical, record.Result.BuildID)
	require.Equal(t, "sensitive-per-run-key", record.PublisherNsec)
	var winners atomic.Int32
	casErrors := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := *record
			next.Stage = StageRequestUnconfirmed
			err := store.Advance(ctx, StageRequestReady, &next)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrInitiationConflict) {
				casErrors <- err
			}
		}()
	}
	wg.Wait()
	close(casErrors)
	for err := range casErrors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, winners.Load())
	req.ServiceID = uuid.New()
	_, _, err = store.Claim(ctx, req)
	require.ErrorContains(t, err, "conflicts")
}

func TestPostgresInitiationResumeAcrossRestart(t *testing.T) {
	proveInitiationResume(t, func(t *testing.T) (InitiationStore, func() InitiationStore) {
		store, restart := hiveCIPostgres(t)
		return store, func() InitiationStore { return restart() }
	})
}

func TestPostgresConcurrentInitiationSingleDispatch(t *testing.T) {
	store, _ := hiveCIPostgres(t)
	proveConcurrentInitiation(t, func() InitiationStore { return NewPgInitiationStore(store.pool, store.cipher) })
}

func TestPostgresInitiationMigrationGuard(t *testing.T) {
	store, _ := hiveCIPostgres(t)
	ctx := context.Background()
	down, err := os.ReadFile("../../db/migrations/000070_hiveci_initiations.down.sql")
	require.NoError(t, err)
	up, err := os.ReadFile("../../db/migrations/000070_hiveci_initiations.up.sql")
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, string(down))
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, string(up))
	require.NoError(t, err)
	_, _, err = store.Claim(ctx, arcanaStartRequest(uuid.New()))
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, string(down))
	require.ErrorContains(t, err, "rollback refused")
	var count int
	require.NoError(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM hiveci_initiations`).Scan(&count))
	require.Equal(t, 1, count)
}

type postgresBuildRegistry struct{ *repository.PgBuildRepository }

func (r postgresBuildRegistry) RegisterBuild(ctx context.Context, build *domain.Build) error {
	return r.Create(ctx, build)
}
func (r postgresBuildRegistry) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	return r.ListByService(ctx, serviceID, limit, offset)
}

type initiationServiceLoader struct{ service *domain.Service }

func (s initiationServiceLoader) GetByID(context.Context, uuid.UUID) (*domain.Service, error) {
	return s.service, nil
}

type initiationCredentialLoader struct{ secret *domain.ServiceSecret }

func (s initiationCredentialLoader) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	return s.secret, nil
}

type initiationMembers struct{}

func (initiationMembers) GetMember(_ context.Context, org uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	return &domain.OrgMember{OrgID: org, Pubkey: pubkey, Role: domain.RoleOwner}, nil
}
func (initiationMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type unavailableBuildRegistry struct{ postgresBuildRegistry }

func (unavailableBuildRegistry) RegisterBuild(context.Context, *domain.Build) error {
	return errSimulatedCrash
}

func TestPostgresBuildRequestReplayCanonicalRowAndEvents(t *testing.T) {
	provePostgresBuildRequestReplay(t, false)
}

func TestPostgresBuildRequestResumesBeforeRowRegistration(t *testing.T) {
	provePostgresBuildRequestReplay(t, true)
}

func provePostgresBuildRequestReplay(t *testing.T, failRegistration bool) {
	store, restart := hiveCIPostgres(t)
	ctx := context.Background()
	server := httptest.NewServer((&fakeGitea{}).handler(t))
	defer server.Close()
	original, _, _, credential, _ := newConformanceInitiator(t, server)
	relay := newInitiationRelay(t)
	org := uuid.New()
	_, err := store.pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'HiveCI test','operator')`, org, org.String())
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `INSERT INTO services(id,org_id,name,artifact_repo) VALUES($1,$2,'hiveci-test','registry.fleet.internal/arcana/web')`, testServiceID, org)
	require.NoError(t, err)
	newHandler := func(store *PgInitiationStore, fail bool) *controlplane.EncryptedBuildHandlers {
		builds := postgresBuildRegistry{repository.NewPgBuildRepository(store.pool)}
		var registry controlplane.BuildRegistry = builds
		if fail {
			registry = unavailableBuildRegistry{builds}
		}
		return controlplane.NewEncryptedBuildHandlers(controlplane.EncryptedBuildHandlersConfig{
			Starter: restartInitiator(t, original, store, relay), Registry: registry, Builds: builds,
			Services: initiationServiceLoader{&domain.Service{ID: testServiceID, OrgID: org, ArtifactRepo: "registry.fleet.internal/arcana/web",
				Repository: &domain.RepositoryRef{RepoCoordinate: controlplane.ArcanaRepositoryCoordinate}}},
			Secrets: initiationCredentialLoader{&domain.ServiceSecret{ID: credential, ServiceID: testServiceID}},
			RBAC:    auth.NewRBAC(initiationMembers{}),
		})
	}
	params, err := json.Marshal(controlplane.ArcanaBuildRequest{ServiceID: testServiceID, GitRef: "main", RepositoryCredentialRef: credential, ArtifactRepo: "registry.fleet.internal/arcana/web"})
	require.NoError(t, err)
	event := &nostr.Event{Kind: nostr.Kind(kinds.ContextVMMessage), CreatedAt: nostr.Now(), Content: string(params)}
	require.NoError(t, controlplane.SignGoNostrEvent(ctx, relay.signer, event))
	request := controlplane.ContextVMRequest{Event: event, RPC: controlplane.ContextVMJSONRPCRequest{Params: params}}
	first, err := newHandler(store, failRegistration).RequestBuild(ctx, request)
	if failRegistration {
		require.ErrorIs(t, err, errSimulatedCrash)
		var count int
		require.NoError(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM builds`).Scan(&count))
		require.Zero(t, count)
	} else {
		require.NoError(t, err)
	}
	store = restart()
	second, err := newHandler(store, false).RequestBuild(ctx, request)
	require.NoError(t, err)
	if !failRegistration {
		require.Equal(t, first, second)
	}
	third, err := newHandler(store, false).RequestBuild(ctx, request)
	require.NoError(t, err)
	require.Equal(t, second, third)
	var count int
	require.NoError(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM builds WHERE source_event_id=$1`, event.ID.Hex()).Scan(&count))
	require.Equal(t, 1, count)
	record, err := store.Get(ctx, event.ID.Hex())
	require.NoError(t, err)
	buildID := second.(map[string]any)["build_id"].(uuid.UUID)
	require.Equal(t, buildID, record.Result.BuildID)
	require.Equal(t, buildID.String(), record.RunEvent.Tags.Find("build")[1])
	require.Equal(t, "hiveci-build:"+buildID.String(), record.EvidenceEvent.Tags.GetD())
	relay.assertOneBuild(t)
}
