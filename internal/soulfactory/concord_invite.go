package soulfactory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

// ConcordCommunity identifies one configured community and where its CORD-05
// CommunityInvite bundle is kept. Exactly one custody source is required:
// InviteBundle carries operator-supplied material (read-only), while
// SealedBundlePath names a file holding only a NIP-44 payload sealed to the
// Signet-held staff key, which is the rotation-capable form (CORD-06).
type ConcordCommunity struct {
	CommunityID      string
	InviteBundle     json.RawMessage
	SealedBundlePath string
}

type concordMembershipAssigner interface {
	Assign(context.Context, string) ([]string, error)
}

type concordInviteSigner interface {
	Sign(context.Context, *nostr.Event) error
	GetPublicKey(context.Context) (string, error)
	NIP44Encrypt(context.Context, nostr.PubKey, string) (string, error)
	NIP44Decrypt(context.Context, nostr.PubKey, string) (string, error)
}

type concordMembership struct {
	signer      concordInviteSigner
	communities []*concordCommunitySource
	bus         *SoulFactoryRelayBus
	now         func() time.Time
	mintKey     func() (string, error)
	// rotateMu serializes rotations. Each one is a read-modify-write over
	// custody, so two concurrent rotations could otherwise both mint from the
	// same epoch and leave one branch distributed but never persisted.
	rotateMu sync.Mutex
}

// concordCommunitySource binds a configured community to its custody. Material
// behind writable custody is resolved per operation so a CORD-06 rotation is
// picked up without a restart, and is never cached in memory afterwards.
type concordCommunitySource struct {
	communityID string
	custody     concordBundleCustody
	cached      *validatedConcordCommunity
}

func (s *concordCommunitySource) resolve(ctx context.Context, bus *SoulFactoryRelayBus) (validatedConcordCommunity, concordCustodyRecord, error) {
	record, err := s.custody.Load(ctx)
	if err != nil {
		return validatedConcordCommunity{}, concordCustodyRecord{}, fmt.Errorf("load Concord invite material from %s: %w", s.custody.Source(), err)
	}
	if s.cached != nil {
		return *s.cached, record, nil
	}
	validated, err := validateConcordCommunity(record.Bundle, s.communityID, bus)
	if err != nil {
		return validatedConcordCommunity{}, concordCustodyRecord{}, err
	}
	return validated, record, nil
}

// validateConcordCommunity validates a bundle against its configured community
// and binds its relays to the SoulFactory relay bus, failing closed on either.
func validateConcordCommunity(bundle json.RawMessage, communityID string, bus *SoulFactoryRelayBus) (validatedConcordCommunity, error) {
	validated, err := validateConcordInviteBundle(bundle, communityID)
	if err != nil {
		return validatedConcordCommunity{}, fmt.Errorf("Concord community %s invite bundle: %w", communityID, err)
	}
	validated.relayEndpoints, err = concordRelayEndpoints(bus, validated.relays)
	if err != nil {
		return validatedConcordCommunity{}, fmt.Errorf("Concord community %s relay configuration: %w", communityID, err)
	}
	return validated, nil
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
		return nil, fmt.Errorf("Concord onboarding requires a Signet signer with NIP-44 encryption and decryption")
	}
	if bus == nil {
		return nil, fmt.Errorf("Concord onboarding requires a SoulFactory relay bus")
	}

	membership := &concordMembership{
		signer:  inviteSigner,
		bus:     bus,
		now:     time.Now,
		mintKey: mintConcordKey,
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
		source, err := newConcordCommunitySource(communityID, community, inviteSigner, bus)
		if err != nil {
			return nil, err
		}
		seen[communityID] = struct{}{}
		membership.communities = append(membership.communities, source)
	}
	return membership, nil
}

func newConcordCommunitySource(communityID string, community ConcordCommunity, signer concordInviteSigner, bus *SoulFactoryRelayBus) (*concordCommunitySource, error) {
	sealedPath := strings.TrimSpace(community.SealedBundlePath)
	hasBundle := len(bytes.TrimSpace(community.InviteBundle)) > 0
	if hasBundle == (sealedPath != "") {
		return nil, fmt.Errorf("Concord community %s requires exactly one custody source", communityID)
	}
	if sealedPath != "" {
		custody, err := newSealedConcordCustody(sealedPath, signer)
		if err != nil {
			return nil, fmt.Errorf("Concord community %s custody: %w", communityID, err)
		}
		return &concordCommunitySource{communityID: communityID, custody: custody}, nil
	}
	// Operator-supplied material is validated at construction so a malformed
	// bundle fails the process at boot rather than mid-provision.
	custody := &staticConcordCustody{bundle: append(json.RawMessage(nil), community.InviteBundle...)}
	validated, err := validateConcordCommunity(custody.bundle, communityID, bus)
	if err != nil {
		return nil, err
	}
	return &concordCommunitySource{communityID: communityID, custody: custody, cached: &validated}, nil
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
	resolved := make([]validatedConcordCommunity, 0, len(m.communities))
	for _, source := range m.communities {
		community, _, resolveErr := source.resolve(ctx, m.bus)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolved = append(resolved, community)
	}
	for _, community := range resolved {
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

	// The inbox is a property of the recipient, not of a community, so it is
	// resolved once even when several communities are assigned.
	inbox, err := m.resolveConcordInbox(ctx, recipientPK)
	if err != nil {
		return nil, err
	}

	assigned := make([]string, 0, len(resolved))
	for _, community := range resolved {
		if err := m.deliver(ctx, community, staffPK, recipientPK, recipient, inbox); err != nil {
			return assigned, err
		}
		assigned = append(assigned, community.communityID)
	}
	return assigned, nil
}

// deliver mints and publishes one CORD-05 §6 Direct Invite. The rumor is
// encrypted and its seal signed by the Signet-held staff identity; only the
// outer kind-1059 giftwrap uses a local ephemeral key.
//
// The wrap goes to the community's own relays and, when the recipient has
// published one, to their giftwrap inbox. A recipient Bahia just provisioned
// has no inbox yet and reads the fleet relays, so community relays alone carry
// that case; a member invited or re-keyed later is reached where they read.
func (m *concordMembership) deliver(ctx context.Context, community validatedConcordCommunity, staffPK, recipientPK nostr.PubKey, recipient string, inbox concordInbox) error {
	if err := authenticateConcordRelays(ctx, m.bus, community.relayEndpoints); err != nil {
		return fmt.Errorf("authenticate Concord relays for %s: %w", community.communityID, err)
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
		return fmt.Errorf("build Concord direct invite for %s: %w", community.communityID, err)
	}
	if !validConcordGiftWrap(wrap, recipient) {
		return fmt.Errorf("build Concord direct invite for %s: invalid giftwrap", community.communityID)
	}
	if err := publishConcordInvite(ctx, m.bus, community.relayEndpoints, wrap); err != nil {
		return fmt.Errorf("publish Concord direct invite for %s: %w", community.communityID, err)
	}
	if inbox.empty() {
		return nil
	}
	endpoints, closeEndpoints := concordInboxEndpoints(m.bus, inbox.relays)
	defer closeEndpoints()
	if err := publishConcordInviteToInbox(ctx, m.bus, endpoints, wrap); err != nil {
		return fmt.Errorf("publish Concord direct invite for %s to the recipient's %s: %w", community.communityID, inbox.source, err)
	}
	return nil
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
		if !validConcordRelayURL(relay) {
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
