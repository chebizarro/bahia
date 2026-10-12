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
	"crypto/sha256"
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
	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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
  bahia-assistant-reencrypt --config PATH --old-key-file PATH --relay URL [--relay URL ...]
      [--execute] [--max-attempts 5] [--read-timeout 2m]

  --config        the daemon's configuration: nostr.signer (the current, rotated
                  service identity), secret_store and secret_store.system_keys
  --old-key-file  the retired nostr.private_key, exactly as the daemon was
                  configured with it (a regular file, owner-only)
  --relay         the daemon's control-plane relays, all in nostr.controlled_relays
                  (repeatable)
  --execute       sign and publish; without it the tool only reports the plan
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
	return deps{
		loadConfig: config.Load,
		openSigner: func(ctx context.Context, cfg *config.Config, admission *nostrout.Admission) (nostr.Keyer, error) {
			return servicesigner.Open(ctx, cfg.Nostr, servicesigner.Options{Admission: admission})
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

type relayFlags []string

func (r *relayFlags) String() string     { return strings.Join(*r, ",") }
func (r *relayFlags) Set(v string) error { *r = append(*r, v); return nil }

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "bahia-assistant-reencrypt: "+format+"\n", args...)
	return exitFailure
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	nostr.InfoLogger.SetOutput(io.Discard)
	nostr.DebugLogger.SetOutput(io.Discard)
	flags := flag.NewFlagSet("bahia-assistant-reencrypt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	configPath := flags.String("config", "", "daemon configuration file")
	oldKeyFile := flags.String("old-key-file", "", "file holding the retired nostr.private_key")
	var relayArgs relayFlags
	flags.Var(&relayArgs, "relay", "control-plane relay (repeatable)")
	execute := flags.Bool("execute", false, "sign and publish the re-established history")
	maxAttempts := flags.Int("max-attempts", 5, "publish attempts per event (1..20)")
	readTimeout := flags.Duration("read-timeout", 2*time.Minute, "bound on reading each identity's history through EOSE")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitFailure
	}
	if *configPath == "" || *oldKeyFile == "" || len(relayArgs) == 0 || *maxAttempts < 1 || *maxAttempts > 20 || *readTimeout <= 0 {
		fmt.Fprint(stderr, usage)
		return exitFailure
	}
	cfg, err := d.loadConfig(*configPath)
	if err != nil {
		return fail(stderr, "load config: %v", err)
	}
	relays := []string(relayArgs)
	if err := cfg.Nostr.ValidateControlPlaneRelays("bahia-assistant-reencrypt --relay:", config.ControlPlaneRelays{Service: relays}); err != nil {
		return fail(stderr, "%v", err)
	}
	if len(cfg.SecretStore.SystemKeys.AssistantTranscript) == 0 {
		return fail(stderr, "secret_store.system_keys.assistant_transcript is not configured: there is no key to re-encrypt under")
	}
	oldAuthor, oldKeys, err := retiredIdentity(*oldKeyFile)
	if err != nil {
		return fail(stderr, "--old-key-file: %v", err)
	}
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
	fmt.Fprintf(stderr, "retired identity %s; current identity %s\n", oldAuthor.Hex(), newAuthor.Hex())

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
	oldEvents, err := readHistory(ctx, pool, oldAuthor, *readTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "read the retired identity's history: %v\n", err)
		return exitIncomplete
	}
	newEvents, err := readHistory(ctx, pool, newAuthor, *readTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "read the current identity's history: %v\n", err)
		return exitIncomplete
	}
	plan, err := service.PlanAssistantReencryption(ctx, service.AssistantReencryptInput{
		OldAuthor: oldAuthor, OldKeys: oldKeys, NewKeys: service.KeyringTranscriptKeys{Ring: keys}, Signer: signer,
		OldEvents: oldEvents, NewEvents: newEvents, DryRun: !*execute,
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	fmt.Fprintf(stdout, "retired history: %d events; already re-established: %d items\n", len(oldEvents), plan.Present)
	fmt.Fprintf(stdout, "to publish: %d checkpoints, %d transcripts, %d session projections\n", plan.Checkpoints, plan.Transcripts, plan.Sessions)
	for _, failure := range plan.Failures {
		fmt.Fprintf(stdout, "cannot re-establish %s\n", failure)
	}
	status := exitOK
	if len(plan.Failures) > 0 {
		status = exitIncomplete
	}
	if !*execute {
		fmt.Fprintln(stdout, "dry run: nothing signed or published; re-run with --execute")
		return status
	}
	published, err := publishInOrder(ctx, pool, admission, plan.Events, *maxAttempts, d.wait)
	fmt.Fprintf(stdout, "published %d of %d events\n", published, len(plan.Events))
	if err != nil {
		fmt.Fprintf(stderr, "publish stopped: %v; re-run to resume\n", err)
		return exitIncomplete
	}
	return status
}

// retiredIdentity reads the retired private key and derives its pubkey and the
// v1 assistant transcript key the daemon derived from it: SHA-256 over
// "bahia assistant transcript key v1\x00" and the configured key text.
func retiredIdentity(path string) (nostr.PubKey, service.StaticAssistantTranscriptKeyProvider, error) {
	var none service.StaticAssistantTranscriptKeyProvider
	info, err := os.Lstat(path)
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nostr.PubKey{}, none, errors.New("must be a regular file readable by its owner only")
	}
	text, err := config.LoadPrivateKey(path, oldKeyEnvRefused)
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	keyer, err := nostrutil.NewLocalKeyer(text)
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	pubkey, err := keyer.GetPublicKey(context.Background())
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	sum := sha256.Sum256([]byte("bahia assistant transcript key v1\x00" + text))
	return pubkey, service.StaticAssistantTranscriptKeyProvider{Key: service.AssistantTranscriptKey{
		Ref: "assistant-transcript/service-nostr-key", Version: "v1", Rotation: "service-nostr-key", Key: sum[:],
	}}, nil
}

// readHistory reads author's assistant checkpoints, transcripts and session
// projections from every relay through EOSE. A relay that does not answer,
// CLOSEs or truncates its answer makes the read incomplete.
func readHistory(ctx context.Context, pool *nostradapter.RelayPool, author nostr.PubKey, timeout time.Duration) ([]nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	const everything = 1 << 20
	filters := []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.Kind(domain.AssistantExecutionCheckpointKind), nostr.Kind(domain.KindAssistantTranscript)}, Authors: []nostr.PubKey{author}, Limit: everything},
		{Kinds: []nostr.Kind{nostr.Kind(domain.KindAssistantSessionState)}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"t": {kinds.AssistantSessionTopic}}, Limit: everything},
	}
	sub, err := pool.SubscribeWithOptions(ctx, filters, nostradapter.SubscribeOptions{AwaitUnavailableRelays: true})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	var events []nostr.Event
	keep := func(ev *nostr.Event) {
		if ev != nil && ev.PubKey == author {
			events = append(events, *ev)
		}
	}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("stream ended before EOSE: %w", sub.StoredEventsIncomplete(nil))
			}
			keep(ev)
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil {
				return nil, err
			}
			// Each relay worker forwards its buffered EVENTs before it records
			// EOSE; take the ones already forwarded.
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return events, nil
					}
					keep(ev)
				default:
					return events, nil
				}
			}
		case <-ctx.Done():
			return nil, sub.StoredEventsIncomplete(ctx.Err())
		}
	}
}

// publishInOrder publishes events in plan order inside bounded bulk admission
// operations, so they are paced by the bulk lane instead of refused by a
// burst. An event counts as published once a relay accepts it (or already
// holds it). The first event no relay accepts after maxAttempts stops the
// run: a later checkpoint names its predecessor, and a re-run resumes on
// what was published.
func publishInOrder(ctx context.Context, pool *nostradapter.RelayPool, admission *nostrout.Admission, events []nostr.Event, maxAttempts int, wait func(context.Context, time.Duration) error) (int, error) {
	chunk := max(1, 2048/maxAttempts)
	published := 0
	for start := 0; start < len(events); start += chunk {
		batch := events[start:min(start+chunk, len(events))]
		op, err := admission.BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: len(batch) * maxAttempts})
		if err != nil {
			return published, fmt.Errorf("admit %d events: %w", len(batch), err)
		}
		opCtx := nostrout.WithOperation(ctx, op)
		for _, ev := range batch {
			if err := publishOne(opCtx, pool, ev, maxAttempts, wait); err != nil {
				op.Close()
				return published, err
			}
			published++
		}
		op.Close()
	}
	return published, nil
}

func publishOne(ctx context.Context, pool *nostradapter.RelayPool, ev nostr.Event, maxAttempts int, wait func(context.Context, time.Duration) error) error {
	backoff := nostradapter.DefaultBackoff()
	var reasons []string
	for attempt := 1; ; attempt++ {
		results, err := pool.PublishWithResults(ctx, ev)
		if errors.Is(err, nostrout.ErrKillSwitch) {
			return err
		}
		reasons = reasons[:0]
		for _, result := range results {
			if result.Accepted || result.IsDuplicate() {
				return nil
			}
			reason := result.Reason
			if result.Error != nil {
				reason = result.Error.Error()
			}
			reasons = append(reasons, result.RelayURL+": "+reason)
		}
		if err != nil && len(reasons) == 0 {
			reasons = append(reasons, err.Error())
		}
		if attempt >= maxAttempts {
			return fmt.Errorf("event %s (kind %d) was accepted by no relay after %d attempts: %s", ev.ID.Hex(), ev.Kind, attempt, strings.Join(reasons, "; "))
		}
		if err := wait(ctx, backoff.Next()); err != nil {
			return err
		}
	}
}

func closeSigner(signer nostr.Keyer) {
	if closer, ok := signer.(io.Closer); ok {
		_ = closer.Close()
	}
}
