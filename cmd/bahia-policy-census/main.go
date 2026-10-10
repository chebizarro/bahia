// Command bahia-policy-census reports which legacy SQL deployment-policy
// coordinates are already present on the selected relays. It never publishes.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type policyCensusRow struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	EventIDs []string `json:"event_ids,omitempty"`
}

type policyCensusReport struct {
	ReadOnly      bool              `json:"read_only"`
	RelaySetType  string            `json:"relay_set_type"`
	PolicyEventID string            `json:"relay_policy_event_id"`
	Relays        []string          `json:"relays"`
	Rows          []policyCensusRow `json:"rows"`
	Page          *policyCensusPage `json:"page,omitempty"`
}

// A page is a bounded observation, not an import receipt. A continuation
// starts a new SQL snapshot; operators must independently fence SQL writers.
type policyCensusPage struct {
	AfterID     string `json:"after_id,omitempty"`
	LastID      string `json:"last_id,omitempty"`
	HasMore     bool   `json:"has_more"`
	NextAfterID string `json:"next_after_id,omitempty"`
}

var policyHeadID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bahia-policy-census", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config.yaml", "Bahia config path")
	relayList := flags.String("relays", "", "comma-separated expected effective control-plane relays; must match canonical policy")
	maxRows := flags.Int("max-rows", 1000, "maximum SQL rows; census fails if more exist")
	pageSize := flags.Int("page-size", 0, "opt-in keyset page size (1..1000); resume requires --expected-policy-head")
	afterID := flags.String("after-id", "", "exclusive UUID cursor for --page-size")
	expectedHead := flags.String("expected-policy-head", "", "pinned signed relay-policy event ID for paged dry-run")
	deadline := flags.Duration("deadline", 5*time.Minute, "global SQL snapshot and relay read deadline (up to 30m)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *maxRows < 1 || *maxRows > 10000 || *pageSize < 0 || *pageSize > 1000 || *deadline <= 0 || *deadline > 30*time.Minute ||
		(*pageSize == 0 && (*afterID != "" || *expectedHead != "")) || (*pageSize > 0 && ((*expectedHead != "" && !policyHeadID.MatchString(*expectedHead)) || (*afterID != "" && *expectedHead == ""))) {
		fmt.Fprintln(stderr, "usage: bahia-policy-census --config path --relays url,... [--max-rows 1..10000 | --page-size 1..1000 [--expected-policy-head 64-hex-id [--after-id UUID]]] [--deadline 5m]")
		return 1
	}
	var cursor uuid.UUID
	if *afterID != "" {
		var err error
		cursor, err = uuid.Parse(*afterID)
		if err != nil || cursor.String() != *afterID {
			return fail(stderr, "after-id must be a canonical UUID")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *deadline)
	defer cancel()
	cfg, err := config.LoadReadOnly(*path)
	if err != nil {
		return fail(stderr, "load read-only config: %v", err)
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey))
	if err != nil {
		return fail(stderr, "valid service private key is required for signed author and relay AUTH: %v", err)
	}
	authSigner := keyer.NewPlainKeySigner(secret)
	var urls []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(*relayList, ",") {
		if raw == "" {
			continue
		}
		url := nostr.NormalizeURL(strings.TrimSpace(raw))
		if url == "" || seen[url] {
			return fail(stderr, "invalid or duplicate audit relay %q", raw)
		}
		seen[url] = true
		urls = append(urls, url)
	}
	if len(urls) == 0 {
		return fail(stderr, "explicit expected relays are required")
	}
	conn, err := db.Connect(ctx, cfg.DB, zap.NewNop())
	if err != nil {
		return fail(stderr, "connect legacy SQL: %v", cfg.DB.RedactError(err))
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fail(stderr, "start SQL snapshot: %v", err)
	}
	defer tx.Rollback(ctx)
	projection, err := readPolicyProjectionHint(ctx, tx, secret.Public().Hex())
	if err != nil {
		return fail(stderr, "read durable relay policy discovery hint: %v", err)
	}
	hint, hasHint, err := controlplane.PolicyCensusProjectionHint(projection, secret.Public())
	if err != nil {
		return fail(stderr, "validate durable relay policy discovery hint: %v", err)
	}
	bootstrap, err := controlplane.PolicyCensusBootstrapRelays(cfg.Nostr)
	if err != nil {
		return fail(stderr, "resolve configured bootstrap relays: %v", err)
	}
	if hasHint {
		bootstrap, err = controlplane.PolicyCensusHydrationRelaysForState(bootstrap, hint.State)
		if err != nil {
			return fail(stderr, "expand durable projection discovery hints: %v", err)
		}
	}
	bootstrapPool := nostradapter.NewRelayPool(bootstrap, zap.NewNop(), nostradapter.WithAuthSigner(authSigner))
	defer bootstrapPool.Close()
	bootstrapPool.Connect(ctx)
	initialHead, err := controlplane.ReadCanonicalRelayPolicyHead(ctx, bootstrapPool, secret.Public())
	if err != nil {
		return fail(stderr, "read canonical relay policy from bootstrap relays: %v", err)
	}
	if hasHint && (hint.EventID != initialHead.EventID || hint.PayloadHash != initialHead.PayloadHash) {
		return fail(stderr, "durable projection hint disagrees with signed canonical relay policy head")
	}
	if *expectedHead != "" && initialHead.EventID != *expectedHead {
		return fail(stderr, "signed canonical relay policy head changed: expected %s, found %s", *expectedHead, initialHead.EventID)
	}
	effective, err := controlplane.VerifyPolicyCensusRelays(cfg.Nostr, initialHead, urls)
	if err != nil {
		return fail(stderr, "bind audit relays to canonical policy: %v", err)
	}
	discovery, err := controlplane.PolicyCensusHydrationRelaysForState(bootstrap, initialHead.State)
	if err != nil {
		return fail(stderr, "expand canonical policy discovery relays: %v", err)
	}
	discoveryPool := nostradapter.NewRelayPool(discovery, zap.NewNop(), nostradapter.WithAuthSigner(authSigner))
	defer discoveryPool.Close()
	discoveryPool.Connect(ctx)
	discoveryHead, err := controlplane.ReadCanonicalRelayPolicyHead(ctx, discoveryPool, secret.Public())
	if err != nil {
		return fail(stderr, "verify canonical relay policy on expanded discovery relays: %v", err)
	}
	if discoveryHead.EventID != initialHead.EventID {
		return fail(stderr, "canonical relay policy changed across discovery relays")
	}
	pool := nostradapter.NewRelayPool(effective, zap.NewNop(), nostradapter.WithAuthSigner(authSigner))
	defer pool.Close()
	pool.Connect(ctx)
	effectiveHead, err := controlplane.ReadCanonicalRelayPolicyHead(ctx, pool, secret.Public())
	if err != nil {
		return fail(stderr, "read canonical relay policy from effective relays: %v", err)
	}
	if effectiveHead.EventID != initialHead.EventID {
		return fail(stderr, "canonical relay policy changed between bootstrap and effective relay sets")
	}
	limit := *maxRows
	if *pageSize > 0 {
		limit = *pageSize
	}
	var lowerBound *uuid.UUID
	if *afterID != "" {
		lowerBound = &cursor
	}
	rows, err := tx.Query(ctx, "SELECT id FROM deployment_policies WHERE ($1::uuid IS NULL OR id > $1) ORDER BY id LIMIT $2", lowerBound, limit+1)
	if err != nil {
		return fail(stderr, "read policy ids: %v", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fail(stderr, "scan policy id: %v", err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fail(stderr, "read policy ids: %v", err)
	}
	if *pageSize == 0 && len(ids) > *maxRows {
		return fail(stderr, "SQL policy census exceeds max-rows %d; no partial report", *maxRows)
	}
	hasMore := *pageSize > 0 && len(ids) > *pageSize
	if hasMore {
		ids = ids[:*pageSize]
	}
	report := policyCensusReport{ReadOnly: true, RelaySetType: "signed-canonical-policy-verified", PolicyEventID: initialHead.EventID, Relays: effective, Rows: make([]policyCensusRow, 0, len(ids))}
	if *pageSize > 0 {
		report.Page = &policyCensusPage{HasMore: hasMore}
		if *afterID != "" {
			report.Page.AfterID = *afterID
		}
		if len(ids) > 0 {
			report.Page.LastID = ids[len(ids)-1].String()
			if hasMore {
				report.Page.NextAfterID = report.Page.LastID
			}
		}
	}
	for _, id := range ids {
		result, err := nostradapter.CensusPolicyCoordinate(ctx, pool, secret.Public(), id)
		if err != nil {
			return fail(stderr, "policy %s history incomplete; no partial report: %v", id, err)
		}
		report.Rows = append(report.Rows, policyCensusRow{ID: id.String(), Status: "relay-present-sql-skipped", EventIDs: result.EventIDs})
	}
	if err := controlplane.RecheckCanonicalRelayPolicyHead(ctx, discoveryPool, secret.Public(), initialHead.EventID); err != nil {
		return fail(stderr, "reverify canonical relay policy on all discovery relays before report: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fail(stderr, "commit read-only SQL snapshot: %v", err)
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return fail(stderr, "encode census: %v", err)
	}
	return 0
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, format+"\n", args...)
	return 1
}

// The read-only SQL snapshot supplies discovery hints only. No projection
// field authorizes a relay head until the signed EVENT is read from every
// candidate relay and matched by event ID and canonical payload hash.
func readPolicyProjectionHint(ctx context.Context, tx pgx.Tx, author string) (*repository.RelayPolicyProjection, error) {
	row := tx.QueryRow(ctx, `SELECT author_pubkey, event_id, event_created_at, event_accepted_at, schema,
		canonical_payload, payload_hash, source_relay, last_sync_at, relay_confirmed_at
		FROM relay_policy_projections WHERE author_pubkey = $1`, author)
	projection := &repository.RelayPolicyProjection{}
	var confirmed sql.NullTime
	if err := row.Scan(&projection.AuthorPubkey, &projection.EventID, &projection.EventCreatedAt,
		&projection.EventAcceptedAt, &projection.Schema, &projection.CanonicalPayload,
		&projection.PayloadHash, &projection.SourceRelay, &projection.LastSyncAt, &confirmed); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if confirmed.Valid {
		projection.RelayConfirmedAt = &confirmed.Time
	}
	return projection, nil
}
