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
	CommunityID    string                   `json:"community_id"`
	Refounded      bool                     `json:"refounded"`
	PrevRootEpoch  uint64                   `json:"prev_root_epoch"`
	RootEpoch      uint64                   `json:"root_epoch"`
	RootPrevCommit string                   `json:"root_prev_commit,omitempty"`
	Channels       []ConcordChannelRotation `json:"channels,omitempty"`
	Recipients     []string                 `json:"recipients"`
	Reason         string                   `json:"reason,omitempty"`
	RotatedAt      time.Time                `json:"rotated_at"`
}

// concordCommunityRotator is the CORD-06 surface the provisioner exposes.
type concordCommunityRotator interface {
	Rotate(context.Context, ConcordRotation) (*ConcordRotationReceipt, error)
}

type concordRotationPlan struct {
	bundle      json.RawMessage
	controlRoot string
	receipt     ConcordRotationReceipt
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

	current, record, err := source.resolve(ctx, m.bus)
	if err != nil {
		return nil, err
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

	staffPK, err := m.staffPubKey(ctx)
	if err != nil {
		return nil, err
	}
	if err := source.custody.Store(ctx, concordCustodyRecord{Bundle: plan.bundle, ControlRoot: plan.controlRoot}); err != nil {
		return nil, fmt.Errorf("persist rotated Concord material for %s: %w", communityID, err)
	}

	receipt := plan.receipt
	for _, recipient := range recipients {
		recipientPK, pkErr := nostr.PubKeyFromHex(recipient)
		if pkErr != nil {
			return &receipt, fmt.Errorf("redistribute rotated Concord material for %s: invalid recipient: %w", communityID, pkErr)
		}
		if deliverErr := m.deliver(ctx, next, staffPK, recipientPK, recipient); deliverErr != nil {
			return &receipt, fmt.Errorf("redistribute rotated Concord material for %s: %w", communityID, deliverErr)
		}
		receipt.Recipients = append(receipt.Recipients, recipient)
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

// mintConcordKey mints a fresh 32-byte community, control, or channel key.
func mintConcordKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("read random key material: %w", err)
	}
	return hex.EncodeToString(key), nil
}
