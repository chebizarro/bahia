// Package identityrecovery runs operator-governed identity recovery outside the daemon.
package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/relayguard"
)

type Phase string

const (
	Declare                Phase = "declare"
	Contain                Phase = "contain"
	Inventory              Phase = "inventory"
	ProvisionNewIdentity   Phase = "provision-new-identity"
	CutActivateTrust       Phase = "cut-activate-trust"
	ReassertCanonicalState Phase = "reassert-canonical-state"
	RotateExternalSecrets  Phase = "rotate-external-secrets"
	DeleteAndPurge         Phase = "delete-and-purge"
	Verify                 Phase = "verify"
	RetireOldIdentity      Phase = "retire-old-identity"
)

var Phases = []Phase{Declare, Contain, Inventory, ProvisionNewIdentity, CutActivateTrust, ReassertCanonicalState, RotateExternalSecrets, DeleteAndPurge, Verify, RetireOldIdentity}
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var ErrAdapterNotRegistered = errors.New("recovery adapter not registered")
var ErrForwardOnly = errors.New("trust cutover is activated; recovery is forward-only")
var ErrIncomplete = errors.New("recovery evidence incomplete")

type Config struct {
	ServiceID         string
	OldPubkey         nostr.PubKey
	ReasonCode        string
	ControlledRelays  []string
	CleanupRelays     []string
	ConsumerRoster    []string
	OperatorPubkey    nostr.PubKey
	LocalStorePath    string
	AcceptedEventIDs  []string
	VerifiedSnapshots []SnapshotArtifact
}

func (c Config) Validate() error {
	if c.ServiceID == "" || c.OldPubkey == nostr.ZeroPK || c.OperatorPubkey == nostr.ZeroPK || c.OldPubkey == c.OperatorPubkey || c.ReasonCode == "" || !codePattern.MatchString(c.ReasonCode) || !codePattern.MatchString(c.ServiceID) || len(c.ControlledRelays) == 0 || len(c.CleanupRelays) == 0 || len(c.ConsumerRoster) == 0 || c.LocalStorePath == "" {
		return errors.New("incomplete recovery configuration")
	}
	if err := validateSnapshotArtifacts(c.VerifiedSnapshots); err != nil {
		return err
	}
	if slices.Contains(c.ControlledRelays, "") || slices.Contains(c.CleanupRelays, "") || slices.Contains(c.ConsumerRoster, "") {
		return errors.New("empty relay or consumer")
	}
	seen := make(map[string]bool, len(c.CleanupRelays))
	for _, relay := range c.CleanupRelays {
		url := relayguard.Normalize(relay)
		if url == "" || seen[url] {
			return errors.New("invalid or duplicate cleanup relay")
		}
		seen[url] = true
	}
	for _, relay := range c.ControlledRelays {
		if !seen[relayguard.Normalize(relay)] {
			return errors.New("cleanup relays must include controlled relays")
		}
	}
	return nil
}

type EventSource interface {
	// Read returns only validated events after all configured relays reached EOSE.
	Read(context.Context, nostr.Filter) ([]nostr.Event, error)
	// Publish requires a protocol OK (or duplicate) from every controlled relay.
	Publish(context.Context, nostr.Event) error
}
type LocalInventory interface {
	Events(context.Context, nostr.PubKey) ([]nostr.Event, error)
}
type DomainRecoveryAdapter interface {
	Inventory(context.Context) ([]string, error)
	Reassert(context.Context, nostr.Keyer) ([]string, error)
	Verify(context.Context) ([]string, error)
}
type SecretRef struct {
	ID         string                     `json:"id"`
	Provider   string                     `json:"provider"`
	Label      string                     `json:"label,omitempty"`
	Credential domain.SecretCredentialRef `json:"credential_ref"`
}
type SecretRotationTracker interface {
	Inventory(context.Context) ([]SecretRef, error)
	Rotate(context.Context, []SecretRef) (map[string]string, error)
}
type PurgeReceiptSink interface {
	Purge(context.Context, []nostr.ID) ([]string, error)
}
type ContainmentAdapter interface {
	Contain(context.Context) ([]string, error)
}
type ConsumerAcknowledgments interface {
	Missing(context.Context, int64, []string) ([]string, error)
	Readiness(context.Context, int64, string, string, []string) (CutoverReadiness, error)
}

type CutoverReadiness struct {
	EvidenceIDs   []string
	Waivers       map[string]string
	WaiverDetails map[string]WaiverEvidence
	Missing       []string
}

type WaiverEvidence struct {
	ReasonCode  string   `json:"reason_code"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Incident struct {
	IncidentID       string   `json:"incident_id"`
	OldPubkey        string   `json:"old_pubkey"`
	DeclaredAt       string   `json:"declared_at"`
	ReasonCode       string   `json:"reason_code"`
	ControlledRelays []string `json:"controlled_relays"`
	Phase            Phase    `json:"phase"`
	Aborted          bool     `json:"aborted"`
}
type TrustEpoch struct {
	IncidentID        string   `json:"incident_id"`
	Epoch             int64    `json:"epoch"`
	OldPubkey         string   `json:"old_pubkey"`
	NewPubkey         string   `json:"new_pubkey"`
	ControlledRelays  []string `json:"controlled_relays"`
	ActivationBarrier int64    `json:"activation_barrier"`
	InventoryDigest   string   `json:"inventory_digest"`
	InventoryCount    int      `json:"inventory_count"`
	ConsumerRoster    []string `json:"consumer_roster"`
	Activated         bool     `json:"activated"`
}
type Receipt struct {
	IncidentID             string                    `json:"incident_id"`
	Phase                  Phase                     `json:"phase"`
	Status                 string                    `json:"status"`
	InputDigest            string                    `json:"input_digest"`
	EvidenceIDs            []string                  `json:"evidence_ids"`
	InventoryRelays        map[string]RelayEvidence  `json:"inventory_relays,omitempty"`
	InventorySnapshots     []SnapshotReceipt         `json:"inventory_snapshots,omitempty"`
	InventorySources       map[string]string         `json:"inventory_sources,omitempty"`
	InventorySourceDefault string                    `json:"inventory_source_default,omitempty"`
	WaiverEventIDs         map[string]string         `json:"waiver_event_ids,omitempty"`
	WaiverDetails          map[string]WaiverEvidence `json:"waiver_details,omitempty"`
	CompletedAt            string                    `json:"completed_at"`
}
type InventoryEvent struct {
	ID        string `json:"id"`
	Kind      int    `json:"kind"`
	CreatedAt int64  `json:"created_at"`
}
type State struct {
	IncidentID            string                              `json:"incident_id"`
	DeclaredAt            string                              `json:"declared_at"`
	OldPubkey             string                              `json:"old_pubkey"`
	OperatorPubkey        string                              `json:"operator_pubkey"`
	NewPubkey             string                              `json:"new_pubkey"`
	Inventory             []InventoryEvent                    `json:"inventory"`
	Observed              []InventoryEvent                    `json:"observed"`
	DeletionIDs           []string                            `json:"deletion_ids"`
	CleanupInventory      map[string]RelayEvidence            `json:"cleanup_inventory,omitempty"`
	CleanupDeletions      map[string]map[string]RelayEvidence `json:"cleanup_deletions,omitempty"`
	InventoryDigest       string                              `json:"inventory_digest"`
	InventorySnapshots    []SnapshotReceipt                   `json:"inventory_snapshots,omitempty"`
	InventorySources      map[string]string                   `json:"inventory_sources,omitempty"`
	VerifiedSnapshots     []SnapshotArtifact                  `json:"verified_snapshots,omitempty"`
	Epoch                 int64                               `json:"epoch"`
	ActivationBarrier     int64                               `json:"activation_barrier"`
	CutoverWaiverEventIDs map[string]string                   `json:"cutover_waiver_event_ids,omitempty"`
	CutoverWaiverDetails  map[string]WaiverEvidence           `json:"cutover_waiver_details,omitempty"`
	Aborted               bool                                `json:"aborted"`
	LastTimestamp         int64                               `json:"last_timestamp"`
	Records               map[string]nostr.Event              `json:"records"`
	Pending               []string                            `json:"pending"`
}

func (s State) Receipt(p Phase) (Receipt, bool) {
	author, err := nostr.PubKeyFromHex(s.OperatorPubkey)
	if err != nil {
		return Receipt{}, false
	}
	d := "identity-incident:" + s.IncidentID + ":phase:" + string(p)
	var candidates []nostr.Event
	for id, ev := range s.Records {
		if slices.Contains(s.Pending, id) {
			continue
		}
		if validRecord(ev, author, kinds.CPStateTopicIdentityRecoveryReceipt, d, kinds.CPStateFamilyIdentityRecoveryReceipt) && tag(ev, "incident") == s.IncidentID {
			candidates = append(candidates, ev)
		}
	}
	latestEvent, ok := latest(candidates)
	if !ok {
		return Receipt{}, false
	}
	var r Receipt
	if json.Unmarshal([]byte(latestEvent.Content), &r) != nil || r.IncidentID != s.IncidentID || r.Phase != p || r.Status != "completed" {
		return Receipt{}, false
	}
	return r, true
}
func (s State) signedCompletion(p Phase) bool {
	for id, ev := range s.Records {
		if !slices.Contains(s.Pending, id) {
			continue
		}
		var r Receipt
		if json.Unmarshal([]byte(ev.Content), &r) == nil && r.IncidentID == s.IncidentID && r.Phase == p && r.Status == "completed" && ev.CheckID() && ev.VerifySignature() {
			return true
		}
	}
	return false
}
func (s State) Next() (Phase, bool) {
	if s.Aborted {
		return "", false
	}
	for _, p := range Phases {
		if _, ok := s.Receipt(p); !ok {
			return p, true
		}
	}
	return "", false
}
func (s State) CutComplete() bool { _, ok := s.Receipt(CutActivateTrust); return ok }
func InventoryDigest(events []InventoryEvent) (string, int, error) {
	ids := make([]string, 0, len(events))
	seen := map[string]struct{}{}
	for _, e := range events {
		id, err := nostr.IDFromHex(e.ID)
		if err != nil || id.Hex() != e.ID {
			return "", 0, errors.New("invalid inventory event id")
		}
		if _, ok := seen[e.ID]; !ok {
			seen[e.ID] = struct{}{}
			ids = append(ids, e.ID)
		}
	}
	slices.Sort(ids)
	digest := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(digest[:]), len(ids), nil
}
func tag(ev nostr.Event, key string) string {
	for _, t := range ev.Tags {
		if len(t) == 2 && t[0] == key {
			return t[1]
		}
	}
	return ""
}
func validRecord(ev nostr.Event, author nostr.PubKey, topic, d string, family kinds.CPStateFamily) bool {
	schema := ""
	switch topic {
	case kinds.CPStateTopicIdentityIncident:
		schema = "bahia.identity-incident.v1"
	case kinds.CPStateTopicTrustEpoch:
		schema = "bahia.trust-epoch.v1"
	case kinds.CPStateTopicIdentityRecoveryReceipt:
		schema = "bahia.identity-recovery-receipt.v1"
	}
	return schema != "" && ev.Kind == nostr.Kind(kinds.CASControlState) && ev.PubKey == author && ev.CheckID() && ev.VerifySignature() && tag(ev, "t") == topic && tag(ev, "d") == d && tag(ev, "domain") == "identity" && tag(ev, "schema") == schema && tag(ev, "legacy_kind") == family.TagValue() && tag(ev, "deleted") == "false"
}
func latest(events []nostr.Event) (nostr.Event, bool) {
	if len(events) == 0 {
		return nostr.Event{}, false
	}
	slices.SortFunc(events, func(a, b nostr.Event) int {
		if a.CreatedAt != b.CreatedAt {
			if a.CreatedAt > b.CreatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID.Hex(), b.ID.Hex())
	})
	return events[0], true
}
func newIncidentID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func digestInput(s State, p Phase) string {
	sum := sha256.Sum256([]byte(s.IncidentID + "\x00" + string(p) + "\x00" + s.InventoryDigest + "\x00" + s.NewPubkey))
	return hex.EncodeToString(sum[:])
}
func safeEvidence(ids []string) error {
	for _, id := range ids {
		if strings.Contains(strings.ToLower(id), "nsec1") || strings.ContainsAny(id, "\n\r") {
			return errors.New("unsafe recovery evidence")
		}
	}
	return nil
}
func (s State) ValidateReceipts(author nostr.PubKey) error {
	for _, p := range Phases {
		r, ok := s.Receipt(p)
		if !ok {
			return fmt.Errorf("%w: %s receipt", ErrIncomplete, p)
		}
		if r.Status != "completed" {
			return fmt.Errorf("%w: %s", ErrIncomplete, p)
		}
		d := "identity-incident:" + s.IncidentID + ":phase:" + string(p)
		found := false
		for _, ev := range s.Records {
			if validRecord(ev, author, kinds.CPStateTopicIdentityRecoveryReceipt, d, kinds.CPStateFamilyIdentityRecoveryReceipt) && tag(ev, "incident") == s.IncidentID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: invalid %s receipt", ErrIncomplete, p)
		}
	}
	return nil
}
