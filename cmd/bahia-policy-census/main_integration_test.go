//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Each test owns a schema on a disposable PostgreSQL 16 instance. PGOPTIONS
// makes the command's independently constructed connection use that schema too.
func censusPostgres(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BAHIA_MIGRATE_TEST_DATABASE_URL to disposable PostgreSQL 16")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := "policy_census_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err)
	})
	t.Setenv("PGOPTIONS", "-c search_path="+schema+",public")
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var serverVersion int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&serverVersion))
	require.GreaterOrEqual(t, serverVersion, 160000)
	require.Less(t, serverVersion, 170000)
	require.NoError(t, db.Migrate(t.Context(), pool, zap.NewNop()))
	return pool, dsn
}

func censusRelay(t *testing.T) (string, *khatru.Relay) {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return nostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http")), relay
}

func censusSignedPolicy(t *testing.T, key nostr.SecretKey, state controlplane.RelayPolicyState) nostr.Event {
	t.Helper()
	content, err := json.Marshal(state)
	require.NoError(t, err)
	event := nostr.Event{Kind: kinds.CASControlState, CreatedAt: nostr.Timestamp(time.Now().Add(-time.Minute).Unix()), Tags: nostr.Tags{{kinds.CASControlStateTagD, controlplane.RelaySettingsDTag}, {kinds.CASControlStateTagDomain, controlplane.RelaySettingsDomain}, {kinds.CASControlStateTagSchema, controlplane.RelaySettingsSchema}}, Content: string(content)}
	require.NoError(t, event.Sign(key))
	return event
}

func censusConfig(t *testing.T, dsn, relay string, key nostr.SecretKey) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	password, _ := u.User.Password()
	port := "5432"
	if u.Port() != "" {
		port = u.Port()
	}
	data := fmt.Sprintf("dev_mode: true\ndb:\n  host: %q\n  port: %s\n  user: %q\n  password: %q\n  name: %q\n  sslmode: disable\nnostr:\n  private_key: %q\n  contextvm_relays:\n    - %q\n", u.Hostname(), port, u.User.Username(), password, strings.TrimPrefix(u.Path, "/"), key.Hex(), relay)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	return path
}

func insertCensusProjection(t *testing.T, pool *pgxpool.Pool, event nostr.Event, payload []byte, hash string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := pool.Exec(t.Context(), `INSERT INTO relay_policy_projections
 (author_pubkey,event_id,event_created_at,event_accepted_at,schema,canonical_payload,payload_hash,source_relay,last_sync_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, event.PubKey.Hex(), event.ID.Hex(), event.CreatedAt.Time(), now, controlplane.RelaySettingsSchema, payload, hash, "ws://source.invalid", now)
	require.NoError(t, err)
}

func TestPolicyCensusPostgresSignedProjectionAndFailClosedBounds(t *testing.T) {
	pool, dsn := censusPostgres(t)
	key := nostr.Generate()
	relayURL, relay := censusRelay(t)
	hintedURL, hintedRelay := censusRelay(t)
	state := controlplane.RelayPolicyState{Schema: controlplane.RelaySettingsSchema, BrowserRelays: []string{hintedURL}, ContextVMRelays: []string{relayURL}, ServiceRelays: []string{}}
	event := censusSignedPolicy(t, key, state)
	_, err := relay.AddEvent(t.Context(), event)
	require.NoError(t, err)
	_, err = hintedRelay.AddEvent(t.Context(), event)
	require.NoError(t, err)
	payload, err := json.Marshal(state)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	insertCensusProjection(t, pool, event, payload, hash)
	configPath := censusConfig(t, dsn, relayURL, key)
	initial, err := os.ReadFile(configPath)
	require.NoError(t, err)
	invoke := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errors bytes.Buffer
		base := []string{"--config", configPath, "--relays", relayURL, "--deadline", "15s"}
		code := run(t.Context(), append(base, args...), &out, &errors)
		return code, out.String(), errors.String()
	}
	code, out, errors := invoke()
	require.Zero(t, code, errors)
	var report policyCensusReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.True(t, report.ReadOnly)
	require.Equal(t, event.ID.Hex(), report.PolicyEventID)
	require.Equal(t, "signed-canonical-policy-verified", report.RelaySetType)
	require.Empty(t, report.Rows)
	after, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, initial, after, "read-only config load must not rewrite YAML")
	// The row limit must abort atomically, never returning a partial JSON report.
	for i := 0; i < 2; i++ {
		_, err = pool.Exec(t.Context(), `INSERT INTO deployment_policies (name) VALUES ($1)`, fmt.Sprintf("policy-%d", i))
		require.NoError(t, err)
	}
	code, out, errors = invoke("--max-rows", "1")
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errors, "exceeds max-rows")
	code, out, errors = invoke("--deadline", "1ns")
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.NotEmpty(t, errors)
	// The stored projection is a discovery hint, not authority. Invalid hashes
	// and mismatched signed IDs must never yield even a partial report.
	_, err = pool.Exec(t.Context(), `UPDATE relay_policy_projections SET payload_hash=$1`, strings.Repeat("0", 64))
	require.NoError(t, err)
	code, out, errors = invoke()
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errors, "payload hash mismatch")
	_, err = pool.Exec(t.Context(), `UPDATE relay_policy_projections SET payload_hash=$1,event_id=$2`, hash, strings.Repeat("a", 64))
	require.NoError(t, err)
	code, out, errors = invoke()
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errors, "disagrees with signed canonical")
}

func TestPolicyCensusPostgresRepeatableReadAndReadOnly(t *testing.T) {
	pool, _ := censusPostgres(t)
	key := nostr.Generate()
	relayURL, _ := censusRelay(t)
	state := controlplane.RelayPolicyState{Schema: controlplane.RelaySettingsSchema, BrowserRelays: []string{}, ContextVMRelays: []string{relayURL}, ServiceRelays: []string{}}
	event := censusSignedPolicy(t, key, state)
	payload, err := json.Marshal(state)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	insertCensusProjection(t, pool, event, payload, hash)
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer tx.Rollback(t.Context())
	first, err := readPolicyProjectionHint(t.Context(), tx, event.PubKey.Hex())
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), first.EventID)
	_, err = pool.Exec(t.Context(), `UPDATE relay_policy_projections SET event_id=$1`, strings.Repeat("b", 64))
	require.NoError(t, err)
	second, err := readPolicyProjectionHint(t.Context(), tx, event.PubKey.Hex())
	require.NoError(t, err)
	require.Equal(t, first.EventID, second.EventID, "snapshot must not see a concurrent projection rewrite")
	_, err = tx.Exec(t.Context(), `INSERT INTO deployment_policies (name) VALUES ('forbidden')`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestPolicyCensusPagedDryRunResumesWithPinnedHead(t *testing.T) {
	pool, dsn := censusPostgres(t)
	key := nostr.Generate()
	relayURL, relay := censusRelay(t)
	state := controlplane.RelayPolicyState{Schema: controlplane.RelaySettingsSchema, ContextVMRelays: []string{relayURL}}
	head := censusSignedPolicy(t, key, state)
	_, err := relay.AddEvent(t.Context(), head)
	require.NoError(t, err)
	configPath := censusConfig(t, dsn, relayURL, key)
	ids := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002"), uuid.MustParse("00000000-0000-0000-0000-000000000003")}
	for _, id := range ids {
		_, err = pool.Exec(t.Context(), `INSERT INTO deployment_policies (id, name) VALUES ($1, $2)`, id, id.String())
		require.NoError(t, err)
		event := nostr.Event{Kind: kinds.CASControlState, CreatedAt: nostr.Timestamp(time.Now().Add(-time.Minute).Unix()),
			Tags: nostr.Tags{{"d", id.String()}, {"t", kinds.CPStateTopicPolicyRegistry}, {"domain", "policy"}}, Content: `{"deleted":true}`}
		require.NoError(t, event.Sign(key))
		_, err = relay.AddEvent(t.Context(), event)
		require.NoError(t, err)
	}
	invoke := func(extra ...string) (int, policyCensusReport, string, string) {
		t.Helper()
		var out, errors bytes.Buffer
		args := []string{"--config", configPath, "--relays", relayURL, "--page-size", "2", "--expected-policy-head", head.ID.Hex(), "--deadline", "15s"}
		code := run(t.Context(), append(args, extra...), &out, &errors)
		var report policyCensusReport
		if code == 0 {
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		}
		return code, report, out.String(), errors.String()
	}
	var initialOut, initialErrors bytes.Buffer
	code := run(t.Context(), []string{"--config", configPath, "--relays", relayURL, "--page-size", "2", "--deadline", "15s"}, &initialOut, &initialErrors)
	require.Zero(t, code, initialErrors.String())
	var initial policyCensusReport
	require.NoError(t, json.Unmarshal(initialOut.Bytes(), &initial))
	require.Equal(t, head.ID.Hex(), initial.PolicyEventID, "first page discovers the signed head to pin on resume")
	initialOut.Reset()
	initialErrors.Reset()
	code = run(t.Context(), []string{"--config", configPath, "--relays", relayURL, "--page-size", "2", "--after-id", ids[1].String()}, &initialOut, &initialErrors)
	require.Equal(t, 1, code)
	require.Empty(t, initialOut.String())
	require.Contains(t, initialErrors.String(), "expected-policy-head")
	code, first, _, errors := invoke()
	require.Zero(t, code, errors)
	require.True(t, first.ReadOnly)
	require.Equal(t, head.ID.Hex(), first.PolicyEventID)
	require.Equal(t, []string{ids[0].String(), ids[1].String()}, []string{first.Rows[0].ID, first.Rows[1].ID})
	require.Equal(t, "relay-present-sql-skipped", first.Rows[0].Status)
	require.True(t, first.Page.HasMore)
	require.Equal(t, ids[1].String(), first.Page.NextAfterID)
	code, again, _, errors := invoke()
	require.Zero(t, code, errors)
	require.Equal(t, first, again, "same cursor must be replayable without a write-side checkpoint")
	code, last, _, errors := invoke("--after-id", first.Page.NextAfterID)
	require.Zero(t, code, errors)
	require.Equal(t, ids[2].String(), last.Rows[0].ID)
	require.Equal(t, ids[1].String(), last.Page.AfterID)
	require.False(t, last.Page.HasMore)
	require.Empty(t, last.Page.NextAfterID)
	code, _, out, errors := invoke("--after-id", "not-a-uuid")
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errors, "canonical UUID")
	var badOut, badErrors bytes.Buffer
	code = run(t.Context(), []string{"--config", configPath, "--relays", relayURL, "--page-size", "2", "--expected-policy-head", strings.Repeat("a", 64)}, &badOut, &badErrors)
	require.Equal(t, 1, code)
	require.Empty(t, badOut.String())
	require.Contains(t, badErrors.String(), "head changed")
	_, err = pool.Exec(t.Context(), `INSERT INTO deployment_policies (id, name) VALUES ($1, 'sql-only')`, uuid.MustParse("00000000-0000-0000-0000-000000000004"))
	require.NoError(t, err)
	code, _, out, errors = invoke("--after-id", ids[2].String())
	require.Equal(t, 1, code)
	require.Empty(t, out, "SQL-only coordinate cannot produce an import permit")
	require.Contains(t, errors, "absence is unprovable")
}
