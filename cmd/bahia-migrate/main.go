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
	"time"

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
	"       bahia-migrate [--config path] [--cutoff RFC3339] f74a-census\n" +
	"       bahia-migrate [--config path] --cutoff RFC3339 f74a-compact (read-only dry run)\n" +
	"       bahia-migrate [--config path] --confirm-quiesced f74a-import (stop daemon and all SQL writers first)\n" +
	"       bahia-migrate [--config path] [--confirm-quiesced] legacy-cutover (census; seal only when empty)\n" +
	"       bahia-migrate [--config path] --target default|control-plane [--after token] [--max-rows n] outbox-transfer (read-only inventory)\n" +
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
	confirmQuiesced := flags.Bool("confirm-quiesced", false, "confirm all daemon and SQL writers are stopped for offline cutover")
	cutoffText := flags.String("cutoff", "", "F74a census/compaction UTC cutoff in RFC3339 format")
	force := flags.Bool("force", false, "allow down across out-of-order applied history")
	to := flags.String("to", "", "full filename stem to retain when running down")
	dryRun := flags.Bool("dry-run", false, "nostr: report what would be migrated without signing or publishing")
	transferTarget := flags.String("target", "", "outbox-transfer: source publish target to inventory (default or control-plane)")
	transferAfter := flags.String("after", "", "outbox-transfer: continuation token printed by a prior inventory page")
	maxRows := flags.Int("max-rows", 1000, "outbox-transfer: maximum SQL rows inspected (1..10000)")
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
	if action == "f74a-compact" && *confirm {
		return reportError(stderr, "confirmed F74a compaction is disabled: concurrent backdated observations can turn dry-run candidates into material transitions")
	}
	if action == "f74a-import" && !*confirmQuiesced {
		return reportError(stderr, "f74a-import requires --confirm-quiesced; stop all daemon and SQL writers first")
	}
	if action != "f74a-import" && action != "legacy-cutover" && *confirmQuiesced {
		return reportError(stderr, "--confirm-quiesced is only valid for f74a-import or legacy-cutover")
	}
	if action != "down" && *confirm {
		return reportError(stderr, "--confirm is only valid for down")
	}
	if action != "down" && (*force || *to != "") {
		return reportError(stderr, "--force and --to are only valid for down")
	}
	if action != "nostr" && (*dryRun || *relays != "" || *relayBackfill) {
		return reportError(stderr, "--dry-run, --relays and --relay-backfill are only valid for nostr")
	}
	if action != "outbox-transfer" && (*transferTarget != "" || *transferAfter != "" || *maxRows != 1000) {
		return reportError(stderr, "outbox inventory flags are only valid for outbox-transfer")
	}
	if action == "outbox-transfer" {
		if *transferTarget != "default" && *transferTarget != "control-plane" {
			return reportError(stderr, "outbox-transfer requires --target default|control-plane")
		}
		if *maxRows < 1 || *maxRows > 10000 {
			return reportError(stderr, "outbox-transfer --max-rows must be in 1..10000")
		}
	}
	if action != "f74a-census" && action != "f74a-compact" && *cutoffText != "" {
		return reportError(stderr, "--cutoff is only valid for F74a actions")
	}
	var cutoff time.Time
	if *cutoffText != "" {
		var parseErr error
		cutoff, parseErr = time.Parse(time.RFC3339, *cutoffText)
		if parseErr != nil {
			return reportError(stderr, "invalid --cutoff: %v", parseErr)
		}
		cutoff = cutoff.UTC()
	}
	if action == "f74a-compact" {
		if cutoff.IsZero() {
			return reportError(stderr, "f74a-compact requires --cutoff")
		}
		if !cutoff.Before(time.Now().UTC()) {
			return reportError(stderr, "f74a-compact requires a past --cutoff")
		}
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
	if action == "outbox-transfer" {
		return runOutboxTransfer(ctx, repository.NewPgNostrEventRepository(pool), outboxTransferOptions{target: *transferTarget, after: *transferAfter, maxRows: *maxRows}, stdout, stderr)
	}
	if action == "f74a-import" {
		return runF74aImport(ctx, cfg, pool, stdout, stderr)
	}
	if action == "legacy-cutover" {
		return runLegacyCutover(ctx, pool, cfg.Nostr.LocalStore.ResolvedOutboxPath(), *confirmQuiesced, stdout, stderr)
	}
	if action == "f74a-census" || action == "f74a-compact" {
		return runF74aMaintenance(ctx, pool, action, cutoff, stdout, stderr)
	}
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
	case "status", "up", "down", "nostr", "f74a-census", "f74a-compact", "f74a-import", "legacy-cutover", "outbox-transfer":
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
// skipped). The daemon does not run it on startup.
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

// runF74aMaintenance is read-only. Deletion requires a separate design that
// remains safe under backdated concurrent observation inserts.
func runF74aMaintenance(ctx context.Context, pool *pgxpool.Pool, action string, cutoff time.Time, stdout, stderr io.Writer) int {
	census, err := repository.CensusF74a(ctx, pool, cutoff)
	if err != nil {
		return reportError(stderr, "F74a census: %v", err)
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{"cutoff", census.Cutoff.Format(time.RFC3339Nano)},
		{"observations", census.Observations},
		{"state_linked_observations", census.LinkedObservations},
		{"unlinked_observations", census.UnlinkedObservations},
		{"material_observation_runs", census.MaterialRuns},
		{"suppressible_observations_before_cutoff", census.SuppressibleObservations},
		{"package_rows", census.PackageRows},
		{"semantic_packages", census.SemanticPackages},
		{"duplicate_package_rows", census.DuplicatePackages},
		{"legacy_package_coordinates_to_tombstone", census.LegacyPackageCoordinates},
		{"releases", census.Releases},
		{"signatures", census.Signatures},
		{"sboms", census.SBOMs},
		{"estimated_f74a_publications", census.EstimatedPublications},
		{"outbox_pending", "unavailable_without_local_store"},
		{"outbox_failed", "unavailable_without_local_store"},
	} {
		if _, err := fmt.Fprintf(stdout, "%s\t%v\n", item.name, item.value); err != nil {
			return reportError(stderr, "writing F74a census: %v", err)
		}
	}
	if action == "f74a-census" {
		return 0
	}
	if _, err := fmt.Fprintln(stdout, "package_deletion\tdisabled_pending_semantic_tombstone_proof"); err != nil {
		return reportError(stderr, "writing F74a compaction status: %v", err)
	}
	if _, err := fmt.Fprintln(stdout, "observation_deletion\tdry_run_only"); err != nil {
		return reportError(stderr, "writing F74a dry run: %v", err)
	}
	return 0
}
