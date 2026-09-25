package soulfactory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestConcordRotationUnresolvedAuthorityHasNoSideEffects(t *testing.T) {
	for _, actor := range []string{"owner", "non-owner"} {
		for _, operation := range []string{"channel", "refound", "refound and channel"} {
			if actor == "owner" && operation == "channel" {
				continue // The owner channel-only success path is tested separately.
			}
			for _, plane := range []string{"empty", "owner grant", "equal version", "higher version"} {
				t.Run(actor+"/"+operation+"/"+plane, func(t *testing.T) {
					owner := newFakeSigner(t)
					ownerHex := owner.pubkey
					if actor == "owner" {
						ownerHex = ""
					}
					f := newConcordRotationFixtureOwnedBy(t, 0, ownerHex)
					if actor == "owner" {
						owner = f.staff.fakeSigner
						// Replace the default inbox responses with the candidate plane.
						for len(f.endpoint.subscribeQueue) > 0 {
							<-f.endpoint.subscribeQueue
						}
					}
					var events []*nostr.Event
					if plane != "empty" {
						first := concordTestGrantEdition(t, f, owner, f.staff.pubkey, 1, "")
						events = append(events, first.wrap)
						if plane == "equal version" || plane == "higher version" {
							version, prev := uint64(1), ""
							if plane == "higher version" {
								version, prev = 2, first.hash
							}
							untrusted := concordTestGrantEdition(t, f, newFakeSigner(t), f.staff.pubkey, version, prev)
							events = append(events, untrusted.wrap)
						}
					}
					queueConcordInboxLookup(f.endpoint, events...)
					queueConcordInboxLookup(f.endpoint)
					before, err := os.ReadFile(f.path)
					if err != nil {
						t.Fatal(err)
					}
					custody := &concordCountingCustody{concordBundleCustody: f.custody}
					f.membership.communities[0].custody = custody
					mints := 0
					f.membership.mintKey = func() (string, error) {
						mints++
						return strings.Repeat("ab", 32), nil
					}
					rotation := ConcordRotation{
						CommunityID: f.communityID,
						Refound:     operation != "channel",
						Recipients:  []string{newFakeSigner(t).pubkey},
					}
					if operation != "refound" {
						rotation.ChannelIDs = []string{f.privateChannelID}
					}
					receipt, err := f.membership.Rotate(t.Context(), rotation)
					if err == nil || !strings.Contains(err.Error(), "CORD-04 authority unresolved") {
						t.Errorf("Rotate() error = %v, want an explicit unresolved-authority refusal", err)
					} else {
						for _, reason := range []string{"Roles/Grants", "exact", "docs/soul-factory.md"} {
							if !strings.Contains(err.Error(), reason) {
								t.Errorf("refusal lacks operator context %q: %v", reason, err)
							}
						}
					}
					if receipt != nil || mints != 0 || custody.stores != 0 {
						t.Errorf("refused operation returned receipt=%v, minted=%d, custody writes=%d", receipt != nil, mints, custody.stores)
					}
					after, err := os.ReadFile(f.path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Error("refused rotation changed sealed custody bytes")
					}
					if f.endpoint.publishCalls != 0 || len(f.endpoint.published) != 0 ||
						len(f.endpoint.authCalls) != 0 || len(f.endpoint.subscribeCalls) != 0 {
						t.Error("refused rotation reached relay AUTH, query, or publication")
					}
				})
			}
		}
	}
}

type concordCountingCustody struct {
	concordBundleCustody
	stores   int
	storeErr error
}

func (c *concordCountingCustody) Store(ctx context.Context, record concordCustodyRecord) error {
	c.stores++
	if c.storeErr != nil {
		return c.storeErr
	}
	return c.concordBundleCustody.Store(ctx, record)
}

func TestConcordRotationOwnerMustBeBoundToConfiguredCommunity(t *testing.T) {
	f := newConcordRotationFixtureOwnedBy(t, 0, newFakeSigner(t).pubkey)
	record, err := f.custody.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record.Bundle, &fields); err != nil {
		t.Fatal(err)
	}
	fields["owner"], err = json.Marshal(f.staff.pubkey)
	if err != nil {
		t.Fatal(err)
	}
	record.Bundle, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.custody.Store(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	custody := &concordCountingCustody{concordBundleCustody: f.custody}
	f.membership.communities[0].custody = custody
	f.membership.mintKey = func() (string, error) {
		t.Fatal("an unbound owner reached key minting")
		return "", nil
	}
	receipt, err := f.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: f.communityID,
		ChannelIDs:  []string{f.privateChannelID},
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "community_id") || receipt != nil {
		t.Fatalf("unbound owner was not refused: receipt=%v err=%v", receipt, err)
	}
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if custody.stores != 0 || !bytes.Equal(before, after) || len(f.endpoint.published) != 0 ||
		len(f.endpoint.authCalls) != 0 || len(f.endpoint.subscribeCalls) != 0 {
		t.Fatal("unbound owner changed custody or reached relays")
	}
}
