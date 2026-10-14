package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const HostnameIntentSchema = "bahia.intent.hostname.v1"

// One process can temporarily host overlapping app instances during reload.
var hostnameIntentMu sync.Mutex

type HostnameRecordPublisher interface {
	PublishHostnameRecord(context.Context, kinds.CPStateFamily, string, bool, string) (*nostr.Event, error)
}

type HostnameRouteManager interface {
	CommitClaim(context.Context, *domain.DesiredPublicRoutePlan, uuid.UUID, bool, func(context.Context) error) error
	Withdraw(context.Context, *domain.DesiredPublicRoutePlan, func(context.Context, string) error, func(context.Context) error) error
}

type HostnameIntentHandler struct {
	events  *localstore.Store
	author  nostr.PubKey
	publish HostnameRecordPublisher
	routes  HostnameRouteManager
	ready   func() bool
	trust   *TrustSet
}

func NewHostnameIntentHandler(events *localstore.Store, servicePubkey string, publish HostnameRecordPublisher, routes HostnameRouteManager, trust *TrustSet, ready func() bool) (*HostnameIntentHandler, error) {
	if events == nil || publish == nil || routes == nil || trust == nil || ready == nil {
		return nil, fmt.Errorf("hostname canonical store, publisher, routes, and readiness are required")
	}
	pubkey, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("service pubkey: %w", err)
	}
	return &HostnameIntentHandler{events: events, author: pubkey, publish: publish, routes: routes, trust: trust, ready: ready}, nil
}

func (*HostnameIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteDeployments
}

type hostnameIntentPayload struct {
	Hostname          string                         `json:"hostname"`
	ServiceID         uuid.UUID                      `json:"service_id"`
	EnvironmentID     uuid.UUID                      `json:"environment_id"`
	DeploymentUnitID  uuid.UUID                      `json:"deployment_unit_id"`
	Plan              *domain.DesiredPublicRoutePlan `json:"plan,omitempty"`
	ExpectedUpdatedAt string                         `json:"expected_updated_at,omitempty"`
	IntentID          string                         `json:"intent_id"`
}

type hostnameRecoveryRecord struct {
	Hostname      string                        `json:"hostname"`
	IntentID      string                        `json:"intent_id"`
	IntentEventID string                        `json:"intent_event_id"`
	RequestedBy   string                        `json:"requested_by"`
	Phase         string                        `json:"phase"`
	UpdatedAt     time.Time                     `json:"updated_at"`
	Plan          domain.DesiredPublicRoutePlan `json:"plan"`
}

func (h *HostnameIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if !h.ready() {
		return fmt.Errorf("canonical hostname history has not caught up")
	}
	if intent == nil || intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() || intent.Actor != intent.Event.PubKey.Hex() || intent.Domain != "hostname" || intent.Schema != HostnameIntentSchema {
		return fmt.Errorf("hostname mutation requires a matching signed intent")
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Coordinate != intent.Coordinate || parsed.Op != intent.Op || parsed.OrgID != intent.OrgID || parsed.IntentID != intent.IntentID {
		return fmt.Errorf("hostname signed envelope differs from parsed intent")
	}
	var payload hostnameIntentPayload
	dec := json.NewDecoder(strings.NewReader(intent.Event.Content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return fmt.Errorf("decode hostname intent: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("hostname intent has trailing content")
	}
	hostname, err := domain.NormalizePublicHostname(payload.Hostname)
	if err != nil || hostname != payload.Hostname || intent.Coordinate != kinds.HostnameReservationDTag(hostname) || payload.IntentID != intent.IntentID || payload.ServiceID == uuid.Nil || payload.EnvironmentID == uuid.Nil || payload.DeploymentUnitID == uuid.Nil {
		return fmt.Errorf("hostname intent coordinate, identity, or content is invalid")
	}
	intentID, parseErr := uuid.Parse(intent.IntentID)
	if parseErr != nil || intentID.Version() != 7 {
		return fmt.Errorf("hostname intent_id must be UUIDv7")
	}
	if payload.ExpectedUpdatedAt == "" && intent.ExpectedUpdatedAt != nil || payload.ExpectedUpdatedAt != "" && intent.ExpectedUpdatedAt == nil {
		return fmt.Errorf("hostname revision does not match signed content")
	}
	hostnameIntentMu.Lock()
	defer hostnameIntentMu.Unlock()
	var environmentProtected bool
	if intent.Op == string(domain.RouteSet) {
		environmentProtected, err = h.validateOwner(ctx, payload.ServiceID, payload.EnvironmentID, intent.OrgID)
		if err != nil {
			return err
		}
	}
	winner, err := h.winningIntent(ctx, intent)
	if err != nil {
		return err
	}
	if winner != intent.Event.ID {
		return &intentSupersededError{winner: winner.Hex()}
	}
	current, err := h.Current(ctx, hostname)
	if err != nil {
		return err
	}
	if current != nil && current.IntentID == intent.IntentID && current.IntentEventID == intent.Event.ID.Hex() && ((intent.Op == string(domain.RouteSet) && !current.Deleted) || (intent.Op == string(domain.RouteRemove) && current.Deleted)) {
		return nil
	}
	if current == nil {
		if intent.ExpectedUpdatedAt != nil {
			return &intentStateConflictError{message: "hostname has no revision for expected_updated_at"}
		}
	} else if intent.ExpectedUpdatedAt == nil || !intent.RevisionMatches(current.UpdatedAt) {
		return &intentStateConflictError{message: "hostname reservation revision conflict"}
	}
	switch intent.Op {
	case string(domain.RouteSet):
		if current != nil && !current.Deleted && (current.ServiceID != payload.ServiceID || current.EnvironmentID != payload.EnvironmentID || current.DeploymentUnitID != payload.DeploymentUnitID) {
			return &intentStateConflictError{message: "hostname is reserved by another owner"}
		}
		record := domain.HostnameReservation{Hostname: hostname, OrgID: intent.OrgID, ServiceID: payload.ServiceID, EnvironmentID: payload.EnvironmentID, DeploymentUnitID: payload.DeploymentUnitID, Plan: payload.Plan, IntentID: intent.IntentID, IntentEventID: intent.Event.ID.Hex(), RequestedBy: intent.Actor}
		if current == nil {
			record.UpdatedAt = domain.NewRevisionTime()
		} else {
			record.UpdatedAt = domain.NextRevisionTime(current.UpdatedAt)
		}
		if err := record.Validate(); err != nil {
			return err
		}
		return h.routes.CommitClaim(ctx, record.Plan, intent.OrgID, environmentProtected, func(commitCtx context.Context) error {
			return h.publishRecord(commitCtx, kinds.CPStateFamilyHostnameReservation, hostname, false, record)
		})
	case string(domain.RouteRemove):
		if payload.Plan != nil {
			return fmt.Errorf("remove uses the canonical route plan, not an intent-supplied plan")
		}
		if current == nil || current.Deleted || current.OrgID != intent.OrgID || current.ServiceID != payload.ServiceID || current.EnvironmentID != payload.EnvironmentID || current.DeploymentUnitID != payload.DeploymentUnitID || current.Plan == nil {
			return &intentStateConflictError{message: "hostname is not held by the requested owner"}
		}
		if err := h.publishRecovery(ctx, intent, current, "prepared"); err != nil {
			return err
		}
		tombstone := *current
		tombstone.Plan = nil
		tombstone.Deleted = true
		tombstone.IntentID = intent.IntentID
		tombstone.IntentEventID = intent.Event.ID.Hex()
		tombstone.RequestedBy = intent.Actor
		tombstone.UpdatedAt = domain.NextRevisionTime(current.UpdatedAt)
		return h.routes.Withdraw(ctx, current.Plan, func(effectCtx context.Context, phase string) error {
			return h.publishRecovery(effectCtx, intent, current, phase)
		}, func(effectCtx context.Context) error {
			return h.publishRecord(effectCtx, kinds.CPStateFamilyHostnameReservation, hostname, true, tombstone)
		})
	case string(domain.RoutePreserve):
		if current == nil || current.Deleted || current.OrgID != intent.OrgID || current.ServiceID != payload.ServiceID || current.EnvironmentID != payload.EnvironmentID || current.DeploymentUnitID != payload.DeploymentUnitID || payload.Plan != nil {
			return &intentStateConflictError{message: "hostname cannot be preserved without its current owner"}
		}
		return nil
	default:
		return fmt.Errorf("unsupported hostname operation %q", intent.Op)
	}
}

func (h *HostnameIntentHandler) Current(_ context.Context, hostname string) (*domain.HostnameReservation, error) {
	normalized, err := domain.NormalizePublicHostname(hostname)
	if err != nil || hostname != normalized {
		return nil, fmt.Errorf("hostname must be normalized")
	}
	event, err := h.latest(kinds.HostnameReservationDTag(hostname), kinds.CPStateFamilyHostnameReservation, kinds.CPStateTopicHostnameReservation)
	if err != nil || event == nil {
		return nil, err
	}
	var record domain.HostnameReservation
	if err := json.Unmarshal([]byte(event.Content), &record); err != nil {
		return nil, fmt.Errorf("decode canonical hostname: %w", err)
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.Hostname != hostname || record.Deleted != (hostnameTagValue(event.Tags, "deleted") == "true") {
		return nil, fmt.Errorf("canonical hostname content and tags differ")
	}
	return &record, nil
}

func (h *HostnameIntentHandler) RequireClaim(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if !h.ready() {
		return fmt.Errorf("hostname canonical history has not caught up")
	}
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	record, err := h.Current(ctx, plan.Hostname)
	if err != nil {
		return err
	}
	if record == nil || !record.Owns(plan) || record.Plan.ProviderConfigHash != plan.ProviderConfigHash || !reflect.DeepEqual(record.Plan, plan) {
		return fmt.Errorf("public route %s has no matching canonical hostname claim", plan.Hostname)
	}
	return nil
}

// RequireReleased permits a desired-state route removal only after the exact
// owner has a canonical tombstone. A subsequent reclaim makes removal stale.
func (h *HostnameIntentHandler) RequireReleased(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if !h.ready() {
		return fmt.Errorf("hostname canonical history has not caught up")
	}
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	record, err := h.Current(ctx, plan.Hostname)
	if err != nil {
		return err
	}
	if record == nil || !record.Deleted || record.ServiceID != plan.ServiceID || record.EnvironmentID != plan.EnvironmentID || record.DeploymentUnitID != plan.DeploymentUnitID {
		return fmt.Errorf("public route %s has no matching canonical release", plan.Hostname)
	}
	return nil
}

func (h *HostnameIntentHandler) validateOwner(_ context.Context, serviceID, environmentID, orgID uuid.UUID) (bool, error) {
	var protected bool
	for _, item := range []struct {
		id     uuid.UUID
		family kinds.CPStateFamily
		topic  string
	}{
		{serviceID, kinds.CPStateFamily(kinds.ServiceRegistry), kinds.CPStateTopicServiceRegistry},
		{environmentID, kinds.CPStateFamily(kinds.EnvironmentRegistry), kinds.CPStateTopicEnvironmentRegistry},
	} {
		ev, err := h.latest(item.id.String(), item.family, item.topic)
		if err != nil {
			return false, err
		}
		if ev == nil || hostnameTagValue(ev.Tags, "deleted") == "true" {
			return false, fmt.Errorf("canonical service or environment owner is unavailable")
		}
		var record struct {
			OrgID     uuid.UUID `json:"org_id"`
			Protected bool      `json:"protected"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &record); err != nil || record.OrgID != orgID {
			return false, fmt.Errorf("hostname owner does not match canonical organization")
		}
		if item.id == environmentID {
			protected = record.Protected
		}
	}
	return protected, nil
}

func (h *HostnameIntentHandler) latest(coordinate string, family kinds.CPStateFamily, topic string) (*nostr.Event, error) {
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{h.author}, Tags: nostr.TagMap{"d": {coordinate}, "t": {topic}}, Limit: 1}
	for event := range h.events.QueryEvents(filter) {
		if !event.CheckID() || !event.VerifySignature() || event.PubKey != h.author || hostnameTagValue(event.Tags, "d") != coordinate || hostnameTagValue(event.Tags, "legacy_kind") != family.TagValue() || hostnameTagValue(event.Tags, "t") != topic || hostnameTagValue(event.Tags, "schema") != kinds.CASControlStateSchema {
			return nil, fmt.Errorf("invalid canonical hostname dependency at %s", coordinate)
		}
		return &event, nil
	}
	return nil, nil
}

func (h *HostnameIntentHandler) publishRecord(ctx context.Context, family kinds.CPStateFamily, hostname string, deleted bool, record any) error {
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	coordinate := kinds.HostnameReservationDTag(hostname)
	if family == kinds.CPStateFamilyHostnameRecovery {
		coordinate = kinds.HostnameRecoveryDTag(hostname)
	}
	event, err := h.publish.PublishHostnameRecord(ctx, family, coordinate, deleted, string(content))
	if err != nil {
		return err
	}
	if event == nil {
		current, readErr := h.latest(coordinate, family, map[kinds.CPStateFamily]string{kinds.CPStateFamilyHostnameReservation: kinds.CPStateTopicHostnameReservation, kinds.CPStateFamilyHostnameRecovery: kinds.CPStateTopicHostnameRecovery}[family])
		if readErr != nil {
			return readErr
		}
		if current == nil || current.Content != string(content) {
			return fmt.Errorf("canonical hostname publication had no retained event")
		}
		return nil
	}
	_, err = h.events.SaveEvent(*event)
	if err != nil {
		return fmt.Errorf("retain canonical hostname event: %w", err)
	}
	return nil
}

func (h *HostnameIntentHandler) publishRecovery(ctx context.Context, intent *Intent, current *domain.HostnameReservation, phase string) error {
	order := map[string]int{"prepared": 1, "internal_withdrawn": 4}
	switch current.Plan.Provider {
	case "cloudflare_tunnel":
		order["dns_withdrawn"], order["tunnel_withdrawn"] = 2, 3
	case "caddy":
		order["route_withdrawn"], order["tls_withdrawn"] = 2, 3
	default:
		return fmt.Errorf("unknown hostname recovery provider")
	}
	if order[phase] == 0 {
		return fmt.Errorf("unknown hostname recovery phase %q", phase)
	}
	previous, err := h.latest(kinds.HostnameRecoveryDTag(current.Hostname), kinds.CPStateFamilyHostnameRecovery, kinds.CPStateTopicHostnameRecovery)
	if err != nil {
		return err
	}
	record := hostnameRecoveryRecord{Hostname: current.Hostname, IntentID: intent.IntentID, IntentEventID: intent.Event.ID.Hex(), RequestedBy: intent.Actor, Phase: phase, UpdatedAt: domain.NewRevisionTime(), Plan: *current.Plan}
	if previous != nil {
		var prior hostnameRecoveryRecord
		if err := json.Unmarshal([]byte(previous.Content), &prior); err != nil {
			return fmt.Errorf("decode hostname recovery receipt: %w", err)
		}
		if prior.IntentID == intent.IntentID {
			if prior.IntentEventID != intent.Event.ID.Hex() || prior.RequestedBy != intent.Actor {
				return &intentReplayConflictError{intentID: intent.IntentID}
			}
			if order[prior.Phase] == 0 {
				return fmt.Errorf("unknown retained hostname recovery phase %q", prior.Phase)
			}
			if order[prior.Phase] >= order[phase] {
				return nil
			}
		}
		record.UpdatedAt = domain.NextRevisionTime(prior.UpdatedAt)
	}
	return h.publishRecord(ctx, kinds.CPStateFamilyHostnameRecovery, current.Hostname, false, record)
}

func hostnameTagValue(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func (h *HostnameIntentHandler) winningIntent(ctx context.Context, intent *Intent) (nostr.ID, error) {
	winner := intent.Event.ID
	latest := *intent.Event
	authors := make([]nostr.PubKey, 0)
	for _, raw := range h.trust.AuthorPubkeys() {
		author, err := nostr.PubKeyFromHex(raw)
		if err == nil {
			authors = append(authors, author)
		}
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: authors, Tags: nostr.TagMap{"d": {intent.Coordinate}, "t": {"bahia-intent"}}}
	for candidate := range h.events.QueryEvents(filter) {
		if !candidate.CheckID() || !candidate.VerifySignature() || !h.trust.HasPermission(ctx, intent.OrgID, candidate.PubKey.Hex(), domain.PermWriteDeployments) {
			continue
		}
		parsed, err := ParseIntent(&candidate)
		if err != nil || parsed.Domain != "hostname" || parsed.Schema != HostnameIntentSchema || parsed.OrgID != intent.OrgID || parsed.Coordinate != intent.Coordinate {
			continue
		}
		if candidate.CreatedAt > latest.CreatedAt || candidate.CreatedAt == latest.CreatedAt && bytes.Compare(candidate.ID[:], latest.ID[:]) < 0 {
			latest = candidate
			winner = candidate.ID
		}
	}
	return winner, nil
}

type intentSupersededError struct{ winner string }

func (e *intentSupersededError) Error() string { return "hostname intent superseded by " + e.winner }
