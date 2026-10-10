// Command bahia-policy-census reports which legacy SQL deployment-policy
// coordinates are already present on the selected relays. It never publishes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"go.uber.org/zap"
)

type policyCensusRow struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	EventIDs []string `json:"event_ids,omitempty"`
}

type policyCensusReport struct {
	ReadOnly     bool              `json:"read_only"`
	RelaySetType string            `json:"relay_set_type"`
	Relays       []string          `json:"relays"`
	Rows         []policyCensusRow `json:"rows"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bahia-policy-census", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config.yaml", "Bahia config path")
	relayList := flags.String("relays", "", "comma-separated relays to audit (operator supplied; not canonical policy proof)")
	maxRows := flags.Int("max-rows", 1000, "maximum SQL rows; census fails if more exist")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *maxRows < 1 || *maxRows > 10000 {
		fmt.Fprintln(stderr, "usage: bahia-policy-census --config path --relays url,... [--max-rows 1..10000]")
		return 1
	}
	cfg, err := config.LoadReadOnly(*path)
	if err != nil {
		return fail(stderr, "load read-only config: %v", err)
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(cfg.Nostr.PrivateKey))
	if err != nil {
		return fail(stderr, "valid service private key is required for signed author and relay AUTH: %v", err)
	}
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
		return fail(stderr, "explicit audit relays are required; config alone does not prove effective canonical policy")
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
	rows, err := tx.Query(ctx, "SELECT id FROM deployment_policies ORDER BY id LIMIT $1", *maxRows+1)
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
	if len(ids) > *maxRows {
		return fail(stderr, "SQL policy census exceeds max-rows %d; no partial report", *maxRows)
	}
	pool := nostradapter.NewRelayPool(urls, zap.NewNop(), nostradapter.WithPrivateKey(secret.Hex()))
	defer pool.Close()
	pool.Connect(ctx)
	report := policyCensusReport{ReadOnly: true, RelaySetType: "operator-supplied-unverified", Relays: pool.URLs(), Rows: make([]policyCensusRow, 0, len(ids))}
	for _, id := range ids {
		result, err := nostradapter.CensusPolicyCoordinate(ctx, pool, secret.Public(), id)
		if err != nil {
			return fail(stderr, "policy %s history incomplete; no partial report: %v", id, err)
		}
		status := "absent-on-selected-relays"
		if len(result.EventIDs) > 0 {
			status = "relay-present-sql-skipped"
		}
		report.Rows = append(report.Rows, policyCensusRow{ID: id.String(), Status: status, EventIDs: result.EventIDs})
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
