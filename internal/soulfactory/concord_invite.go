package soulfactory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip59"
)

const (
	concordDirectInviteKind  nostr.Kind = 3313
	concordInviteMaxBytes               = 65535
	concordInviteMaxChannels            = 256
	concordInviteMaxRelays              = 5
)

// ConcordCommunity identifies one configured community and its CORD-05
// CommunityInvite bundle. InviteBundle contains membership key material and
// must be loaded from a secret source rather than inline operator config.
type ConcordCommunity struct {
	CommunityID  string
	InviteBundle json.RawMessage
}

type concordMembershipAssigner interface {
	Assign(context.Context, string) ([]string, error)
}

type concordInviteSigner interface {
	Sign(context.Context, *nostr.Event) error
	GetPublicKey(context.Context) (string, error)
	NIP44Encrypt(context.Context, nostr.PubKey, string) (string, error)
}

type concordMembership struct {
	signer      concordInviteSigner
	communities []validatedConcordCommunity
	bus         *SoulFactoryRelayBus
	now         func() time.Time
}

type validatedConcordCommunity struct {
	communityID    string
	bundle         json.RawMessage
	expiresAt      *int64
	relays         []string
	relayEndpoints []relayBusEndpoint
}

type concordInviteBundle struct {
	CommunityID   string                 `json:"community_id"`
	Owner         string                 `json:"owner"`
	OwnerSalt     string                 `json:"owner_salt"`
	CommunityRoot string                 `json:"community_root"`
	RootEpoch     uint64                 `json:"root_epoch"`
	ControlPK     string                 `json:"control_pk"`
	Channels      []concordInviteChannel `json:"channels"`
	Relays        []string               `json:"relays"`
	Name          string                 `json:"name"`
	ExpiresAt     *int64                 `json:"expires_at,omitempty"`
	CreatorNpub   string                 `json:"creator_npub,omitempty"`
	Label         string                 `json:"label,omitempty"`
}

type concordInviteChannel struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Epoch uint64 `json:"epoch"`
	Name  string `json:"name"`
}

func newConcordMembership(communities []ConcordCommunity, signer Signer, bus *SoulFactoryRelayBus) (*concordMembership, error) {
	if len(communities) == 0 {
		return nil, nil
	}
	inviteSigner, ok := signer.(concordInviteSigner)
	if !ok {
		return nil, fmt.Errorf("Concord onboarding requires a Signet signer with NIP-44 encryption")
	}
	if bus == nil {
		return nil, fmt.Errorf("Concord onboarding requires a SoulFactory relay bus")
	}

	membership := &concordMembership{
		signer: inviteSigner,
		bus:    bus,
		now:    time.Now,
	}
	seen := make(map[string]struct{}, len(communities))
	for i, community := range communities {
		communityID := strings.ToLower(strings.TrimSpace(community.CommunityID))
		if !validConcordHex32(communityID) {
			return nil, fmt.Errorf("Concord community %d has invalid community_id", i)
		}
		if _, duplicate := seen[communityID]; duplicate {
			continue
		}
		validated, err := validateConcordInviteBundle(community.InviteBundle, communityID)
		if err != nil {
			return nil, fmt.Errorf("Concord community %s invite bundle: %w", communityID, err)
		}
		validated.relayEndpoints, err = concordRelayEndpoints(bus, validated.relays)
		if err != nil {
			return nil, fmt.Errorf("Concord community %s relay configuration: %w", communityID, err)
		}
		seen[communityID] = struct{}{}
		membership.communities = append(membership.communities, validated)
	}
	return membership, nil
}

// Assign sends a CORD-05 Direct Invite for every configured Concord community.
// The rumor is encrypted and its kind-13 seal is signed by the Signet-held
// staff identity; only the outer kind-1059 giftwrap uses a local ephemeral key.
func (m *concordMembership) Assign(ctx context.Context, recipient string) ([]string, error) {
	if m == nil || len(m.communities) == 0 {
		return nil, nil
	}
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	recipientPK, err := nostr.PubKeyFromHex(recipient)
	if err != nil {
		return nil, fmt.Errorf("invalid provisioned agent pubkey: %w", err)
	}
	for _, community := range m.communities {
		if community.expiresAt != nil && *community.expiresAt <= m.now().UnixMilli() {
			return nil, fmt.Errorf("Concord community %s invite bundle is expired", community.communityID)
		}
	}
	staffHex, err := m.signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve Concord staff pubkey from Signet: %w", err)
	}
	staffHex = strings.ToLower(strings.TrimSpace(staffHex))
	staffPK, err := nostr.PubKeyFromHex(staffHex)
	if err != nil {
		return nil, fmt.Errorf("invalid Concord staff pubkey from Signet: %w", err)
	}

	assigned := make([]string, 0, len(m.communities))
	for _, community := range m.communities {
		if err := authenticateConcordRelays(ctx, m.bus, community.relayEndpoints); err != nil {
			return assigned, fmt.Errorf("authenticate Concord relays for %s: %w", community.communityID, err)
		}
		rumor := nostr.Event{
			Kind:      concordDirectInviteKind,
			PubKey:    staffPK,
			CreatedAt: nostr.Now(),
			Tags:      nostr.Tags{},
			Content:   string(community.bundle),
		}
		rumor.ID = rumor.GetID()

		wrap, err := nip59.GiftWrap(
			rumor,
			recipientPK,
			func(plaintext string) (string, error) {
				if len([]byte(plaintext)) > concordInviteMaxBytes {
					return "", fmt.Errorf("Concord rumor exceeds NIP-44 plaintext limit")
				}
				ciphertext, encryptErr := m.signer.NIP44Encrypt(ctx, recipientPK, plaintext)
				if encryptErr != nil {
					return "", fmt.Errorf("Signet NIP-44 encrypt Concord rumor: %w", encryptErr)
				}
				return ciphertext, nil
			},
			func(seal *nostr.Event) error {
				if seal == nil || seal.Kind != nostr.KindSeal {
					return fmt.Errorf("Concord invite seal has invalid kind")
				}
				if signErr := m.signer.Sign(ctx, seal); signErr != nil {
					return fmt.Errorf("Signet sign Concord invite seal: %w", signErr)
				}
				if seal.PubKey != staffPK || !validSignedEvent(seal) {
					return fmt.Errorf("Signet returned an invalid Concord invite seal")
				}
				return nil
			},
			func(gift *nostr.Event) {
				gift.Tags = append(gift.Tags, nostr.Tag{"k", "3313"})
			},
		)
		if err != nil {
			return assigned, fmt.Errorf("build Concord direct invite for %s: %w", community.communityID, err)
		}
		if !validConcordGiftWrap(wrap, recipient) {
			return assigned, fmt.Errorf("build Concord direct invite for %s: invalid giftwrap", community.communityID)
		}
		if err := publishConcordInvite(ctx, m.bus, community.relayEndpoints, wrap); err != nil {
			return assigned, fmt.Errorf("publish Concord direct invite for %s: %w", community.communityID, err)
		}
		assigned = append(assigned, community.communityID)
	}
	return assigned, nil
}

func validateConcordInviteBundle(raw json.RawMessage, configuredCommunityID string) (validatedConcordCommunity, error) {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 {
		return validatedConcordCommunity{}, fmt.Errorf("bundle is empty")
	}
	if len(raw) > concordInviteMaxBytes {
		return validatedConcordCommunity{}, fmt.Errorf("bundle exceeds NIP-44 plaintext limit")
	}
	if !json.Valid(raw) {
		return validatedConcordCommunity{}, fmt.Errorf("bundle is not valid JSON")
	}

	var bundle concordInviteBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return validatedConcordCommunity{}, fmt.Errorf("decode bundle: %w", err)
	}
	if !validConcordHex32(bundle.CommunityID) || bundle.CommunityID != configuredCommunityID {
		return validatedConcordCommunity{}, fmt.Errorf("community_id does not match configured community")
	}
	if _, err := nostr.PubKeyFromHex(bundle.Owner); err != nil || bundle.Owner != strings.ToLower(bundle.Owner) {
		return validatedConcordCommunity{}, fmt.Errorf("owner must be a lowercase 32-byte x-only pubkey")
	}
	if !validConcordHex32(bundle.OwnerSalt) {
		return validatedConcordCommunity{}, fmt.Errorf("owner_salt must be 32-byte lowercase hex")
	}
	if computeConcordCommunityID(bundle.Owner, bundle.OwnerSalt) != bundle.CommunityID {
		return validatedConcordCommunity{}, fmt.Errorf("community_id self-certification failed")
	}
	if !validConcordHex32(bundle.CommunityRoot) {
		return validatedConcordCommunity{}, fmt.Errorf("community_root must be 32-byte lowercase hex")
	}
	if _, err := nostr.PubKeyFromHex(bundle.ControlPK); err != nil || bundle.ControlPK != strings.ToLower(bundle.ControlPK) {
		return validatedConcordCommunity{}, fmt.Errorf("control_pk must be a lowercase 32-byte x-only pubkey")
	}
	if len(bundle.Channels) > concordInviteMaxChannels {
		return validatedConcordCommunity{}, fmt.Errorf("channels exceeds %d entries", concordInviteMaxChannels)
	}
	seenChannels := make(map[string]struct{}, len(bundle.Channels))
	for i, channel := range bundle.Channels {
		if !validConcordHex32(channel.ID) {
			return validatedConcordCommunity{}, fmt.Errorf("channels[%d].id must be 32-byte lowercase hex", i)
		}
		if !validConcordHex32(channel.Key) {
			return validatedConcordCommunity{}, fmt.Errorf("channels[%d].key must be 32-byte lowercase hex", i)
		}
		if _, duplicate := seenChannels[channel.ID]; duplicate {
			return validatedConcordCommunity{}, fmt.Errorf("channels[%d].id is duplicated", i)
		}
		seenChannels[channel.ID] = struct{}{}
		if strings.TrimSpace(channel.Name) == "" || len([]byte(channel.Name)) > 64 {
			return validatedConcordCommunity{}, fmt.Errorf("channels[%d].name must contain 1 to 64 UTF-8 bytes", i)
		}
	}
	if len(bundle.Relays) == 0 || len(bundle.Relays) > concordInviteMaxRelays {
		return validatedConcordCommunity{}, fmt.Errorf("relays must contain 1 to %d entries", concordInviteMaxRelays)
	}
	seenRelays := make(map[string]struct{}, len(bundle.Relays))
	for i, relay := range bundle.Relays {
		if relay != strings.TrimSpace(relay) {
			return validatedConcordCommunity{}, fmt.Errorf("relays[%d] contains surrounding whitespace", i)
		}
		parsed, err := url.Parse(relay)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
			return validatedConcordCommunity{}, fmt.Errorf("relays[%d] must be a ws or wss URL", i)
		}
		if _, duplicate := seenRelays[relay]; duplicate {
			return validatedConcordCommunity{}, fmt.Errorf("relays[%d] is duplicated", i)
		}
		seenRelays[relay] = struct{}{}
	}
	if strings.TrimSpace(bundle.Name) == "" || len([]byte(bundle.Name)) > 64 {
		return validatedConcordCommunity{}, fmt.Errorf("name must contain 1 to 64 UTF-8 bytes")
	}
	if bundle.CreatorNpub != "" {
		if _, err := nostr.PubKeyFromHex(bundle.CreatorNpub); err != nil || bundle.CreatorNpub != strings.ToLower(bundle.CreatorNpub) {
			return validatedConcordCommunity{}, fmt.Errorf("creator_npub must be a lowercase 32-byte x-only pubkey")
		}
	}
	if len([]byte(bundle.Label)) > 64 {
		return validatedConcordCommunity{}, fmt.Errorf("label must not exceed 64 UTF-8 bytes")
	}
	return validatedConcordCommunity{
		communityID: bundle.CommunityID,
		bundle:      append(json.RawMessage(nil), raw...),
		expiresAt:   bundle.ExpiresAt,
		relays:      append([]string(nil), bundle.Relays...),
	}, nil
}

func computeConcordCommunityID(ownerHex, ownerSaltHex string) string {
	owner, ownerErr := hex.DecodeString(ownerHex)
	salt, saltErr := hex.DecodeString(ownerSaltHex)
	if ownerErr != nil || saltErr != nil || len(owner) != 32 || len(salt) != 32 {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("concord/community"))
	_, _ = hash.Write(owner)
	_, _ = hash.Write(salt)
	return hex.EncodeToString(hash.Sum(nil))
}

func validConcordHex32(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validConcordGiftWrap(event nostr.Event, recipient string) bool {
	if event.Kind != nostr.KindGiftWrap || !validSignedEvent(&event) || len(event.Tags) != 2 {
		return false
	}
	return len(event.Tags[0]) == 2 && event.Tags[0][0] == "p" && event.Tags[0][1] == recipient &&
		len(event.Tags[1]) == 2 && event.Tags[1][0] == "k" && event.Tags[1][1] == "3313"
}

func concordRelayEndpoints(bus *SoulFactoryRelayBus, relays []string) ([]relayBusEndpoint, error) {
	configured := make(map[string]relayBusEndpoint, len(bus.endpoints))
	for _, endpoint := range bus.endpoints {
		configured[strings.TrimRight(strings.TrimSpace(endpoint.URL()), "/")] = endpoint
	}
	endpoints := make([]relayBusEndpoint, 0, len(relays))
	for _, relay := range relays {
		normalized := strings.TrimRight(relay, "/")
		endpoint, ok := configured[normalized]
		if !ok {
			return nil, fmt.Errorf("bundle relay %s is not configured on the SoulFactory relay bus", relay)
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}

func authenticateConcordRelays(ctx context.Context, bus *SoulFactoryRelayBus, endpoints []relayBusEndpoint) error {
	if bus.signer == nil {
		return fmt.Errorf("soul factory relay auth signer is not configured")
	}
	for _, endpoint := range endpoints {
		if err := endpoint.Auth(ctx, bus.signer); err != nil {
			return fmt.Errorf("authenticate to %s: %w", endpoint.URL(), err)
		}
	}
	return nil
}

func publishConcordInvite(ctx context.Context, bus *SoulFactoryRelayBus, endpoints []relayBusEndpoint, event nostr.Event) error {
	for _, endpoint := range endpoints {
		result := endpoint.Publish(ctx, event)
		if result.Accepted {
			continue
		}
		if isRelayAuthRequired(result.Reason) || (result.Error != nil && strings.Contains(result.Error.Error(), "auth-required:")) {
			if bus.signer == nil {
				return fmt.Errorf("%s requested auth but no relay auth signer is configured", endpoint.URL())
			}
			if err := endpoint.Auth(ctx, bus.signer); err != nil {
				return fmt.Errorf("authenticate to %s after relay challenge: %w", endpoint.URL(), err)
			}
			result = endpoint.Publish(ctx, event)
			if result.Accepted {
				continue
			}
		}
		if result.Error != nil {
			return fmt.Errorf("%s: %w", endpoint.URL(), result.Error)
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "OK false"
		}
		return fmt.Errorf("%s: %s", endpoint.URL(), reason)
	}
	return nil
}
