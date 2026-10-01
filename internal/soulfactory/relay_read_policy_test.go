package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

// fakeRelayAnswer is how one fake relay answers the initial REQ.
type fakeRelayAnswer int

const (
	relayAnswersEOSE fakeRelayAnswer = iota
	relayAnswersClosed
	relayStaysSilent
)

type fakeRelayScript struct {
	answer fakeRelayAnswer
	events []*nostr.Event
}

// neverReissueBackoff keeps a relay that CLOSED from reissuing its REQ
// within a test: it stays settled until the read ends.
func neverReissueBackoff() *nostradapter.Backoff {
	return &nostradapter.Backoff{Initial: time.Hour, Max: time.Hour}
}

// newScriptedRelayClient builds a bus over fake relays that answer the first REQ
// per script. It returns the bus and the relay URLs in script order.
func newScriptedRelayClient(t *testing.T, logger *slog.Logger, scripts ...fakeRelayScript) (*RelayClient, []string) {
	t.Helper()
	endpoints := make([]*fakeRelayEndpoint, 0, len(scripts))
	urls := make([]string, 0, len(scripts))
	for _, script := range scripts {
		endpoint := newFakeRelayEndpoint(t)
		sub := newFakeRelaySubscription()
		sub.events = make(chan *nostr.Event, len(script.events)+1)
		for _, event := range script.events {
			sub.events <- event
		}
		switch script.answer {
		case relayAnswersEOSE:
			close(sub.eose)
		case relayAnswersClosed:
			sub.closed <- "error: shutting down"
		}
		endpoint.subscribeQueue <- sub
		endpoints = append(endpoints, endpoint)
		urls = append(urls, endpoint.url)
	}
	bus, err := newRelayClientFromEndpoints(endpoints, withRelayResubscribeBackoff(neverReissueBackoff), WithRelayLogger(logger))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	t.Cleanup(bus.Close)
	return bus, urls
}

// recordingLogHandler keeps every record for assertions.
type recordingLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}
func (h *recordingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingLogHandler) WithGroup(string) slog.Handler      { return h }

// degradedCallers returns the caller attribute of every degraded-read warning.
func (h *recordingLogHandler) degradedCallers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var callers []string
	for _, record := range h.records {
		if record.Level != slog.LevelWarn || record.Message != "soul factory relay read degraded; accepting partial result" {
			continue
		}
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "caller" {
				callers = append(callers, attr.Value.String())
			}
			return true
		})
	}
	return callers
}

func signedSoulFactoryEventAt(t *testing.T, signer fakeSigner, kind int, createdAt nostr.Timestamp, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: createdAt, Tags: tags, Content: content}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}

func assertIncompleteRelay(t *testing.T, err error, relay string, status RelayStoredEventsStatus) {
	t.Helper()
	var incomplete *RelayReadIncompleteError
	if !errors.As(err, &incomplete) || !errors.Is(err, ErrRelayReadIncomplete) {
		t.Fatalf("error = %v, want *RelayReadIncompleteError", err)
	}
	if incomplete.Total != 3 {
		t.Fatalf("incomplete.Total = %d, want 3", incomplete.Total)
	}
	if len(incomplete.Relays) != 1 || incomplete.Relays[0].RelayURL != relay || incomplete.Relays[0].Status != status {
		t.Fatalf("incomplete relays = %+v, want only %s %s", incomplete.Relays, relay, status)
	}
}

// A latest-wins lookup under RelayReadLatestQuorum: two of three relays send
// EOSE, the third never answers (silent until the caller's deadline) or CLOSES
// the REQ. The caller gets the newest template across the answering relays, and
// the degradation is reported (RelayRead.Degraded and a Warn log), not failed.
func TestRelayReadQuorumLookupReturnsNewestAndReportsDegradation(t *testing.T) {
	signer := newFakeSigner(t)
	now := nostr.Now()
	older := signedSoulFactoryEventAt(t, signer, domain.KindSoulTemplate, now-100, nostr.Tags{{tagParameterizedD, "research"}, {"name", "Research v1"}}, "v1")
	newer := signedSoulFactoryEventAt(t, signer, domain.KindSoulTemplate, now-10, nostr.Tags{{tagParameterizedD, "research"}, {"name", "Research v2"}}, "v2")

	for name, third := range map[string]fakeRelayAnswer{"silent relay": relayStaysSilent, "CLOSED relay": relayAnswersClosed} {
		t.Run(name, func(t *testing.T) {
			logs := &recordingLogHandler{}
			logger := slog.New(logs)
			bus, urls := newScriptedRelayClient(t, logger,
				fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{older}},
				fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{newer}},
				fakeRelayScript{answer: third},
			)
			wantStatus := RelayStoredEventsPending
			ctx := t.Context()
			if third == relayStaysSilent {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, relayBusCallerDeadline)
				defer cancel()
			} else {
				wantStatus = RelayStoredEventsClosed
			}

			// The bus read itself: quorum accepted, the silent relay named.
			read, err := bus.QueryWithPolicy(ctx, "test.templates", RelayReadLatestQuorum(), templateLookupFilters("research"))
			if err != nil {
				t.Fatalf("QueryWithPolicy() error = %v, want quorum-accepted read", err)
			}
			if read.Degraded == nil {
				t.Fatal("QueryWithPolicy() Degraded = nil, want the partial read reported")
			}
			assertIncompleteRelay(t, read.Degraded, urls[2], wantStatus)
			var latest *nostr.Event
			for _, event := range read.Events {
				latest = newerRelayEvent(latest, event)
			}
			if latest == nil || latest.ID != newer.ID {
				t.Fatalf("newest event = %v, want %s", latest, newer.ID.Hex())
			}
		})
	}

	t.Run("reactor template lookup", func(t *testing.T) {
		logs := &recordingLogHandler{}
		logger := slog.New(logs)
		bus, _ := newScriptedRelayClient(t, logger,
			fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{older}},
			fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{newer}},
			fakeRelayScript{answer: relayStaysSilent},
		)
		reactor := NewReactor(Config{Relays: []string{"wss://relay-a.example"}, SoulFactoryPubkey: signer.pubkey}, nil, signer, logger)
		reactor.relayClient = bus
		ctx, cancel := context.WithTimeout(t.Context(), relayBusCallerDeadline)
		defer cancel()
		template, err := reactor.getProvisioningTemplate(ctx, "research")
		if err != nil {
			t.Fatalf("getProvisioningTemplate() error = %v, want quorum-accepted lookup", err)
		}
		if template == nil || template.EventID != newer.ID.Hex() || template.Name != "Research v2" {
			t.Fatalf("template = %+v, want newest %s", template, newer.ID.Hex())
		}
		if callers := logs.degradedCallers(); len(callers) != 1 || callers[0] != "reactor.provisioning_template" {
			t.Fatalf("degraded warnings = %v, want one for reactor.provisioning_template", callers)
		}
	})
}

// Fail-closed callers: the same topology (two relays EOSE with the newest
// state, one silent) still errors, because completeness is the requirement.
func TestRelayReadFailClosedCallersErrorWithOneSilentRelay(t *testing.T) {
	signer := newFakeSigner(t)
	listAuthor, err := nostr.PubKeyFromHex(signer.pubkey)
	if err != nil {
		t.Fatalf("pubkey: %v", err)
	}
	now := nostr.Now()
	soul := signedSoulFactoryEventAt(t, signer, domain.KindAgentSoul, now-10, nostr.Tags{{tagParameterizedD, "scout"}, {"status", "active"}}, "# Scout")
	profileList := signedSoulFactoryEventAt(t, signer, int(communikeysProfileListKind), now-10, nostr.Tags{{"d", "section"}, {"p", soulTestPubKeyHex("member")}}, "")

	callers := map[string]struct {
		event *nostr.Event
		call  func(context.Context, *RelayClient) error
	}{
		"fleet reconcile souls": {soul, func(ctx context.Context, bus *RelayClient) error {
			reactor := NewReactor(Config{Relays: []string{"wss://relay-a.example"}, SoulFactoryPubkey: signer.pubkey}, nil, signer, slog.Default())
			reactor.relayClient = bus
			_, err := reactor.listFleetReconcileSouls(ctx)
			return err
		}},
		"communikeys membership profile list": {profileList, func(ctx context.Context, bus *RelayClient) error {
			_, err := (&communikeysMembership{relayClient: bus}).latestProfileList(ctx, listAuthor, "section")
			return err
		}},
	}
	for name, caller := range callers {
		t.Run(name, func(t *testing.T) {
			bus, urls := newScriptedRelayClient(t, slog.Default(),
				fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{caller.event}},
				fakeRelayScript{answer: relayAnswersEOSE, events: []*nostr.Event{caller.event}},
				fakeRelayScript{answer: relayStaysSilent},
			)
			ctx, cancel := context.WithTimeout(t.Context(), relayBusCallerDeadline)
			defer cancel()
			err := caller.call(ctx, bus)
			assertIncompleteRelay(t, err, urls[2], RelayStoredEventsPending)
		})
	}
}

// Policy decisions on a finished read, independent of transport.
func TestResolveRelayReadPolicies(t *testing.T) {
	signer := newFakeSigner(t)
	found := signedRelayBusEvent(t, signer, 1, "found")
	missing := soulTestID("missing")
	partial := func(total, missingRelays int, cause error) error {
		relays := make([]RelayStoredEventsOutcome, missingRelays)
		for i := range relays {
			relays[i] = RelayStoredEventsOutcome{RelayURL: "wss://silent.example", Status: RelayStoredEventsPending}
		}
		return &RelayReadIncompleteError{Relays: relays, Total: total, Cause: cause}
	}
	isFound := func(event *nostr.Event) bool { return event.ID == found.ID }
	cases := []struct {
		name   string
		policy RelayReadPolicy
		events []*nostr.Event
		err    error
		accept bool
	}{
		{"complete read passes every policy", RelayReadComplete(), nil, nil, true},
		{"zero policy fails closed", RelayReadPolicy{}, []*nostr.Event{found}, partial(3, 1, context.DeadlineExceeded), false},
		{"complete fails closed", RelayReadComplete(), []*nostr.Event{found}, partial(3, 1, context.DeadlineExceeded), false},
		{"quorum 2 of 3", RelayReadLatestQuorum(), nil, partial(3, 1, context.DeadlineExceeded), true},
		{"quorum needs a strict majority: 1 of 2", RelayReadLatestQuorum(), []*nostr.Event{found}, partial(2, 1, nil), false},
		{"quorum 1 of 3 fails", RelayReadLatestQuorum(), []*nostr.Event{found}, partial(3, 2, context.DeadlineExceeded), false},
		{"quorum without relay count fails", RelayReadLatestQuorum(), nil, &RelayReadIncompleteError{Cause: context.DeadlineExceeded}, false},
		{"caller cancellation is never accepted", RelayReadLatestQuorum(), nil, partial(3, 1, context.Canceled), false},
		{"found match accepted", RelayReadFound(isFound), []*nostr.Event{found}, partial(3, 2, context.DeadlineExceeded), true},
		{"absence needs every relay", RelayReadFound(isFound), nil, partial(3, 1, context.DeadlineExceeded), false},
		{"all ids delivered", RelayReadAllIDs(found.ID), []*nostr.Event{found}, partial(3, 2, nil), true},
		{"an id not delivered", RelayReadAllIDs(found.ID, missing), []*nostr.Event{found}, partial(3, 1, nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, err := resolveRelayRead(t.Context(), slog.Default(), "test", tc.policy, tc.events, tc.err)
			if tc.accept {
				if err != nil {
					t.Fatalf("resolveRelayRead() error = %v, want accepted", err)
				}
				if (tc.err == nil) != (read.Degraded == nil) {
					t.Fatalf("Degraded = %v, want non-nil exactly for a partial read", read.Degraded)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("resolveRelayRead() error = %v, want the original %v", err, tc.err)
			}
		})
	}
	other := errors.New("not configured")
	if _, err := resolveRelayRead(t.Context(), nil, "test", RelayReadLatestQuorum(), nil, other); !errors.Is(err, other) {
		t.Fatalf("non-read error = %v, want passthrough", err)
	}
}

// Latest-wins selection (used by Reactor.GetSoul, which previously took the
// first merged event) keeps the newest copy whichever relay answered first.
func TestNewerRelayEventPrefersCreatedAtThenLowestID(t *testing.T) {
	signer := newFakeSigner(t)
	now := nostr.Now()
	a := signedSoulFactoryEventAt(t, signer, 1, now, nil, "a")
	b := signedSoulFactoryEventAt(t, signer, 1, now, nil, "b")
	older := signedSoulFactoryEventAt(t, signer, 1, now-1, nil, "older")
	lowest, highest := a, b
	if b.ID.Hex() < a.ID.Hex() {
		lowest, highest = b, a
	}
	if got := newerRelayEvent(newerRelayEvent(older, highest), lowest); got != lowest {
		t.Fatalf("newerRelayEvent() = %s, want the lowest id on a created_at tie", got.ID.Hex())
	}
	if got := newerRelayEvent(lowest, older); got != lowest {
		t.Fatal("newerRelayEvent() replaced a newer event with an older one")
	}
}
