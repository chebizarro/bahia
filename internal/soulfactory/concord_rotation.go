package soulfactory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

// ConcordRotation describes an explicit CORD-06 rotation: a Rekey of one or
// more Private Channels, or a Refounding that rolls the community_root itself.
// Fresh material reaches only Recipients, so the caller supplies the surviving
// membership; anyone omitted is severed from the rotated scope.
type ConcordRotation struct {
	CommunityID string
	// Refound performs a whole-community base rotation (CORD-06 §3): a fresh
	// community_root and a fresh control_root, with Public Channels rotating
	// alongside the base.
	Refound bool
	// ChannelIDs names Private Channels to rekey independently (CORD-06 §1).
	ChannelIDs []string
	// Recipients are the surviving members, as 32-byte lowercase hex pubkeys.
	Recipients []string
	// Staff names the survivors holding a Control-writing permission (CORD-04
	// §3). Only they receive the next epoch's control_root in their base Rekey
	// Blob (CORD-06 §1), so every entry must also appear in Recipients.
	Staff []string
	// DirectInvites narrows the CORD-05 §6 handoff to the survivors Soul
	// Factory can reach individually; every Recipient still receives a Rekey
	// Blob. Empty means every Recipient, which is the fleet-provisioned case.
	DirectInvites []string
	// Reason is operator audit context. It must never carry key material.
	Reason string
}

// ConcordChannelRotation records one rotated Channel. It carries epochs and the
// continuity commitment only, never a key.
type ConcordChannelRotation struct {
	ChannelID  string `json:"channel_id"`
	PrevEpoch  uint64 `json:"prev_epoch"`
	NewEpoch   uint64 `json:"new_epoch"`
	PrevCommit string `json:"prev_commit"`
	Public     bool   `json:"public"`
}

// ConcordRotationReceipt is the auditable result of a rotation. Every field is
// safe to log or persist: it names scopes, epochs, and recipients, and never
// the material itself.
type ConcordRotationReceipt struct {
	CommunityID    string                    `json:"community_id"`
	Refounded      bool                      `json:"refounded"`
	PrevRootEpoch  uint64                    `json:"prev_root_epoch"`
	RootEpoch      uint64                    `json:"root_epoch"`
	RootPrevCommit string                    `json:"root_prev_commit,omitempty"`
	Channels       []ConcordChannelRotation  `json:"channels,omitempty"`
	Recipients     []string                  `json:"recipients"`
	DirectInvites  []string                  `json:"direct_invites,omitempty"`
	Staff          []string                  `json:"staff,omitempty"`
	Rekeys         []ConcordRekeyPublication `json:"rekeys,omitempty"`
	// Compaction and GuestbookSnapshot are set by a Refounding only (CORD-06
	// §3). The snapshot is best-effort, so it may be present and carry an Error
	// on a Refounding that otherwise succeeded.
	Compaction        *ConcordCompaction        `json:"compaction,omitempty"`
	GuestbookSnapshot *ConcordGuestbookSnapshot `json:"guestbook_snapshot,omitempty"`
	Reason            string                    `json:"reason,omitempty"`
	RotatedAt         time.Time                 `json:"rotated_at"`
}

// concordFoldIsCompactable enforces CORD-06 §3's abort rule: if the Refounder
// cannot reliably fold all Control events, the Refounding must be aborted.
//
// A suspended entity is exactly that failure. Its chain forked, so no head is
// trustworthy — and a compaction that silently omitted it would prune the
// ancestors that still carried its state, turning a recoverable fork into a
// permanent loss.
func concordFoldIsCompactable(fold *concordControlFold) error {
	if fold == nil {
		return fmt.Errorf("CORD-06 §3 requires a folded Control Plane before a Refounding, and none was resolved")
	}
	if len(fold.suspended) > 0 {
		return fmt.Errorf("%d Control Plane entities have forked chains; CORD-06 §3 aborts a Refounding it cannot reliably fold rather than compacting a partial one", len(fold.suspended))
	}
	return nil
}

// concordCommunityRotator is the CORD-06 surface the provisioner exposes.
type concordCommunityRotator interface {
	Rotate(context.Context, ConcordRotation) (*ConcordRotationReceipt, error)
}

type concordRotationPlan struct {
	bundle      json.RawMessage
	controlRoot string
	receipt     ConcordRotationReceipt
	// priorRoot is the community_root every rekey address derives from, and
	// scopes carry the fresh material each blob delivers. Neither is logged
	// nor persisted; both die with the plan.
	priorRoot []byte
	scopes    []concordRekeyScope
	// Refounding only: the freshly minted material the compaction and the
	// Guestbook snapshot publish under (CORD-06 §3). Key material, so like
	// priorRoot these die with the plan and never reach a receipt or a log.
	communityID32   [32]byte
	nextRoot        [32]byte
	nextControlRoot [32]byte
	// nextControlPK is the address the compaction must land on — the same one
	// the rotated bundle hands members — and is public.
	nextControlPK string
}

// Rotate performs a CORD-06 Rekey or Refounding: it mints fresh material, bumps
// the affected epochs, seals the result back into custody, and redistributes it
// to the surviving members as CORD-05 §6 Direct Invites.
//
// The rotation is resumable rather than atomic (CORD-06 §3). Custody is written
// before the first publish, so a partial redistribution returns both the
// receipt and the error, and re-running the delivery is idempotent.
func (m *concordMembership) Rotate(ctx context.Context, rotation ConcordRotation) (*ConcordRotationReceipt, error) {
	if m == nil || len(m.communities) == 0 {
		return nil, fmt.Errorf("Concord onboarding is not configured")
	}
	m.rotateMu.Lock()
	defer m.rotateMu.Unlock()

	communityID := strings.ToLower(strings.TrimSpace(rotation.CommunityID))
	source := m.findCommunity(communityID)
	if source == nil {
		return nil, fmt.Errorf("Concord community %s is not configured", communityID)
	}
	if !rotation.Refound && len(rotation.ChannelIDs) == 0 {
		return nil, fmt.Errorf("Concord rotation for %s must name a Refounding or at least one channel", communityID)
	}
	if !source.custody.Writable() {
		return nil, fmt.Errorf("Concord community %s cannot rotate: %s is read-only; CORD-06 requires Signet-sealed custody (invite_bundle_sealed_file)", communityID, source.custody.Source())
	}
	recipients, err := normalizeConcordRecipients(rotation.Recipients)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}
	staff, err := normalizeConcordStaff(rotation.Staff, recipients)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}
	blobRecipients, err := concordRekeyRecipients(recipients, staff)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}
	inviteTargets, err := normalizeConcordDirectInvites(rotation.DirectInvites, recipients)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}

	current, record, err := source.resolve(ctx, m.bus)
	if err != nil {
		return nil, err
	}
	var bundle concordInviteBundle
	if err := json.Unmarshal(current.bundle, &bundle); err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: decode current invite bundle: %w", communityID, err)
	}
	staffPK, err := m.staffPubKey(ctx)
	if err != nil {
		return nil, err
	}

	// CORD-06 §3 Authority: the Grant this rotation acts under is resolved from
	// the *current* epoch's folded Control Plane before anything is minted, so
	// an unauthorized Rotator never reaches custody, and — per CORD-06 §3's
	// failure rule — the state being rotated is acquired in full before the
	// first publish.
	// A Refounding additionally *needs* the fold: CORD-06 §3 has it compact the
	// current Control Plane and republish it at the new epoch, and aborts the
	// Refounding outright if it cannot reliably fold all Control events.
	citation, fold, err := m.resolveConcordRotationAuthority(ctx, current, bundle, staffPK, rotation.Refound)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}
	if rotation.Refound {
		if err := concordFoldIsCompactable(fold); err != nil {
			return nil, fmt.Errorf("Concord Refounding for %s: %w", communityID, err)
		}
	}

	plan, err := m.planConcordRotation(current.bundle, record, rotation, communityID)
	if err != nil {
		return nil, fmt.Errorf("Concord rotation for %s: %w", communityID, err)
	}
	// Fail closed on self-minted material: the rotated bundle must validate and
	// bind to the relay bus exactly like a configured one.
	next, err := validateConcordCommunity(plan.bundle, communityID, m.bus)
	if err != nil {
		return nil, fmt.Errorf("rotated %w", err)
	}
	if next.expiresAt != nil && *next.expiresAt <= m.now().UnixMilli() {
		return nil, fmt.Errorf("Concord rotation for %s produced an expired invite bundle", communityID)
	}

	if err := source.custody.Store(ctx, concordCustodyRecord{Bundle: plan.bundle, ControlRoot: plan.controlRoot}); err != nil {
		return nil, fmt.Errorf("persist rotated Concord material for %s: %w", communityID, err)
	}

	receipt := plan.receipt
	// Every survivor is a recipient of the rotation itself; the Direct Invite
	// list only records who was additionally handed the material out of band.
	receipt.Recipients = recipients
	receipt.Staff = staff

	// CORD-06 §1: the Rekey Blobs are what lets a member outside the Direct
	// Invite lane converge on the new epoch from the rekey address alone. They
	// go out first, since the Direct Invites below are the narrower path.
	rekeys, err := m.publishConcordRekeys(ctx, next, staffPK, plan.priorRoot, plan.scopes, blobRecipients, citation)
	receipt.Rekeys = rekeys
	if err != nil {
		return &receipt, fmt.Errorf("publish rotated Concord rekey blobs for %s: %w", communityID, err)
	}

	if rotation.Refound {
		// CORD-06 §3: the compaction is republished only after confirmed
		// publication of the root roll above, so a member who reaches the new
		// Control address can already open it.
		compaction, compactErr := m.republishConcordCompaction(
			ctx, next, fold, plan.communityID32, plan.nextRoot[:], plan.nextControlRoot[:], receipt.RootEpoch, plan.nextControlPK)
		receipt.Compaction = &compaction
		if compactErr != nil {
			return &receipt, fmt.Errorf("republish the compacted Concord Control Plane for %s: %w", communityID, compactErr)
		}

		// The Guestbook snapshot is CORD-06 §3's best-effort final step: a
		// Refounding succeeds with or without it, and a member entering the new
		// epoch to find their own state absent simply publishes a fresh Join.
		// So its failure lands on the receipt and never on the return.
		snapshot := m.seedConcordGuestbookSnapshot(
			ctx, next, staffPK, plan.communityID32, plan.nextRoot[:], receipt.RootEpoch, recipients)
		receipt.GuestbookSnapshot = &snapshot
	}

	for _, recipient := range inviteTargets {
		recipientPK, pkErr := nostr.PubKeyFromHex(recipient)
		if pkErr != nil {
			return &receipt, fmt.Errorf("redistribute rotated Concord material for %s: invalid recipient: %w", communityID, pkErr)
		}
		// Survivors are existing members, not freshly provisioned agents, so
		// each is reached at their own giftwrap inbox when they publish one.
		inbox, inboxErr := m.resolveConcordInbox(ctx, recipientPK)
		if inboxErr != nil {
			return &receipt, fmt.Errorf("redistribute rotated Concord material for %s: %w", communityID, inboxErr)
		}
		if deliverErr := m.deliver(ctx, next, staffPK, recipientPK, recipient, inbox); deliverErr != nil {
			return &receipt, fmt.Errorf("redistribute rotated Concord material for %s: %w", communityID, deliverErr)
		}
		receipt.DirectInvites = append(receipt.DirectInvites, recipient)
	}
	return &receipt, nil
}

func (m *concordMembership) findCommunity(communityID string) *concordCommunitySource {
	for _, source := range m.communities {
		if source.communityID == communityID {
			return source
		}
	}
	return nil
}

func (m *concordMembership) staffPubKey(ctx context.Context) (nostr.PubKey, error) {
	staffHex, err := m.signer.GetPublicKey(ctx)
	if err != nil {
		return nostr.PubKey{}, fmt.Errorf("resolve Concord staff pubkey from Signet: %w", err)
	}
	staffPK, err := nostr.PubKeyFromHex(strings.ToLower(strings.TrimSpace(staffHex)))
	if err != nil {
		return nostr.PubKey{}, fmt.Errorf("invalid Concord staff pubkey from Signet: %w", err)
	}
	return staffPK, nil
}

// planConcordRotation mints the rotated bundle. Unknown bundle and channel
// fields are round-tripped verbatim (CORD-02 §6), and the plan is built in full
// before anything is written or published (CORD-06 §3).
func (m *concordMembership) planConcordRotation(raw json.RawMessage, record concordCustodyRecord, rotation ConcordRotation, communityID string) (concordRotationPlan, error) {
	var plan concordRotationPlan
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return plan, fmt.Errorf("decode current invite bundle: %w", err)
	}
	var bundle concordInviteBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return plan, fmt.Errorf("decode current invite bundle: %w", err)
	}
	var channels []map[string]json.RawMessage
	if rawChannels, ok := fields["channels"]; ok && len(rawChannels) > 0 {
		if err := json.Unmarshal(rawChannels, &channels); err != nil {
			return plan, fmt.Errorf("decode current invite bundle channels: %w", err)
		}
	}
	if len(channels) != len(bundle.Channels) {
		return plan, fmt.Errorf("current invite bundle channels are inconsistent")
	}

	requested := make(map[string]struct{}, len(rotation.ChannelIDs))
	for _, channelID := range rotation.ChannelIDs {
		channelID = strings.ToLower(strings.TrimSpace(channelID))
		if !validConcordHex32(channelID) {
			return plan, fmt.Errorf("channel id must be 32-byte lowercase hex")
		}
		requested[channelID] = struct{}{}
	}

	priorRoot := bundle.CommunityRoot
	// Every rekey address derives from the prior community_root (CORD-02 A.6),
	// which keeps a rotation openable on either branch of a race (CORD-06 §3).
	priorRoot32, err := concordID32(priorRoot)
	if err != nil {
		return plan, fmt.Errorf("community_root: %w", err)
	}
	nextRoot := priorRoot
	nextRootEpoch := bundle.RootEpoch
	controlRoot := record.ControlRoot
	receipt := ConcordRotationReceipt{
		CommunityID:   communityID,
		Refounded:     rotation.Refound,
		PrevRootEpoch: bundle.RootEpoch,
		RootEpoch:     bundle.RootEpoch,
		Reason:        strings.TrimSpace(rotation.Reason),
		RotatedAt:     m.now().UTC(),
		Recipients:    []string{},
	}

	if rotation.Refound {
		mintedRoot, err := m.mintKey()
		if err != nil {
			return plan, fmt.Errorf("mint community_root: %w", err)
		}
		// CORD-06 §3: a compliant base rotation mints a fresh control_root
		// beside the new root, so the Community upgrades to the split plane.
		mintedControlRoot, err := m.mintKey()
		if err != nil {
			return plan, fmt.Errorf("mint control_root: %w", err)
		}
		communityID32, err := concordID32(bundle.CommunityID)
		if err != nil {
			return plan, fmt.Errorf("community_id: %w", err)
		}
		controlRootBytes, err := hex.DecodeString(mintedControlRoot)
		if err != nil {
			return plan, fmt.Errorf("decode minted control_root: %w", err)
		}
		nextRootEpoch = bundle.RootEpoch + 1
		controlSigner, err := deriveConcordGroupKey(concordLabelControlSigner, controlRootBytes, communityID32, &nextRootEpoch)
		if err != nil {
			return plan, err
		}
		prevCommit, err := concordEpochCommitment(bundle.RootEpoch, priorRoot)
		if err != nil {
			return plan, fmt.Errorf("community_root continuity: %w", err)
		}
		nextRoot = mintedRoot
		controlRoot = mintedControlRoot
		if err := setConcordBundleFields(fields, map[string]any{
			"community_root": nextRoot,
			"root_epoch":     nextRootEpoch,
			"control_pk":     controlSigner.PubKey.Hex(),
		}); err != nil {
			return plan, err
		}
		receipt.RootEpoch = nextRootEpoch
		receipt.RootPrevCommit = prevCommit

		nextRoot32, err := concordID32(nextRoot)
		if err != nil {
			return plan, fmt.Errorf("minted community_root: %w", err)
		}
		controlRoot32, err := concordID32(controlRoot)
		if err != nil {
			return plan, fmt.Errorf("minted control_root: %w", err)
		}
		plan.communityID32 = communityID32
		plan.nextRoot = nextRoot32
		plan.nextControlRoot = controlRoot32
		plan.nextControlPK = controlSigner.PubKey.Hex()
		plan.scopes = append(plan.scopes, concordRekeyScope{
			base:           true,
			scopeID:        concordBaseScopeID,
			addressLabel:   concordLabelBaseRekeyPseudonym,
			addressID:      communityID32,
			prevEpoch:      bundle.RootEpoch,
			newEpoch:       nextRootEpoch,
			prevCommit:     prevCommit,
			newKey:         nextRoot32,
			newControlPK:   controlSigner.PubKey,
			newControlRoot: controlRoot32,
		})
	}

	for i, channel := range bundle.Channels {
		_, wanted := requested[channel.ID]
		// A Public Channel derives its key from the community_root (CORD-03),
		// so it has no independent rekey and rotates only with the base.
		public := channel.Key == priorRoot
		if wanted && public && !rotation.Refound {
			return plan, fmt.Errorf("channel %s is public and rotates only with a Refounding", channel.ID)
		}
		if !wanted && !(public && rotation.Refound) {
			continue
		}
		delete(requested, channel.ID)
		prevCommit, err := concordEpochCommitment(channel.Epoch, channel.Key)
		if err != nil {
			return plan, fmt.Errorf("channel %s continuity: %w", channel.ID, err)
		}
		channelKey := nextRoot
		channelEpoch := nextRootEpoch
		if !public {
			minted, mintErr := m.mintKey()
			if mintErr != nil {
				return plan, fmt.Errorf("mint channel %s key: %w", channel.ID, mintErr)
			}
			channelKey = minted
			channelEpoch = channel.Epoch + 1

			channelID32, idErr := concordID32(channel.ID)
			if idErr != nil {
				return plan, fmt.Errorf("channel %s id: %w", channel.ID, idErr)
			}
			channelKey32, keyErr := concordID32(channelKey)
			if keyErr != nil {
				return plan, fmt.Errorf("minted channel %s key: %w", channel.ID, keyErr)
			}
			plan.scopes = append(plan.scopes, concordRekeyScope{
				scopeID:      channelID32,
				addressLabel: concordLabelRekeyPseudonym,
				addressID:    channelID32,
				prevEpoch:    channel.Epoch,
				newEpoch:     channelEpoch,
				prevCommit:   prevCommit,
				newKey:       channelKey32,
			})
		}
		if err := setConcordBundleFields(channels[i], map[string]any{
			"key":   channelKey,
			"epoch": channelEpoch,
		}); err != nil {
			return plan, err
		}
		receipt.Channels = append(receipt.Channels, ConcordChannelRotation{
			ChannelID:  channel.ID,
			PrevEpoch:  channel.Epoch,
			NewEpoch:   channelEpoch,
			PrevCommit: prevCommit,
			Public:     public,
		})
	}
	if len(requested) > 0 {
		missing := make([]string, 0, len(requested))
		for channelID := range requested {
			missing = append(missing, channelID)
		}
		return plan, fmt.Errorf("channels not granted by the current invite bundle: %s", strings.Join(missing, ", "))
	}

	if channels != nil {
		encoded, err := json.Marshal(channels)
		if err != nil {
			return plan, fmt.Errorf("encode rotated channels: %w", err)
		}
		fields["channels"] = encoded
	}
	rotated, err := json.Marshal(fields)
	if err != nil {
		return plan, fmt.Errorf("encode rotated invite bundle: %w", err)
	}
	plan.bundle = rotated
	plan.controlRoot = controlRoot
	plan.receipt = receipt
	plan.priorRoot = priorRoot32[:]
	return plan, nil
}

func setConcordBundleFields(fields map[string]json.RawMessage, values map[string]any) error {
	for name, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode rotated %s: %w", name, err)
		}
		fields[name] = encoded
	}
	return nil
}

func normalizeConcordRecipients(recipients []string) ([]string, error) {
	normalized := make([]string, 0, len(recipients))
	seen := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		recipient = strings.ToLower(strings.TrimSpace(recipient))
		if _, err := nostr.PubKeyFromHex(recipient); err != nil || !validConcordHex32(recipient) {
			return nil, fmt.Errorf("recipient %q is not a 32-byte lowercase hex pubkey", recipient)
		}
		if _, duplicate := seen[recipient]; duplicate {
			continue
		}
		seen[recipient] = struct{}{}
		normalized = append(normalized, recipient)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("at least one surviving recipient is required; a rotation with no recipients severs every member")
	}
	return normalized, nil
}

// normalizeConcordStaff validates the Control-writing survivors (CORD-04 §3).
//
// Staff must be survivors: a base Rekey Blob for a staff recipient carries the
// next epoch's control_root, so handing one to somebody outside the rotation
// would deliver the Control Plane's write secret to a member who is not even
// receiving the root it belongs to.
func normalizeConcordStaff(staff []string, recipients []string) ([]string, error) {
	if len(staff) == 0 {
		return nil, nil
	}
	surviving := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		surviving[recipient] = struct{}{}
	}
	normalized := make([]string, 0, len(staff))
	seen := make(map[string]struct{}, len(staff))
	for _, member := range staff {
		member = strings.ToLower(strings.TrimSpace(member))
		if _, err := nostr.PubKeyFromHex(member); err != nil || !validConcordHex32(member) {
			return nil, fmt.Errorf("staff %q is not a 32-byte lowercase hex pubkey", member)
		}
		if _, ok := surviving[member]; !ok {
			return nil, fmt.Errorf("staff %s is not among the surviving recipients", member)
		}
		if _, duplicate := seen[member]; duplicate {
			continue
		}
		seen[member] = struct{}{}
		normalized = append(normalized, member)
	}
	return normalized, nil
}

// normalizeConcordDirectInvites resolves who additionally receives a CORD-05
// §6 Direct Invite. An empty list means every survivor, the fleet-provisioned
// case; a narrower one still leaves the omitted survivors converging from
// their Rekey Blobs alone (CORD-06 §1).
func normalizeConcordDirectInvites(directInvites []string, recipients []string) ([]string, error) {
	if len(directInvites) == 0 {
		return recipients, nil
	}
	surviving := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		surviving[recipient] = struct{}{}
	}
	normalized := make([]string, 0, len(directInvites))
	seen := make(map[string]struct{}, len(directInvites))
	for _, target := range directInvites {
		target = strings.ToLower(strings.TrimSpace(target))
		if _, err := nostr.PubKeyFromHex(target); err != nil || !validConcordHex32(target) {
			return nil, fmt.Errorf("direct invite %q is not a 32-byte lowercase hex pubkey", target)
		}
		if _, ok := surviving[target]; !ok {
			return nil, fmt.Errorf("direct invite %s is not among the surviving recipients", target)
		}
		if _, duplicate := seen[target]; duplicate {
			continue
		}
		seen[target] = struct{}{}
		normalized = append(normalized, target)
	}
	return normalized, nil
}

// concordRekeyRecipients pairs each survivor with the blob form they receive,
// preserving the caller's recipient order so a rotation chunks deterministically.
func concordRekeyRecipients(recipients []string, staff []string) ([]concordRekeyRecipient, error) {
	staffSet := make(map[string]struct{}, len(staff))
	for _, member := range staff {
		staffSet[member] = struct{}{}
	}
	blobRecipients := make([]concordRekeyRecipient, 0, len(recipients))
	for _, recipient := range recipients {
		pk, err := nostr.PubKeyFromHex(recipient)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient: %w", err)
		}
		_, isStaff := staffSet[recipient]
		blobRecipients = append(blobRecipients, concordRekeyRecipient{hex: recipient, pk: pk, staff: isStaff})
	}
	return blobRecipients, nil
}

// mintConcordKey mints a fresh 32-byte community, control, or channel key.
func mintConcordKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("read random key material: %w", err)
	}
	return hex.EncodeToString(key), nil
}
