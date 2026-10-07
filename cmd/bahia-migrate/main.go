// Command bahia-migrate performs explicit database migration operations without
// starting the Bahia server: SQL schema migrations (status, up, down) and the
// one-off conversion of legacy Bahia nostr_events to canonical events (nostr).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/nostrmigration"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/strutil"
	"go.uber.org/zap"
)

const usage = "usage: bahia-migrate [--config path] [--confirm] [--force] [--to stem] status|up|down\n" +
	"       bahia-migrate [--config path] [--dry-run] [--relays url,...] [--relay-backfill] nostr"

func main() {
	os.Exit(mainExit())
}

func mainExit() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

// reportError preserves the failing exit status even if stderr is unavailable.
func reportError(stderr io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(stderr, format+"\n", args...); err != nil {
		return 1
	}
	return 1
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bahia-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.yaml", "Bahia configuration file")
	confirm := flags.Bool("confirm", false, "confirm destructive down migration")
	force := flags.Bool("force", false, "allow down across out-of-order applied history")
	to := flags.String("to", "", "full filename stem to retain when running down")
	dryRun := flags.Bool("dry-run", false, "nostr: report what would be migrated without signing or publishing")
	relays := flags.String("relays", "", "nostr: comma-separated relays to publish to (default: sidecar plus nostr.relays)")
	relayBackfill := flags.Bool("relay-backfill", false, "nostr: also read legacy events back from the relays (default: nostr.legacy_relay_backfill)")
	action := ""
	if len(args) > 0 && isAction(args[0]) {
		action, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if action == "" && flags.NArg() == 1 {
		action = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return reportError(stderr, usage)
	}
	if !isAction(action) {
		return reportError(stderr, "unknown migration action %q", action)
	}
	if action != "down" && (*confirm || *force || *to != "") {
		return reportError(stderr, "--confirm, --force and --to are only valid for down")
	}
	if action != "nostr" && (*dryRun || *relays != "" || *relayBackfill) {
		return reportError(stderr, "--dry-run, --relays and --relay-backfill are only valid for nostr")
	}
	if action == "down" && !*confirm {
		return reportError(stderr, "down requires --confirm")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return reportError(stderr, "loading Bahia config: %v", err)
	}
	pool, err := db.Connect(ctx, cfg.DB, zap.NewNop())
	if err != nil {
		return reportError(stderr, "%v", cfg.DB.RedactError(err))
	}
	defer pool.Close()
	if action == "nostr" {
		return runNostrMigration(ctx, cfg, pool, nostrMigrationOptions{
			dryRun:        *dryRun,
			relays:        strutil.SplitCSV(*relays),
			relayBackfill: *relayBackfill || cfg.Nostr.LegacyRelayBackfill,
		}, stdout, stderr)
	}
	return runAction(ctx, pool, action, db.DownOptions{To: *to, Confirm: *confirm, Force: *force}, stdout, stderr)
}

func isAction(value string) bool {
	switch value {
	case "status", "up", "down", "nostr":
		return true
	default:
		return false
	}
}

type nostrMigrationOptions struct {
	dryRun        bool
	relays        []string
	relayBackfill bool
}

// runNostrMigration converts legacy Bahia events recorded in nostr_events to
// canonical events, signs them with the service key and publishes them. It is
// resumable (durable cursors) and idempotent (already-migrated records are
// skipped). The daemon no longer runs it on startup (B-28).
func runNostrMigration(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, opts nostrMigrationOptions, stdout, stderr io.Writer) int {
	privateKey := strings.TrimSpace(cfg.Nostr.PrivateKey)
	if privateKey == "" && !opts.dryRun {
		return reportError(stderr, "nostr migration signs with nostr.private_key; set it or use --dry-run")
	}
	relayURLs := opts.relays
	if len(relayURLs) == 0 {
		relayURLs = defaultMigrationRelays(cfg.Nostr)
	}
	if len(relayURLs) == 0 && !opts.dryRun {
		return reportError(stderr, "nostr migration needs relays: configure nostr.relays or the sidecar, or pass --relays")
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return reportError(stderr, "creating logger: %v", err)
	}
	defer func() { _ = logger.Sync() }()

	relayPool := nostradapter.NewRelayPool(relayURLs, logger, nostradapter.WithPrivateKey(privateKey))
	defer relayPool.Close()
	relayPool.Connect(ctx)

	runner := nostrmigration.NewRunner(repository.NewPgNostrEventRepository(pool), nostrmigration.NewRelayPoolPublisher(relayPool), relayPool, nostrmigration.Config{
		PrivateKey:    privateKey,
		RelayBackfill: opts.relayBackfill && len(relayURLs) > 0,
		DryRun:        opts.dryRun,
	}, logger)
	if err := runner.Run(ctx); err != nil {
		return reportError(stderr, "nostr migration: %v", err)
	}
	if _, err := fmt.Fprintln(stdout, "nostr migration complete"); err != nil {
		return reportError(stderr, "writing result: %v", err)
	}
	return 0
}

// defaultMigrationRelays publishes where the daemon's canonical state lives:
// the relay sidecar first, then the configured interop relays.
func defaultMigrationRelays(cfg config.NostrConfig) []string {
	var relays []string
	seen := map[string]struct{}{}
	add := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		relays = append(relays, url)
	}
	if cfg.Sidecar.Enabled {
		if cfg.Sidecar.BackendURL != "" {
			add(cfg.Sidecar.BackendURL)
		} else {
			add(cfg.Sidecar.PublicURL)
		}
	}
	for _, url := range cfg.Relays {
		add(url)
	}
	return relays
}

func runAction(ctx context.Context, pool *pgxpool.Pool, action string, down db.DownOptions, stdout, stderr io.Writer) int {
	logger := zap.NewNop()
	switch action {
	case "status":
		status, err := db.Status(ctx, pool, logger)
		if err != nil {
			return reportError(stderr, "%v", err)
		}
		for _, item := range status.Applied {
			if _, err := fmt.Fprintf(stdout, "applied\t%s\t%s\n", item.Version, item.AppliedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
				return reportError(stderr, "writing status: %v", err)
			}
		}
		for _, version := range status.Pending {
			if _, err := fmt.Fprintf(stdout, "pending\t%s\n", version); err != nil {
				return reportError(stderr, "writing status: %v", err)
			}
		}
		if len(status.Pending) != 0 {
			return 2
		}
		return 0
	case "up":
		if err := db.Migrate(ctx, pool, logger); err != nil {
			return reportError(stderr, "%v", err)
		}
		if _, err := fmt.Fprintln(stdout, "migrations up to date"); err != nil {
			return reportError(stderr, "writing result: %v", err)
		}
		return 0
	case "down":
		versions, err := db.Down(ctx, pool, logger, down)
		for _, version := range versions {
			if _, writeErr := fmt.Fprintf(stdout, "rolled back\t%s\n", version); writeErr != nil {
				return reportError(stderr, "writing result: %v", writeErr)
			}
		}
		if err != nil {
			return reportError(stderr, "%v", err)
		}
		if len(versions) == 0 {
			if _, err := fmt.Fprintln(stdout, "target already current; no migrations rolled back"); err != nil {
				return reportError(stderr, "writing result: %v", err)
			}
		}
		return 0
	default:
		return reportError(stderr, "invalid migration action")
	}
}
