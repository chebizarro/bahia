//go:build integration

package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// This test owns the PostgreSQL 16 container and local bbolt files. No
// externally supplied database URL can be used as a migration target.
func TestOptionalPostgresStartupAndRecoveryMatrix(t *testing.T) {
	if os.Getenv("BAHIA_OPTIONAL_PG_MATRIX_CONFIRM") != "disposable" {
		t.Skip("set BAHIA_OPTIONAL_PG_MATRIX_CONFIRM=disposable for the isolated PostgreSQL 16 matrix")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	name := "bahia-optional-pg-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	docker := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	_, err := docker("run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD=optional-disposable", "postgres:16-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		out, cleanupErr := exec.Command("docker", "rm", "--force", name).CombinedOutput()
		require.NoError(t, cleanupErr, string(out))
	})
	portOutput, err := docker("port", name, "5432/tcp")
	require.NoError(t, err)
	portLine := strings.Split(portOutput, "\n")[0]
	port, err := strconv.Atoi(portLine[strings.LastIndex(portLine, ":")+1:])
	require.NoError(t, err)
	dbCfg := config.DBConfig{Host: "127.0.0.1", Port: port, User: "postgres",
		Password: "optional-disposable", Name: "postgres", SSLMode: "disable",
		MaxOpenConns: 4, StartupProbeTimeout: 5 * time.Second}
	var pool *pgxpool.Pool
	for ctx.Err() == nil {
		pool, err = pgxpool.New(ctx, dbCfg.DSN())
		if err == nil {
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
			pool.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.NoError(t, err)
	defer pool.Close()
	var major int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&major))
	require.Equal(t, 16, major)
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	key := nostr.Generate()
	sqlOnly := signedMatrixEvent(t, key, "sql-only")
	localOnly := signedMatrixEvent(t, key, "local-pending")
	closedPort := func() int {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, listenErr)
		p := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		return p
	}
	var coreReadiness map[string]string
	var readyStatus int
	assertBoot := func(label string, cfgDB config.DBConfig, expectDB bool, afterBoot func(*App)) {
		t.Run(label, func(t *testing.T) {
			cfg := startupTestConfig("full")
			cfg.Nostr.PrivateKey = key.Hex()
			cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "events.bolt")
			cfg.DB = cfgDB
			store, openErr := localstore.Open(cfg.Nostr.LocalStore.Path)
			require.NoError(t, openErr)
			_, openErr = store.SaveEvent(localOnly)
			require.NoError(t, openErr)
			require.NoError(t, store.Close())
			outbox, openErr := localstore.OpenOutbox(cfg.Nostr.LocalStore.ResolvedOutboxPath())
			require.NoError(t, openErr)
			inserted, openErr := outbox.Enqueue(localstore.OutboxEntry{Event: localOnly, Target: repository.NostrPublishTargetControlPlane})
			require.NoError(t, openErr)
			require.True(t, inserted)
			require.NoError(t, outbox.Close())

			started := time.Now()
			instance, newErr := New(cfg)
			elapsed := time.Since(started)
			require.NoError(t, newErr)
			t.Cleanup(func() {
				closeRelayPools(instance.relayPools...)
				require.NoError(t, instance.localEventStore.Close())
				require.NoError(t, instance.localOutbox.Close())
				if instance.DB != nil {
					instance.DB.Close()
				}
			})
			require.Equal(t, expectDB, instance.DB != nil, "optional index attachment")
			require.Less(t, elapsed, 8*time.Second, "ordinary startup must not wait unboundedly on the optional index")
			for _, route := range []string{"/health", "/ready"} {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, route, nil)
				requestedAt := time.Now()
				instance.HTTPServer.Handler.ServeHTTP(response, request)
				require.Less(t, time.Since(requestedAt), time.Second, "%s must answer without waiting for PostgreSQL", route)
				if route == "/health" {
					require.Equal(t, http.StatusOK, response.Code)
				} else if readyStatus == 0 {
					readyStatus = response.Code
				} else {
					require.Equal(t, readyStatus, response.Code, "PostgreSQL state must not change relay-gated readiness")
				}
			}
			_, found, getErr := instance.localOutbox.Get(sqlOnly.ID)
			require.NoError(t, getErr)
			require.False(t, found, "SQL-only signed row must not enter the local publish outbox")
			pending, listErr := instance.localOutbox.ListPending(repository.NostrPublishTargetControlPlane, nil, 10)
			require.NoError(t, listErr)
			require.Len(t, pending, 1, "local pending recovery must remain available")
			require.Equal(t, localOnly.ID, pending[0].Event.ID)
			counts, countErr := instance.localOutbox.Counts()
			require.NoError(t, countErr)
			require.EqualValues(t, 1, counts.Pending, "SQL rows must not add pending local publishes")
			require.Zero(t, counts.Failed)
			require.Empty(t, slices.Collect(instance.localEventStore.QueryEvents(nostr.Filter{IDs: []nostr.ID{sqlOnly.ID}})),
				"SQL-only signed row must not enter the canonical local event store")
			require.Len(t, slices.Collect(instance.localEventStore.QueryEvents(nostr.Filter{IDs: []nostr.ID{localOnly.ID}})), 1,
				"local canonical state must survive optional index changes")
			require.Len(t, slices.Collect(instance.localEventStore.QueryEvents(nostr.Filter{})), 1,
				"SQL rows must not create any local canonical events on ordinary boot")
			checks := map[string]string{}
			for _, check := range instance.Health.Readiness().Checks {
				switch check.Name {
				case "relay_quorum", "bootstrap_ready", "intent_readiness", "background_runners":
					checks[check.Name] = check.Status
				}
			}
			if coreReadiness == nil {
				coreReadiness = checks
			} else {
				require.Equal(t, coreReadiness, checks, "PostgreSQL contents must not change core readiness checks")
			}
			if !expectDB {
				requireCheckStatus(t, instance.Health.Readiness().Checks, "postgres_index", HealthStatusWarn)
			}
			if afterBoot != nil {
				afterBoot(instance)
			}
			t.Logf("matrix=%s postgres_attached=%t startup=%s local_pending=%d sql_promoted=0", label, expectDB, elapsed, len(pending))
		})
	}

	absent := dbCfg
	absent.Port = closedPort()
	assertBoot("absent", absent, false, nil)
	assertBoot("empty", dbCfg, true, nil)
	for i := range 32 {
		_, err = pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'index-fixture')`,
			uuid.New(), fmt.Sprintf("matrix-index-%02d", i))
		require.NoError(t, err)
	}
	assertBoot("populated", dbCfg, true, nil)
	tags, err := json.Marshal(sqlOnly.Tags)
	require.NoError(t, err)
	recorded, err := repository.NewPgNostrEventRepository(pool).Record(ctx, &repository.NostrEventRecord{
		ID: sqlOnly.ID.Hex(), Kind: int(sqlOnly.Kind), PubKey: sqlOnly.PubKey.Hex(), Content: sqlOnly.Content,
		Tags: tags, Sig: hex.EncodeToString(sqlOnly.Sig[:]), CreatedAt: time.Unix(int64(sqlOnly.CreatedAt), 0).UTC(),
		PublishState:  repository.NostrPublishStatePending,
		PublishTarget: repository.NostrPublishTargetControlPlane,
	})
	require.NoError(t, err)
	require.True(t, recorded)
	assertBoot("divergent-sql-only-pending", dbCfg, true, nil)
	var pendingSQL int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM nostr_events WHERE id=$1 AND publish_state='pending'`, sqlOnly.ID.Hex()).Scan(&pendingSQL))
	require.Equal(t, 1, pendingSQL, "normal boot must not consume the legacy SQL pending row")

	// A reachable TCP peer that never sends the PostgreSQL greeting exercises
	// the startup deadline rather than an immediate connection refusal.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	stop := make(chan struct{})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() { <-stop; _ = conn.Close() }()
		}
	}()
	defer close(stop)
	slow := dbCfg
	slow.Port = listener.Addr().(*net.TCPAddr).Port
	slow.StartupProbeTimeout = 200 * time.Millisecond
	assertBoot("slow-unresponsive", slow, false, nil)

	// The proxy initially refuses connections, then forwards to the same
	// populated/divergent PostgreSQL endpoint. Recovery must attach without
	// treating its SQL-only pending row as publish authority.
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer proxy.Close()
	var forward atomic.Bool
	go func() {
		for {
			client, acceptErr := proxy.Accept()
			if acceptErr != nil {
				return
			}
			if !forward.Load() {
				_ = client.Close()
				continue
			}
			go func() {
				defer client.Close()
				upstream, dialErr := net.Dial("tcp", net.JoinHostPort(dbCfg.Host, strconv.Itoa(dbCfg.Port)))
				if dialErr != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
				_, _ = io.Copy(client, upstream)
			}()
		}
	}()
	reconnect := dbCfg
	reconnect.Port = proxy.Addr().(*net.TCPAddr).Port
	assertBoot("reconnect", reconnect, false, func(instance *App) {
		forward.Store(true)
		require.True(t, newDatabaseRecoveryRunner(reconnect, time.Second, zap.NewNop()).tryRecover(ctx),
			"recovery must attach to the newly reachable real PostgreSQL index")
		_, found, getErr := instance.localOutbox.Get(sqlOnly.ID)
		require.NoError(t, getErr)
		require.False(t, found, "recovery must not import SQL-only pending events")
		pending, listErr := instance.localOutbox.ListPending(repository.NostrPublishTargetControlPlane, nil, 10)
		require.NoError(t, listErr)
		require.Len(t, pending, 1)
		require.Equal(t, localOnly.ID, pending[0].Event.ID)
		counts, countErr := instance.localOutbox.Counts()
		require.NoError(t, countErr)
		require.EqualValues(t, 1, counts.Pending)
		require.Zero(t, counts.Failed)
		require.Len(t, slices.Collect(instance.localEventStore.QueryEvents(nostr.Filter{})), 1,
			"SQL reconnect must not add any local canonical events")
	})
}

func signedMatrixEvent(t *testing.T, key nostr.SecretKey, coordinate string) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", coordinate}, {"t", kinds.CPStateTopicServiceRegistry},
			{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema}},
		Content: fmt.Sprintf(`{"coordinate":%q}`, coordinate)}
	var raw [32]byte
	decoded, err := hex.DecodeString(key.Hex())
	require.NoError(t, err)
	copy(raw[:], decoded)
	require.NoError(t, event.Sign(raw))
	return event
}
