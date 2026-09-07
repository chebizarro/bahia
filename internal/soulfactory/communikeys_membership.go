package soulfactory

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const (
	communikeysProfileListKind nostr.Kind = 30000
	// communikeysDefinitionKind is the Communikeys V2 addressable community
	// definition kind. Bahia only reads definitions to verify that configured
	// profile-list coordinates are actually referenced by the community.
	communikeysDefinitionKind nostr.Kind = 32222
	// communikeysMaxIdentifierBytes bounds a section-scoped `d` identifier
	// (Communikeys V2 §Section-Scoped Addressable Identifiers).
	communikeysMaxIdentifierBytes = 200
)

var (
	// communikeysHexPattern validates canonical lowercase 64-character hex.
	// Community IDs are opaque and MUST NOT be curve-checked (V2 §Community ID).
	communikeysHexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// communikeysPurposePattern validates section-purpose tokens
	// (V2 §Section-Scoped Addressable Identifiers).
	communikeysPurposePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// CommunikeysCommunity identifies one exact community branch and the section
// profile-list coordinates this controller is authorized to write.
type CommunikeysCommunity struct {
	// DefinitionAddress is the exact branch: "32222:<owner>:<communityId>".
	// A bare pubkey no longer identifies a community (Communikeys V2 §Identity Model).
	DefinitionAddress string
	// ListAuthor is the delegated signer that authors the section profile lists.
	// Under V2 list authors are ordinary real signers referenced by the definition;
	// they need NOT be the owner and the community ID is never a signer.
	ListAuthor string
	// Purposes are section-purpose tokens, e.g. "general", "chat", "repositories".
	// These are NOT display names; the definition's section name is unrelated.
	Purposes []string
	// Shard selects which shard of each purpose to write. 0/1 (default) is the
	// unsharded base coordinate; >=2 appends ".<shard>".
	Shard int
}

type communikeysMembershipAssigner interface {
	Assign(context.Context, string) ([]string, error)
}

// communikeysCommunityTarget is a validated community branch with derived
// section coordinates. Branch identity is (32222, owner, communityId); the
// community ID itself grants no signing meaning.
type communikeysCommunityTarget struct {
	definitionAddress string
	owner             nostr.PubKey
	communityID       string
	listAuthor        nostr.PubKey
	sections          []communikeysSectionTarget
}

type communikeysSectionTarget struct {
	purpose    string
	identifier string // section-scoped d: <communityId>-<purpose>[.<shard>]
	coordinate string // 30000:<listAuthor>:<identifier>
}

type communikeysMembership struct {
	signer      relayAuthSigner
	communities []communikeysCommunityTarget
	bus         *SoulFactoryRelayBus
}

// parseCommunikeysDefinitionAddress splits and validates an exact V2 branch
// address "32222:<owner>:<communityId>". Both components are validated as
// canonical lowercase hex only; curve validity is established later by
// verifying the referenced definition event, never by lifting the reference.
func parseCommunikeysDefinitionAddress(address string) (owner, communityID string, err error) {
	parts := strings.Split(address, ":")
	if len(parts) != 3 || parts[0] != strconv.Itoa(int(communikeysDefinitionKind)) {
		return "", "", fmt.Errorf("definition address %q must have the form %d:<owner-pubkey>:<community-id>", address, communikeysDefinitionKind)
	}
	if !communikeysHexPattern.MatchString(parts[1]) {
		return "", "", fmt.Errorf("definition address owner %q must be 64 lowercase hex characters", parts[1])
	}
	if !communikeysHexPattern.MatchString(parts[2]) {
		return "", "", fmt.Errorf("definition address community id %q must be 64 lowercase hex characters", parts[2])
	}
	return parts[1], parts[2], nil
}

// sectionListD builds the section-scoped addressable identifier
// <communityId>-<purpose>[.<shard>] (Communikeys V2 §Section-Scoped
// Addressable Identifiers). Shard values 0 and 1 select the unsharded base
// coordinate; the first real shard suffix is ".2".
func sectionListD(communityID, purpose string, shard int) (string, error) {
	d := communityID + "-" + purpose
	if shard >= 2 {
		d = d + "." + strconv.Itoa(shard)
	}
	if len(d) > communikeysMaxIdentifierBytes {
		return "", fmt.Errorf("section identifier %q exceeds %d UTF-8 bytes", d, communikeysMaxIdentifierBytes)
	}
	return d, nil
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
	targetIndexes := make(map[string]int, len(communities))
	sectionSets := make(map[string]map[string]struct{}, len(communities))
	for i, community := range communities {
		address := strings.ToLower(strings.TrimSpace(community.DefinitionAddress))
		ownerHex, communityID, err := parseCommunikeysDefinitionAddress(address)
		if err != nil {
			return nil, fmt.Errorf("Communikeys community %d: %w", i, err)
		}
		// The owner reference is positional: canonical hex only, no curve lift.
		owner, err := nostr.PubKeyFromHexCheap(ownerHex)
		if err != nil {
			return nil, fmt.Errorf("Communikeys community %d has invalid owner pubkey: %w", i, err)
		}
		// The list author is a real delegated signer, so it MUST lift to a
		// usable signing key — unlike the opaque community ID.
		listAuthorHex := strings.ToLower(strings.TrimSpace(community.ListAuthor))
		listAuthor, err := nostr.PubKeyFromHex(listAuthorHex)
		if err != nil {
			return nil, fmt.Errorf("Communikeys community %d list author must be a real signing pubkey: %w", i, err)
		}
		shard := community.Shard
		if shard == 0 {
			shard = 1
		}
		if shard < 0 {
			return nil, fmt.Errorf("Communikeys community %d shard must be 0/1 (unsharded) or >= 2, got %d", i, community.Shard)
		}

		// Two same-ID branches with different owners are distinct communities;
		// dedupe on the exact branch plus delegated author and shard.
		key := address + "\x00" + listAuthorHex + "\x00" + strconv.Itoa(shard)
		index, exists := targetIndexes[key]
		if !exists {
			index = len(membership.communities)
			targetIndexes[key] = index
			sectionSets[key] = make(map[string]struct{}, len(community.Purposes))
			membership.communities = append(membership.communities, communikeysCommunityTarget{
				definitionAddress: address,
				owner:             owner,
				communityID:       communityID,
				listAuthor:        listAuthor,
			})
		}
		for _, rawPurpose := range community.Purposes {
			purpose := strings.TrimSpace(rawPurpose)
			if purpose == "" {
				continue
			}
			if !communikeysPurposePattern.MatchString(purpose) {
				return nil, fmt.Errorf("Communikeys community %d section purpose %q must be a lowercase token of letters, digits, and single hyphens", i, rawPurpose)
			}
			if _, duplicate := sectionSets[key][purpose]; duplicate {
				continue
			}
			identifier, err := sectionListD(communityID, purpose, shard)
			if err != nil {
				return nil, fmt.Errorf("Communikeys community %d: %w", i, err)
			}
			sectionSets[key][purpose] = struct{}{}
			membership.communities[index].sections = append(membership.communities[index].sections, communikeysSectionTarget{
				purpose:    purpose,
				identifier: identifier,
				coordinate: fmt.Sprintf("%d:%s:%s", communikeysProfileListKind, listAuthorHex, identifier),
			})
		}
	}
	for i, community := range membership.communities {
		if len(community.sections) == 0 {
			return nil, fmt.Errorf("Communikeys community %d (%s) requires at least one section purpose", i, community.definitionAddress)
		}
	}
	return membership, nil
}

// Assign grants the provisioned pubkey access to every configured section by
// replacing the delegated-author NIP-51 kind-30000 profile list at each
// section-scoped coordinate. Before granting, the current community definition
// is loaded and each coordinate must appear as a section profile-list `a`
// reference — placement is authoritative, so a grant written to an
// unreferenced coordinate would be invisible to every Communikeys reader.
// Historical reads complete at EOSE and every replacement requires a relay OK.
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
		definition, err := m.latestDefinition(ctx, community)
		if err != nil {
			return assigned, fmt.Errorf("load Communikeys definition %s: %w", community.definitionAddress, err)
		}
		referenced := communikeysReferencedListCoordinates(definition)
		for _, section := range community.sections {
			if _, ok := referenced[section.coordinate]; !ok {
				return assigned, fmt.Errorf("Communikeys definition %s does not reference profile list %s in any content section; grants to an unreferenced coordinate are invisible to readers", community.definitionAddress, section.coordinate)
			}
			latest, err := m.latestProfileList(ctx, community.listAuthor, section.identifier)
			if err != nil {
				return assigned, fmt.Errorf("load Communikeys profile list %s: %w", section.coordinate, err)
			}
			if tagHasValue(latest.Tags, "p", pubkey) {
				assigned = append(assigned, section.coordinate)
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
				return assigned, fmt.Errorf("sign Communikeys profile list %s: %w", section.coordinate, err)
			}
			if replacement.PubKey != community.listAuthor {
				return assigned, fmt.Errorf("sign Communikeys profile list %s: controller pubkey %s is not the configured delegated list author %s", section.coordinate, replacement.PubKey.Hex(), community.listAuthor.Hex())
			}
			if !validSignedEvent(&replacement) {
				return assigned, fmt.Errorf("sign Communikeys profile list %s: signer returned an invalid event", section.coordinate)
			}
			if err := publishCommunikeysReplacement(ctx, m.bus, replacement); err != nil {
				return assigned, fmt.Errorf("assign Communikeys membership %s: %w", section.coordinate, err)
			}
			assigned = append(assigned, section.coordinate)
		}
	}
	return assigned, nil
}

// latestDefinition loads the current valid definition for the exact branch
// address. Selection is greatest created_at, then lexicographically lowest
// event ID (Communikeys V2 §Replacement And Deletion).
func (m *communikeysMembership) latestDefinition(ctx context.Context, community communikeysCommunityTarget) (*nostr.Event, error) {
	events, err := m.bus.Query(ctx, []nostr.Filter{{
		Kinds:   []nostr.Kind{communikeysDefinitionKind},
		Authors: []nostr.PubKey{community.owner},
		Tags:    nostr.TagMap{"d": []string{community.communityID}},
		Limit:   1,
	}})
	if err != nil {
		return nil, err
	}
	var latest *nostr.Event
	for _, event := range events {
		if !validCommunikeysDefinition(event, community.owner, community.communityID) {
			continue
		}
		latest = pickCommunikeysAuthorityEvent(latest, event)
	}
	if latest == nil {
		return nil, fmt.Errorf("no valid owner-signed community definition was found; the branch definition must exist before provisioning can grant section membership")
	}
	return latest, nil
}

// latestProfileList loads the current valid delegated-author profile list at
// one exact section-scoped coordinate. A missing list is a hard error: bahia's
// job is to grant, and it must never create an unreferenced list on its own.
func (m *communikeysMembership) latestProfileList(ctx context.Context, listAuthor nostr.PubKey, identifier string) (*nostr.Event, error) {
	events, err := m.bus.Query(ctx, []nostr.Filter{{
		Kinds:   []nostr.Kind{communikeysProfileListKind},
		Authors: []nostr.PubKey{listAuthor},
		Tags:    nostr.TagMap{"d": []string{identifier}},
		Limit:   1,
	}})
	if err != nil {
		return nil, err
	}
	var latest *nostr.Event
	for _, event := range events {
		if !validCommunikeysProfileList(event, listAuthor, identifier) {
			continue
		}
		latest = pickCommunikeysAuthorityEvent(latest, event)
	}
	if latest == nil {
		return nil, fmt.Errorf("no valid list-author-signed profile list was found at this coordinate; the delegated list author must publish it and the definition must reference it before provisioning can grant into it")
	}
	return latest, nil
}

// pickCommunikeysAuthorityEvent applies the authority replacement rule:
// greatest created_at wins, then the lexicographically lowest event ID
// (Communikeys V2 §Authority Event Replacement).
func pickCommunikeysAuthorityEvent(current, candidate *nostr.Event) *nostr.Event {
	if current == nil ||
		candidate.CreatedAt > current.CreatedAt ||
		(candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() < current.ID.Hex()) {
		return candidate
	}
	return current
}

func validCommunikeysDefinition(event *nostr.Event, owner nostr.PubKey, communityID string) bool {
	if event == nil || event.Kind != communikeysDefinitionKind || event.PubKey != owner || !validSignedEvent(event) {
		return false
	}
	return hasExactSingleDTag(event.Tags, communityID)
}

func validCommunikeysProfileList(event *nostr.Event, listAuthor nostr.PubKey, identifier string) bool {
	if event == nil || event.Kind != communikeysProfileListKind || event.PubKey != listAuthor || !validSignedEvent(event) {
		return false
	}
	return hasExactSingleDTag(event.Tags, identifier)
}

func hasExactSingleDTag(tags nostr.Tags, want string) bool {
	dTags := 0
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "d" {
			continue
		}
		dTags++
		if tag[1] != want {
			return false
		}
	}
	return dTags == 1
}

// communikeysReferencedListCoordinates collects every profile-list `a`
// reference that appears inside a `content` section of the definition.
// Top-level `a` tags before the first section are not section references and
// grant nothing (placement is authoritative).
func communikeysReferencedListCoordinates(definition *nostr.Event) map[string]struct{} {
	referenced := make(map[string]struct{})
	if definition == nil {
		return referenced
	}
	inSection := false
	for _, tag := range definition.Tags {
		if len(tag) >= 1 && tag[0] == "content" {
			inSection = true
			continue
		}
		if inSection && len(tag) >= 2 && tag[0] == "a" {
			referenced[tag[1]] = struct{}{}
		}
	}
	return referenced
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
