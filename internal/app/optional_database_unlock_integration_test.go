//go:build integration

package app

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A wire proxy stalls the unlock response after the real migration has
// completed. This exercises the whole optional startup probe, not a mocked
// dbMigrate call.
func TestOptionalDatabaseStartupBudgetIncludesMigrationUnlock(t *testing.T) {
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BAHIA_MIGRATE_TEST_DATABASE_URL to a disposable PostgreSQL 16 server")
	}
	base, err := url.Parse(dsn)
	require.NoError(t, err)
	admin, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	name := "bahia_unlock_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(t.Context(), `CREATE DATABASE `+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err)
	})
	prepared := *base
	prepared.Path = "/" + name
	pool, err := pgxpool.New(t.Context(), prepared.String())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(t.Context(), pool, zap.NewNop()))
	pool.Close()

	proxy := newUnlockStallProxy(t, base.Host)
	connect := dbConnect
	var attempts atomic.Int32
	dbConnect = func(ctx context.Context, cfg config.DBConfig, logger *zap.Logger) (*pgxpool.Pool, error) {
		attempts.Add(1)
		return connect(ctx, cfg, logger)
	}
	t.Cleanup(func() { dbConnect = connect })
	host, portText, err := net.SplitHostPort(proxy.listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	password, _ := base.User.Password()
	cfg := &config.Config{DB: config.DBConfig{
		Host: host, Port: port, User: base.User.Username(), Password: password,
		Name: name, SSLMode: "disable", MaxOpenConns: 2,
		StartupProbeTimeout: 2 * time.Second,
	}}
	started := time.Now()
	attached, available := connectOptionalDatabase(t.Context(), cfg, zap.NewNop())
	elapsed := time.Since(started)
	require.False(t, available)
	require.Nil(t, attached)
	select {
	case <-proxy.stalled:
	default:
		t.Fatal("migration did not reach the stalled advisory unlock")
	}
	require.Less(t, elapsed, 3*time.Second, "unlock exceeded the optional startup budget")
	require.True(t, db.CanceledPoolCleanupPending(), "driver cleanup must be tracked")
	_, secondAvailable := connectOptionalDatabase(t.Context(), cfg, zap.NewNop())
	require.False(t, secondAvailable)
	require.False(t, newDatabaseRecoveryRunner(cfg.DB, time.Second, zap.NewNop()).tryRecover(t.Context()))
	require.Equal(t, int32(1), attempts.Load(), "a second probe must not accumulate a connection during cleanup")
	proxy.unstall()
}

type unlockStallProxy struct {
	listener    net.Listener
	stalled     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func newUnlockStallProxy(t *testing.T, upstream string) *unlockStallProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &unlockStallProxy{listener: listener, stalled: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { proxy.unstall(); _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go proxy.forward(client, upstream)
		}
	}()
	return proxy
}

func (p *unlockStallProxy) unstall() { p.releaseOnce.Do(func() { close(p.release) }) }

func (p *unlockStallProxy) forward(client net.Conn, upstreamAddr string) {
	defer client.Close()
	upstream, err := net.Dial("tcp", upstreamAddr)
	if err != nil {
		return
	}
	defer upstream.Close()
	go func() { _, _ = io.Copy(client, upstream) }()
	buffer := make([]byte, 8192)
	var tail []byte
	for {
		n, err := client.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			combined := append(tail, chunk...)
			if bytes.Contains(combined, []byte("pg_advisory_unlock")) {
				p.once.Do(func() { close(p.stalled) })
				<-p.release
				return
			}
			if len(combined) > 64 {
				tail = append([]byte(nil), combined[len(combined)-64:]...)
			} else {
				tail = append([]byte(nil), combined...)
			}
			if _, writeErr := upstream.Write(chunk); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
