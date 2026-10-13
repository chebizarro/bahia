package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

const llmRouteReceiptFamily = "canonical-llm-route-v1"

// LLMEffectInput is credential-value-free. Release and route fields are kept
// only for the external-API gateway subset; runtime-managed release execution
// is refused until a worker-placement and backend-observation fence exists.
type LLMEffectInput struct {
	RequestEventID             string            `json:"request_event_id"`
	GatewayRef                 string            `json:"gateway_ref"`
	GatewayAdminRevision       string            `json:"gateway_admin_revision"`
	Route                      domain.LLMRoute   `json:"route"`
	Release                    domain.LLMRelease `json:"release"`
	PreviousTerminalCheckpoint string            `json:"previous_terminal_checkpoint,omitempty"`
}

type LLMCanonicalSource struct {
	Runtime              *CanonicalRuntimeSource
	Outbox               *localstore.Outbox
	DeliveryTarget       string
	RequestEventID       nostr.ID
	DecisionEventID      nostr.ID
	IntentID             uuid.UUID
	RouteID              uuid.UUID
	ReleaseID            uuid.UUID
	EnvironmentID        uuid.UUID
	GatewayRef           string
	GatewayAdminRevision string
	SkipDecisionProof    bool
	AllowedAuthor        func(string) bool
	Records              secretstore.Records
	Secrets              *secretstore.Resolver
	Decryptor            interface {
		DecryptConfidential(context.Context, string, int, string, string) ([]byte, error)
	}
}

func (s *LLMCanonicalSource) signed(id nostr.ID) (*controlplane.Intent, error) {
	if s == nil || s.Runtime == nil || s.Runtime.store == nil || id == nostr.ZeroID {
		return nil, fmt.Errorf("bahia-4f10q.2.2: local LLM intent source unavailable")
	}
	for event := range s.Runtime.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}) {
		if event.ID != id || !event.CheckID() || !event.VerifySignature() {
			return nil, ErrFreshDecisionRequired
		}
		parsed, err := controlplane.ParseIntent(&event)
		if err != nil {
			return nil, err
		}
		parsed.Actor = event.PubKey.Hex()
		if err := controlplane.ValidateLLMLifecycleSignedRequest(parsed); err != nil ||
			s.AllowedAuthor == nil || !s.AllowedAuthor(parsed.Actor) {
			return nil, fmt.Errorf("%w: LLM signed intent is not authorized: %v", ErrFreshDecisionRequired, err)
		}
		for candidate := range s.Runtime.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30900},
			Authors: []nostr.PubKey{event.PubKey}, Tags: nostr.TagMap{"d": {parsed.Coordinate}}}) {
			if !candidate.CheckID() || !candidate.VerifySignature() || candidate.CreatedAt > event.CreatedAt ||
				(candidate.CreatedAt == event.CreatedAt && candidate.ID.Hex() < event.ID.Hex()) {
				return nil, ErrFreshDecisionRequired
			}
		}
		return parsed, nil
	}
	return nil, ErrFreshDecisionRequired
}

func (s *LLMCanonicalSource) accepted(ctx context.Context, intent *controlplane.Intent) error {
	if s.Outbox == nil || s.DeliveryTarget == "" {
		return fmt.Errorf("bahia-4f10q.2.2: LLM acceptance proof store unavailable")
	}
	d := "intent-status:" + intent.Actor + ":" + intent.Coordinate
	for status := range s.Runtime.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30315},
		Authors: []nostr.PubKey{s.Runtime.author}, Tags: nostr.TagMap{"d": {d}, "status": {"accepted"}}}) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !status.CheckID() || !status.VerifySignature() || tagValue(status.Tags, "workflow") != "llm" ||
			tagValue(status.Tags, "e") != intent.Event.ID.Hex() || tagValue(status.Tags, "p") != intent.Actor ||
			tagValue(status.Tags, "intent_id") != intent.IntentID {
			continue
		}
		proof, ok, err := s.Outbox.GetDeliveryProof(status.ID)
		if err != nil {
			return err
		}
		if ok && proof.ValidFor(status, s.DeliveryTarget) {
			return nil
		}
	}
	return fmt.Errorf("%w: exact LLM intent lacks relay-ACKed acceptance", ErrFreshDecisionRequired)
}

func (s *LLMCanonicalSource) winner(ctx context.Context, kind int, coordinate string) (nostr.Event, error) {
	events, err := s.Runtime.records(ctx, kind, coordinate)
	if err != nil || len(events) == 0 {
		return nostr.Event{}, ErrFreshDecisionRequired
	}
	winner := events[0]
	for _, event := range events[1:] {
		if event.CreatedAt > winner.CreatedAt ||
			(event.CreatedAt == winner.CreatedAt && event.ID.Hex() < winner.ID.Hex()) {
			winner = event
		}
	}
	return winner, nil
}

func (s *LLMCanonicalSource) release(ctx context.Context, id uuid.UUID) (nostr.Event, domain.LLMRelease, error) {
	event, err := s.winner(ctx, kinds.LLMReleaseRegistry, "llm:release:"+id.String())
	if err != nil {
		return nostr.Event{}, domain.LLMRelease{}, err
	}
	if tagValue(event.Tags, "deleted") != "false" || s.Decryptor == nil ||
		tagValue(event.Tags, "legacy_kind") != fmt.Sprint(kinds.LLMReleaseRegistry) {
		return nostr.Event{}, domain.LLMRelease{}, ErrFreshDecisionRequired
	}
	proof, ok, err := s.Outbox.GetDeliveryProof(event.ID)
	if err != nil {
		return nostr.Event{}, domain.LLMRelease{}, err
	}
	if !ok || !proof.ValidFor(event, s.DeliveryTarget) {
		return nostr.Event{}, domain.LLMRelease{}, fmt.Errorf("%w: encrypted LLM release has no relay quorum receipt", ErrFreshDecisionRequired)
	}
	plain, err := s.Decryptor.DecryptConfidential(ctx, event.Content, kinds.LLMReleaseRegistry,
		"llm:release:"+id.String(), kinds.CPStateTopicLLMRelease)
	if err != nil {
		return nostr.Event{}, domain.LLMRelease{}, err
	}
	var release domain.LLMRelease
	if err := json.Unmarshal(plain, &release); err != nil || release.ID != id ||
		domain.ValidateLLMReleaseConfig(&release) != nil {
		return nostr.Event{}, domain.LLMRelease{}, ErrFreshDecisionRequired
	}
	return event, release, nil
}

func supportedLLMExternalRelease(release domain.LLMRelease) bool {
	if release.ExternalBackend == nil || release.RuntimeBackend != nil || len(release.Metadata) != 0 ||
		release.ExternalBackend.HealthURL != "" || len(release.ExternalBackend.HealthHeaders) != 0 ||
		len(release.ExternalBackend.HealthHeaderSecretRefs) != 0 {
		return false
	}
	if len(release.BackendPreferences) != 0 &&
		(len(release.BackendPreferences) != 1 || release.BackendPreferences[0] != domain.LLMBackendKindExternalAPI) {
		return false
	}
	endpoint, err := url.Parse(release.ExternalBackend.BaseURL)
	return err == nil && endpoint.Scheme == "https" && endpoint.Host != "" &&
		endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == ""
}

// Terminal checkpoints form a per-route/environment chain through the exact
// prior checkpoint bound into each effect snapshot. Wall-clock seconds cannot
// order two deployments that finished in the same second.
func llmTerminalHistory(outbox *localstore.Outbox, routeID, envID uuid.UUID) ([]autoApplyReceipt, error) {
	rows, err := outbox.ListControlRecords(llmRouteReceiptFamily)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]autoApplyReceipt)
	referenced := make(map[string]bool)
	for _, raw := range rows {
		var receipt autoApplyReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.Phase != "terminal" || receipt.Snapshot.LLM == nil ||
			receipt.Snapshot.LLM.Route.ID != routeID || receipt.Snapshot.Spec.EnvironmentID != envID ||
			!receipt.Checkpoint.CheckID() || !receipt.Checkpoint.VerifySignature() {
			continue
		}
		byID[receipt.Checkpoint.ID.Hex()] = receipt
		if prior := receipt.Snapshot.LLM.PreviousTerminalCheckpoint; prior != "" {
			referenced[prior] = true
		}
	}
	if len(byID) == 0 {
		return nil, nil
	}
	var head string
	for id := range byID {
		if !referenced[id] {
			if head != "" {
				return nil, ErrFreshDecisionRequired
			}
			head = id
		}
	}
	if head == "" {
		return nil, ErrFreshDecisionRequired
	}
	ordered := make([]autoApplyReceipt, 0, len(byID))
	for head != "" {
		receipt, ok := byID[head]
		if !ok || len(ordered) >= len(byID) {
			return nil, ErrFreshDecisionRequired
		}
		ordered = append(ordered, receipt)
		head = receipt.Snapshot.LLM.PreviousTerminalCheckpoint
	}
	if len(ordered) != len(byID) {
		return nil, ErrFreshDecisionRequired
	}
	return ordered, nil
}

func (s *LLMCanonicalSource) latestTerminal(routeID, envID uuid.UUID) (string, error) {
	rows, err := s.Outbox.ListControlRecords(llmRouteReceiptFamily)
	if err != nil {
		return "", err
	}
	for _, raw := range rows {
		var receipt autoApplyReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.Snapshot.LLM == nil ||
			receipt.Snapshot.LLM.Route.ID != routeID || receipt.Snapshot.Spec.EnvironmentID != envID ||
			receipt.Snapshot.IntentID == s.IntentID {
			continue
		}
		switch receipt.Phase {
		case "effect_started", "effect_observed", "recovery_required", "superseded":
			return "", ErrAutoApplyRecoveryRequired
		}
	}
	history, err := llmTerminalHistory(s.Outbox, routeID, envID)
	if err != nil || len(history) == 0 {
		return "", err
	}
	return history[0].Checkpoint.ID.Hex(), nil
}

func (s *LLMCanonicalSource) LoadAutoApplySnapshot(ctx context.Context, serviceID, envID uuid.UUID) (ApplySnapshot, error) {
	if s == nil || s.Runtime == nil || s.Outbox == nil || s.IntentID == uuid.Nil ||
		serviceID != s.RouteID || envID != s.EnvironmentID || s.GatewayRef == "" || s.GatewayAdminRevision == "" {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2.2: canonical LLM source unavailable")
	}
	request, err := s.signed(s.RequestEventID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if request.Op != "deploy" && request.Op != "rollback" {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	if request.IntentID != s.IntentID.String() ||
		fmt.Sprint(request.Content["route_id"]) != s.RouteID.String() ||
		fmt.Sprint(request.Content["environment_id"]) != envID.String() ||
		(request.Op == "deploy" && fmt.Sprint(request.Content["release_id"]) != s.ReleaseID.String()) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	decision := request
	if s.DecisionEventID != s.RequestEventID {
		decision, err = s.signed(s.DecisionEventID)
		if err != nil {
			return ApplySnapshot{}, err
		}
		if decision.Op != "approve" || fmt.Sprint(decision.Content["deployment_intent_id"]) != s.IntentID.String() {
			return ApplySnapshot{}, ErrFreshDecisionRequired
		}
	}
	if !s.SkipDecisionProof {
		if err := s.accepted(ctx, request); err != nil {
			return ApplySnapshot{}, err
		}
		if decision.Event.ID != request.Event.ID {
			if err := s.accepted(ctx, decision); err != nil {
				return ApplySnapshot{}, err
			}
		}
	}
	routeEvent, err := s.winner(ctx, kinds.LLMRouteRegistry, s.RouteID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	routeRecord, ok := decodeCanonical[domain.LLMRoute](routeEvent, s.RouteID.String())
	if !ok || routeRecord.Deleted || routeRecord.Value.ID != s.RouteID || routeRecord.Value.GatewayConfig == nil ||
		len(routeRecord.Value.Metadata) != 0 || len(routeRecord.Value.GatewayConfig.Headers) != 0 {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	envEvent, err := s.winner(ctx, kinds.EnvironmentRegistry, envID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	envRecord, ok := decodeCanonical[canonicalEnvironment](envEvent, envID.String())
	if !ok || envRecord.Deleted || envRecord.Value.ID != envID || envRecord.Value.OrgID != request.OrgID {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	releaseEvent, release, err := s.release(ctx, s.ReleaseID)
	if err != nil || release.RouteID != s.RouteID || !supportedLLMExternalRelease(release) {
		return ApplySnapshot{}, fmt.Errorf("%w: only credential-reference-only external API releases have a gateway effect fence", ErrFreshDecisionRequired)
	}
	pins, err := s.secretPins(ctx, envRecord.Value.Environment, routeRecord.Value)
	if err != nil {
		return ApplySnapshot{}, err
	}
	previous, err := s.latestTerminal(s.RouteID, envID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	hash := sha256.Sum256([]byte(routeEvent.ID.Hex() + releaseEvent.ID.Hex() + envEvent.ID.Hex() + s.GatewayRef + s.GatewayAdminRevision))
	return ApplySnapshot{IntentID: s.IntentID, DecisionEventID: decision.Event.ID.Hex(),
		DecisionCoordinate: decision.Coordinate, DecisionActor: decision.Actor,
		EnvironmentEventID: envEvent.ID.Hex(), RouteEventID: routeEvent.ID.Hex(),
		RouteUpdatedAt: routeRecord.Value.UpdatedAt, ReleaseEventID: releaseEvent.ID.Hex(),
		ReleaseUpdatedAt: release.CreatedAt, Environment: envRecord.Value.Environment,
		Spec: domain.DesiredServiceSpec{ServiceID: s.RouteID, EnvironmentID: envID,
			DesiredHash: fmt.Sprintf("sha256:%x", hash)}, SecretPins: pins,
		LLM: &LLMEffectInput{RequestEventID: request.Event.ID.Hex(), GatewayRef: s.GatewayRef,
			GatewayAdminRevision: s.GatewayAdminRevision,
			Route:                routeRecord.Value, Release: release, PreviousTerminalCheckpoint: previous}}, nil
}

func (s *LLMCanonicalSource) secretPins(ctx context.Context, env domain.Environment, route domain.LLMRoute) ([]ApplySecretPin, error) {
	var bindings []struct{ scope, name, id string }
	for name, id := range route.GatewayConfig.HeaderSecretRefs {
		bindings = append(bindings, struct{ scope, name, id string }{"gateway", name, id})
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].scope+":"+bindings[i].name < bindings[j].scope+":"+bindings[j].name
	})
	pins := make([]ApplySecretPin, 0, len(bindings))
	for _, item := range bindings {
		if s.Records == nil || s.Secrets == nil || strings.TrimSpace(item.name) == "" {
			return nil, fmt.Errorf("bahia-4f10q.2.2: Signet credential-reference source unavailable")
		}
		id, err := uuid.Parse(item.id)
		if err != nil {
			return nil, ErrFreshDecisionRequired
		}
		ref, err := s.Records.CurrentSecretRef(ctx, id)
		if err != nil || ref == nil || ref.CredentialRef == nil ||
			(ref.EnvironmentID != nil && *ref.EnvironmentID != env.ID) ||
			secretstore.ValidateRef(*ref.CredentialRef) != nil {
			return nil, ErrFreshDecisionRequired
		}
		orgID, err := s.Records.ServiceOrgID(ctx, ref.ServiceID)
		if err != nil || orgID != env.OrgID.String() {
			return nil, ErrFreshDecisionRequired
		}
		pins = append(pins, ApplySecretPin{SecretID: id, EnvVar: item.scope + ":" + item.name,
			CredentialID: ref.CredentialRef.CredentialID, Version: ref.CredentialRef.Version})
	}
	return pins, nil
}

func (s *LLMCanonicalSource) ResolvePinnedSecrets(ctx context.Context, snapshot ApplySnapshot) (map[string]string, func(), error) {
	return (&CanonicalAutoApplySource{Records: s.Records, Secrets: s.Secrets}).ResolvePinnedSecrets(ctx, snapshot)
}

type LLMGatewayEffect struct {
	Gateway llmadapter.GatewayRouteManager
	Source  *LLMCanonicalSource
}

func (e LLMGatewayEffect) spec(snapshot ApplySnapshot, secrets map[string]string) (llmadapter.GatewayRouteSpec, error) {
	if snapshot.LLM == nil || snapshot.LLM.Route.GatewayConfig == nil || snapshot.LLM.Release.ExternalBackend == nil {
		return llmadapter.GatewayRouteSpec{}, ErrFreshDecisionRequired
	}
	config := snapshot.LLM.Route.GatewayConfig
	headers := make(map[string]string, len(config.HeaderSecretRefs))
	for header := range config.HeaderSecretRefs {
		value, ok := secrets["gateway:"+header]
		if !ok {
			return llmadapter.GatewayRouteSpec{}, ErrFreshDecisionRequired
		}
		headers[header] = value
	}
	return llmadapter.GatewayRouteSpec{RouteName: snapshot.LLM.Route.ID.String(),
		PublicModel: config.PublicModel, Path: config.Path,
		TargetURL:      snapshot.LLM.Release.ExternalBackend.BaseURL,
		TimeoutSeconds: config.TimeoutSeconds, Headers: headers,
		Metadata: map[string]any{"release_id": snapshot.LLM.Release.ID.String()}}, nil
}

func (e LLMGatewayEffect) Observe(ctx context.Context, snapshot ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	if e.Gateway == nil || e.Source == nil || snapshot.LLM == nil {
		return false, nil, fmt.Errorf("bahia-4f10q.2.2: LLM gateway observer unavailable")
	}
	values, release, err := e.Source.ResolvePinnedSecrets(ctx, snapshot)
	if err != nil {
		return false, nil, err
	}
	defer release()
	spec, err := e.spec(snapshot, values)
	if err != nil {
		return false, nil, err
	}
	observed, err := e.Gateway.GetRoute(ctx, snapshot.LLM.GatewayRef, spec.RouteName)
	if err != nil || observed == nil {
		return false, nil, err
	}
	if observed.RouteName != spec.RouteName || observed.Status != domain.GatewayRouteStatusSynced ||
		observed.GatewayConfigHash != spec.ManagedConfigHash() {
		return false, nil, nil
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(spec.ManagedConfigHash()))
	return true, &domain.RuntimeObservation{ID: id, ObservedAt: time.Now().UTC()}, nil
}

func (e LLMGatewayEffect) Apply(ctx context.Context, snapshot ApplySnapshot, secrets map[string]string) error {
	if e.Gateway == nil || snapshot.LLM == nil {
		return fmt.Errorf("bahia-4f10q.2.2: LLM gateway effect unavailable")
	}
	spec, err := e.spec(snapshot, secrets)
	if err != nil {
		return err
	}
	_, err = e.Gateway.UpsertRoute(ctx, snapshot.LLM.GatewayRef, spec)
	return err
}

var _ AutoApplySnapshotSource = (*LLMCanonicalSource)(nil)
var _ AutoApplyEffect = LLMGatewayEffect{}
