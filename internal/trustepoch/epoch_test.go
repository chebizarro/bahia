package trustepoch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

type testSource struct {
	event   *nostr.Event
	present bool
	err     error
}

func (s *testSource) Head(context.Context, string, string) (*nostr.Event, bool, error) {
	return s.event, s.present, s.err
}

type testInventory struct {
	ids []string
	err error
}

func (s testInventory) IDs(context.Context, string, string) ([]string, error) { return s.ids, s.err }

func signed(t *testing.T, key nostr.SecretKey, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: 100, Tags: tags, Content: content}
	require.NoError(t, ev.Sign(key))
	return ev
}

func fixture(t *testing.T) (nostr.Event, Epoch, nostr.SecretKey, nostr.SecretKey, nostr.SecretKey) {
	t.Helper()
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	old := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
	newKey := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	sum := sha256.Sum256(nil)
	body := Epoch{IncidentID: "0199f4a6-3700-7000-8000-000000000001", Epoch: 1,
		OldPubkey: old.Public().Hex(), NewPubkey: newKey.Public().Hex(),
		ControlledRelays: []string{"wss://relay.example"}, ActivationBarrier: 150,
		InventoryDigest: hex.EncodeToString(sum[:]), InventoryCount: 0,
		ConsumerRoster: ConsumerIDs(), Activated: true}
	content, err := json.Marshal(body)
	require.NoError(t, err)
	tags := nostr.Tags{{"d", "trust-epoch:deployment-a"}, {"t", Topic}, {"domain", Domain},
		{"schema", Schema}, {"legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()},
		{"epoch", "1"}, {"deleted", "false"}}
	return signed(t, root, tags, string(content)), body, root, old, newKey
}

func TestResolveAndRejectRegressions(t *testing.T) {
	ev, body, root, old, _ := fixture(t)
	state, err := OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &testSource{}
	v := &Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: source, Persistence: state}
	r, err := v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err)
	require.Equal(t, old.Public().Hex(), r.Pubkey)
	source.event, source.present = &ev, true
	r, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err)
	require.Equal(t, body.NewPubkey, r.Pubkey)
	require.Equal(t, uint64(1), r.Epoch)
	_, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err, "same signed head is idempotent")
	source.present = false
	_, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.ErrorIs(t, err, ErrRegression)
	source.present = true
	changed := ev
	changed.Content = "{}"
	source.event = &changed
	_, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.ErrorIs(t, err, ErrUnverifiable)
}

func TestInventoryAndHostility(t *testing.T) {
	ev, body, root, old, newKey := fixture(t)
	oldEvent := nostr.Event{Kind: 1, CreatedAt: 140, Content: "old"}
	require.NoError(t, oldEvent.Sign(old))
	ids := []string{oldEvent.ID.Hex()}
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	body.InventoryDigest = hex.EncodeToString(sum[:])
	body.InventoryCount = 1
	content, err := json.Marshal(body)
	require.NoError(t, err)
	ev.Content = string(content)
	require.NoError(t, ev.Sign(root))
	state, err := OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &testSource{event: &ev, present: true}
	v := &Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: source, Persistence: state}
	r, err := v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err)
	require.Equal(t, Hostile, r.JudgeEvent(oldEvent), "digest alone is not inventory")
	v.Inventory = testInventory{ids: []string{strings.Repeat("a", 64)}}
	r, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err)
	require.Equal(t, Hostile, r.JudgeEvent(oldEvent), "wrong artifact fails closed")
	v.Inventory = testInventory{ids: ids}
	r, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.NoError(t, err)
	require.Equal(t, PreEpochAccepted, r.JudgeEvent(oldEvent))
	late := nostr.Event{Kind: 1, CreatedAt: 151, Content: "late"}
	require.NoError(t, late.Sign(old))
	require.Equal(t, Hostile, r.JudgeEvent(late))
	newEvent := nostr.Event{Kind: 1, CreatedAt: 151, Content: "new"}
	require.NoError(t, newEvent.Sign(newKey))
	require.Equal(t, Accept, r.JudgeEvent(newEvent))
}

func TestJudgeServiceEventRejectsOldPostBarrierAndUnverifiableState(t *testing.T) {
	ev, body, root, old, newKey := fixture(t)
	state, err := OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &testSource{event: &ev, present: true}
	v := &Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: source, Persistence: state}
	late := nostr.Event{Kind: 5100, CreatedAt: nostr.Timestamp(body.ActivationBarrier + 1), Content: "old"}
	require.NoError(t, late.Sign(old))
	decision, service, err := v.JudgeServiceEvent(t.Context(), "deployment-a", late)
	require.NoError(t, err)
	require.True(t, service)
	require.Equal(t, Hostile, decision)
	current := nostr.Event{Kind: 5100, CreatedAt: nostr.Timestamp(body.ActivationBarrier + 1), Content: "new"}
	require.NoError(t, current.Sign(newKey))
	decision, service, err = v.JudgeServiceEvent(t.Context(), "deployment-a", current)
	require.NoError(t, err)
	require.True(t, service)
	require.Equal(t, Accept, decision)
	source.err = errors.New("relay unavailable")
	_, _, err = v.JudgeServiceEvent(t.Context(), "deployment-a", current)
	require.Error(t, err)
}

func TestUnverifiableHeadFailsClosed(t *testing.T) {
	ev, _, root, old, _ := fixture(t)
	state, err := OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &testSource{event: &ev, present: true}
	v := &Verifier{OperatorRoot: old.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: source, Persistence: state}
	_, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.ErrorIs(t, err, ErrUnverifiable)
	v.OperatorRoot = root.Public().Hex()
	source.err = errors.New("relay unavailable")
	_, err = v.ServiceIdentity(t.Context(), "deployment-a")
	require.ErrorContains(t, err, "relay unavailable")
}

func TestAckEnvelopeRoundTrip(t *testing.T) {
	ev, body, _, _, newKey := fixture(t)
	r := Resolution{ServiceID: "deployment-a", Pubkey: body.NewPubkey, Epoch: body.Epoch,
		Record: &body, EventID: ev.ID.Hex()}
	ack, err := NewAcknowledgment(r, "daemon", 200)
	require.NoError(t, err)
	require.NoError(t, ack.Sign(newKey))
	require.NoError(t, VerifyAcknowledgment(ack, r, "daemon"))
	wire, err := json.Marshal(ack)
	require.NoError(t, err)
	var recovered nostr.Event
	require.NoError(t, json.Unmarshal(wire, &recovered))
	require.Equal(t, string(wire), mustJSON(t, recovered))
	require.NoError(t, VerifyAcknowledgment(recovered, r, "daemon"))
	ack.Tags[0][1] = "trust-epoch-ack:deployment-a:cli"
	require.Error(t, VerifyAcknowledgment(ack, r, "daemon"))
}

func TestOperatorVerificationAcknowledgmentAuthority(t *testing.T) {
	ev, body, root, _, newKey := fixture(t)
	r := Resolution{ServiceID: "deployment-a", Pubkey: body.NewPubkey, Epoch: body.Epoch,
		Record: &body, EventID: ev.ID.Hex()}
	ack, err := NewAcknowledgmentForAuthority(r, "web", 200, root.Public().Hex())
	require.NoError(t, err)
	require.NoError(t, ack.Sign(root))
	require.NoError(t, VerifyAcknowledgmentAuthority(ack, r, "web", root.Public().Hex(), true))
	require.Error(t, VerifyAcknowledgmentAuthority(ack, r, "web", newKey.Public().Hex(), true))
	ack, err = NewAcknowledgment(r, "web", 201)
	require.NoError(t, err)
	require.NoError(t, ack.Sign(root))
	require.Error(t, VerifyAcknowledgmentAuthority(ack, r, "web", root.Public().Hex(), true))
}

func TestReadinessEnvelopeIsOperatorBoundAndByteStable(t *testing.T) {
	_, body, root, _, newKey := fixture(t)
	value := Readiness{ServiceID: "deployment-a", ConsumerID: "dns", Epoch: 1,
		IncidentID: body.IncidentID, EnforcingPubkey: body.NewPubkey,
		EvidenceIDs: []string{strings.Repeat("a", 64)}}
	ev, err := NewReadiness(value, 199)
	require.NoError(t, err)
	require.Equal(t, `{"service_id":"deployment-a","consumer_id":"dns","epoch":1,"incident_id":"0199f4a6-3700-7000-8000-000000000001","enforcing_pubkey":"`+body.NewPubkey+`","waived":false,"evidence_ids":["`+strings.Repeat("a", 64)+`"]}`, ev.Content)
	require.NoError(t, ev.Sign(root))
	require.NoError(t, VerifyReadiness(ev, value, root.Public().Hex()))
	require.Error(t, VerifyReadiness(ev, value, newKey.Public().Hex()))
	waiver := value
	waiver.Waived = true
	waiver.ReasonCode = WaiverConsumerDisabled
	ev, err = NewReadiness(waiver, 200)
	require.NoError(t, err)
	require.NoError(t, ev.Sign(root))
	require.NoError(t, VerifyReadiness(ev, waiver, root.Public().Hex()))
	waiver.ReasonCode = "contains secret"
	_, err = NewReadiness(waiver, 201)
	require.Error(t, err)
	waiver.ReasonCode = WaiverConsumerDisabled
	waiver.EvidenceIDs = nil
	_, err = NewReadiness(waiver, 202)
	require.Error(t, err)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestPendingConsumersAlwaysMissing(t *testing.T) {
	all := map[string]bool{}
	for _, id := range ConsumerIDs() {
		all[id] = true
	}
	require.Equal(t, []string{"soulfactory", "pinned-runtimes"}, MissingAcknowledgments(all))
}

func TestSharedEpochAndAckEnvelopeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
		Ack        nostr.Event `json:"ack"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	body, err := Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
	require.NoError(t, err)
	require.Equal(t, fixture.OldPubkey, body.OldPubkey)
	require.Equal(t, fixture.NewPubkey, body.NewPubkey)
	require.Equal(t, string(mustMarshal(t, body)), fixture.Epoch.Content)
	resolved := Resolution{ServiceID: "deployment-a", Pubkey: fixture.NewPubkey,
		Epoch: body.Epoch, Record: &body, EventID: fixture.Epoch.ID.Hex()}
	require.NoError(t, VerifyAcknowledgment(fixture.Ack, resolved, "daemon"))
	ack, err := NewAcknowledgment(resolved, "daemon", 200)
	require.NoError(t, err)
	require.Equal(t, fixture.Ack.Tags, ack.Tags)
	require.Equal(t, fixture.Ack.Content, ack.Content)
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestRetiredIdentitiesRemainHostileAcrossRotationsAndRestart(t *testing.T) {
	event, body, root, old, first := fixture(t)
	path := filepath.Join(t.TempDir(), "epoch.db")
	state, err := OpenBolt(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &testSource{event: &event, present: true}
	verifier := &Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: source, Persistence: state}
	keys := []nostr.SecretKey{old, first, nostr.MustSecretKeyFromHex(strings.Repeat("4", 64)), nostr.MustSecretKeyFromHex(strings.Repeat("5", 64))}
	for i := 1; i < len(keys); i++ {
		body.Epoch, body.OldPubkey, body.NewPubkey = uint64(i), keys[i-1].Public().Hex(), keys[i].Public().Hex()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		event.Content = string(raw)
		for j, tag := range event.Tags {
			if tag[0] == "epoch" {
				event.Tags[j] = nostr.Tag{"epoch", strconv.Itoa(i)}
			}
		}
		require.NoError(t, event.Sign(root))
		source.event = &event
		_, err = verifier.ServiceIdentity(t.Context(), "deployment-a")
		require.NoError(t, err)
	}
	assertRetired := func() {
		t.Helper()
		for _, key := range keys[:len(keys)-1] {
			event := nostr.Event{Kind: 1, CreatedAt: 200, Content: "retired"}
			require.NoError(t, event.Sign(key))
			decision, known, err := verifier.JudgeServiceEvent(t.Context(), "deployment-a", event)
			require.NoError(t, err)
			require.True(t, known, "retired service author must not become an unrelated component")
			require.Equal(t, Hostile, decision)
		}
		current := nostr.Event{Kind: 1, CreatedAt: 200}
		require.NoError(t, current.Sign(keys[len(keys)-1]))
		decision, known, err := verifier.JudgeServiceEvent(t.Context(), "deployment-a", current)
		require.NoError(t, err)
		require.True(t, known)
		require.Equal(t, Accept, decision)
		require.NoError(t, current.Sign(nostr.Generate()))
		decision, known, err = verifier.JudgeServiceEvent(t.Context(), "deployment-a", current)
		require.NoError(t, err)
		require.False(t, known)
		require.Equal(t, Accept, decision)
	}
	assertRetired()
	require.NoError(t, state.Close())
	state, err = OpenBolt(path)
	require.NoError(t, err)
	verifier.Persistence = state
	assertRetired()
}

func TestLegacyEpochLedgerDoesNotInventLostIdentityHistory(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(strconv.FormatBool(later), func(t *testing.T) {
			event, body, root, old, first := fixture(t)
			if later {
				body.Epoch = 2
				body.OldPubkey = first.Public().Hex()
				body.NewPubkey = nostr.Generate().Public().Hex()
				event.Tags[5] = nostr.Tag{"epoch", "2"}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				event.Content = string(raw)
				require.NoError(t, event.Sign(root))
			}
			state, err := OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, state.Close()) })
			legacy := Accepted{Epoch: body.Epoch, EventID: event.ID.Hex(), OldPubkey: body.OldPubkey, NewPubkey: body.NewPubkey, Activated: true}
			raw, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, state.shared.db.Update(func(tx *bbolt.Tx) error { return tx.Bucket(acceptedBucket).Put([]byte("deployment-a"), raw) }))
			v := &Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(), Source: &testSource{event: &event, present: true}, Persistence: state}
			_, err = v.ServiceIdentity(t.Context(), "deployment-a")
			if later {
				require.ErrorIs(t, err, ErrUnverifiable)
			} else {
				require.NoError(t, err)
				head, present, err := state.Last(t.Context(), "deployment-a")
				require.NoError(t, err)
				require.True(t, present)
				require.ElementsMatch(t, []string{body.OldPubkey, body.NewPubkey}, head.KnownPubkeys)
			}
		})
	}
}
