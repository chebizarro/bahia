package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	toolBuildReceiptFamily  = "canonical-tool-build-v1"
	toolDeployReceiptFamily = "canonical-tool-deploy-v1"
)

var toolAtom = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]*$`)
var toolImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_-]*@sha256:[a-fA-F0-9]{64}$`)

type ToolEffectInput struct {
	RequestEventID    string                `json:"request_event_id"`
	RequestActor      string                `json:"request_actor"`
	RequestCoordinate string                `json:"request_coordinate"`
	BaseImage         string                `json:"base_image"`
	DerivedImage      string                `json:"derived_image"`
	ToolsetHash       string                `json:"toolset_hash"`
	InstallerVersion  string                `json:"installer_version"`
	Step              string                `json:"step"`
	Tools             []domain.ResolvedTool `json:"tools"`
	DenylistEventIDs  []string              `json:"denylist_event_ids,omitempty"`
}

type toolRequestWire struct {
	ID            uuid.UUID            `json:"id"`
	ServiceID     uuid.UUID            `json:"service_id"`
	EnvironmentID uuid.UUID            `json:"environment_id"`
	Operation     string               `json:"operation"`
	Tools         []domain.ToolRequest `json:"tools"`
	Reason        string               `json:"reason"`
}

type toolApprovalWire struct {
	IntentID       uuid.UUID `json:"intent_id"`
	Action         string    `json:"action"`
	Reason         string    `json:"reason"`
	RequestEventID string    `json:"request_event_id"`
}

// ToolCanonicalSource re-reads an exact accepted request and approval from the
// local relay store before every step. The canonical Docker desired state and
// Signet version checks are delegated to the auto-apply source.
type ToolCanonicalSource struct {
	Base            *CanonicalAutoApplySource
	Outbox          *localstore.Outbox
	DeliveryTarget  string
	RequestEventID  nostr.ID
	ApprovalEventID nostr.ID
	Step            string
	// Admission binds the snapshot before the processor publishes the checked
	// approval status. Execution must leave this false and require its proof.
	SkipApprovalProof bool
	ImageRepo         string
	InstallerVersion  string
	AllowedAuthors    map[string]bool
	AllowedAuthor     func(string) bool
	Decryptor         interface {
		DecryptConfidential(context.Context, string, int, string, string) ([]byte, error)
	}
}

func (s *ToolCanonicalSource) allowed(author string) bool {
	if s.AllowedAuthor != nil {
		return s.AllowedAuthor(author)
	}
	return s.AllowedAuthors[author]
}

func (s *ToolCanonicalSource) signed(id nostr.ID) (*controlplane.Intent, error) {
	if s == nil || s.Base == nil || s.Base.Runtime == nil || s.Base.Runtime.store == nil || id == nostr.ZeroID {
		return nil, fmt.Errorf("bahia-4f10q.2.1: local tool intent source is unavailable")
	}
	for event := range s.Base.Runtime.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}) {
		if event.Kind != 30900 || event.ID != id || !event.CheckID() || !event.VerifySignature() {
			return nil, ErrFreshDecisionRequired
		}
		parsed, err := controlplane.ParseIntent(&event)
		if err != nil {
			return nil, err
		}
		parsed.Actor = event.PubKey.Hex()
		if err := s.current(&event); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("%w: signed tool intent %s was superseded or is absent", ErrFreshDecisionRequired, id.Hex())
}

// The exact event, not merely a matching coordinate, must still be the
// locally observed winner. EOSE is not used as a global absence proof.
func (s *ToolCanonicalSource) current(event *nostr.Event) error {
	coordinate := tagValue(event.Tags, "d")
	for candidate := range s.Base.Runtime.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30900},
		Authors: []nostr.PubKey{event.PubKey}, Tags: nostr.TagMap{"d": {coordinate}}}) {
		if !candidate.CheckID() || !candidate.VerifySignature() {
			return ErrFreshDecisionRequired
		}
		if candidate.CreatedAt > event.CreatedAt ||
			(candidate.CreatedAt == event.CreatedAt && candidate.ID.Hex() < event.ID.Hex()) {
			return fmt.Errorf("%w: tool coordinate %s has a newer observed winner", ErrFreshDecisionRequired, coordinate)
		}
	}
	return nil
}

func (s *ToolCanonicalSource) selected(ctx context.Context) (*controlplane.Intent, *controlplane.Intent, toolRequestWire, toolApprovalWire, error) {
	request, err := s.signed(s.RequestEventID)
	if err != nil {
		return nil, nil, toolRequestWire{}, toolApprovalWire{}, err
	}
	if err := controlplane.ValidateToolRequestSignedEnvelope(request); err != nil || !s.allowed(request.Actor) {
		return nil, nil, toolRequestWire{}, toolApprovalWire{}, fmt.Errorf("%w: tool requester is not an authorized signed principal: %v", ErrFreshDecisionRequired, err)
	}
	var body toolRequestWire
	if err := json.Unmarshal([]byte(request.Event.Content), &body); err != nil {
		return nil, nil, body, toolApprovalWire{}, err
	}
	approval, err := s.signed(s.ApprovalEventID)
	if err != nil {
		return nil, nil, body, toolApprovalWire{}, err
	}
	if err := controlplane.ValidateToolApprovalSignedEnvelope(approval); err != nil || !s.allowed(approval.Actor) {
		return nil, nil, body, toolApprovalWire{}, fmt.Errorf("%w: tool approval is not operator-signed: %v", ErrFreshDecisionRequired, err)
	}
	var decision toolApprovalWire
	if err := json.Unmarshal([]byte(approval.Event.Content), &decision); err != nil {
		return nil, nil, body, decision, err
	}
	if decision.IntentID != body.ID || decision.RequestEventID != request.Event.ID.Hex() ||
		approval.Coordinate != "tool-approval:"+body.ID.String() {
		return nil, nil, body, decision, fmt.Errorf("%w: approval does not bind the exact request event", ErrFreshDecisionRequired)
	}
	proofs := []*controlplane.Intent{request}
	if !s.SkipApprovalProof {
		proofs = append(proofs, approval)
	}
	for _, item := range proofs {
		if err := s.acceptedIntent(ctx, item); err != nil {
			return nil, nil, body, decision, err
		}
	}
	return request, approval, body, decision, nil
}

func (s *ToolCanonicalSource) acceptedIntent(ctx context.Context, request *controlplane.Intent) error {
	if s.Outbox == nil || s.DeliveryTarget == "" {
		return fmt.Errorf("bahia-4f10q.2.1: request acceptance proof store is unavailable")
	}
	d := kinds.IntentStatusCoordinate(request.Actor, request.Coordinate)
	filter := nostr.Filter{Kinds: []nostr.Kind{30315}, Authors: []nostr.PubKey{s.Base.Runtime.author},
		Tags: nostr.TagMap{"d": {d}, "status": {"accepted"}}}
	for event := range s.Base.Runtime.store.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if event.CheckID() && event.VerifySignature() && tagValue(event.Tags, "e") == request.Event.ID.Hex() &&
			tagValue(event.Tags, "p") == request.Actor && tagValue(event.Tags, "intent_id") == request.IntentID {
			proof, ok, err := s.Outbox.GetDeliveryProof(event.ID)
			if err != nil {
				return err
			}
			if ok && proof.Target == s.DeliveryTarget && proof.ValidFor(event, s.DeliveryTarget) {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: signed tool intent has no exact relay-ACKed acceptance", ErrFreshDecisionRequired)
}

func (s *ToolCanonicalSource) LoadAutoApplySnapshot(ctx context.Context, serviceID, envID uuid.UUID) (ApplySnapshot, error) {
	requestIntent, approval, request, decision, err := s.selected(ctx)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if request.ServiceID != serviceID || request.EnvironmentID != envID ||
		(s.Step != "build" && s.Step != "deploy" && s.Step != "decision") ||
		(decision.Action != "approve" && decision.Action != "reject") ||
		(decision.Action == "reject" && s.Step != "decision") {
		return ApplySnapshot{}, fmt.Errorf("%w: tool step is not approved for this target", ErrFreshDecisionRequired)
	}
	base, err := s.Base.LoadAutoApplySnapshot(ctx, serviceID, envID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if !toolImage.MatchString(base.Spec.ImageRef) || s.ImageRepo == "" || s.InstallerVersion == "" ||
		strings.ContainsAny(s.ImageRepo, " \r\n\t\"'") {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2.1: tool base image or output repository is not immutable and safe")
	}
	tools := make([]domain.ResolvedTool, len(request.Tools))
	for i, item := range request.Tools {
		manager := strings.ToLower(strings.TrimSpace(item.Manager))
		source := map[string]string{"apt": "debian", "pip": "pypi", "npm": "npmjs", "cargo": "crates.io"}[manager]
		if source == "" || !toolAtom.MatchString(item.Name) || !toolAtom.MatchString(item.Version) ||
			strings.EqualFold(item.Version, "latest") {
			return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2.1: unsupported or unpinned tool package")
		}
		tools[i] = domain.ResolvedTool{Name: item.Name, Manager: manager, Version: item.Version, Source: source}
	}
	hash := build.ComputeToolsetHash(base.Spec.ImageRef, tools, s.InstallerVersion)
	derived := strings.TrimRight(s.ImageRepo, "/") + ":" + strings.TrimPrefix(hash, "sha256:")
	denylistIDs, err := s.checkDenylist(ctx, tools)
	if err != nil {
		return ApplySnapshot{}, err
	}
	base.IntentID = request.ID
	base.DecisionEventID = approval.Event.ID.Hex()
	base.DecisionCoordinate = approval.Coordinate
	base.DecisionActor = approval.Actor
	base.Tool = &ToolEffectInput{RequestEventID: s.RequestEventID.Hex(), RequestActor: requestIntent.Actor,
		RequestCoordinate: "tool-request:" + request.ID.String(), BaseImage: base.Spec.ImageRef,
		DerivedImage: derived, ToolsetHash: hash, InstallerVersion: s.InstallerVersion,
		Step: s.Step, Tools: tools, DenylistEventIDs: denylistIDs}
	base.Spec.ImageRef = derived
	base.Spec.PullPolicy = "never"
	base.Spec.ComputeDesiredHash()
	return base, nil
}

func (s *ToolCanonicalSource) checkDenylist(ctx context.Context, tools []domain.ResolvedTool) ([]string, error) {
	if s.Decryptor == nil {
		return nil, fmt.Errorf("bahia-4f10q.2.1: fleet denylist decryptor is unavailable")
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{s.Base.Runtime.author},
		Tags: nostr.TagMap{"t": {kinds.CPStateTopicToolDenylist}}}
	winners := make(map[string]nostr.Event)
	for event := range s.Base.Runtime.store.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !event.CheckID() || !event.VerifySignature() || tagValue(event.Tags, "schema") != kinds.CASControlStateSchema ||
			tagValue(event.Tags, "legacy_kind") != fmt.Sprint(kinds.ToolDenylistState) || tagValue(event.Tags, "d") == "" {
			return nil, ErrFreshDecisionRequired
		}
		coordinate := tagValue(event.Tags, "d")
		previous, ok := winners[coordinate]
		if !ok || event.CreatedAt > previous.CreatedAt ||
			(event.CreatedAt == previous.CreatedAt && event.ID.Hex() < previous.ID.Hex()) {
			winners[coordinate] = event
		}
	}
	var ids []string
	for _, event := range winners {
		ids = append(ids, event.ID.Hex())
		if tagValue(event.Tags, "deleted") == "true" {
			continue
		}
		plaintext, err := s.Decryptor.DecryptConfidential(ctx, event.Content, kinds.ToolDenylistState,
			tagValue(event.Tags, "d"), kinds.CPStateTopicToolDenylist)
		if err != nil {
			return nil, err
		}
		var blocked domain.ToolDenylistEntry
		if err := json.Unmarshal(plaintext, &blocked); err != nil {
			return nil, err
		}
		for _, tool := range tools {
			if tool.Name == blocked.PackageName && tool.Manager == blocked.Manager {
				return nil, fmt.Errorf("%w: package is on canonical tool denylist", ErrFreshDecisionRequired)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *ToolCanonicalSource) ResolvePinnedSecrets(ctx context.Context, snapshot ApplySnapshot) (map[string]string, func(), error) {
	return s.Base.ResolvePinnedSecrets(ctx, snapshot)
}

var _ AutoApplySnapshotSource = (*ToolCanonicalSource)(nil)
