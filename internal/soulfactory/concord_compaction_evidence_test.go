package soulfactory

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
)

// This is an availability witness, not an authority resolver: an exact signed
// Grant exists before compaction, but neither the returned fold nor the new
// epoch retains it. Current roles deliberately do not demote the action's actor.
func TestConcordCompactionOmitsExactAuthorityCitationEvidence(t *testing.T) {
	f := newConcordRotationFixture(t, 4)
	owner := f.staff.fakeSigner
	delegate := newFakeSigner(t)
	member := newFakeSigner(t)
	adminRole := concordTestBytes32(t, 0xa1)
	memberRole := concordTestBytes32(t, 0xb2)
	adminRoleHex, memberRoleHex := hex.EncodeToString(adminRole[:]), hex.EncodeToString(memberRole[:])
	admin := concordTestTypedControlEdition(t, f, owner, 1, adminRole, 1, "",
		`{"role_id":"`+adminRoleHex+`","name":"admin","position":1,"permissions":"1","scope":{"kind":"server"},"color":0}`)
	basic := concordTestTypedControlEdition(t, f, owner, 1, memberRole, 1, "",
		`{"role_id":"`+memberRoleHex+`","name":"member","position":2,"permissions":"0","scope":{"kind":"server"},"color":0}`)
	grantID := concordTestGrantEID(t, f.communityID, delegate.pubkey)
	grantEID, err := concordID32(grantID)
	if err != nil {
		t.Fatal(err)
	}
	// A staff-making Grant also delivers the current control_root. Include
	// the real 40-byte pairwise wrap so missing delivery is not this witness.
	controlRoot, err := hex.DecodeString(f.priorControlRoot)
	if err != nil {
		t.Fatal(err)
	}
	controlWrap, err := f.staff.NIP44EncryptBytes(t.Context(), mustConcordPubKey(t, delegate.pubkey),
		append(binary.BigEndian.AppendUint64(nil, 3), controlRoot...))
	if err != nil {
		t.Fatal(err)
	}
	grantBody := `{"member":"` + delegate.pubkey + `","role_ids":["` + adminRoleHex + `"],"control_wrap":"` + controlWrap + `"}`
	first := concordTestControlEdition(t, f, owner, grantEID, 1, "", grantBody)
	// Same role assignment, different version/hash: a current head cannot
	// stand in for the exact edition the action's signature pins.
	head := concordTestControlEdition(t, f, owner, grantEID, 2, first.hash, grantBody)
	memberEID, err := concordID32(concordTestGrantEID(t, f.communityID, member.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	oldCitation := concordAuthorityCitation{eid: grantID, version: 1, hash: first.hash}
	action := concordTestControlEdition(t, f, delegate, memberEID, 1, "",
		`{"member":"`+member.pubkey+`","role_ids":["`+memberRoleHex+`"]}`, oldCitation.tag())
	prior := []nostr.Event{*admin.wrap, *basic.wrap, *first.wrap, *head.wrap, *action.wrap}
	// Prove the exact citation was readable and signature-checked before the
	// lossy fold, without teaching the structural parser to authorize it.
	parsedFirst, err := parseConcordControlEdition(first.seal.String())
	if err != nil || parsedFirst.entity != grantEID || parsedFirst.version != 1 || parsedFirst.hash != oldCitation.hash {
		t.Fatalf("original exact Grant unavailable: %v", err)
	}
	fold := concordFoldRepublished(t, f, concordRotatedBundle(t, f), prior)
	if fold.editions != 5 || len(fold.heads) != 4 || len(fold.suspended) != 0 {
		t.Fatalf("prior structural fold = %#v", fold)
	}
	plan, next, bundle := concordTestRefoundingPlan(t, f)
	compaction, err := f.membership.republishConcordCompaction(t.Context(), next, fold,
		plan.communityID32, plan.nextRoot[:], plan.nextControlRoot[:], 4, plan.nextControlPK)
	if err != nil || compaction.Entities != 4 {
		t.Fatalf("structural compaction = %#v: %v", compaction, err)
	}
	joined := concordFoldRepublished(t, f, bundle, f.endpoint.published)
	if joined.editions != 4 || len(joined.heads) != 4 {
		t.Fatalf("fresh-joiner structural fold = %#v", joined)
	}
	retained, ok := joined.head(memberEID)
	if !ok || retained.seal.Content != action.seal.Content || retained.seal.Sig != action.seal.Sig {
		t.Fatal("compaction changed the signed action rather than preserving it")
	}
	var rumor nostr.Event
	if err := json.Unmarshal([]byte(retained.seal.Content), &rumor); err != nil {
		t.Fatal(err)
	}
	assertConcordTag(t, rumor, "vac", grantID, "1", first.hash)
	currentGrant, ok := joined.head(grantEID)
	if !ok || currentGrant.version != 2 || currentGrant.hash != head.hash || currentGrant.prev != first.hash {
		t.Fatal("expected only the newer Grant head and its predecessor hash")
	}
	// Scan every emitted seal, not just the fresh fold's heads: the evidence
	// is missing from the compaction output itself, not hidden by another fold.
	communityID, err := concordID32(f.communityID)
	if err != nil {
		t.Fatal(err)
	}
	read, err := concordControlReadKey(plan.nextRoot[:], communityID, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrap := range f.endpoint.published {
		single, err := foldConcordControlPlane([]*nostr.Event{&wrap}, mustConcordPubKey(t, bundle.ControlPK), read.ConversationKey)
		if err != nil || single.editions != 1 {
			t.Fatalf("emitted seal did not parse: %v", err)
		}
		for _, edition := range single.heads {
			if edition.entity == grantEID && edition.version == oldCitation.version && edition.hash == oldCitation.hash {
				t.Fatal("fixture must demonstrate omission of the exact cited edition")
			}
		}
	}
	// Having old ciphertext is insufficient with only the new invite material.
	oldAtNewEpoch := concordFoldRepublished(t, f, bundle, prior)
	if oldAtNewEpoch.editions != 0 {
		t.Fatal("fresh-joiner input unexpectedly includes old-epoch evidence")
	}
	f.assertUnrotated(t) // The test invoked compaction mechanics, never Rotate.
}
