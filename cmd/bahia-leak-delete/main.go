// Command bahia-leak-delete removes the Bahia status events (kinds 30360,
// 31410 and 31411) whose d tag carried the service private key.
//
//	scan         lists, per relay, the ids of the leaked identity's status
//	             events (REQ paged until EOSE, NIP-42 AUTH when challenged)
//	delete       plans NIP-09 deletion requests naming those events by id only
//	             (e and k tags); with --execute signs them as the leaked
//	             identity and publishes them, recording every relay's answer
//	purge-local  removes the events from the daemon's local event store,
//	             outbox and PostgreSQL nostr_events (dry run by default)
//
// It never builds or publishes an a-tag (coordinate) deletion: the coordinate
// <kind>:<pubkey>:<d> of a leaked event contains the leaked key. It never
// prints, logs or records a d value or a leaked event. The procedure is in
// docs/runbooks/leaked-status-event-deletion.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"go.uber.org/zap"
)

const (
	exitOK = 0
	// exitFailure is a usage, configuration, signer or I/O error.
	exitFailure = 1
	// exitIncomplete means the command ran but some relay scan is incomplete
	// or some deletion is not yet accepted everywhere; re-run it.
	exitIncomplete = 2
)

const usage = `usage:
  bahia-leak-delete scan --old-pubkey PUBKEY --relay URL [--relay URL ...] --report PATH
      [--page-limit 100] [--page-timeout 30s] [--concurrency 4] [SIGNER FLAGS]
  bahia-leak-delete delete --old-pubkey PUBKEY --report PATH [--relay URL ...]
      [--batch-size 50] [--execute SIGNER FLAGS] [--max-attempts 5]
  bahia-leak-delete purge-local --old-pubkey PUBKEY [--event-store PATH] [--outbox PATH]
      [--db-config PATH] [--execute]

signer flags (the leaked identity; --signer-timeout applies to every method):
  --signer local  --key-file PATH
  --signer nip46  --bunker-uri-file PATH --client-key-file ABS_PATH
  --signer nip55l [--nip55l-bus ADDR] [--nip55l-app-id ID]
`

// deps are the process dependencies tests replace.
type deps struct {
	// admission paces relay publications and NIP-46 requests; nil uses the
	// process-wide controller.
	admission *nostrout.Admission
	// wait sleeps out a retry backoff; nil uses a timer.
	wait func(context.Context, time.Duration) error
	// newBackoff paces publish retries; nil uses nostradapter.DefaultBackoff.
	newBackoff func() *nostradapter.Backoff
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps{}))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	silenceLibraryLogs()
	if d.wait == nil {
		d.wait = waitTimer
	}
	if d.newBackoff == nil {
		d.newBackoff = nostradapter.DefaultBackoff
	}
	if len(args) == 0 {
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
	switch args[0] {
	case "scan":
		return runScan(ctx, args[1:], stdout, stderr, d)
	case "delete":
		return runDelete(ctx, args[1:], stdout, stderr, d)
	case "purge-local":
		return runPurge(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		if _, err := fmt.Fprint(stdout, usage); err != nil {
			return exitFailure
		}
		return exitOK
	default:
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
}

// silenceLibraryLogs stops fiatjaf.com/nostr from writing relay traffic to
// stderr: its info logger prints a whole event when a relay sends one that
// does not match the REQ, and a leaked event's d tag is the leaked key.
func silenceLibraryLogs() {
	nostr.InfoLogger.SetOutput(io.Discard)
	nostr.DebugLogger.SetOutput(io.Discard)
}

type relayFlags []string

func (r *relayFlags) String() string     { return strings.Join(*r, ",") }
func (r *relayFlags) Set(v string) error { *r = append(*r, v); return nil }

func fail(stderr io.Writer, format string, args ...any) int {
	// Failure or progress diagnostics are best-effort; they confer no authority.
	_, _ = fmt.Fprintf(stderr, "bahia-leak-delete: "+format+"\n", args...)
	return exitFailure
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		// Flag usage has no error return; parsing still reports failure.
		_, _ = fmt.Fprint(stderr, usage)
	}
	return flags
}

func newPool(relays []string, signer nostr.Signer, admission *nostrout.Admission) *nostradapter.RelayPool {
	opts := []nostradapter.RelayPoolOption{nostradapter.WithOutboundAdmission(admission)}
	if signer != nil {
		opts = append(opts, nostradapter.WithAuthSigner(signer))
	}
	return nostradapter.NewRelayPool(relays, zap.NewNop(), opts...)
}

// openVerifiedSigner opens the signer before anything else runs and keeps
// its session until the returned close function is called.
func openVerifiedSigner(ctx context.Context, flags signerFlags, oldPubkey nostr.PubKey, d deps, stderr io.Writer) (nostr.Keyer, func(), error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	keyer, err := flags.openSigner(sessionCtx, oldPubkey, d.admission, stderr)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	// Failure or progress diagnostics are best-effort; they confer no authority.
	_, _ = fmt.Fprintf(stderr, "signer reports the leaked identity %s\n", oldPubkey.Hex())
	return keyer, func() {
		closeSigner(keyer)
		cancel()
	}, nil
}

func runScan(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	flags := newFlagSet("scan", stderr)
	oldFlag := flags.String("old-pubkey", "", "the leaked service pubkey (64 hex or npub)")
	var relayArgs relayFlags
	flags.Var(&relayArgs, "relay", "relay to scan (repeatable)")
	reportPath := flags.String("report", "", "JSON report to create or update")
	pageLimit := flags.Int("page-limit", 100, "REQ limit per page (1..5000)")
	pageTimeout := flags.Duration("page-timeout", 30*time.Second, "bound on waiting for one page's EOSE")
	concurrency := flags.Int("concurrency", 4, "relays scanned at once (1..32)")
	var signer signerFlags
	signer.register(flags)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitFailure
	}
	if *pageLimit < 1 || *pageLimit > 5000 || *pageTimeout <= 0 || *concurrency < 1 || *concurrency > 32 || *reportPath == "" || len(relayArgs) == 0 {
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
	oldPubkey, err := parsePubkey(*oldFlag)
	if err != nil {
		return fail(stderr, "--old-pubkey: %v", err)
	}
	var authSigner nostr.Signer
	if signer.configured() {
		keyer, closeKeyer, err := openVerifiedSigner(ctx, signer, oldPubkey, d, stderr)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		defer closeKeyer()
		authSigner = keyer
	} else {
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprintln(stderr, "no signer: relays that require NIP-42 AUTH to read will be reported incomplete")
	}
	relays, err := normalizeRelays(relayArgs)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	r, err := loadOrNewReport(*reportPath, oldPubkey)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	pool := newPool(relays, authSigner, d.admission)
	defer pool.Close()
	scans, dValues := scanRelays(ctx, pool, relays, scanOptions{oldPubkey: oldPubkey, pageLimit: *pageLimit, pageTimeout: *pageTimeout, concurrency: *concurrency})
	for _, scan := range scans {
		r.putScan(scan)
	}
	if err := r.mergeFingerprints(dValues); err != nil {
		return fail(stderr, "%v", err)
	}
	if err := r.save(ctx, *reportPath); err != nil {
		return fail(stderr, "%v", err)
	}
	complete := true
	for _, scan := range scans {
		if err := printScan(stdout, scan, r); err != nil {
			return exitFailure
		}
		complete = complete && scan.Status == scanComplete && !scan.Truncated
	}
	if !complete {
		return exitIncomplete
	}
	return exitOK
}

// printScan writes one relay's result: ids, kinds and created_at only.
func printScan(w io.Writer, scan relayScan, r *report) error {
	byKind := map[int]int{}
	for _, ev := range scan.Events {
		byKind[ev.Kind]++
	}
	line := fmt.Sprintf("relay %s: %s, %d events in %d pages", scan.Relay, scan.Status, len(scan.Events), scan.Pages)
	for _, kind := range leakedKinds {
		line += fmt.Sprintf(" %d=%d", kind, byKind[int(kind)])
	}
	if scan.Truncated {
		line += ", truncated (more events share one second than a page returned; raise --page-limit)"
	}
	if scan.Invalid > 0 {
		line += fmt.Sprintf(", %d invalid events dropped", scan.Invalid)
	}
	if scan.Reason != "" {
		line += ": " + scan.Reason
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	accepted := map[string]struct{}{}
	for _, del := range r.Deletions {
		if outcome := del.Relays[scan.Relay]; outcome != nil && outcome.done() {
			for _, target := range del.Targets {
				accepted[target.ID] = struct{}{}
			}
		}
	}
	stillServed := 0
	for _, ev := range scan.Events {
		extra := ""
		if _, ok := accepted[ev.ID]; ok {
			extra = " (deletion accepted by this relay, still served)"
			stillServed++
		}
		if _, err := fmt.Fprintf(w, "  %s kind=%d created_at=%s%s\n", ev.ID, ev.Kind, time.Unix(ev.CreatedAt, 0).UTC().Format(time.RFC3339), extra); err != nil {
			return err
		}
	}
	if stillServed > 0 {
		if _, err := fmt.Fprintf(w, "  %d events are still served although this relay accepted their deletion\n", stillServed); err != nil {
			return err
		}
	}
	return nil
}

func runDelete(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	flags := newFlagSet("delete", stderr)
	oldFlag := flags.String("old-pubkey", "", "the leaked service pubkey (64 hex or npub)")
	reportPath := flags.String("report", "", "JSON report written by scan")
	var relayArgs relayFlags
	flags.Var(&relayArgs, "relay", "additional relay to publish the deletions to (repeatable); every scanned relay is a target")
	batchSize := flags.Int("batch-size", 50, "events per deletion request (1..500)")
	execute := flags.Bool("execute", false, "sign and publish; without it, only print the plan")
	maxAttempts := flags.Int("max-attempts", 5, "delivery rounds in this run; rate-limited, error: and transport failures are retried in the next round (1..20)")
	var signer signerFlags
	signer.register(flags)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitFailure
	}
	if *reportPath == "" || *batchSize < 1 || *batchSize > maxBatchSize || *maxAttempts < 1 || *maxAttempts > 20 {
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
	if *execute && !signer.configured() {
		return fail(stderr, "--execute signs as the leaked identity and needs --signer")
	}
	oldPubkey, err := parsePubkey(*oldFlag)
	if err != nil {
		return fail(stderr, "--old-pubkey: %v", err)
	}
	var keyer nostr.Keyer
	if signer.configured() {
		var closeKeyer func()
		keyer, closeKeyer, err = openVerifiedSigner(ctx, signer, oldPubkey, d, stderr)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		defer closeKeyer()
	}

	r, err := loadReport(*reportPath, oldPubkey)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	extra, err := normalizeRelays(relayArgs)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	relays, err := normalizeRelays(append(r.relayURLs(), extra...))
	if err != nil {
		return fail(stderr, "%v", err)
	}
	for _, scan := range r.Scans {
		if scan.Status != scanComplete || scan.Truncated {
			// Failure or progress diagnostics are best-effort; they confer no authority.
			_, _ = fmt.Fprintf(stderr, "WARNING: the scan of %s is incomplete; events it missed are not in this plan (re-run scan)\n", scan.Relay)
		}
	}
	guard, err := r.guard(oldPubkey)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	for _, del := range r.Deletions {
		if err := guard.checkSigned(del.Event); err != nil {
			return fail(stderr, "recorded deletion %s: %v", del.Event.ID.Hex(), err)
		}
	}
	planned := planDeletions(r, oldPubkey, *batchSize, nostr.Now())
	for _, del := range planned {
		if err := guard.check(del.Event); err != nil {
			return fail(stderr, "planned deletion: %v", err)
		}
	}
	if len(r.Deletions)+len(planned) == 0 {
		if _, err := fmt.Fprintln(stdout, "nothing to delete: the scans found no leaked events"); err != nil {
			return exitFailure
		}
		return exitOK
	}
	if err := printPlan(stdout, r.Deletions, planned, relays); err != nil {
		return exitFailure
	}
	if !*execute {
		if _, err := fmt.Fprintln(stdout, "dry run: nothing was signed or published; add --execute and the signer flags to publish"); err != nil {
			return exitFailure
		}
		return exitOK
	}

	for _, del := range planned {
		ev := del.Event
		if err := keyer.SignEvent(ctx, &ev); err != nil {
			return fail(stderr, "sign deletion request: %v", err)
		}
		if err := guard.checkSigned(ev); err != nil {
			return fail(stderr, "signed deletion request: %v", err)
		}
		del.Event = ev
		r.Deletions = append(r.Deletions, del)
	}
	for i := range r.Deletions {
		for _, relay := range relays {
			if r.Deletions[i].Relays[relay] == nil {
				r.Deletions[i].Relays[relay] = &relayOutcome{Status: outcomePending}
			}
		}
	}
	// The signed requests are on disk before the first publish, so a resumed
	// run republishes exactly these events.
	if err := r.save(ctx, *reportPath); err != nil {
		return fail(stderr, "%v", err)
	}

	admission := nostrout.Or(d.admission)
	pool := newPool(relays, keyer, admission)
	defer pool.Close()
	p := &publisher{
		pool: pool, admission: admission, guard: guard, report: r, reportPath: *reportPath,
		maxAttempts: *maxAttempts, wait: d.wait, newBackoff: d.newBackoff, now: time.Now, out: stdout,
	}
	runErr := p.run(ctx, relays)
	complete, outputErr := summarize(stdout, r, relays)
	if outputErr != nil {
		return exitFailure
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fail(stderr, "%v", runErr)
	}
	if !complete {
		return exitIncomplete
	}
	return exitOK
}

func runPurge(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("purge-local", stderr)
	oldFlag := flags.String("old-pubkey", "", "the leaked service pubkey (64 hex or npub)")
	eventStore := flags.String("event-store", "", "the daemon's local event store (nostr.local_store.path)")
	outbox := flags.String("outbox", "", "the daemon's outbox (nostr.local_store.outbox_path)")
	dbConfig := flags.String("db-config", "", "Bahia config file whose db section names the PostgreSQL database")
	execute := flags.Bool("execute", false, "remove; without it, only list")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitFailure
	}
	if *eventStore == "" && *outbox == "" && *dbConfig == "" {
		// Failure or progress diagnostics are best-effort; they confer no authority.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
	oldPubkey, err := parsePubkey(*oldFlag)
	if err != nil {
		return fail(stderr, "--old-pubkey: %v", err)
	}
	if *eventStore != "" {
		if err := purgeEventStore(stdout, *eventStore, oldPubkey, *execute); err != nil {
			return fail(stderr, "%v", err)
		}
	}
	if *outbox != "" {
		if err := purgeOutbox(stdout, *outbox, oldPubkey, *execute); err != nil {
			return fail(stderr, "%v", err)
		}
	}
	if *dbConfig != "" {
		if err := purgePostgres(ctx, stdout, *dbConfig, oldPubkey, *execute); err != nil {
			return fail(stderr, "%v", err)
		}
	}
	if !*execute {
		if _, err := fmt.Fprintln(stdout, "dry run: nothing was removed; add --execute to remove"); err != nil {
			return exitFailure
		}
	}
	return exitOK
}
