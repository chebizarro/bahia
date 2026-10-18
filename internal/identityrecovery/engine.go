package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

type Engine struct {
	Config          Config
	Ledger          *Ledger
	Source          EventSource
	Cleanup         CleanupTransport
	Local           LocalInventory
	Operator        nostr.Keyer
	NewSigner       func(context.Context) (nostr.Keyer, error)
	OldSigner       func(context.Context) (nostr.Keyer, error)
	Containment     ContainmentAdapter
	Domains         map[string]DomainRecoveryAdapter
	RequiredDomains []string
	Rotation        SecretRotationTracker
	Purge           PurgeReceiptSink
	Acks            ConsumerAcknowledgments
	Now             func() time.Time
}

func (e *Engine) clock() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func (e *Engine) validate(ctx context.Context) error {
	if err := e.Config.Validate(); err != nil {
		return err
	}
	if e.Ledger == nil || e.Source == nil || e.Cleanup == nil || e.Operator == nil {
		return errors.New("recovery ledger, relay source and operator signer required")
	}
	pub, err := e.Operator.GetPublicKey(ctx)
	if err != nil {
		return errors.New("operator signer unavailable")
	}
	if pub != e.Config.OperatorPubkey {
		return errors.New("operator signer does not match pinned root")
	}
	return nil
}
func (e *Engine) Status() (State, error) {
	if e.Ledger == nil {
		return State{}, errors.New("ledger required")
	}
	s, err := e.Ledger.Load()
	if err != nil {
		return s, err
	}
	if s.IncidentID != "" && (s.OldPubkey != e.Config.OldPubkey.Hex() || s.OperatorPubkey != e.Config.OperatorPubkey.Hex()) {
		return s, errors.New("ledger incident identity mismatch")
	}
	if s.IncidentID != "" && !s.Aborted {
		if _, has := s.Next(); !has {
			if err := s.ValidateReceipts(e.Config.OperatorPubkey); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}
func (e *Engine) Apply(ctx context.Context, confirmed Phase) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	s, err := e.Status()
	if err != nil {
		return err
	}
	completionSigned := s.signedCompletion(confirmed)
	if err := e.publishPending(ctx, &s); err != nil {
		return err
	}
	if completionSigned {
		return nil
	}
	p, ok := s.Next()
	if !ok {
		return ErrIncomplete
	}
	if confirmed != p {
		return fmt.Errorf("confirmation must name next phase %s", p)
	}
	if s.IncidentID == "" {
		id, err := newIncidentID()
		if err != nil {
			return err
		}
		s.IncidentID = id
		s.DeclaredAt = e.clock().Format(time.RFC3339)
		s.OldPubkey = e.Config.OldPubkey.Hex()
		s.OperatorPubkey = e.Config.OperatorPubkey.Hex()
		if err := e.Ledger.Save(s); err != nil {
			return err
		}
	}
	if err := e.receipt(ctx, &s, p, "started", nil); err != nil {
		return err
	}
	evidence, err := e.execute(ctx, &s, p)
	if err != nil {
		_ = e.receipt(ctx, &s, p, "failed", cleanupEvidence(s, p))
		return err
	}
	if err = e.incident(ctx, &s, p, false); err != nil {
		return err
	}
	return e.receipt(ctx, &s, p, "completed", evidence)
}
func (e *Engine) Resume(ctx context.Context, confirmed Phase) error { return e.Apply(ctx, confirmed) }
func (e *Engine) Abort(ctx context.Context) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	s, err := e.Status()
	if err != nil {
		return err
	}
	if s.IncidentID == "" {
		return errors.New("no incident to abort")
	}
	if s.Aborted {
		return nil
	}
	if s.CutComplete() {
		return ErrForwardOnly
	}
	for _, ev := range s.Records {
		if tag(ev, "t") == kinds.CPStateTopicTrustEpoch {
			return ErrForwardOnly
		}
	}
	head, ok, err := e.epochHead(ctx)
	if err != nil {
		return err
	}
	if ok && head.Activated && head.IncidentID == s.IncidentID {
		return ErrForwardOnly
	}
	if err = e.incident(ctx, &s, func() Phase { p, _ := s.Next(); return p }(), true); err != nil {
		return err
	}
	s.Aborted = true
	return e.Ledger.Save(s)
}
func (e *Engine) nextTimestamp(s *State) nostr.Timestamp {
	n := e.clock().Unix()
	if n <= s.LastTimestamp {
		n = s.LastTimestamp + 1
	}
	s.LastTimestamp = n
	return nostr.Timestamp(n)
}
func (e *Engine) record(ctx context.Context, s *State, topic, d, schema string, family kinds.CPStateFamily, extra nostr.Tags, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	tags := nostr.Tags{{"d", d}, {"t", topic}, {"domain", "identity"}, {"schema", schema}, {"legacy_kind", family.TagValue()}, {"deleted", "false"}}
	tags = append(tags, extra...)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: e.nextTimestamp(s), Tags: tags, Content: string(raw)}
	if err = e.Operator.SignEvent(ctx, &ev); err != nil {
		return errors.New("operator record signing failed")
	}
	if !ev.CheckID() || !ev.VerifySignature() || ev.PubKey != e.Config.OperatorPubkey {
		return errors.New("operator returned invalid signature")
	}
	if s.Records == nil {
		s.Records = map[string]nostr.Event{}
	}
	s.Records[ev.ID.Hex()] = ev
	s.Pending = append(s.Pending, ev.ID.Hex())
	if err = e.Ledger.Save(*s); err != nil {
		return err
	}
	return e.publishPending(ctx, s)
}
func (e *Engine) publishPending(ctx context.Context, s *State) error {
	for len(s.Pending) > 0 {
		id := s.Pending[0]
		ev, ok := s.Records[id]
		if !ok {
			return errors.New("missing signed pending record")
		}
		if err := e.validatePending(*s, id, ev); err != nil {
			return err
		}
		if tag(ev, "t") == kinds.CPStateTopicTrustEpoch {
			var pending TrustEpoch
			if json.Unmarshal([]byte(ev.Content), &pending) != nil {
				return errors.New("invalid pending trust epoch")
			}
			if _, err := e.cutoverReadiness(ctx, s, pending.Epoch, pending.NewPubkey); err != nil {
				return err
			}
			head, exists, err := e.epochHead(ctx)
			if err != nil {
				return err
			}
			if exists && head.Epoch >= pending.Epoch && (head.Epoch != pending.Epoch || head.IncidentID != pending.IncidentID || head.NewPubkey != pending.NewPubkey || head.InventoryDigest != pending.InventoryDigest) {
				return errors.New("trust epoch head advanced; refusing pending non-monotonic publication")
			}
		}
		if ev.Kind == nostr.KindDeletion {
			outcomes := e.Cleanup.PublishDeletion(ctx, ev)
			if s.CleanupDeletions == nil {
				s.CleanupDeletions = map[string]map[string]RelayEvidence{}
			}
			s.CleanupDeletions[id] = outcomes
			if err := e.Ledger.Save(*s); err != nil {
				return err
			}
			if !completeCleanup(outcomes, e.Config.CleanupRelays, "ok") {
				return fmt.Errorf("cleanup relay deletion incomplete: %w", ErrIncomplete)
			}
		} else if err := e.Source.Publish(ctx, ev); err != nil {
			return fmt.Errorf("publish signed recovery record %s: %w", id, err)
		}
		s.Pending = s.Pending[1:]
		if err := e.Ledger.Save(*s); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) receipt(ctx context.Context, s *State, p Phase, status string, evidence []string) error {
	if err := safeEvidence(evidence); err != nil {
		return err
	}
	r := Receipt{IncidentID: s.IncidentID, Phase: p, Status: status, InputDigest: digestInput(*s, p), EvidenceIDs: evidence, CompletedAt: e.clock().Format(time.RFC3339)}
	if p == Inventory && status == "completed" {
		r.InventoryRelays = make(map[string]RelayEvidence, len(s.CleanupInventory))
		for relay, proof := range s.CleanupInventory {
			sum := sha256.Sum256([]byte(relayguard.Normalize(relay)))
			r.InventoryRelays[hex.EncodeToString(sum[:])] = proof
		}
	}
	if p == CutActivateTrust && status == "completed" {
		r.WaiverEventIDs = s.CutoverWaiverEventIDs
		r.WaiverDetails = s.CutoverWaiverDetails
	}
	return e.record(ctx, s, kinds.CPStateTopicIdentityRecoveryReceipt, "identity-incident:"+s.IncidentID+":phase:"+string(p), "bahia.identity-recovery-receipt.v1", kinds.CPStateFamilyIdentityRecoveryReceipt, nostr.Tags{{"incident", s.IncidentID}}, r)
}
func (e *Engine) incident(ctx context.Context, s *State, p Phase, aborted bool) error {
	body := Incident{IncidentID: s.IncidentID, OldPubkey: s.OldPubkey, DeclaredAt: s.DeclaredAt, ReasonCode: e.Config.ReasonCode, ControlledRelays: e.Config.ControlledRelays, Phase: p, Aborted: aborted}
	return e.record(ctx, s, kinds.CPStateTopicIdentityIncident, "identity-incident:"+s.IncidentID, "bahia.identity-incident.v1", kinds.CPStateFamilyIdentityIncident, nostr.Tags{{"old", s.OldPubkey}}, body)
}
func (e *Engine) execute(ctx context.Context, s *State, p Phase) ([]string, error) {
	switch p {
	case Declare:
		return []string{s.IncidentID}, nil
	case Contain:
		if e.Containment == nil {
			return nil, fmt.Errorf("contain: %w", ErrAdapterNotRegistered)
		}
		return e.Containment.Contain(ctx)
	case Inventory:
		return e.inventory(ctx, s)
	case ProvisionNewIdentity:
		return e.provision(ctx, s)
	case CutActivateTrust:
		return e.cut(ctx, s)
	case ReassertCanonicalState:
		return e.reassert(ctx)
	case RotateExternalSecrets:
		return e.rotate(ctx)
	case DeleteAndPurge:
		return e.deleteAndPurge(ctx, s)
	case Verify:
		return e.verify(ctx, *s)
	case RetireOldIdentity:
		deletion, ok := s.Receipt(DeleteAndPurge)
		if !ok || !hasPurgeEvidence(deletion, s.DeletionIDs, e.Config.CleanupRelays) {
			return nil, fmt.Errorf("retirement requires deletion and controlled re-encryption receipts: %w", ErrIncomplete)
		}
		if err := e.requireNotificationReencryption(ctx, *s, deletion.EvidenceIDs); err != nil {
			return nil, err
		}
		if _, ok := s.Receipt(ReassertCanonicalState); !ok {
			return nil, fmt.Errorf("retirement requires canonical reassertion: %w", ErrIncomplete)
		}
		return []string{s.IncidentID}, nil
	default:
		return nil, errors.New("unknown recovery phase")
	}
}

func (e *Engine) requireNotificationReencryption(ctx context.Context, s State, deletionEvidence []string) error {
	if !slices.Contains(e.RequiredDomains, "notification-channels") {
		return nil
	}
	adapter := e.Domains["notification-channels"]
	if adapter == nil {
		return fmt.Errorf("notification receipts: %w", ErrAdapterNotRegistered)
	}
	reassertion, ok := s.Receipt(ReassertCanonicalState)
	if !ok {
		return fmt.Errorf("notification reassertion receipt: %w", ErrIncomplete)
	}
	proof, err := adapter.Verify(ctx)
	if err != nil {
		return fmt.Errorf("notification re-encryption verification: %w", err)
	}
	for _, id := range proof {
		if !slices.Contains(reassertion.EvidenceIDs, id) || deletionEvidence != nil && !slices.Contains(deletionEvidence, id) {
			return fmt.Errorf("notification re-encryption receipt missing: %w", ErrIncomplete)
		}
	}
	return nil
}
func (e *Engine) inventory(ctx context.Context, s *State) ([]string, error) {
	s.Inventory = nil
	s.Observed = nil
	s.InventoryDigest = ""
	if e.Local == nil {
		return nil, errors.New("local event store required")
	}
	if e.Config.AcceptedEventIDs == nil {
		return nil, errors.New("reviewed accepted event IDs required")
	}
	filter := nostr.Filter{Authors: []nostr.PubKey{e.Config.OldPubkey}}
	relayEvents, outcomes := e.Cleanup.Scan(ctx, filter)
	s.CleanupInventory = outcomes
	if err := e.Ledger.Save(*s); err != nil {
		return nil, err
	}
	if !completeCleanup(outcomes, e.Config.CleanupRelays, "complete") {
		return nil, fmt.Errorf("cleanup relay inventory incomplete: %w", ErrIncomplete)
	}
	localEvents, err := e.Local.Events(ctx, e.Config.OldPubkey)
	if err != nil {
		return nil, err
	}
	scanned := map[string]InventoryEvent{}
	for _, ev := range append(relayEvents, localEvents...) {
		if ev.PubKey != e.Config.OldPubkey || !ev.CheckID() || !ev.VerifySignature() {
			return nil, errors.New("invalid old-key inventory event")
		}
		scanned[ev.ID.Hex()] = InventoryEvent{ID: ev.ID.Hex(), Kind: int(ev.Kind), CreatedAt: int64(ev.CreatedAt)}
	}
	var missing []string
	for _, id := range e.Config.AcceptedEventIDs {
		if _, ok := scanned[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		fetched, err := e.Cleanup.FetchCandidates(ctx, e.Config.OldPubkey, missing)
		if err != nil {
			return nil, err
		}
		for _, ev := range fetched {
			if ev.PubKey != e.Config.OldPubkey || !ev.CheckID() || !ev.VerifySignature() {
				return nil, errors.New("invalid old-key candidate event")
			}
			scanned[ev.ID.Hex()] = InventoryEvent{ID: ev.ID.Hex(), Kind: int(ev.Kind), CreatedAt: int64(ev.CreatedAt)}
		}
	}
	s.Observed = make([]InventoryEvent, 0, len(scanned))
	for _, item := range scanned {
		s.Observed = append(s.Observed, item)
	}
	slices.SortFunc(s.Observed, func(a, b InventoryEvent) int { return slices.Compare([]byte(a.ID), []byte(b.ID)) })
	approved := map[string]struct{}{}
	missing = missing[:0]
	for _, id := range e.Config.AcceptedEventIDs {
		if _, ok := scanned[id]; !ok {
			missing = append(missing, id)
			continue
		}
		approved[id] = struct{}{}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("%w: %d reviewed event IDs absent from audited sources after by-ID fetch (sample: %v)", ErrIncomplete, len(missing), missing[:min(len(missing), 5)])
	}
	s.Inventory = make([]InventoryEvent, 0, len(approved))
	for id := range approved {
		s.Inventory = append(s.Inventory, scanned[id])
	}
	slices.SortFunc(s.Inventory, func(a, b InventoryEvent) int { return slices.Compare([]byte(a.ID), []byte(b.ID)) })
	digest, count, err := InventoryDigest(s.Inventory)
	if err != nil {
		return nil, err
	}
	s.InventoryDigest = digest
	if err = e.Ledger.Save(*s); err != nil {
		return nil, err
	}
	return append([]string{digest, strconv.Itoa(count)}, cleanupEvidence(*s, Inventory)...), nil
}
func (e *Engine) provision(ctx context.Context, s *State) ([]string, error) {
	if e.NewSigner == nil {
		return nil, errors.New("new identity signer required")
	}
	signer, err := e.NewSigner(ctx)
	if err != nil {
		return nil, errors.New("new identity signer unavailable")
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub == nostr.ZeroPK || pub == e.Config.OldPubkey || pub == e.Config.OperatorPubkey {
		return nil, errors.New("new identity signer pubkey invalid")
	}
	probe := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(e.clock().Unix()), Tags: nostr.Tags{{"d", "identity-recovery-probe:" + s.IncidentID}}, Content: ""}
	if err = signer.SignEvent(ctx, &probe); err != nil || probe.PubKey != pub || !probe.CheckID() || !probe.VerifySignature() {
		return nil, errors.New("new identity signer probe failed")
	}
	s.NewPubkey = pub.Hex()
	if err = e.Ledger.Save(*s); err != nil {
		return nil, err
	}
	return []string{pub.Hex()}, nil
}
func (e *Engine) epochHead(ctx context.Context) (TrustEpoch, bool, error) {
	d := "trust-epoch:" + e.Config.ServiceID
	events, err := e.Source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{e.Config.OperatorPubkey}, Tags: nostr.TagMap{"d": {d}, "t": {kinds.CPStateTopicTrustEpoch}}})
	if err != nil {
		return TrustEpoch{}, false, err
	}
	var best TrustEpoch
	found := false
	for _, ev := range events {
		if !validRecord(ev, e.Config.OperatorPubkey, kinds.CPStateTopicTrustEpoch, d, kinds.CPStateFamilyTrustEpoch) {
			return TrustEpoch{}, false, errors.New("invalid trust epoch head candidate")
		}
		var candidate TrustEpoch
		if json.Unmarshal([]byte(ev.Content), &candidate) != nil || candidate.Epoch < 1 || candidate.OldPubkey == "" || candidate.NewPubkey == "" || tag(ev, "epoch") != strconv.FormatInt(candidate.Epoch, 10) {
			return TrustEpoch{}, false, errors.New("malformed trust epoch head")
		}
		if !found || candidate.Epoch > best.Epoch {
			best = candidate
			found = true
		} else if candidate.Epoch == best.Epoch && (candidate.IncidentID != best.IncidentID || candidate.NewPubkey != best.NewPubkey) {
			return TrustEpoch{}, false, errors.New("conflicting trust epoch head")
		}
	}
	return best, found, nil
}
func (e *Engine) cut(ctx context.Context, s *State) ([]string, error) {
	if s.NewPubkey == "" || s.InventoryDigest == "" {
		return nil, errors.New("provision and inventory required before cutover")
	}
	head, ok, err := e.epochHead(ctx)
	if err != nil {
		return nil, err
	}
	if ok && head.IncidentID == s.IncidentID && head.Activated {
		if head.InventoryDigest != s.InventoryDigest || head.NewPubkey != s.NewPubkey {
			return nil, errors.New("activated epoch conflicts with ledger")
		}
		s.Epoch = head.Epoch
		s.ActivationBarrier = head.ActivationBarrier
		proof, err := e.cutoverReadiness(ctx, s, head.Epoch, head.NewPubkey)
		if err != nil {
			return nil, err
		}
		_ = e.Ledger.Save(*s)
		return append([]string{strconv.FormatInt(head.Epoch, 10)}, proof.EvidenceIDs...), nil
	}
	epoch := int64(1)
	if ok {
		epoch = head.Epoch + 1
	}
	if s.Epoch != 0 && s.Epoch != epoch {
		return nil, errors.New("epoch changed during interrupted cutover")
	}
	proof, err := e.cutoverReadiness(ctx, s, epoch, s.NewPubkey)
	if err != nil {
		return nil, err
	}
	barrier := e.clock().Unix()
	body := TrustEpoch{IncidentID: s.IncidentID, Epoch: epoch, OldPubkey: s.OldPubkey, NewPubkey: s.NewPubkey, ControlledRelays: e.Config.ControlledRelays, ActivationBarrier: barrier, InventoryDigest: s.InventoryDigest, InventoryCount: len(s.Inventory), ConsumerRoster: e.Config.ConsumerRoster, Activated: true}
	// Re-read immediately before publishing so an intervening epoch cannot be overwritten.
	current, exists, err := e.epochHead(ctx)
	if err != nil {
		return nil, err
	}
	if (exists && current.Epoch >= epoch) || (!exists && epoch != 1) {
		return nil, errors.New("trust epoch head advanced; refusing non-monotonic publication")
	}
	if err = e.record(ctx, s, kinds.CPStateTopicTrustEpoch, "trust-epoch:"+e.Config.ServiceID, "bahia.trust-epoch.v1", kinds.CPStateFamilyTrustEpoch, nostr.Tags{{"epoch", strconv.FormatInt(epoch, 10)}}, body); err != nil {
		return nil, err
	}
	s.Epoch = epoch
	s.ActivationBarrier = barrier
	if err = e.Ledger.Save(*s); err != nil {
		return nil, err
	}
	return append([]string{strconv.FormatInt(epoch, 10)}, proof.EvidenceIDs...), nil
}

func (e *Engine) cutoverReadiness(ctx context.Context, s *State, epoch int64, newPubkey string) (CutoverReadiness, error) {
	if e.Acks == nil {
		return CutoverReadiness{}, fmt.Errorf("%w: consumer readiness reader unavailable", ErrIncomplete)
	}
	proof, err := e.Acks.Readiness(ctx, epoch, s.IncidentID, newPubkey, e.Config.ConsumerRoster)
	if err != nil {
		return CutoverReadiness{}, err
	}
	if len(proof.Missing) != 0 {
		return CutoverReadiness{}, fmt.Errorf("%w: missing consumer enforcement readiness or waiver %v", ErrIncomplete, proof.Missing)
	}
	if len(proof.EvidenceIDs) < len(e.Config.ConsumerRoster) {
		return CutoverReadiness{}, fmt.Errorf("%w: incomplete consumer readiness evidence", ErrIncomplete)
	}
	if len(proof.Waivers) != len(proof.WaiverDetails) {
		return CutoverReadiness{}, fmt.Errorf("%w: incomplete waiver evidence", ErrIncomplete)
	}
	for consumer, eventID := range proof.Waivers {
		detail, exists := proof.WaiverDetails[consumer]
		if !exists || !trustepoch.Registered(consumer) || !trustepoch.ValidWaiverReason(detail.ReasonCode) || len(detail.EvidenceIDs) == 0 || !validEvidenceID(eventID) {
			return CutoverReadiness{}, fmt.Errorf("%w: invalid waiver evidence", ErrIncomplete)
		}
		for _, id := range detail.EvidenceIDs {
			if !validEvidenceID(id) {
				return CutoverReadiness{}, fmt.Errorf("%w: invalid waiver evidence ID", ErrIncomplete)
			}
		}
	}
	s.CutoverWaiverEventIDs = proof.Waivers
	s.CutoverWaiverDetails = proof.WaiverDetails
	return proof, nil
}

func validEvidenceID(value string) bool {
	id, err := nostr.IDFromHex(value)
	return err == nil && id.Hex() == value
}
func (e *Engine) reassert(ctx context.Context) ([]string, error) {
	if err := e.requireDomains(); err != nil {
		return nil, err
	}
	if len(e.Domains) == 0 {
		return nil, fmt.Errorf("reassert: %w", ErrAdapterNotRegistered)
	}
	if e.NewSigner == nil {
		return nil, errors.New("new signer required")
	}
	signer, err := e.NewSigner(ctx)
	if err != nil {
		return nil, err
	}
	var evidence []string
	for _, name := range e.domainOrder() {
		a := e.Domains[name]
		if a == nil {
			return nil, fmt.Errorf("domain %s: %w", name, ErrAdapterNotRegistered)
		}
		if _, err := a.Inventory(ctx); err != nil {
			return nil, err
		}
		ids, err := a.Reassert(ctx, signer)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, ids...)
	}
	slices.Sort(evidence)
	return evidence, nil
}

func (e *Engine) domainOrder() []string {
	names := make([]string, 0, len(e.Domains))
	for name := range e.Domains {
		if name != "discovery-system" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if _, ok := e.Domains["discovery-system"]; ok {
		names = append(names, "discovery-system")
	}
	return names
}

func (e *Engine) requireDomains() error {
	for _, name := range e.RequiredDomains {
		if e.Domains[name] == nil {
			return fmt.Errorf("domain %s: %w", name, ErrAdapterNotRegistered)
		}
	}
	return nil
}
func (e *Engine) rotate(ctx context.Context) ([]string, error) {
	if e.Rotation == nil {
		return nil, fmt.Errorf("rotate: %w", ErrAdapterNotRegistered)
	}
	refs, err := e.Rotation.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	receipts, err := e.Rotation.Rotate(ctx, refs)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		proof := receipts[ref.ID]
		if proof == "" {
			return nil, fmt.Errorf("%w: rotation receipt for %s", ErrIncomplete, ref.ID)
		}
		ids = append(ids, proof)
	}
	slices.Sort(ids)
	return ids, nil
}
func (e *Engine) verify(ctx context.Context, s State) ([]string, error) {
	if err := e.requireDomains(); err != nil {
		return nil, err
	}
	for _, p := range Phases[:8] {
		if _, ok := s.Receipt(p); !ok {
			return nil, fmt.Errorf("%w: %s", ErrIncomplete, p)
		}
	}
	head, ok, err := e.epochHead(ctx)
	if err != nil {
		return nil, err
	}
	if !ok || !head.Activated || head.IncidentID != s.IncidentID || head.Epoch != s.Epoch {
		return nil, fmt.Errorf("trust epoch: %w", ErrIncomplete)
	}
	if e.Acks == nil {
		return nil, fmt.Errorf("%w: missing consumer acknowledgments %v", ErrIncomplete, e.Config.ConsumerRoster)
	}
	missing, err := e.Acks.Missing(ctx, s.Epoch, e.Config.ConsumerRoster)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: missing consumer acknowledgments %v", ErrIncomplete, missing)
	}
	var domainEvidence []string
	for _, name := range e.domainOrder() {
		a := e.Domains[name]
		proof, err := a.Verify(ctx)
		if err != nil {
			return nil, fmt.Errorf("domain %s: %w", name, err)
		}
		domainEvidence = append(domainEvidence, proof...)
	}
	slices.Sort(domainEvidence)
	return append([]string{strconv.FormatInt(s.Epoch, 10)}, domainEvidence...), nil
}
