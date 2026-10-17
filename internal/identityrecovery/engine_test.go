package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

type testSource struct {
	events         []nostr.Event
	failPhase      Phase
	failed         bool
	reads          int
	onRead         func(int)
	scanOutcomes   map[string]RelayEvidence
	deleteOutcomes map[string]RelayEvidence
}

func (s *testSource) Read(_ context.Context, f nostr.Filter) ([]nostr.Event, error) {
	s.reads++
	if s.onRead != nil {
		s.onRead(s.reads)
	}
	var out []nostr.Event
	for _, ev := range s.events {
		if len(f.Authors) > 0 && !slices.Contains(f.Authors, ev.PubKey) {
			continue
		}
		if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, ev.Kind) {
			continue
		}
		match := true
		for k, values := range f.Tags {
			if !slices.Contains(values, tag(ev, k)) {
				match = false
				break
			}
		}
		if match {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (s *testSource) Publish(_ context.Context, ev nostr.Event) error {
	var r Receipt
	if tag(ev, "t") == kinds.CPStateTopicIdentityRecoveryReceipt && json.Unmarshal([]byte(ev.Content), &r) == nil && r.Phase == s.failPhase && r.Status == "completed" && !s.failed {
		s.failed = true
		return errors.New("interrupted delivery")
	}
	s.events = append(s.events, ev)
	return nil
}

type testLocal struct{ events []nostr.Event }

func (l testLocal) Events(context.Context, nostr.PubKey) ([]nostr.Event, error) { return l.events, nil }

type testContain struct{}

func (testContain) Contain(context.Context) ([]string, error) { return []string{"contain-proof"}, nil }

type testDomain struct{}

func (testDomain) Inventory(context.Context) ([]string, error) {
	return []string{"domain-inventory"}, nil
}
func (testDomain) Reassert(context.Context, nostr.Keyer) ([]string, error) {
	return []string{"domain-reassert"}, nil
}
func (testDomain) Verify(context.Context) ([]string, error) { return []string{"domain-verify"}, nil }

type testNotificationDomain struct{ proof string }

func (a testNotificationDomain) Inventory(context.Context) ([]string, error) {
	return []string{"old-channel"}, nil
}
func (a testNotificationDomain) Reassert(context.Context, nostr.Keyer) ([]string, error) {
	return []string{a.proof}, nil
}
func (a testNotificationDomain) Verify(context.Context) ([]string, error) {
	return []string{a.proof}, nil
}

type testRotation struct{}

func (testRotation) Inventory(context.Context) ([]SecretRef, error) {
	return []SecretRef{{ID: "secret-1", Provider: "test"}}, nil
}
func (testRotation) Rotate(context.Context, []SecretRef) (map[string]string, error) {
	return map[string]string{"secret-1": "rotation-proof"}, nil
}

type testPurge struct{}

func (testPurge) Purge(context.Context, []nostr.ID) ([]string, error) {
	return []string{"purge:archive-artifacts:archive-proof", "purge:pg-backups:backup-proof", "purge:wal:wal-proof", "purge:controlled-re-encryption:reencrypt-proof"}, nil
}

type testAcks struct{}

func (testAcks) Missing(context.Context, int64, []string) ([]string, error) { return nil, nil }
func (testAcks) Readiness(_ context.Context, _ int64, _, _ string, roster []string) (CutoverReadiness, error) {
	proof := CutoverReadiness{}
	for i := range roster {
		proof.EvidenceIDs = append(proof.EvidenceIDs, fmt.Sprintf("%064x", i+1))
	}
	return proof, nil
}

type testWaiverAcks struct{ testAcks }

func (testWaiverAcks) Readiness(_ context.Context, _ int64, _, _ string, _ []string) (CutoverReadiness, error) {
	id := fmt.Sprintf("%064x", 42)
	return CutoverReadiness{EvidenceIDs: []string{id}, Waivers: map[string]string{"daemon": id},
		WaiverDetails: map[string]WaiverEvidence{"daemon": {ReasonCode: "consumer_disabled", EvidenceIDs: []string{fmt.Sprintf("%064x", 43)}}}}, nil
}

type testEmptyWaiverAcks struct{ testWaiverAcks }

func (testEmptyWaiverAcks) Readiness(ctx context.Context, epoch int64, incidentID, newPubkey string, roster []string) (CutoverReadiness, error) {
	proof, err := (testWaiverAcks{}).Readiness(ctx, epoch, incidentID, newPubkey, roster)
	proof.WaiverDetails["daemon"] = WaiverEvidence{ReasonCode: "consumer_disabled"}
	return proof, err
}
func signer(t *testing.T) nostr.Keyer {
	t.Helper()
	k, err := nostrutil.NewLocalKeyer(nostrutil.GeneratePrivateKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func pub(t *testing.T, k nostr.Keyer) nostr.PubKey {
	t.Helper()
	p, err := k.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func oldEvent(t *testing.T, k nostr.Keyer) nostr.Event {
	t.Helper()
	ev := nostr.Event{Kind: 1, CreatedAt: 100, Content: "old"}
	if err := k.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}
func fixture(t *testing.T) (*Engine, *testSource, func()) {
	t.Helper()
	op, newKey, old := signer(t), signer(t), signer(t)
	oldEv := oldEvent(t, old)
	src := &testSource{events: []nostr.Event{oldEv}}
	path := filepath.Join(t.TempDir(), "recovery.bolt")
	cfg := Config{ServiceID: "bahia-service-v1", OldPubkey: pub(t, old), ReasonCode: "compromised", ControlledRelays: []string{"wss://relay.example"}, CleanupRelays: []string{"wss://relay.example"}, ConsumerRoster: []string{"daemon"}, OperatorPubkey: pub(t, op), LocalStorePath: filepath.Join(t.TempDir(), "daemon.bolt"), AcceptedEventIDs: []string{oldEv.ID.Hex()}}
	open := func() *Ledger {
		l, err := OpenLedger(path, cfg.LocalStorePath)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	e := &Engine{Config: cfg, Ledger: open(), Source: src, Cleanup: src, Local: testLocal{events: []nostr.Event{oldEv}}, Operator: op, NewSigner: func(context.Context) (nostr.Keyer, error) { return newKey, nil }, OldSigner: func(context.Context) (nostr.Keyer, error) { return old, nil }, Containment: testContain{}, Domains: map[string]DomainRecoveryAdapter{"domain": testDomain{}}, Rotation: testRotation{}, Purge: testPurge{}, Acks: testAcks{}, Now: func() time.Time { return time.Unix(1000, 0).UTC() }}
	restart := func() {
		if err := e.Ledger.Close(); err != nil {
			t.Fatal(err)
		}
		e.Ledger = open()
	}
	t.Cleanup(func() { _ = e.Ledger.Close() })
	return e, src, restart
}
func TestResumeAfterInterruptedCompletedReceiptEveryPhase(t *testing.T) {
	ctx := context.Background()
	e, src, restart := fixture(t)
	for _, p := range Phases {
		t.Run(string(p), func(t *testing.T) {
			src.failPhase = p
			src.failed = false
			if err := e.Apply(ctx, p); err == nil || !strings.Contains(err.Error(), "interrupted delivery") {
				t.Fatalf("expected interrupted delivery, got %v", err)
			}
			restart()
			if err := e.Resume(ctx, p); err != nil {
				t.Fatalf("resume %s: %v", p, err)
			}
			s, err := e.Status()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.Receipt(p); !ok {
				t.Fatalf("missing %s completion", p)
			}
			if len(s.Pending) != 0 {
				t.Fatal("pending publication remained")
			}
		})
	}
	s, err := e.Status()
	_, has := s.Next()
	if err != nil || has {
		t.Fatalf("terminal state: %v %v", has, err)
	}
	if err := s.ValidateReceipts(e.Config.OperatorPubkey); err != nil {
		t.Fatal(err)
	}
}
func TestAbortBeforeAndAfterCutover(t *testing.T) {
	e, _, _ := fixture(t)
	ctx := context.Background()
	if err := e.Apply(ctx, Declare); err != nil {
		t.Fatal(err)
	}
	if err := e.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status()
	if err != nil || !s.Aborted {
		t.Fatalf("abort state: %v %v", s.Aborted, err)
	}
	e2, _, _ := fixture(t)
	for _, p := range Phases[:5] {
		if err := e2.Apply(ctx, p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if err := e2.Abort(ctx); !errors.Is(err, ErrForwardOnly) {
		t.Fatalf("post-cut abort: %v", err)
	}
}

func TestCutRefusesAbsentReadinessBeforePublishingEpoch(t *testing.T) {
	e, src, _ := fixture(t)
	for _, p := range Phases[:4] {
		if err := e.Apply(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	e.Acks = nil
	if err := e.Apply(t.Context(), CutActivateTrust); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("cut without readiness: %v", err)
	}
	for _, ev := range src.events {
		if tag(ev, "t") == kinds.CPStateTopicTrustEpoch {
			t.Fatal("trust activated without readiness")
		}
	}
}

func TestCutReceiptCarriesSignedWaiverEventID(t *testing.T) {
	e, _, _ := fixture(t)
	for _, p := range Phases[:4] {
		if err := e.Apply(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	e.Acks = testEmptyWaiverAcks{}
	if err := e.Apply(t.Context(), CutActivateTrust); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("zero-evidence waiver activated cut: %v", err)
	}
	e.Acks = testWaiverAcks{}
	if err := e.Apply(t.Context(), CutActivateTrust); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := s.Receipt(CutActivateTrust)
	if !ok || receipt.WaiverEventIDs["daemon"] != fmt.Sprintf("%064x", 42) ||
		receipt.WaiverDetails["daemon"].ReasonCode != "consumer_disabled" ||
		!slices.Equal(receipt.WaiverDetails["daemon"].EvidenceIDs, []string{fmt.Sprintf("%064x", 43)}) {
		t.Fatalf("waiver absent from signed phase receipt: %+v", receipt)
	}
}
func TestInventoryDigestDeterministic(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	x, n, err := InventoryDigest([]InventoryEvent{{ID: b}, {ID: a}, {ID: a}})
	if err != nil || n != 2 {
		t.Fatalf("digest: %v %d", err, n)
	}
	y, _, _ := InventoryDigest([]InventoryEvent{{ID: a}, {ID: b}})
	if x != y {
		t.Fatal("digest order changed")
	}
}
func TestQuarantinedSignerRefusesUnscopedOperations(t *testing.T) {
	ctx := context.Background()
	old := signer(t)
	target := oldEvent(t, old)
	q, err := NewQuarantinedOldSigner(ctx, old, pub(t, old), []InventoryEvent{{ID: target.ID.Hex(), Kind: int(target.Kind)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []nostr.Event{{Kind: nostr.Kind(kinds.CASControlState), PubKey: pub(t, old), Tags: nostr.Tags{{"e", target.ID.Hex()}}}, {Kind: nostr.KindDeletion, PubKey: pub(t, old), Tags: nostr.Tags{{"a", "coordinate"}}}, {Kind: nostr.KindDeletion, PubKey: pub(t, old), Tags: nostr.Tags{{"e", strings.Repeat("f", 64)}, {"k", "1"}}}} {
		if err := q.SignDeletion(ctx, &ev); err == nil {
			t.Fatal("unscoped sign allowed")
		}
	}
	valid := nostr.Event{Kind: nostr.KindDeletion, PubKey: pub(t, old), CreatedAt: 200, Tags: nostr.Tags{{"e", target.ID.Hex()}, {"k", "1"}}}
	if err := q.SignDeletion(ctx, &valid); err != nil {
		t.Fatal(err)
	}
	if !valid.VerifySignature() {
		t.Fatal("invalid signed deletion")
	}
}
func TestEpochRefusesHeadAdvance(t *testing.T) {
	e, src, _ := fixture(t)
	ctx := context.Background()
	for _, p := range Phases[:4] {
		if err := e.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	makeHead := func(epoch int64) nostr.Event {
		body := TrustEpoch{IncidentID: fmt.Sprintf("prior-%d", epoch), Epoch: epoch, OldPubkey: e.Config.OldPubkey.Hex(), NewPubkey: "new", Activated: true}
		raw, _ := json.Marshal(body)
		ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(epoch), Tags: nostr.Tags{{"d", "trust-epoch:" + e.Config.ServiceID}, {"t", kinds.CPStateTopicTrustEpoch}, {"domain", "identity"}, {"schema", "bahia.trust-epoch.v1"}, {"legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()}, {"deleted", "false"}, {"epoch", fmt.Sprint(epoch)}}, Content: string(raw)}
		if err := e.Operator.SignEvent(ctx, &ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	src.events = append(src.events, makeHead(5))
	src.reads = 0
	src.onRead = func(n int) {
		if n == 2 {
			src.events = append(src.events, makeHead(6))
		}
	}
	if err := e.Apply(ctx, CutActivateTrust); err == nil || !strings.Contains(err.Error(), "non-monotonic") {
		t.Fatalf("cutover should refuse advanced head: %v", err)
	}
	for _, ev := range src.events {
		if tag(ev, "t") == kinds.CPStateTopicTrustEpoch && tag(ev, "epoch") == "6" && tag(ev, "d") != "trust-epoch:"+e.Config.ServiceID {
			t.Fatal("bad test")
		}
	}
}
func TestMissingAdaptersDoNotComplete(t *testing.T) {
	e, _, _ := fixture(t)
	e.Containment = nil
	ctx := context.Background()
	if err := e.Apply(ctx, Declare); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, Contain); !errors.Is(err, ErrAdapterNotRegistered) {
		t.Fatalf("missing containment: %v", err)
	}
	s, _ := e.Status()
	if _, ok := s.Receipt(Contain); ok {
		t.Fatal("missing adapter completed phase")
	}
}

func TestRetirementConsumesSignedNotificationReencryptionReceipts(t *testing.T) {
	e, _, _ := fixture(t)
	proof := "notification:channel-id:" + strings.Repeat("a", 64)
	e.RequiredDomains = []string{"domain", "notification-channels"}
	e.Domains["notification-channels"] = testNotificationDomain{proof: proof}
	ctx := context.Background()
	for _, p := range Phases[:7] {
		if err := e.Apply(ctx, p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	e.Domains["notification-channels"] = testNotificationDomain{proof: "notification:channel-id:changed"}
	if err := e.Apply(ctx, DeleteAndPurge); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("missing exact notification receipt: %v", err)
	}
	e.Domains["notification-channels"] = testNotificationDomain{proof: proof}
	if err := e.Apply(ctx, DeleteAndPurge); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := s.Receipt(DeleteAndPurge)
	if !ok || !slices.Contains(r.EvidenceIDs, proof) {
		t.Fatal("delete receipt omitted notification coordinate")
	}
	if err := e.Apply(ctx, Verify); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, RetireOldIdentity); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRejectsOutOfScopePendingEvent(t *testing.T) {
	e, src, _ := fixture(t)
	ctx := context.Background()
	if err := e.Apply(ctx, Declare); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status()
	if err != nil {
		t.Fatal(err)
	}
	forged := nostr.Event{Kind: 1, CreatedAt: 1001, Content: "unrelated"}
	if err := e.Operator.SignEvent(ctx, &forged); err != nil {
		t.Fatal(err)
	}
	s.Records[forged.ID.Hex()] = forged
	s.Pending = append(s.Pending, forged.ID.Hex())
	if err := e.Ledger.Save(s); err != nil {
		t.Fatal(err)
	}
	before := len(src.events)
	if err := e.Resume(ctx, Contain); err == nil {
		t.Fatal("published unrelated pending event")
	}
	if len(src.events) != before {
		t.Fatal("pending event reached relay")
	}
}

func (s *testSource) Scan(ctx context.Context, f nostr.Filter) ([]nostr.Event, map[string]RelayEvidence) {
	events, _ := s.Read(ctx, f)
	if s.scanOutcomes != nil {
		return events, s.scanOutcomes
	}
	return events, map[string]RelayEvidence{"wss://relay.example": {Status: "eose"}}
}
func (s *testSource) PublishDeletion(ctx context.Context, ev nostr.Event) map[string]RelayEvidence {
	_ = s.Publish(ctx, ev)
	if s.deleteOutcomes != nil {
		return s.deleteOutcomes
	}
	return map[string]RelayEvidence{"wss://relay.example": {Status: "ok"}}
}

func TestCleanupInventoryAndDeletionFailClosedPerRelay(t *testing.T) {
	ctx := context.Background()
	engine, source, _ := fixture(t)
	public := "wss://relay.damus.io"
	engine.Config.CleanupRelays = append(engine.Config.CleanupRelays, public)
	source.scanOutcomes = map[string]RelayEvidence{
		"wss://relay.example": {Status: "eose"}, public: {Status: "incomplete", Reason: "auth-required"},
	}
	for _, phase := range []Phase{Declare, Contain} {
		if err := engine.Apply(ctx, phase); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.Apply(ctx, Inventory); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("unanswered audit accepted: %v", err)
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if state.CleanupInventory[public].Reason != "auth-required" {
		t.Fatal("per-relay incomplete audit not durable")
	}
	if slices.Contains(cleanupEvidence(state, Inventory), "eose") {
		t.Fatal("EOSE misrepresented as absence proof")
	}
	source.scanOutcomes[public] = RelayEvidence{Status: "eose"}
	if err := engine.Resume(ctx, Inventory); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{ProvisionNewIdentity, CutActivateTrust, ReassertCanonicalState, RotateExternalSecrets} {
		if err := engine.Apply(ctx, phase); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
	}
	source.deleteOutcomes = map[string]RelayEvidence{
		"wss://relay.example": {Status: "ok"}, public: {Status: "ok-false", Reason: "blocked"},
	}
	if err := engine.Apply(ctx, DeleteAndPurge); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("OK-false deletion accepted: %v", err)
	}
	state, err = engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.DeletionIDs) != 1 || state.CleanupDeletions[state.DeletionIDs[0]][public].Status != "ok-false" {
		t.Fatal("per-relay deletion refusal not durable")
	}
	if _, ok := state.Receipt(DeleteAndPurge); ok {
		t.Fatal("refused relay produced completion receipt")
	}
	source.deleteOutcomes[public] = RelayEvidence{Status: "ok"}
	if err := engine.Resume(ctx, DeleteAndPurge); err != nil {
		t.Fatal(err)
	}
	state, err = engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := state.Receipt(DeleteAndPurge)
	if !ok || !hasPurgeEvidence(receipt, state.DeletionIDs, engine.Config.CleanupRelays) {
		t.Fatal("per-relay cleanup proof absent")
	}
}
