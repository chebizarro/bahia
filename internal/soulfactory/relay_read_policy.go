package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"fiatjaf.com/nostr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RelayReadPolicy decides whether a stored-event read that did not get EOSE
// from every relay (a *RelayBusIncompleteError) may stand in for a complete
// one. The zero value is RelayReadComplete: partial reads fail closed.
//
// Every SoulFactory bus read names its policy explicitly. Decision table
// (bahia-irsry.27):
//
//	Caller                                       Read                          Policy          Why
//	-------------------------------------------  ----------------------------  --------------  ------------------------------------------------
//	Reactor.GetSoul                              31951 latest by d-tag         Complete        Feeds read-modify-write (lifecycle actions, full
//	                                                                                           provisioner, late-runtime projection), saga
//	                                                                                           observation and bootstrap absence checks; a
//	                                                                                           stale soul republished is a lost update.
//	Reactor.correlatedRuntimeRecoveryState       38384 / 5950 by event id      AllIDs          Ids are content-addressed and signed: a found
//	                                                                                           event is the event. Absence still fails closed.
//	Reactor.findExistingProvisioningResult       7950 by e-tag (idempotency)   Found           A found authoritative result is final. Absence
//	                                                                                           would re-run provisioning, so it needs every relay.
//	LifecycleHandler.findExistingTerminalResult  7950 / legacy by e-tag        Found           Same idempotency argument as above.
//	Reactor.listFleetReconcileSouls              31951 fan-out                 Complete        Reconcile republishes each soul; a missing or
//	                                                                                           stale soul is skipped or overwritten.
//	Reactor.getFleetConfigRevision               fleet config by event id      AllIDs          Content-addressed revision lookup.
//	Reactor.getProvisioningFleetConfig           fleet config latest           Complete        Carries auth/tools/mcp/hooks/plugins policy; a
//	                                                                                           stale revision baked into a new agent is not
//	                                                                                           repaired until the next revision is published.
//	Reactor.getProvisioningDraft                 31952 latest by ref or id     LatestQuorum    Operator-authored input; newest of a majority.
//	Reactor.getProvisioningTemplate              template latest by ref        LatestQuorum    Operator-authored input; newest of a majority.
//	communikeysMembership.latestDefinition       Communikeys definition        Complete        Authority for a membership grant.
//	communikeysMembership.latestProfileList      Communikeys profile list      Complete        Read-modify-write of a membership list: a stale
//	                                                                                           base silently revokes newer members.
//	concordMembership.resolveConcordInbox        10050 / 10002 latest          LatestQuorum    Routing lookup of replaceable relay lists.
//	concordMembership.fetchConcordControlPlane   Control Plane giftwraps       Complete        Folds grants and revocations (authority check).
//	NostrClient.ListSouls/GetSoul/ListTemplates  display reads (CLI, MCP)      LatestQuorum    Display only; actions are decided by the
//	                                                                                           factory's own complete reads.
//	runtimeControlAdapter.DiscoverCapabilities   30317 latest                  LatestQuorum    Freshness is bounded by MaxCapabilityAge and the
//	                                                                                           runtime enforces its own controller trust.
//	runtimeControlAdapter runtime relay list     10002 latest                  LatestQuorum    Routing only; a missed result surfaces as
//	                                                                                           *NoTerminalResultError.
//	RelayRuntimeValidationEventSource.LoadEvents events by id                  AllIDs          Validation judges exactly the named events.
//	OpenClaw sidecar readiness                   live REQ backfill             Complete        Controller trust and revocations; readiness
//	                                                                                           stays false until every relay sends EOSE.
//	Reactor.Run request backfill                 live REQ backfill             (continue)      Not a read: realtime processing continues and a
//	                                                                                           late relay's stored requests still arrive on the
//	                                                                                           same subscription; handlers are idempotent.
//	NIP29Membership                              writes only                   n/a
//
// Accepted partial reads are logged at Warn and counted in the
// bahia.soulfactory.relay_read.partial metric with outcome=accepted; rejected
// ones are counted with outcome=rejected and returned as the original error.
type RelayReadPolicy struct {
	name string
	// acceptPartial reports whether events, read with incomplete, may stand in
	// for a complete read. nil means never.
	acceptPartial func(events []*nostr.Event, incomplete *RelayBusIncompleteError) bool
}

// String names the policy for logs and metrics.
func (p RelayReadPolicy) String() string {
	if p.name == "" {
		return "complete"
	}
	return p.name
}

// RelayReadComplete fails closed: only a read with EOSE from every relay is a
// result. Use it whenever an absent or stale answer is unsafe.
func RelayReadComplete() RelayReadPolicy { return RelayReadPolicy{name: "complete"} }

// RelayReadLatestQuorum accepts a partial read once a strict majority of the
// queried relays sent EOSE. It is for latest-wins lookups of replaceable or
// addressable state: the caller keeps the newest event by (created_at, lowest
// id), see newerRelayEvent. With one or two relays a majority is every relay,
// so the policy only relaxes reads against three or more.
func RelayReadLatestQuorum() RelayReadPolicy {
	return RelayReadPolicy{name: "latest_quorum", acceptPartial: func(_ []*nostr.Event, incomplete *RelayBusIncompleteError) bool {
		answered := incomplete.Total - len(incomplete.Relays)
		return incomplete.Total > 0 && 2*answered > incomplete.Total
	}}
}

// RelayReadFound accepts a partial read that already holds an event satisfying
// found, for lookups where any authoritative match is final (idempotency
// checks). A partial read without a match fails closed, because absence can
// only be established by every relay.
func RelayReadFound(found func(*nostr.Event) bool) RelayReadPolicy {
	return RelayReadPolicy{name: "found", acceptPartial: func(events []*nostr.Event, _ *RelayBusIncompleteError) bool {
		for _, event := range events {
			if event != nil && found(event) {
				return true
			}
		}
		return false
	}}
}

// RelayReadAllIDs accepts a partial read that delivered a validly signed event
// for every id. Event ids are content hashes, so one relay's copy is the event.
func RelayReadAllIDs(ids ...nostr.ID) RelayReadPolicy {
	return RelayReadPolicy{name: "all_ids", acceptPartial: func(events []*nostr.Event, _ *RelayBusIncompleteError) bool {
		if len(ids) == 0 {
			return false
		}
		delivered := make(map[nostr.ID]struct{}, len(events))
		for _, event := range events {
			if event != nil && validSignedEvent(event) {
				delivered[event.ID] = struct{}{}
			}
		}
		for _, id := range ids {
			if _, ok := delivered[id]; !ok {
				return false
			}
		}
		return true
	}}
}

// RelayRead is a stored-event read resolved under a RelayReadPolicy.
type RelayRead struct {
	Events []*nostr.Event
	// Degraded is non-nil when the policy accepted a partial read. It names the
	// relays that did not send EOSE.
	Degraded *RelayBusIncompleteError
}

// QueryWithPolicy runs Query and resolves a partial result under policy.
// caller is a stable, low-cardinality label for logs and metrics.
func (b *SoulFactoryRelayBus) QueryWithPolicy(ctx context.Context, caller string, policy RelayReadPolicy, filters []nostr.Filter) (RelayRead, error) {
	events, err := b.Query(ctx, filters)
	var logger *slog.Logger
	if b != nil {
		logger = b.log()
	}
	return resolveRelayRead(ctx, logger, caller, policy, events, err)
}

// resolveRelayRead applies policy to what Query or CollectStoredEvents
// returned. Errors other than *RelayBusIncompleteError, and partial reads the
// caller cancelled, are returned unchanged.
func resolveRelayRead(ctx context.Context, logger *slog.Logger, caller string, policy RelayReadPolicy, events []*nostr.Event, err error) (RelayRead, error) {
	if err == nil {
		return RelayRead{Events: events}, nil
	}
	var incomplete *RelayBusIncompleteError
	if !errors.As(err, &incomplete) || errors.Is(incomplete.Cause, context.Canceled) {
		return RelayRead{}, err
	}
	if policy.acceptPartial == nil || !policy.acceptPartial(events, incomplete) {
		recordRelayReadPartial(ctx, caller, policy, "rejected")
		return RelayRead{}, err
	}
	recordRelayReadPartial(ctx, caller, policy, "accepted")
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("soul factory relay read degraded; accepting partial result",
		"caller", caller, "policy", policy.String(),
		"relays", incomplete.Total, "answered", incomplete.Total-len(incomplete.Relays),
		"events", len(events), "error", incomplete)
	return RelayRead{Events: events, Degraded: incomplete}, nil
}

// newerRelayEvent keeps the newer of two replaceable or addressable events:
// greatest created_at, then lowest event id (NIP-01), so readers converge.
func newerRelayEvent(current, candidate *nostr.Event) *nostr.Event {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.CreatedAt > current.CreatedAt ||
		(candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() < current.ID.Hex()) {
		return candidate
	}
	return current
}

const soulFactoryInstrumentationName = "github.com/openagentsinc/bahia/internal/soulfactory"

var relayReadPartialCounter = sync.OnceValue(func() metric.Int64Counter {
	counter, err := otel.Meter(soulFactoryInstrumentationName).Int64Counter(
		"bahia.soulfactory.relay_read.partial",
		metric.WithDescription("SoulFactory relay reads that ended without EOSE from every relay, by caller, policy and outcome (accepted or rejected)"),
	)
	if err != nil {
		otel.Handle(err)
		return nil
	}
	return counter
})

func recordRelayReadPartial(ctx context.Context, caller string, policy RelayReadPolicy, outcome string) {
	counter := relayReadPartialCounter()
	if counter == nil {
		return
	}
	counter.Add(context.WithoutCancel(ctx), 1, metric.WithAttributes(
		attribute.String("caller", caller),
		attribute.String("policy", policy.String()),
		attribute.String("outcome", outcome),
	))
}
