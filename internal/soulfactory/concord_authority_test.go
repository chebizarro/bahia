package soulfactory

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func mustConcordID32(t *testing.T, value string) [32]byte {
	t.Helper()
	id, err := concordID32(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func concordEditionRumorID(t *testing.T, edition concordTestEdition) string {
	t.Helper()
	parsed, err := parseConcordControlEdition(edition.seal.String())
	if err != nil {
		t.Fatal(err)
	}
	return parsed.rumorID.Hex()
}

func concordAuthorityFold(t *testing.T, f *concordRotationFixture, owner fakeSigner, events ...concordTestEdition) *concordControlFold {
	t.Helper()
	bundle := concordRotatedBundle(t, f)
	community := mustConcordID32(t, f.communityID)
	root, err := hex.DecodeString(bundle.CommunityRoot)
	if err != nil {
		t.Fatal(err)
	}
	read, err := concordControlReadKey(root, community, bundle.RootEpoch)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := make([]*nostr.Event, len(events))
	for i := range events {
		wrapped[i] = events[i].wrap
	}
	fold, err := foldConcordControlPlane(wrapped, mustConcordPubKey(t, bundle.ControlPK),
		read.ConversationKey, community, mustConcordPubKey(t, owner.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	return fold
}

func TestConcordAuthorityPrecedesVersionAndRumorID(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	delegate := newFakeSigner(t)
	roleID := concordTestBytes32(t, 0xa1)
	role := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"manager","position":1,"permissions":"4","scope":{"kind":"server"},"color":0}`, roleID))
	grantID, err := concordGrantLocator(community, mustConcordID32(t, delegate.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	grant := concordTestTypedControlEdition(t, f, owner, 3, grantID, 1, "", fmt.Sprintf(
		`{"member":"%s","role_ids":["%x"]}`, delegate.pubkey, roleID))
	citation := concordAuthorityCitation{eid: hex.EncodeToString(grantID[:]), version: 1, hash: grant.hash}
	ownerEdit := concordTestTypedControlEdition(t, f, owner, 0, community, 1, "", `{"name":"owner"}`)
	var delegateEdit concordTestEdition
	for i := 0; i < 1024; i++ {
		delegateEdit = concordTestTypedControlEdition(t, f, delegate, 0, community, 1, "", fmt.Sprintf(`{"name":"delegate-%d"}`, i), citation.tag())
		if concordEditionRumorID(t, delegateEdit) < concordEditionRumorID(t, ownerEdit) {
			break
		}
	}
	if concordEditionRumorID(t, delegateEdit) >= concordEditionRumorID(t, ownerEdit) {
		t.Fatal("could not craft a lower-id delegate twin")
	}
	for _, order := range [][]concordTestEdition{
		{delegateEdit, ownerEdit, grant, role}, {role, grant, ownerEdit, delegateEdit},
	} {
		fold := concordAuthorityFold(t, f, owner, order...)
		head, ok := fold.head(community)
		if !ok || head.hash != ownerEdit.hash {
			t.Fatalf("authority lost to lower rumor id: head=%#v ok=%t", head, ok)
		}
	}
	// An unauthorized higher version is ineligible before version selection.
	stranger := newFakeSigner(t)
	forged := concordTestTypedControlEdition(t, f, stranger, 0, community, 2, ownerEdit.hash, `{"name":"forged"}`)
	fold := concordAuthorityFold(t, f, owner, role, grant, ownerEdit, forged)
	if head, ok := fold.head(community); !ok || head.hash != ownerEdit.hash {
		t.Fatalf("unauthorized higher version won: %#v ok=%t", head, ok)
	}
}

func TestConcordAuthorityEqualRankUsesLowestRumorID(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	roleID := concordTestBytes32(t, 0xb2)
	role := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"peer","position":1,"permissions":"4","scope":{"kind":"server"},"color":0}`, roleID))
	var editions []concordTestEdition
	editions = append(editions, role)
	var actions []concordTestEdition
	for i := 0; i < 2; i++ {
		actor := newFakeSigner(t)
		grantID, err := concordGrantLocator(community, mustConcordID32(t, actor.pubkey))
		if err != nil {
			t.Fatal(err)
		}
		grant := concordTestTypedControlEdition(t, f, owner, 3, grantID, 1, "", fmt.Sprintf(
			`{"member":"%s","role_ids":["%x"]}`, actor.pubkey, roleID))
		citation := concordAuthorityCitation{eid: hex.EncodeToString(grantID[:]), version: 1, hash: grant.hash}
		action := concordTestTypedControlEdition(t, f, actor, 0, community, 1, "", fmt.Sprintf(`{"name":"peer-%d"}`, i), citation.tag())
		editions = append(editions, grant)
		actions = append(actions, action)
	}
	want := actions[0]
	if concordEditionRumorID(t, actions[1]) < concordEditionRumorID(t, want) {
		want = actions[1]
	}
	for _, order := range [][]concordTestEdition{
		append(append([]concordTestEdition{}, editions...), actions...),
		{actions[1], editions[2], actions[0], editions[1], editions[0]},
	} {
		fold := concordAuthorityFold(t, f, owner, order...)
		head, ok := fold.head(community)
		if !ok || head.hash != want.hash {
			t.Fatalf("same-rank NIP-01 tie = %#v ok=%t, want %s", head, ok, want.hash)
		}
	}
}

func TestConcordAuthorityDefersMissingExactGrantAndRejectsDemotion(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	delegate := newFakeSigner(t)
	roleID := concordTestBytes32(t, 0xc3)
	role := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"manager","position":1,"permissions":"4","scope":{"kind":"server"},"color":0}`, roleID))
	grantID, err := concordGrantLocator(community, mustConcordID32(t, delegate.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	grant := concordTestTypedControlEdition(t, f, owner, 3, grantID, 1, "", fmt.Sprintf(
		`{"member":"%s","role_ids":["%x"]}`, delegate.pubkey, roleID))
	citation := concordAuthorityCitation{eid: hex.EncodeToString(grantID[:]), version: 1, hash: grant.hash}
	action := concordTestTypedControlEdition(t, f, delegate, 0, community, 1, "", `{"name":"staff"}`, citation.tag())
	missing := concordAuthorityFold(t, f, owner, role, action)
	if _, ok := missing.head(community); ok || len(missing.unresolved) == 0 {
		t.Fatalf("missing exact citation produced a head: %#v", missing)
	}
	if err := concordFoldIsCompactable(missing); err == nil || !strings.Contains(err.Error(), "unresolved CORD-04 authority") {
		t.Fatalf("missing citation allowed compaction: %v", err)
	}
	revoked := concordTestTypedControlEdition(t, f, owner, 3, grantID, 2, grant.hash, fmt.Sprintf(
		`{"member":"%s","role_ids":[]}`, delegate.pubkey))
	fold := concordAuthorityFold(t, f, owner, role, grant, revoked, action)
	if _, ok := fold.head(community); ok {
		t.Fatal("superseded Grant kept an old cited permission alive")
	}
	if _, unresolved := fold.unresolved[community]; unresolved {
		t.Fatal("complete demotion evidence should reject, not defer")
	}
}

func TestConcordAuthorityResolvesRecursiveOwnerRootedGrantClosure(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	leader, delegate := newFakeSigner(t), newFakeSigner(t)
	managerID, memberID := concordTestBytes32(t, 0xe1), concordTestBytes32(t, 0xe2)
	manager := concordTestTypedControlEdition(t, f, owner, 1, managerID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"manager","position":1,"permissions":"5","scope":{"kind":"server"},"color":0}`, managerID))
	memberRole := concordTestTypedControlEdition(t, f, owner, 1, memberID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"member","position":2,"permissions":"4","scope":{"kind":"server"},"color":0}`, memberID))
	leaderGrantID, err := concordGrantLocator(community, mustConcordID32(t, leader.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	leaderGrant := concordTestTypedControlEdition(t, f, owner, 3, leaderGrantID, 1, "", fmt.Sprintf(
		`{"member":"%s","role_ids":["%x"]}`, leader.pubkey, managerID))
	delegateGrantID, err := concordGrantLocator(community, mustConcordID32(t, delegate.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	leaderCitation := concordAuthorityCitation{eid: hex.EncodeToString(leaderGrantID[:]), version: 1, hash: leaderGrant.hash}
	delegateGrant := concordTestTypedControlEdition(t, f, leader, 3, delegateGrantID, 1, "", fmt.Sprintf(
		`{"member":"%s","role_ids":["%x"]}`, delegate.pubkey, memberID), leaderCitation.tag())
	delegateCitation := concordAuthorityCitation{eid: hex.EncodeToString(delegateGrantID[:]), version: 1, hash: delegateGrant.hash}
	action := concordTestTypedControlEdition(t, f, delegate, 0, community, 1, "", `{"name":"authorized"}`, delegateCitation.tag())
	complete := concordAuthorityFold(t, f, owner, action, delegateGrant, memberRole, leaderGrant, manager)
	if head, ok := complete.head(community); !ok || head.hash != action.hash {
		t.Fatalf("recursive owner-rooted authority failed: %#v ok=%t", head, ok)
	}
	incomplete := concordAuthorityFold(t, f, owner, action, delegateGrant, memberRole, manager)
	if _, ok := incomplete.head(community); ok {
		t.Fatal("missing ancestor Grant authorized descendant action")
	}
	if _, missing := incomplete.unresolved[community]; !missing {
		t.Fatal("missing recursive closure was not deferred")
	}
}

func TestConcordAuthorityDefersCyclicGrantEvidence(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	first, second := newFakeSigner(t), newFakeSigner(t)
	roleID := concordTestBytes32(t, 0xf1)
	role := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"manager","position":1,"permissions":"1","scope":{"kind":"server"},"color":0}`, roleID))
	firstID, err := concordGrantLocator(community, mustConcordID32(t, first.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := concordGrantLocator(community, mustConcordID32(t, second.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	firstBody := fmt.Sprintf(`{"member":"%s","role_ids":["%x"]}`, first.pubkey, roleID)
	secondBody := fmt.Sprintf(`{"member":"%s","role_ids":["%x"]}`, second.pubkey, roleID)
	firstHash, err := concordEditionHash(firstID, 1, nil, []byte(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := concordEditionHash(secondID, 1, nil, []byte(secondBody))
	if err != nil {
		t.Fatal(err)
	}
	firstGrant := concordTestTypedControlEdition(t, f, second, 3, firstID, 1, "", firstBody,
		concordAuthorityCitation{eid: hex.EncodeToString(secondID[:]), version: 1, hash: secondHash}.tag())
	secondGrant := concordTestTypedControlEdition(t, f, first, 3, secondID, 1, "", secondBody,
		concordAuthorityCitation{eid: hex.EncodeToString(firstID[:]), version: 1, hash: firstHash}.tag())
	fold := concordAuthorityFold(t, f, owner, firstGrant, role, secondGrant)
	if _, ok := fold.head(firstID); ok {
		t.Fatal("cyclic Grant chain produced authority")
	}
	if _, missing := fold.unresolved[firstID]; !missing {
		t.Fatal("cyclic Grant chain did not defer")
	}
}

func TestConcordAuthorityIgnoresRolesBeyondLowestHundred(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	community := mustConcordID32(t, f.communityID)
	owner := f.staff.fakeSigner
	delegate := newFakeSigner(t)
	editions := make([]concordTestEdition, 0, 103)
	for i := 0; i < 100; i++ {
		roleID := [32]byte{}
		roleID[31] = byte(i)
		editions = append(editions, concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
			`{"role_id":"%x","name":"other","position":2,"permissions":"0","scope":{"kind":"server"},"color":0}`, roleID)))
	}
	ignoredID := concordTestBytes32(t, 0xff)
	editions = append(editions, concordTestTypedControlEdition(t, f, owner, 1, ignoredID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","name":"ignored","position":1,"permissions":"4","scope":{"kind":"server"},"color":0}`, ignoredID)))
	grantID, err := concordGrantLocator(community, mustConcordID32(t, delegate.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	grant := concordTestTypedControlEdition(t, f, owner, 3, grantID, 1, "", fmt.Sprintf(
		`{"member":"%s","role_ids":["%x"]}`, delegate.pubkey, ignoredID))
	citation := concordAuthorityCitation{eid: hex.EncodeToString(grantID[:]), version: 1, hash: grant.hash}
	action := concordTestTypedControlEdition(t, f, delegate, 0, community, 1, "", `{"name":"not-authorized"}`, citation.tag())
	editions = append(editions, grant, action)
	fold := concordAuthorityFold(t, f, owner, editions...)
	if _, ok := fold.head(community); ok {
		t.Fatal("101st Role granted authority")
	}
	if _, ok := fold.head(ignoredID); ok {
		t.Fatal("101st Role was folded")
	}
}

func TestConcordAuthorityKeepsTerminalChannelMetadata(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	owner := f.staff.fakeSigner
	channel := concordTestBytes32(t, 0x9a)
	deleted := concordTestTypedControlEdition(t, f, owner, 2, channel, 1, "", `{"deleted":true}`)
	resurrected := concordTestTypedControlEdition(t, f, owner, 2, channel, 2, deleted.hash,
		`{"name":"resurrected","private":false}`)
	fold := concordAuthorityFold(t, f, owner, resurrected, deleted)
	head, ok := fold.head(channel)
	if !ok || head.hash != deleted.hash {
		t.Fatalf("terminal Channel was revived: %#v ok=%t", head, ok)
	}
}

func TestConcordAuthorityPredecessorIgnoresUnauthorizedSameHashTwin(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	owner := f.staff.fakeSigner
	stranger := newFakeSigner(t)
	roleID := concordTestBytes32(t, 0x9b)
	body := fmt.Sprintf(`{"role_id":"%x","name":"member","position":2,"permissions":"0","scope":{"kind":"server"}}`, roleID)
	first := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", body)
	forged := concordTestTypedControlEdition(t, f, stranger, 1, roleID, 1, "", body)
	if first.hash != forged.hash {
		t.Fatal("fixture needs identical edition hashes")
	}
	second := concordTestTypedControlEdition(t, f, owner, 1, roleID, 2, first.hash, body)
	for _, order := range [][]concordTestEdition{
		{forged, first, second}, {first, second, forged},
	} {
		fold := concordAuthorityFold(t, f, owner, order...)
		head, ok := fold.head(roleID)
		if !ok || head.hash != second.hash {
			t.Fatalf("authorized predecessor lost to same-hash twin: %#v ok=%t", head, ok)
		}
	}
}

func TestConcordAuthorityCitationAndRosterPayloadValidation(t *testing.T) {
	f := newConcordRotationFixture(t, 0)
	owner := f.staff.fakeSigner
	community := mustConcordID32(t, f.communityID)
	badTags := []nostr.Tag{
		{"vac", "bad", "1", strings.Repeat("a", 64)},
		{"vac", strings.Repeat("a", 64), "01", strings.Repeat("b", 64)},
		{"vac", strings.Repeat("a", 64), "+1", strings.Repeat("b", 64)},
		{"vac", strings.Repeat("a", 64), "1"},
	}
	for _, tag := range badTags {
		candidate := concordTestTypedControlEdition(t, f, owner, 0, community, 1, "", `{"name":"bad"}`, tag)
		if _, err := parseConcordControlEdition(candidate.seal.String()); err == nil {
			t.Fatalf("accepted malformed vac %#v", tag)
		}
	}
	roleID := concordTestBytes32(t, 0xd4)
	badRole := concordTestTypedControlEdition(t, f, owner, 1, roleID, 1, "", fmt.Sprintf(
		`{"role_id":"%x","position":0,"permissions":"4","scope":{"kind":"server"}}`, roleID))
	badGrantID, err := concordGrantLocator(community, mustConcordID32(t, owner.pubkey))
	if err != nil {
		t.Fatal(err)
	}
	badGrant := concordTestTypedControlEdition(t, f, owner, 3, badGrantID, 1, "", `{"member":"bad","role_ids":[]}`)
	fold := concordAuthorityFold(t, f, owner, badRole, badGrant)
	if len(fold.heads) != 0 {
		t.Fatalf("invalid typed roster seated heads: %v", fold.heads)
	}
}
