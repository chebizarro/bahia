// Command bahia-assistant-reencrypt re-establishes the assistant history a
// retired service identity sealed with its nsec-derived transcript key:
// execution checkpoints (4903), transcripts (30316) and session projections.
// It reads that history from the relays (REQ through EOSE), opens it with the
// key derived from the retired private key file, re-seals it under the active
// assistant-transcript system key held in Signet, and, with --execute,
// publishes it signed by the current service identity (nostr.signer). The
// retired key is used for nothing else, never leaves the process, and the
// daemon itself has no path that derives it. The procedure is in
// docs/runbooks/assistant-transcript-keys.md.
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

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

const (
	exitOK = 0
	// exitFailure is a usage, configuration, signer, key or relay error.
	exitFailure = 1
	// exitIncomplete means some history could not be read completely,
	// re-sealed or published; the run is idempotent, so re-run it.
	exitIncomplete = 2
)

// oldKeyEnvRefused names the variable whose presence makes the tool refuse to
// start: the retired key is read only from a file.
const oldKeyEnvRefused = "BAHIA_ASSISTANT_REENCRYPT_OLD_KEY"

const usage = `usage:
  bahia-assistant-reencrypt --config PATH --old-key-file PATH
      [--execute] [--max-attempts 5] [--read-timeout 2m] [--page-limit 500]

  --config        the daemon's configuration: nostr.signer (the current, rotated
                  service identity), its relays and nostr.publish_quorum,
                  secret_store and secret_store.system_keys
  --old-key-file  the retired nostr.private_key, exactly as the daemon was
                  configured with it (a regular file owned by this user, mode
                  0600 or stricter)
  --execute       sign and publish; without it the tool only reports the plan

It reads and writes every relay the daemon publishes assistant events to (the
control-plane relays and the service write relays, all of which must be in
nostr.controlled_relays) and counts an event as re-established only once
nostr.publish_quorum of them hold it.
`

// deps are the process dependencies tests replace.
type deps struct {
	loadConfig  func(path string) (*config.Config, error)
	openSigner  func(ctx context.Context, cfg *config.Config, admission *nostrout.Admission) (nostr.Keyer, error)
	resolveKeys func(ctx context.Context, cfg *config.Config, servicePubkey string, admission *nostrout.Admission) (*secretstore.Keyring, func(), error)
	admission   func(cfg *config.Config) (*nostrout.Admission, error)
	wait        func(context.Context, time.Duration) error
}

func defaultDeps() deps {
	logConfig := zap.NewDevelopmentConfig()
	logConfig.Level.SetLevel(zap.InfoLevel)
	logger, err := logConfig.Build()
	if err != nil {
		logger = zap.NewNop()
	}
	return deps{
		loadConfig: config.Load,
		openSigner: func(ctx context.Context, cfg *config.Config, admission *nostrout.Admission) (nostr.Keyer, error) {
			return servicesigner.Open(ctx, cfg.Nostr, servicesigner.Options{Admission: admission, Logger: logger})
		},
		resolveKeys: func(ctx context.Context, cfg *config.Config, servicePubkey string, admission *nostrout.Admission) (*secretstore.Keyring, func(), error) {
			return app.ResolveSystemKeys(ctx, cfg, servicePubkey, admission, zap.NewNop())
		},
		admission: func(cfg *config.Config) (*nostrout.Admission, error) {
			return nostrout.InitDefault(cfg.Nostr.Outbound.Admission())
		},
		wait: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

func fail(stderr io.Writer, format string, args ...any) int {
	// Failure diagnostics are best-effort; the exit status already signals failure.
	_, _ = fmt.Fprintf(stderr, "bahia-assistant-reencrypt: "+format+"\n", args...)
	return exitFailure
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	nostr.InfoLogger.SetOutput(io.Discard)
	nostr.DebugLogger.SetOutput(io.Discard)
	flags := flag.NewFlagSet("bahia-assistant-reencrypt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		// Flag usage has no error return; parsing still fails on invalid input.
		_, _ = fmt.Fprint(stderr, usage)
	}
	configPath := flags.String("config", "", "daemon configuration file")
	oldKeyFile := flags.String("old-key-file", "", "file holding the retired nostr.private_key")
	execute := flags.Bool("execute", false, "sign and publish the re-established history")
	maxAttempts := flags.Int("max-attempts", 5, "publish attempts per event (1..20)")
	readTimeout := flags.Duration("read-timeout", 2*time.Minute, "bound on reading one relay's history through EOSE")
	pageLimit := flags.Int("page-limit", 500, "REQ limit of each history page (1..5000)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitFailure
	}
	if *configPath == "" || *oldKeyFile == "" || *maxAttempts < 1 || *maxAttempts > 20 || *readTimeout <= 0 || *pageLimit < 1 || *pageLimit > 5000 {
		// Failure diagnostics are best-effort; the exit status already signals failure.
		_, _ = fmt.Fprint(stderr, usage)
		return exitFailure
	}
	cfg, err := d.loadConfig(*configPath)
	if err != nil {
		return fail(stderr, "load config: %v", err)
	}
	relays := assistantRelays(cfg.Nostr)
	if len(relays) == 0 {
		return fail(stderr, "the configuration names no control-plane or service relays")
	}
	if err := cfg.Nostr.ValidateControlPlaneRelays("bahia-assistant-reencrypt:", config.ControlPlaneRelays{Service: relays}); err != nil {
		return fail(stderr, "%v", err)
	}
	quorum, err := publishQuorum(cfg.Nostr.PublishQuorum, len(relays))
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if len(cfg.SecretStore.SystemKeys.AssistantTranscript) == 0 {
		return fail(stderr, "secret_store.system_keys.assistant_transcript is not configured: there is no key to re-encrypt under")
	}
	oldAuthor, oldKeys, err := retiredIdentity(*oldKeyFile)
	if err != nil {
		return fail(stderr, "--old-key-file: %v", err)
	}
	defer clear(oldKeys.Key.Key)
	admission, err := d.admission(cfg)
	if err != nil {
		return fail(stderr, "outbound admission: %v", err)
	}
	signer, err := d.openSigner(ctx, cfg, admission)
	if err != nil {
		return fail(stderr, "open the current service signer (nostr.signer): %v", err)
	}
	defer closeSigner(signer)
	newAuthor, err := signer.GetPublicKey(ctx)
	if err != nil {
		return fail(stderr, "read the current service pubkey: %v", err)
	}
	if newAuthor == oldAuthor {
		return fail(stderr, "nostr.signer is the retired identity %s: rotate the service key first", oldAuthor.Hex())
	}
	keys, release, err := d.resolveKeys(ctx, cfg, newAuthor.Hex(), admission)
	if err != nil {
		return fail(stderr, "resolve the assistant transcript system key from Signet: %v", err)
	}
	defer release()
	// Progress diagnostics are best-effort; they do not authorize publication.
	_, _ = fmt.Fprintf(stderr, "retired identity %s; current identity %s; relays %s (quorum %d)\n", oldAuthor.Hex(), newAuthor.Hex(), strings.Join(relays, " "), quorum)

	// The pool signs as the service, so it carries the daemon's relay rules:
	// only nostr.controlled_relays, never a public relay, checked again on
	// every EVENT frame.
	guard, err := relayguard.New(newAuthor, cfg.Nostr.ControlledRelaySet())
	if err != nil {
		return fail(stderr, "relay guard: %v", err)
	}
	pool := nostradapter.NewRelayPool(relays, zap.NewNop(), nostradapter.WithOutboundAdmission(admission), nostradapter.WithAuthSigner(signer),
		nostradapter.WithControlPlaneRelayGuard(guard), nostradapter.WithControlPlanePurpose())
	defer pool.Close()
	read := historyReader{pool: pool, relays: pool.URLs(), pageLimit: *pageLimit, timeout: *readTimeout}
	old, err := read.history(ctx, oldAuthor)
	if err != nil {
		// Failure diagnostics are best-effort; the exit status already signals failure.
		_, _ = fmt.Fprintf(stderr, "read the retired identity's history: %v\n", err)
		return exitIncomplete
	}
	current, err := read.history(ctx, newAuthor)
	if err != nil {
		// Failure diagnostics are best-effort; the exit status already signals failure.
		_, _ = fmt.Fprintf(stderr, "read the current identity's history: %v\n", err)
		return exitIncomplete
	}
	plan, err := service.PlanAssistantReencryption(ctx, service.AssistantReencryptInput{
		OldAuthor: oldAuthor, OldKeys: oldKeys, NewKeys: service.KeyringTranscriptKeys{Ring: keys}, Signer: signer,
		OldEvents: old.events(), NewEvents: current.events(), DryRun: !*execute,
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	topUps := current.underQuorum(newAuthor, quorum)
	if _, err := fmt.Fprintf(stdout, "retired history: %d events; already re-established: %d items\n", len(old), plan.Present); err != nil {
		return exitFailure
	}
	if _, err := fmt.Fprintf(stdout, "to publish: %d checkpoints, %d transcripts, %d session projections\n", plan.Checkpoints, plan.Transcripts, plan.Sessions); err != nil {
		return exitFailure
	}
	if _, err := fmt.Fprintf(stdout, "re-established events below quorum %d: %d\n", quorum, len(topUps)); err != nil {
		return exitFailure
	}
	for _, failure := range plan.Failures {
		if _, err := fmt.Fprintf(stdout, "cannot re-establish %s\n", failure); err != nil {
			return exitFailure
		}
	}
	status := exitOK
	if len(plan.Failures) > 0 {
		status = exitIncomplete
	}
	if !*execute {
		if _, err := fmt.Fprintln(stdout, "dry run: nothing signed or published; re-run with --execute"); err != nil {
			return exitFailure
		}
		return status
	}
	// Events the current identity already holds below quorum go first: the
	// plan's checkpoints may name them as predecessors.
	work := append(topUps, fresh(plan.Events)...)
	published, err := publishInOrder(ctx, pool, admission, work, quorum, *maxAttempts, d.wait)
	if _, err := fmt.Fprintf(stdout, "published %d of %d events to quorum\n", published, len(work)); err != nil {
		return exitFailure
	}
	if err != nil {
		// Failure diagnostics are best-effort; the exit status already signals failure.
		_, _ = fmt.Fprintf(stderr, "publish stopped: %v; re-run to resume\n", err)
		return exitIncomplete
	}
	return status
}

func closeSigner(signer nostr.Keyer) {
	if closer, ok := signer.(io.Closer); ok {
		_ = closer.Close()
	}
}
