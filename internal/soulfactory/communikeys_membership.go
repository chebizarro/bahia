package soulfactory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const communikeysProfileListKind nostr.Kind = 30000

// CommunikeysCommunity identifies the controller-owned section profile lists
// that grant newly provisioned souls community write access.
type CommunikeysCommunity struct {
	Pubkey   string
	Sections []string
}

type communikeysMembershipAssigner interface {
	Assign(context.Context, string) ([]string, error)
}

type communikeysMembership struct {
	signer      relayAuthSigner
	communities []CommunikeysCommunity
	bus         *SoulFactoryRelayBus
}

func newCommunikeysMembership(communities []CommunikeysCommunity, signer relayAuthSigner, bus *SoulFactoryRelayBus) (*communikeysMembership, error) {
	if len(communities) == 0 {
		return nil, nil
	}
	if signer == nil {
		return nil, fmt.Errorf("Communikeys assignment requires a signer")
	}
	if bus == nil {
		return nil, fmt.Errorf("Communikeys assignment requires a SoulFactory relay bus")
	}

	membership := &communikeysMembership{signer: signer, bus: bus}
	communityIndexes := make(map[string]int, len(communities))
	sectionSets := make(map[string]map[string]struct{}, len(communities))
	for i, community := range communities {
		community.Pubkey = strings.ToLower(strings.TrimSpace(community.Pubkey))
		if _, err := nostr.PubKeyFromHex(community.Pubkey); err != nil {
			return nil, fmt.Errorf("Communikeys community %d has invalid pubkey: %w", i, err)
		}

		index, exists := communityIndexes[community.Pubkey]
		if !exists {
			index = len(membership.communities)
			communityIndexes[community.Pubkey] = index
			sectionSets[community.Pubkey] = make(map[string]struct{}, len(community.Sections))
			membership.communities = append(membership.communities, CommunikeysCommunity{Pubkey: community.Pubkey})
		}
		for _, rawSection := range community.Sections {
			section := strings.TrimSpace(rawSection)
			if section == "" {
				continue
			}
			if _, duplicate := sectionSets[community.Pubkey][section]; duplicate {
				continue
			}
			sectionSets[community.Pubkey][section] = struct{}{}
			membership.communities[index].Sections = append(membership.communities[index].Sections, section)
		}
	}
	for i, community := range membership.communities {
		if len(community.Sections) == 0 {
			return nil, fmt.Errorf("Communikeys community %d requires at least one section", i)
		}
	}
	return membership, nil
}

// Assign grants the provisioned pubkey access to every configured section by
// replacing the controller-owned NIP-51 kind-30000 profile list. Historical
// reads complete at EOSE and every replacement requires a relay OK.
func (m *communikeysMembership) Assign(ctx context.Context, pubkey string) ([]string, error) {
	if m == nil || len(m.communities) == 0 {
		return nil, nil
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if _, err := nostr.PubKeyFromHex(pubkey); err != nil {
		return nil, fmt.Errorf("invalid provisioned agent pubkey: %w", err)
	}
	if err := m.bus.Authenticate(ctx); err != nil {
		return nil, fmt.Errorf("authenticate Communikeys relay bus: %w", err)
	}

	assigned := make([]string, 0)
	for _, community := range m.communities {
		adminPubkey, err := nostr.PubKeyFromHex(community.Pubkey)
		if err != nil {
			return assigned, fmt.Errorf("parse Communikeys community pubkey %s: %w", community.Pubkey, err)
		}
		for _, section := range community.Sections {
			latest, err := m.latestProfileList(ctx, adminPubkey, section)
			if err != nil {
				return assigned, fmt.Errorf("load Communikeys profile list 30000:%s:%s: %w", community.Pubkey, section, err)
			}
			coordinate := fmt.Sprintf("30000:%s:%s", community.Pubkey, section)
			if tagHasValue(latest.Tags, "p", pubkey) {
				assigned = append(assigned, coordinate)
				continue
			}

			createdAt := nostr.Now()
			if createdAt <= latest.CreatedAt {
				createdAt = latest.CreatedAt + 1
			}
			replacement := nostr.Event{
				Kind:      communikeysProfileListKind,
				CreatedAt: createdAt,
				Tags:      cloneCommunikeysTags(latest.Tags),
				Content:   latest.Content,
			}
			replacement.Tags = append(replacement.Tags, nostr.Tag{"p", pubkey})
			if err := m.signer.Sign(ctx, &replacement); err != nil {
				return assigned, fmt.Errorf("sign Communikeys profile list %s: %w", coordinate, err)
			}
			if replacement.PubKey.Hex() != community.Pubkey {
				return assigned, fmt.Errorf("sign Communikeys profile list %s: controller pubkey %s does not own configured community", coordinate, replacement.PubKey.Hex())
			}
			if !validSignedEvent(&replacement) {
				return assigned, fmt.Errorf("sign Communikeys profile list %s: signer returned an invalid event", coordinate)
			}
			if err := publishCommunikeysReplacement(ctx, m.bus, replacement); err != nil {
				return assigned, fmt.Errorf("assign Communikeys membership %s: %w", coordinate, err)
			}
			assigned = append(assigned, coordinate)
		}
	}
	return assigned, nil
}

func (m *communikeysMembership) latestProfileList(ctx context.Context, adminPubkey nostr.PubKey, section string) (*nostr.Event, error) {
	events, err := m.bus.Query(ctx, []nostr.Filter{{
		Kinds:   []nostr.Kind{communikeysProfileListKind},
		Authors: []nostr.PubKey{adminPubkey},
		Tags:    nostr.TagMap{"d": []string{section}},
		Limit:   1,
	}})
	if err != nil {
		return nil, err
	}
	var latest *nostr.Event
	for _, event := range events {
		if !validCommunikeysProfileList(event, adminPubkey.Hex(), section) {
			continue
		}
		if latest == nil || event.CreatedAt > latest.CreatedAt || (event.CreatedAt == latest.CreatedAt && event.ID.Hex() > latest.ID.Hex()) {
			latest = event
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no valid admin-owned profile list was found")
	}
	return latest, nil
}

func validCommunikeysProfileList(event *nostr.Event, adminPubkey, section string) bool {
	if event == nil || event.Kind != communikeysProfileListKind || event.PubKey.Hex() != adminPubkey || !validSignedEvent(event) {
		return false
	}
	dTags := 0
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "d" {
			continue
		}
		dTags++
		if tag[1] != section {
			return false
		}
	}
	return dTags == 1
}

func cloneCommunikeysTags(tags nostr.Tags) nostr.Tags {
	cloned := make(nostr.Tags, 0, len(tags)+1)
	for _, tag := range tags {
		cloned = append(cloned, append(nostr.Tag(nil), tag...))
	}
	return cloned
}

func publishCommunikeysReplacement(ctx context.Context, bus *SoulFactoryRelayBus, event nostr.Event) error {
	if _, err := bus.Publish(ctx, event); err != nil {
		if !strings.Contains(err.Error(), "auth-required:") {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if _, retryErr := bus.Publish(ctx, event); retryErr != nil {
			return fmt.Errorf("publish after auth: %w", retryErr)
		}
	}
	return nil
}
