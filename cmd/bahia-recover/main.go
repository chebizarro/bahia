package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/identityrecovery"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/openagentsinc/bahia/internal/version"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// The staging gate verifies the binary inside the immutable release image
	// matches the image's OCI revision; print both forms for that check.
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, _ = fmt.Fprintf(stdout, "%s commit=%s\n", version.Semantic(), version.Commit)
		return 0
	}
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: bahia-recover {plan|apply|status|resume|abort|report-drafts|attest-readiness|attest-ack} --config FILE [--confirm-phase PHASE|--confirm-abort|--consumer ID --evidence-ids IDS [--waiver-reason consumer_disabled|consumer_isolated|consumer_decommissioned]] [--confirm-daemon-unreachable] [--rotation-updates FILE]")
		return 2
	}
	op := args[0]
	if op != "plan" && op != "apply" && op != "status" && op != "resume" && op != "abort" && op != "report-drafts" && op != "attest-readiness" && op != "attest-ack" {
		_, _ = fmt.Fprintln(stderr, "unknown recovery command")
		return 2
	}
	flags := flag.NewFlagSet(op, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "Bahia config file")
	phase := flags.String("confirm-phase", "", "exact next phase")
	abort := flags.Bool("confirm-abort", false, "confirm pre-cutover abort")
	daemonUnreachable := flags.Bool("confirm-daemon-unreachable", false, "operator confirms the old daemon is already unreachable")
	rotationUpdates := flags.String("rotation-updates", "", "operator-supplied credential rotation evidence JSON")
	draftPath := flags.String("draft-report", "", "unsigned SQL review draft artifact path")
	consumer := flags.String("consumer", "", "consumer id for operator-signed readiness or waiver")
	evidenceIDs := flags.String("evidence-ids", "", "comma-separated event IDs proving enforcement or consumer isolation")
	waiverReason := flags.String("waiver-reason", "", "consumer_disabled, consumer_isolated, or consumer_decommissioned")
	if err := flags.Parse(args[1:]); err != nil || *path == "" || len(flags.Args()) != 0 {
		_, _ = fmt.Fprintln(stderr, "config file and valid flags required")
		return 2
	}
	cfg, err := config.LoadReadOnly(*path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "configuration could not be loaded")
		return 1
	}
	if err = cfg.ValidateIdentityRecovery(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	root, err := nostr.PubKeyFromHex(cfg.IdentityRecovery.Operator.PublicKey)
	if err != nil {
		return 1
	}
	old, err := nostr.PubKeyFromHex(cfg.IdentityRecovery.OldPubkey)
	if err != nil {
		return 1
	}
	r := cfg.IdentityRecovery
	if op == "status" || op == "plan" {
		state, readErr := identityrecovery.LoadStateReadOnly(r.LedgerPath)
		if readErr != nil {
			_, _ = fmt.Fprintln(stderr, "recovery ledger unavailable")
			return 1
		}
		if state.IncidentID != "" && (state.OldPubkey != old.Hex() || state.OperatorPubkey != root.Hex()) {
			_, _ = fmt.Fprintln(stderr, "recovery ledger identity mismatch")
			return 1
		}
		next, has := state.Next()
		view := struct {
			State   identityrecovery.State   `json:"state"`
			Next    identityrecovery.Phase   `json:"next_phase,omitempty"`
			HasNext bool                     `json:"has_next"`
			Phases  []identityrecovery.Phase `json:"phases,omitempty"`
		}{State: state, Next: next, HasNext: has}
		if op == "plan" {
			view.Phases = identityrecovery.Phases
		}
		if err = json.NewEncoder(stdout).Encode(view); err != nil {
			return 1
		}
		return 0
	}
	if op == "abort" && !(*abort) {
		_, _ = fmt.Fprintln(stderr, "--confirm-abort required")
		return 2
	}
	if (op == "apply" || op == "resume") && strings.TrimSpace(*phase) == "" {
		_, _ = fmt.Fprintln(stderr, "--confirm-phase required")
		return 2
	}
	if op == "attest-readiness" && (*consumer == "" || *evidenceIDs == "" || (*waiverReason != "" && !trustepoch.ValidWaiverReason(*waiverReason))) {
		_, _ = fmt.Fprintln(stderr, "attest-readiness requires --consumer, --evidence-ids, and an optional closed-enum --waiver-reason")
		return 2
	}
	if op == "attest-ack" && (*consumer != "cli" && *consumer != "web" && *consumer != "pinned-runtimes" && *consumer != "loom" && *consumer != "hiveci" && *consumer != "soulfactory") {
		_, _ = fmt.Fprintln(stderr, "attest-ack requires --consumer cli, web, pinned-runtimes, loom, hiveci, or soulfactory")
		return 2
	}
	runtime, err := identityrecovery.NewProductionRuntime(ctx, cfg, identityrecovery.RuntimeOptions{ConfirmDaemonUnreachable: *daemonUnreachable, RotationUpdatesPath: *rotationUpdates, DraftPath: *draftPath})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "recovery runtime unavailable:", err)
		return 1
	}
	defer func() { _ = runtime.Close() }()
	engine := runtime.Engine
	sqlDrafts := runtime.SQLDrafts
	if *draftPath == "" {
		*draftPath = r.LedgerPath + ".sql-review-drafts.json"
	}
	if op == "report-drafts" {
		if _, err := sqlDrafts.Reassert(ctx, nil); err != nil {
			_, _ = fmt.Fprintln(stderr, "unsigned SQL review draft report failed:", err)
			return 1
		}
		if _, err := sqlDrafts.Verify(ctx); err != nil {
			_, _ = fmt.Fprintln(stderr, "unsigned SQL review draft verification failed:", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, *draftPath)
		return 0
	}
	switch op {
	case "abort":
		err = engine.Abort(ctx)
	case "attest-readiness":
		var ids []string
		if *evidenceIDs != "" {
			ids = strings.Split(*evidenceIDs, ",")
		}
		err = engine.AttestReadiness(ctx, *consumer, ids, *waiverReason)
	case "attest-ack":
		err = engine.AttestAcknowledgment(ctx, *consumer)
	case "resume":
		err = engine.Resume(ctx, identityrecovery.Phase(*phase))
	default:
		err = engine.Apply(ctx, identityrecovery.Phase(*phase))
	}
	if err != nil {
		if errors.Is(err, identityrecovery.ErrAdapterNotRegistered) {
			_, _ = fmt.Fprintln(stderr, "phase adapter not registered; phase remains incomplete")
		} else {
			_, _ = fmt.Fprintln(stderr, "recovery phase failed:", err)
		}
		return 1
	}
	switch op {
	case "attest-readiness":
		_, _ = fmt.Fprintln(stdout, "consumer readiness or waiver published to controlled relays")
	case "attest-ack":
		_, _ = fmt.Fprintln(stdout, "operator consumer acknowledgment published to controlled relays")
	default:
		_, _ = fmt.Fprintln(stdout, "recovery phase recorded")
	}
	return 0
}
