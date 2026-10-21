// Package trustepoch verifies operator-authorized changes to a Bahia service
// identity. It has no relay dependency: callers provide an event head from a
// subscription or durable store and keep their own subscription open.
package trustepoch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	Topic     = kinds.CPStateTopicTrustEpoch
	Schema    = "bahia.trust-epoch.v1"
	AckTopic  = kinds.CPStateTopicIdentityRecoveryReceipt
	AckSchema = "bahia.trust-epoch-ack.v1"
	Domain    = "identity"
)

var ErrUnverifiable = errors.New("trust epoch is present but unverifiable")
var ErrRegression = errors.New("trust epoch regressed")
var ErrInventory = errors.New("accepted inventory is absent or invalid")

// Epoch is the content of an operator-root-signed trust transition.
type Epoch struct {
	IncidentID        string   `json:"incident_id"`
	Epoch             uint64   `json:"epoch"`
	OldPubkey         string   `json:"old_pubkey"`
	NewPubkey         string   `json:"new_pubkey"`
	ControlledRelays  []string `json:"controlled_relays"`
	ActivationBarrier int64    `json:"activation_barrier"`
	InventoryDigest   string   `json:"inventory_digest"`
	InventoryCount    int      `json:"inventory_count"`
	ConsumerRoster    []string `json:"consumer_roster"`
	Activated         bool     `json:"activated"`
}

// Accepted is the durable high-water mark. EventID makes an identical head
// idempotent on restart while refusing a different head at the same epoch.
type Accepted struct {
	Epoch     uint64 `json:"epoch"`
	EventID   string `json:"event_id"`
	OldPubkey string `json:"old_pubkey"`
	NewPubkey string `json:"new_pubkey"`
	Activated bool   `json:"activated"`
	// KnownPubkeys retains generations from verified heads even when the
	// addressable relay coordinate has replaced those heads.
	KnownPubkeys []string `json:"known_pubkeys,omitempty"`
}

// Source returns the current head at the operator root's trust-epoch coordinate.
// A network EOSE alone is not an absence proof. A source must return an error
// when its configured authoritative head cannot be established.
type Source interface {
	Head(context.Context, string, string) (*nostr.Event, bool, error)
}

// Persistence atomically rejects lower epochs and conflicting same-epoch heads.
type Persistence interface {
	Last(context.Context, string) (Accepted, bool, error)
	Accept(context.Context, string, Accepted) error
}

// VerifiedInventory supplies the full accepted event-ID artifact exported by
// the recovery engine. The verifier independently checks its count and digest.
type VerifiedInventory interface {
	IDs(context.Context, string, string) ([]string, error)
}

type Verifier struct {
	OperatorRoot string
	LegacyPubkey string
	Source       Source
	Persistence  Persistence
	Inventory    VerifiedInventory
}

type Resolution struct {
	ServiceID    string
	Pubkey       string
	Epoch        uint64
	Record       *Epoch
	EventID      string
	Inventory    map[string]struct{}
	KnownPubkeys []string
}

type Decision string

const (
	Accept           Decision = "accept"
	Hostile          Decision = "hostile"
	PreEpochAccepted Decision = "pre-epoch-accepted"
)

func validHex64(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func exactly(tags nostr.Tags, name, want string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) > 1 && tag[0] == name {
			count++
			if tag[1] != want {
				return false
			}
		}
	}
	return count == 1
}

// Parse validates the complete signed envelope, not merely its JSON payload.
func Parse(ev nostr.Event, serviceID, operatorRoot string) (Epoch, error) {
	bad := func(reason string) (Epoch, error) { return Epoch{}, fmt.Errorf("%w: %s", ErrUnverifiable, reason) }
	if serviceID == "" || strings.ContainsAny(serviceID, " \t\r\n:") || !validHex64(operatorRoot) || ev.PubKey.Hex() != operatorRoot {
		return bad("service id or independent operator root")
	}
	if ev.Kind != nostr.Kind(kinds.CASControlState) || !ev.CheckID() || !ev.VerifySignature() {
		return bad("kind, id, or signature")
	}
	if !exactly(ev.Tags, "d", "trust-epoch:"+serviceID) ||
		!exactly(ev.Tags, "t", Topic) || !exactly(ev.Tags, "domain", Domain) ||
		!exactly(ev.Tags, "schema", Schema) ||
		!exactly(ev.Tags, "legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()) ||
		!exactly(ev.Tags, "deleted", "false") {
		return bad("contract tags")
	}
	var body Epoch
	decoder := json.NewDecoder(strings.NewReader(ev.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return bad("content JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return bad("trailing content")
	}
	if body.IncidentID == "" || body.Epoch == 0 || !validHex64(body.OldPubkey) ||
		!validHex64(body.NewPubkey) || body.OldPubkey == body.NewPubkey ||
		body.OldPubkey == operatorRoot || body.NewPubkey == operatorRoot ||
		body.ActivationBarrier <= 0 || !validHex64(body.InventoryDigest) ||
		body.InventoryCount < 0 || body.ControlledRelays == nil || body.ConsumerRoster == nil ||
		!exactly(ev.Tags, "epoch", strconv.FormatUint(body.Epoch, 10)) {
		return bad("content fields")
	}
	seen := make(map[string]bool, len(body.ConsumerRoster))
	for _, id := range body.ConsumerRoster {
		if !Registered(id) || seen[id] {
			return bad("consumer roster")
		}
		seen[id] = true
	}
	for _, id := range ConsumerIDs() {
		if !seen[id] {
			return bad("incomplete consumer roster")
		}
	}
	return body, nil
}

// CheckInventory returns an ID set only after exact count, uniqueness, order,
// and digest verification. The artifact is never inferred from the digest.
func CheckInventory(ids []string, digest string, count int) (map[string]struct{}, error) {
	if ids == nil || count < 0 || len(ids) != count || !validHex64(digest) {
		return nil, ErrInventory
	}
	copyIDs := slices.Clone(ids)
	slices.Sort(copyIDs)
	result := make(map[string]struct{}, len(copyIDs))
	for i, id := range copyIDs {
		if !validHex64(id) || (i > 0 && copyIDs[i-1] == id) {
			return nil, ErrInventory
		}
		result[id] = struct{}{}
	}
	sum := sha256.Sum256([]byte(strings.Join(copyIDs, "\n")))
	if hex.EncodeToString(sum[:]) != digest {
		return nil, ErrInventory
	}
	return result, nil
}

// ServiceIdentity resolves and durably accepts one head. Absence is legacy
// only before any epoch has ever been accepted for this service id.
func (v *Verifier) ServiceIdentity(ctx context.Context, serviceID string) (Resolution, error) {
	if v == nil || v.Source == nil || v.Persistence == nil || serviceID == "" ||
		!validHex64(v.LegacyPubkey) || !validHex64(v.OperatorRoot) || v.OperatorRoot == v.LegacyPubkey {
		return Resolution{}, fmt.Errorf("%w: verifier configuration", ErrUnverifiable)
	}
	_, hadLast, err := v.Persistence.Last(ctx, serviceID)
	if err != nil {
		return Resolution{}, fmt.Errorf("read accepted trust epoch: %w", err)
	}
	ev, present, err := v.Source.Head(ctx, serviceID, v.OperatorRoot)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve trust epoch: %w", err)
	}
	if !present {
		if hadLast {
			return Resolution{}, fmt.Errorf("%w: accepted head disappeared", ErrRegression)
		}
		return Resolution{ServiceID: serviceID, Pubkey: v.LegacyPubkey, KnownPubkeys: []string{v.LegacyPubkey}}, nil
	}
	if ev == nil {
		return Resolution{}, fmt.Errorf("%w: nil head", ErrUnverifiable)
	}
	return v.AcceptHead(ctx, serviceID, *ev)
}

// AcceptHead validates and durably advances a newly delivered head before a
// service event from the same relay can be admitted.
func (v *Verifier) AcceptHead(ctx context.Context, serviceID string, ev nostr.Event) (Resolution, error) {
	if v == nil || v.Persistence == nil || !validHex64(v.OperatorRoot) ||
		!validHex64(v.LegacyPubkey) || v.OperatorRoot == v.LegacyPubkey {
		return Resolution{}, fmt.Errorf("%w: verifier configuration", ErrUnverifiable)
	}
	last, hadLast, err := v.Persistence.Last(ctx, serviceID)
	if err != nil {
		return Resolution{}, fmt.Errorf("read accepted trust epoch: %w", err)
	}
	body, err := Parse(ev, serviceID, v.OperatorRoot)
	if err != nil {
		return Resolution{}, err
	}
	if hadLast {
		// Older ledgers retain only their last transition. Once the original
		// identity is no longer its old key, missing lineage cannot be inferred.
		if (len(last.KnownPubkeys) == 0 && last.OldPubkey != v.LegacyPubkey) ||
			(len(last.KnownPubkeys) > 0 && !slices.Contains(last.KnownPubkeys, v.LegacyPubkey)) {
			return Resolution{}, fmt.Errorf("%w: accepted identity lineage is incomplete", ErrUnverifiable)
		}
		if body.Epoch < last.Epoch {
			return Resolution{}, ErrRegression
		}
		if body.Epoch == last.Epoch && ev.ID.Hex() != last.EventID {
			return Resolution{}, fmt.Errorf("%w: conflicting same-epoch head", ErrRegression)
		}
		if body.Epoch == last.Epoch && (body.OldPubkey != last.OldPubkey ||
			body.NewPubkey != last.NewPubkey || body.Activated != last.Activated) {
			return Resolution{}, fmt.Errorf("%w: accepted head state differs", ErrUnverifiable)
		}
		previousKey := last.NewPubkey
		if !last.Activated {
			previousKey = last.OldPubkey
		}
		if body.Epoch > last.Epoch && body.OldPubkey != previousKey {
			return Resolution{}, fmt.Errorf("%w: broken key chain", ErrUnverifiable)
		}
	} else if body.OldPubkey != v.LegacyPubkey {
		return Resolution{}, fmt.Errorf("%w: old key is not configured legacy key", ErrUnverifiable)
	}
	accepted := Accepted{Epoch: body.Epoch, EventID: ev.ID.Hex(), OldPubkey: body.OldPubkey,
		NewPubkey: body.NewPubkey, Activated: body.Activated}
	if err := v.Persistence.Accept(ctx, serviceID, accepted); err != nil {
		return Resolution{}, err
	}
	persisted, present, err := v.Persistence.Last(ctx, serviceID)
	if err != nil {
		return Resolution{}, fmt.Errorf("read persisted identity lineage: %w", err)
	}
	if !present || persisted.EventID != ev.ID.Hex() {
		return Resolution{}, ErrRegression
	}
	if len(persisted.KnownPubkeys) == 0 {
		return Resolution{}, fmt.Errorf("%w: identity lineage was not persisted", ErrUnverifiable)
	}
	resolved := Resolution{ServiceID: serviceID, Pubkey: body.OldPubkey, Epoch: body.Epoch, Record: &body, EventID: ev.ID.Hex(), KnownPubkeys: slices.Clone(persisted.KnownPubkeys)}
	if body.Activated {
		resolved.Pubkey = body.NewPubkey
		if v.Inventory != nil {
			ids, err := v.Inventory.IDs(ctx, serviceID, body.InventoryDigest)
			if err == nil {
				verified, verifyErr := CheckInventory(ids, body.InventoryDigest, body.InventoryCount)
				if verifyErr == nil {
					resolved.Inventory = verified
				}
			}
		}
	}
	return resolved, nil
}

// JudgeEvent labels only the current key as ordinary acceptance. In an
// activated epoch, an old-key event is accepted exclusively by inventory ID
// and barrier; its age alone never grants admission.
func (r Resolution) JudgeEvent(ev nostr.Event) Decision {
	if !ev.CheckID() || !ev.VerifySignature() {
		return Hostile
	}
	if ev.PubKey.Hex() == r.Pubkey {
		return Accept
	}
	if r.Record != nil && r.Record.Activated && ev.PubKey.Hex() == r.Record.OldPubkey &&
		r.Inventory != nil && int64(ev.CreatedAt) <= r.Record.ActivationBarrier {
		if _, ok := r.Inventory[ev.ID.Hex()]; ok {
			return PreEpochAccepted
		}
	}
	return Hostile
}

func (v *Verifier) JudgeEvent(ctx context.Context, serviceID string, ev nostr.Event) (Decision, error) {
	r, err := v.ServiceIdentity(ctx, serviceID)
	if err != nil {
		return Hostile, err
	}
	return r.JudgeEvent(ev), nil
}

// JudgeServiceEvent resolves the current epoch before admitting an event from
// any known generation of the service identity. Unrelated component authors
// remain subject to the caller's own authority policy.
func (v *Verifier) JudgeServiceEvent(ctx context.Context, serviceID string, ev nostr.Event) (Decision, bool, error) {
	r, err := v.ServiceIdentity(ctx, serviceID)
	if err != nil {
		return Hostile, true, err
	}
	if !r.IsServiceAuthor(ev.PubKey.Hex()) {
		return Accept, false, nil
	}
	return r.JudgeEvent(ev), true, nil
}

// IsServiceAuthor identifies every observed generation before a caller applies
// unrelated-component policy. It does not grant event or read authorization.
func (r Resolution) IsServiceAuthor(pubkey string) bool {
	return pubkey == r.Pubkey || slices.Contains(r.KnownPubkeys, pubkey) ||
		(r.Record != nil && (pubkey == r.Record.OldPubkey || pubkey == r.Record.NewPubkey))
}
