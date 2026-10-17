package identityrecovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

func TestProductionAcknowledgmentReaderUsesPinnedConsumerAuthorities(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
		Ack        nostr.Event `json:"ack"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	body, err := trustepoch.Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "ledger.db"), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	})
	state := State{IncidentID: body.IncidentID, OldPubkey: fixture.OldPubkey,
		OperatorPubkey: fixture.RootPubkey, NewPubkey: fixture.NewPubkey, Epoch: 1,
		Records: map[string]nostr.Event{fixture.Epoch.ID.Hex(): fixture.Epoch}}
	if err := ledger.Save(state); err != nil {
		t.Fatal(err)
	}
	authorities := map[string]string{"daemon": fixture.NewPubkey,
		"cli": fixture.RootPubkey, "web": fixture.RootPubkey, "pinned-runtimes": fixture.RootPubkey}
	for i, id := range []string{"sidecar", "dns", "soulfactory"} {
		key := nostr.MustSecretKeyFromHex(strings.Repeat(string(rune('4'+i)), 64))
		authorities[id] = key.Public().Hex()
	}
	authorities["loom"], authorities["hiveci"] = fixture.RootPubkey, fixture.RootPubkey
	source := &testSource{events: []nostr.Event{fixture.Epoch}}
	reader := &AcknowledgmentReader{Source: source, Local: testLocal{events: []nostr.Event{fixture.Ack}},
		Ledger: ledger, ServiceID: "deployment-a", OperatorRoot: fixture.RootPubkey,
		OldPubkey: fixture.OldPubkey, Authorities: authorities}
	missing, err := reader.Missing(t.Context(), 1, trustepoch.ConsumerIDs())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(missing, "daemon") || !slices.Contains(missing, "sidecar") || !slices.Contains(missing, "pinned-runtimes") {
		t.Fatalf("wrong consumer authorities: %v", missing)
	}
	forged, err := trustepoch.NewAcknowledgment(trustepoch.Resolution{ServiceID: "deployment-a",
		Pubkey: fixture.NewPubkey, Epoch: 1, Record: &body, EventID: fixture.Epoch.ID.Hex()}, "sidecar", 201)
	if err != nil {
		t.Fatal(err)
	}
	if err := forged.Sign(nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))); err != nil {
		t.Fatal(err)
	}
	source.events = append(source.events, forged)
	missing, err = reader.Missing(t.Context(), 1, trustepoch.ConsumerIDs())
	if err != nil || !slices.Contains(missing, "sidecar") {
		t.Fatalf("service-key sidecar forgery admitted: %v %v", missing, err)
	}
	authorities["sidecar"] = fixture.NewPubkey
	if _, err := reader.Missing(t.Context(), 1, trustepoch.ConsumerIDs()); err == nil {
		t.Fatal("shared service-key authority accepted for sidecar")
	}
	delete(authorities, "sidecar")
	if _, err := reader.Missing(t.Context(), 1, trustepoch.ConsumerIDs()); err == nil {
		t.Fatal("missing authority table entry accepted")
	}
}

func TestProductionReadinessRequiresAttestationOrWaiverForEveryConsumer(t *testing.T) {
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	old := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
	newKey := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "ledger.db"), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	})
	incident := "incident-1"
	if err := ledger.Save(State{IncidentID: incident, OldPubkey: old.Public().Hex(),
		OperatorPubkey: root.Public().Hex(), NewPubkey: newKey.Public().Hex()}); err != nil {
		t.Fatal(err)
	}
	authorities := map[string]string{"daemon": newKey.Public().Hex(), "cli": root.Public().Hex(),
		"web": root.Public().Hex(), "pinned-runtimes": root.Public().Hex()}
	for i, id := range []string{"sidecar", "dns", "soulfactory"} {
		key := nostr.MustSecretKeyFromHex(strings.Repeat(string(rune('4'+i)), 64))
		authorities[id] = key.Public().Hex()
	}
	authorities["loom"], authorities["hiveci"] = root.Public().Hex(), root.Public().Hex()
	src := &testSource{}
	reader := &AcknowledgmentReader{Source: src, Local: testLocal{}, Ledger: ledger,
		ServiceID: "deployment-a", OperatorRoot: root.Public().Hex(),
		OldPubkey: old.Public().Hex(), Authorities: authorities}
	for i, id := range trustepoch.ConsumerIDs() {
		if id == "soulfactory" || id == "pinned-runtimes" {
			continue
		}
		value := trustepoch.Readiness{ServiceID: "deployment-a", ConsumerID: id,
			Epoch: 1, IncidentID: incident, EnforcingPubkey: newKey.Public().Hex(),
			EvidenceIDs: []string{fmt.Sprintf("%064x", i+1)}}
		ev, err := trustepoch.NewReadiness(value, nostr.Timestamp(100+i))
		if err != nil {
			t.Fatal(err)
		}
		if err := ev.Sign(root); err != nil {
			t.Fatal(err)
		}
		src.events = append(src.events, ev)
	}
	proof, err := reader.Readiness(t.Context(), 1, incident, newKey.Public().Hex(), trustepoch.ConsumerIDs())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(proof.Missing, []string{"soulfactory", "pinned-runtimes"}) {
		t.Fatalf("missing readiness not reported: %v", proof.Missing)
	}
	for i, id := range proof.Missing {
		value := trustepoch.Readiness{ServiceID: "deployment-a", ConsumerID: id,
			Epoch: 1, IncidentID: incident, EnforcingPubkey: newKey.Public().Hex(),
			Waived: true, ReasonCode: trustepoch.WaiverConsumerDisabled,
			EvidenceIDs: []string{fmt.Sprintf("%064x", i+50)}}
		ev, err := trustepoch.NewReadiness(value, nostr.Timestamp(200+i))
		if err != nil {
			t.Fatal(err)
		}
		if err := ev.Sign(root); err != nil {
			t.Fatal(err)
		}
		src.events = append(src.events, ev)
	}
	proof, err = reader.Readiness(t.Context(), 1, incident, newKey.Public().Hex(), trustepoch.ConsumerIDs())
	if err != nil || len(proof.Missing) != 0 || len(proof.Waivers) != 2 {
		t.Fatalf("signed waivers not recorded: %+v %v", proof, err)
	}
	invalid := trustepoch.Readiness{ServiceID: "deployment-a", ConsumerID: "soulfactory",
		Epoch: 1, IncidentID: incident, EnforcingPubkey: newKey.Public().Hex(),
		Waived: true, ReasonCode: trustepoch.WaiverConsumerDisabled}
	content, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	invalidEvent, err := trustepoch.NewReadiness(trustepoch.Readiness{ServiceID: invalid.ServiceID,
		ConsumerID: invalid.ConsumerID, Epoch: invalid.Epoch, IncidentID: invalid.IncidentID,
		EnforcingPubkey: invalid.EnforcingPubkey, Waived: true, ReasonCode: invalid.ReasonCode,
		EvidenceIDs: []string{fmt.Sprintf("%064x", 60)}}, 250)
	if err != nil {
		t.Fatal(err)
	}
	invalidEvent.Content = string(content)
	if err := invalidEvent.Sign(root); err != nil {
		t.Fatal(err)
	}
	src.events = append(src.events, invalidEvent)
	if proof, err := reader.Readiness(t.Context(), 1, incident, newKey.Public().Hex(), trustepoch.ConsumerIDs()); err == nil || len(proof.Waivers) != 0 {
		t.Fatalf("zero-evidence waiver counted at read: %+v %v", proof, err)
	}
	for i, id := range []string{"soulfactory", "pinned-runtimes"} {
		value := trustepoch.Readiness{ServiceID: "deployment-a", ConsumerID: id,
			Epoch: 1, IncidentID: incident, EnforcingPubkey: newKey.Public().Hex(),
			EvidenceIDs: []string{fmt.Sprintf("%064x", i+100)}}
		ev, err := trustepoch.NewReadiness(value, nostr.Timestamp(300+i))
		if err != nil {
			t.Fatal(err)
		}
		if err := ev.Sign(root); err != nil {
			t.Fatal(err)
		}
		src.events = append(src.events, ev)
	}
	proof, err = reader.Readiness(t.Context(), 1, incident, newKey.Public().Hex(), trustepoch.ConsumerIDs())
	if err != nil || len(proof.Missing) != 0 || len(proof.Waivers) != 0 {
		t.Fatalf("signed enforcement attestations did not supersede waivers: %+v %v", proof, err)
	}
}

func TestOperatorAcknowledgmentPublisherBindsWebAttestationToActivatedEpoch(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	body, err := trustepoch.Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
	if err != nil {
		t.Fatal(err)
	}
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	if root.Public().Hex() != fixture.RootPubkey {
		t.Fatal("fixture root mismatch")
	}
	cut := Receipt{IncidentID: body.IncidentID, Phase: CutActivateTrust, Status: "completed",
		InputDigest: strings.Repeat("a", 64), CompletedAt: "2026-10-16T00:00:00Z"}
	content, err := json.Marshal(cut)
	if err != nil {
		t.Fatal(err)
	}
	cutEvent := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: 201,
		Tags: nostr.Tags{{"d", "identity-incident:" + body.IncidentID + ":phase:cut-activate-trust"},
			{"t", kinds.CPStateTopicIdentityRecoveryReceipt}, {"domain", "identity"},
			{"schema", "bahia.identity-recovery-receipt.v1"},
			{"legacy_kind", kinds.CPStateFamilyIdentityRecoveryReceipt.TagValue()},
			{"incident", body.IncidentID}, {"deleted", "false"}}, Content: string(content)}
	if err := cutEvent.Sign(root); err != nil {
		t.Fatal(err)
	}
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "ledger.db"), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Fatal(err)
		}
	})
	state := State{IncidentID: body.IncidentID, OldPubkey: fixture.OldPubkey,
		OperatorPubkey: fixture.RootPubkey, NewPubkey: fixture.NewPubkey, Epoch: 1,
		LastTimestamp: 201, Records: map[string]nostr.Event{fixture.Epoch.ID.Hex(): fixture.Epoch,
			cutEvent.ID.Hex(): cutEvent}}
	if err := ledger.Save(state); err != nil {
		t.Fatal(err)
	}
	authorities := map[string]string{"daemon": fixture.NewPubkey,
		"cli": fixture.RootPubkey, "web": fixture.RootPubkey, "pinned-runtimes": fixture.RootPubkey}
	for i, id := range []string{"sidecar", "dns", "soulfactory"} {
		authorities[id] = nostr.MustSecretKeyFromHex(strings.Repeat(string(rune('4'+i)), 64)).Public().Hex()
	}
	authorities["loom"], authorities["hiveci"] = fixture.RootPubkey, fixture.RootPubkey
	source := &testSource{events: []nostr.Event{fixture.Epoch}}
	reader := &AcknowledgmentReader{Source: source, Local: testLocal{}, Ledger: ledger,
		ServiceID: "deployment-a", OperatorRoot: fixture.RootPubkey,
		OldPubkey: fixture.OldPubkey, Authorities: authorities}
	operator, err := nostrutil.NewLocalKeyer(root.Hex())
	if err != nil {
		t.Fatal(err)
	}
	old, _ := nostr.PubKeyFromHex(fixture.OldPubkey)
	engine := &Engine{Config: Config{ServiceID: "deployment-a", OldPubkey: old,
		ReasonCode: "compromised", ControlledRelays: []string{"wss://relay.example"}, CleanupRelays: []string{"wss://relay.example"},
		ConsumerRoster: trustepoch.ConsumerIDs(), OperatorPubkey: root.Public(),
		LocalStorePath: filepath.Join(t.TempDir(), "daemon.db")},
		Ledger: ledger, Source: source, Cleanup: source, Local: testLocal{}, Operator: operator, Acks: reader}
	authorities["loom"] = nostr.MustSecretKeyFromHex(strings.Repeat("9", 64)).Public().Hex()
	if err := engine.AttestAcknowledgment(t.Context(), "loom"); err == nil {
		t.Fatal("accepted a fabricated Loom component authority")
	}
	authorities["loom"] = fixture.RootPubkey
	if err := engine.AttestAcknowledgment(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"loom", "hiveci"} {
		if err := engine.AttestAcknowledgment(t.Context(), id); err != nil {
			t.Fatalf("%s operator attestation: %v", id, err)
		}
	}
	missing, err := reader.Missing(t.Context(), 1, trustepoch.ConsumerIDs())
	if err != nil || slices.Contains(missing, "web") || slices.Contains(missing, "loom") || slices.Contains(missing, "hiveci") {
		t.Fatalf("operator web attestation was not admitted: %v %v", missing, err)
	}
	if err := engine.AttestAcknowledgment(t.Context(), "sidecar"); err == nil {
		t.Fatal("operator impersonated component acknowledgment")
	}
}
