package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const (
	llmReleaseAdmissionFamily = "canonical-llm-release-admission-v1"
	llmPendingFamily          = "canonical-llm-deployment-admission-v1"
)

type llmReleaseAdmission struct {
	RequestEventID string      `json:"request_event_id"`
	RouteEventID   string      `json:"route_event_id"`
	ReleaseEvent   nostr.Event `json:"release_event"`
}

type llmPending struct {
	RequestEventID  string        `json:"request_event_id"`
	DecisionEventID string        `json:"decision_event_id,omitempty"`
	DecisionAction  string        `json:"decision_action,omitempty"`
	Protected       bool          `json:"protected"`
	Snapshot        ApplySnapshot `json:"snapshot"`
}

// LLMCanonicalController is intentionally independent of PostgreSQL. Release
// publication is a synchronous, encrypted, quorum-ACKed admission; route
// effects use the shared local receipt and filesystem fence.
type LLMCanonicalController struct {
	Runtime        *CanonicalRuntimeSource
	Outbox         *localstore.Outbox
	DeliveryTarget string
	Signer         nostr.Signer
	Encryptor      interface {
		EncryptConfidential(context.Context, string, []byte, int, string, string, []byte) (string, error)
		DecryptConfidential(context.Context, string, int, string, string) ([]byte, error)
	}
	PublishRelease       func(context.Context, nostr.Event) error
	Enqueue              func(context.Context, nostr.Event) error
	AllowedAuthor        func(string) bool
	Records              secretstore.Records
	Secrets              *secretstore.Resolver
	Gateway              llmadapter.GatewayRouteManager
	GatewayRef           string
	GatewayAdminRevision string
	Lock                 service.EnvironmentApplyTryLocker
	Ready                <-chan struct{}
	Logger               *zap.Logger
	wakeOnce             sync.Once
	wake                 chan struct{}
}

func (c *LLMCanonicalController) Name() string { return "canonical-llm-recovery" }

func (c *LLMCanonicalController) Run(ctx context.Context) error {
	c.wakeOnce.Do(func() { c.wake = make(chan struct{}, 1) })
	if c.Ready != nil {
		select {
		case <-ctx.Done():
			return nil
		case <-c.Ready:
		}
	}
	c.reconcileAndLog(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.wake:
			c.reconcileAndLog(ctx)
		}
	}
}

func (c *LLMCanonicalController) reconcileAndLog(ctx context.Context) {
	if err := c.ReconcilePending(ctx); err != nil {
		logger := c.Logger
		if logger == nil {
			logger = zap.NewNop()
		}
		logger.Error("canonical LLM recovery requires attention", zap.Error(err))
	}
}

func (c *LLMCanonicalController) AfterAccepted(_ context.Context, intent *controlplane.Intent) {
	if intent == nil || intent.Domain != "llm" || intent.Op == "release-register" {
		return
	}
	c.wakeOnce.Do(func() { c.wake = make(chan struct{}, 1) })
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *LLMCanonicalController) source(requestID, decisionID nostr.ID, intentID, routeID, releaseID, envID uuid.UUID, skipProof bool) *LLMCanonicalSource {
	return &LLMCanonicalSource{Runtime: c.Runtime, Outbox: c.Outbox, DeliveryTarget: c.DeliveryTarget,
		RequestEventID: requestID, DecisionEventID: decisionID, IntentID: intentID,
		RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID,
		GatewayRef: c.GatewayRef, GatewayAdminRevision: c.GatewayAdminRevision,
		SkipDecisionProof: skipProof, AllowedAuthor: c.AllowedAuthor,
		Records: c.Records, Secrets: c.Secrets, Decryptor: c.Encryptor}
}

func (c *LLMCanonicalController) HandleLLMIntent(ctx context.Context, intent *controlplane.Intent) error {
	if c == nil || c.Runtime == nil || c.Outbox == nil || c.Signer == nil || c.AllowedAuthor == nil ||
		intent == nil || intent.Event == nil || !c.AllowedAuthor(intent.Actor) {
		return fmt.Errorf("bahia-4f10q.2.2: canonical LLM lifecycle unavailable")
	}
	if err := controlplane.ValidateLLMLifecycleSignedRequest(intent); err != nil {
		return err
	}
	if _, err := c.source(intent.Event.ID, intent.Event.ID, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, true).signed(intent.Event.ID); err != nil {
		return err
	}
	switch intent.Op {
	case "release-register":
		return c.registerRelease(ctx, intent)
	case "deploy", "rollback":
		return c.stageRequest(ctx, intent)
	case "approve", "reject":
		return c.stageDecision(ctx, intent)
	default:
		return fmt.Errorf("bahia-4f10q.2.2: unsupported LLM lifecycle operation %q", intent.Op)
	}
}

func (c *LLMCanonicalController) registerRelease(ctx context.Context, intent *controlplane.Intent) error {
	if c.Encryptor == nil || c.PublishRelease == nil || c.DeliveryTarget == "" {
		return fmt.Errorf("bahia-4f10q.2.2: encrypted release publisher unavailable")
	}
	var release domain.LLMRelease
	if err := json.Unmarshal([]byte(intent.Event.Content), &release); err != nil {
		return err
	}
	release.CreatedAt = time.Unix(int64(intent.Event.CreatedAt), 0).UTC()
	if release.ID == uuid.Nil || domain.ValidateLLMReleaseConfig(&release) != nil || !supportedLLMExternalRelease(release) {
		return fmt.Errorf("%w: bahia-4f10q.2.2 only registers credential-reference-only external API releases", ErrFreshDecisionRequired)
	}
	id := release.ID.String()
	previous, err := c.Outbox.GetControlRecord(llmReleaseAdmissionFamily, id)
	if err != nil {
		return err
	}
	var admission llmReleaseAdmission
	if previous != nil {
		if json.Unmarshal(previous, &admission) != nil || admission.RequestEventID != intent.Event.ID.Hex() ||
			!admission.ReleaseEvent.CheckID() || !admission.ReleaseEvent.VerifySignature() {
			return fmt.Errorf("%w: LLM release registration conflicts with retained admission", ErrFreshDecisionRequired)
		}
		if proof, ok, err := c.Outbox.GetDeliveryProof(admission.ReleaseEvent.ID); err != nil {
			return err
		} else if ok && proof.ValidFor(admission.ReleaseEvent, c.DeliveryTarget) {
			return nil
		}
	}
	source := c.source(intent.Event.ID, intent.Event.ID, uuid.Nil, release.RouteID, release.ID, uuid.Nil, true)
	routeEvent, err := source.winner(ctx, kinds.LLMRouteRegistry, release.RouteID.String())
	if err != nil {
		return err
	}
	routeRecord, ok := decodeCanonical[domain.LLMRoute](routeEvent, release.RouteID.String())
	if !ok || routeRecord.Deleted || routeRecord.Value.ID != release.RouteID {
		return ErrFreshDecisionRequired
	}
	if previous != nil {
		if admission.RouteEventID != routeEvent.ID.Hex() {
			return fmt.Errorf("%w: LLM release registration conflicts with retained admission", ErrFreshDecisionRequired)
		}
	} else {
		coordinate := "llm:release:" + id
		plain, err := json.Marshal(release)
		if err != nil {
			return err
		}
		sealed, err := c.Encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, plain,
			kinds.LLMReleaseRegistry, coordinate, kinds.CPStateTopicLLMRelease, nil)
		if err != nil {
			return err
		}
		kind, tags := nostradapter.ControlStateEnvelope(kinds.LLMReleaseRegistry, coordinate, false)
		event := nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(),
			Tags:    append(tags, nostr.Tag{"route_id", release.RouteID.String()}, nostr.Tag{"e", intent.Event.ID.Hex()}),
			Content: sealed}
		if err := c.Signer.SignEvent(ctx, &event); err != nil {
			return err
		}
		admission = llmReleaseAdmission{RequestEventID: intent.Event.ID.Hex(), RouteEventID: routeEvent.ID.Hex(), ReleaseEvent: event}
		raw, err := json.Marshal(admission)
		if err != nil {
			return err
		}
		_, err = c.Outbox.UpdateControlRecord(llmReleaseAdmissionFamily, id, func(prior []byte) ([]byte, error) {
			if prior != nil {
				return nil, fmt.Errorf("LLM release admission raced another request")
			}
			return raw, nil
		})
		if err != nil {
			return err
		}
	}
	if proof, ok, err := c.Outbox.GetDeliveryProof(admission.ReleaseEvent.ID); err != nil {
		return err
	} else if ok {
		if proof.ValidFor(admission.ReleaseEvent, c.DeliveryTarget) {
			return nil
		}
		return fmt.Errorf("LLM release receipt has invalid relay proof")
	}
	if err := c.PublishRelease(ctx, admission.ReleaseEvent); err != nil {
		return err
	}
	proof, ok, err := c.Outbox.GetDeliveryProof(admission.ReleaseEvent.ID)
	if err != nil || !ok || !proof.ValidFor(admission.ReleaseEvent, c.DeliveryTarget) {
		return fmt.Errorf("bahia-4f10q.2.2: encrypted release publication lacks durable relay quorum receipt: %w", err)
	}
	return nil
}

func (c *LLMCanonicalController) stageRequest(ctx context.Context, intent *controlplane.Intent) error {
	intentID, err := uuid.Parse(intent.IntentID)
	if err != nil {
		return err
	}
	if raw, err := c.Outbox.GetControlRecord(llmPendingFamily, intentID.String()); err != nil {
		return err
	} else if raw != nil {
		var admitted llmPending
		if json.Unmarshal(raw, &admitted) == nil && admitted.RequestEventID == intent.Event.ID.Hex() &&
			admitted.Snapshot.IntentID == intentID {
			return nil
		}
		return ErrFreshDecisionRequired
	}
	routeID, err := uuid.Parse(fmt.Sprint(intent.Content["route_id"]))
	if err != nil {
		return err
	}
	envID, err := uuid.Parse(fmt.Sprint(intent.Content["environment_id"]))
	if err != nil {
		return err
	}
	var releaseID uuid.UUID
	if intent.Op == "deploy" {
		releaseID, err = uuid.Parse(fmt.Sprint(intent.Content["release_id"]))
	} else {
		releaseID, err = c.rollbackRelease(routeID, envID)
	}
	if err != nil {
		return err
	}
	source := c.source(intent.Event.ID, intent.Event.ID, intentID, routeID, releaseID, envID, true)
	snapshot, err := source.LoadAutoApplySnapshot(ctx, routeID, envID)
	if err != nil {
		return err
	}
	stage := llmPending{RequestEventID: intent.Event.ID.Hex(), Protected: snapshot.Environment.Protected, Snapshot: snapshot}
	raw, err := json.Marshal(stage)
	if err != nil {
		return err
	}
	_, err = c.Outbox.UpdateControlRecord(llmPendingFamily, intentID.String(), func(previous []byte) ([]byte, error) {
		if previous != nil && !bytes.Equal(previous, raw) {
			return nil, fmt.Errorf("%w: LLM deployment request ID conflicts with retained admission", ErrFreshDecisionRequired)
		}
		return raw, nil
	})
	return err
}

func (c *LLMCanonicalController) rollbackRelease(routeID, envID uuid.UUID) (uuid.UUID, error) {
	history, err := llmTerminalHistory(c.Outbox, routeID, envID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(history) < 2 {
		return uuid.Nil, fmt.Errorf("%w: rollback has no prior checkpointed release", ErrFreshDecisionRequired)
	}
	current := history[0].Snapshot.LLM.Release.ID
	for _, receipt := range history[1:] {
		if receipt.Snapshot.LLM.Release.ID != current {
			return receipt.Snapshot.LLM.Release.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%w: rollback has no different prior release", ErrFreshDecisionRequired)
}

func (c *LLMCanonicalController) stageDecision(ctx context.Context, intent *controlplane.Intent) error {
	id, err := uuid.Parse(fmt.Sprint(intent.Content["deployment_intent_id"]))
	if err != nil {
		return err
	}
	previous, err := c.Outbox.GetControlRecord(llmPendingFamily, id.String())
	if err != nil || previous == nil {
		return fmt.Errorf("%w: LLM deployment request is absent", ErrFreshDecisionRequired)
	}
	var stage llmPending
	if err := json.Unmarshal(previous, &stage); err != nil || !stage.Protected || stage.RequestEventID == "" {
		return ErrFreshDecisionRequired
	}
	if stage.DecisionEventID != "" && stage.DecisionEventID != intent.Event.ID.Hex() {
		return ErrFreshDecisionRequired
	}
	if stage.DecisionEventID == intent.Event.ID.Hex() {
		if stage.DecisionAction == intent.Op {
			return nil
		}
		return ErrFreshDecisionRequired
	}
	requestID, err := nostr.IDFromHex(stage.RequestEventID)
	if err != nil {
		return err
	}
	requestSource := c.source(requestID, requestID, id, stage.Snapshot.Spec.ServiceID,
		stage.Snapshot.LLM.Release.ID, stage.Snapshot.Spec.EnvironmentID, true)
	request, err := requestSource.signed(requestID)
	if err != nil {
		return err
	}
	if err := requestSource.accepted(ctx, request); err != nil {
		return err
	}
	if intent.Op == "approve" {
		source := c.source(requestID, intent.Event.ID, id, stage.Snapshot.Spec.ServiceID,
			stage.Snapshot.LLM.Release.ID, stage.Snapshot.Spec.EnvironmentID, true)
		current, err := source.LoadAutoApplySnapshot(ctx, stage.Snapshot.Spec.ServiceID, stage.Snapshot.Spec.EnvironmentID)
		if err != nil {
			return err
		}
		if stage.Snapshot.LLM.PreviousTerminalCheckpoint != current.LLM.PreviousTerminalCheckpoint {
			return ErrFreshDecisionRequired
		}
		stage.Snapshot = current
	} else {
		stage.Snapshot.DecisionEventID = intent.Event.ID.Hex()
		stage.Snapshot.DecisionCoordinate = intent.Coordinate
		stage.Snapshot.DecisionActor = intent.Actor
	}
	stage.DecisionEventID, stage.DecisionAction = intent.Event.ID.Hex(), intent.Op
	raw, err := json.Marshal(stage)
	if err != nil {
		return err
	}
	_, err = c.Outbox.UpdateControlRecord(llmPendingFamily, id.String(), func(actual []byte) ([]byte, error) {
		if !bytes.Equal(actual, previous) {
			return nil, fmt.Errorf("LLM approval raced another decision")
		}
		return raw, nil
	})
	return err
}

func (c *LLMCanonicalController) ReconcilePending(ctx context.Context) error {
	if c == nil || c.Outbox == nil {
		return fmt.Errorf("bahia-4f10q.2.2: LLM recovery store unavailable")
	}
	rows, err := c.Outbox.ListControlRecords(llmPendingFamily)
	if err != nil {
		return err
	}
	var failures []error
	for _, raw := range rows {
		var stage llmPending
		if json.Unmarshal(raw, &stage) != nil || stage.Snapshot.IntentID == uuid.Nil || stage.Snapshot.LLM == nil {
			failures = append(failures, fmt.Errorf("corrupt canonical LLM admission"))
			continue
		}
		if err := c.reconcileOne(ctx, stage); err != nil {
			failures = append(failures, fmt.Errorf("LLM deployment %s: %w", stage.Snapshot.IntentID, err))
		}
	}
	return errors.Join(failures...)
}

func (c *LLMCanonicalController) executor(source AutoApplySnapshotSource, effect AutoApplyEffect) *CanonicalAutoApplyExecutor {
	return &CanonicalAutoApplyExecutor{Source: source, Effect: effect, Lock: c.Lock, Outbox: c.Outbox,
		Signer: c.Signer, Enqueue: c.Enqueue, Family: llmRouteReceiptFamily, StatusScope: "llm-route"}
}

func (c *LLMCanonicalController) reconcileOne(ctx context.Context, stage llmPending) error {
	e := c.executor(nil, nil)
	if receipt, err := e.readReceipt(stage.Snapshot.IntentID); err != nil {
		return err
	} else if receipt != nil && (receipt.Phase == "terminal" || receipt.Phase == "refused" ||
		receipt.Phase == "superseded" || receipt.Phase == "recovery_required") {
		return e.deliverCheckpoint(ctx, receipt.Checkpoint)
	}
	requestID, err := nostr.IDFromHex(stage.RequestEventID)
	if err != nil {
		return err
	}
	decisionID := requestID
	if stage.Protected {
		if stage.DecisionEventID == "" {
			return nil
		}
		decisionID, err = nostr.IDFromHex(stage.DecisionEventID)
		if err != nil {
			return err
		}
	}
	source := c.source(requestID, decisionID, stage.Snapshot.IntentID, stage.Snapshot.Spec.ServiceID,
		stage.Snapshot.LLM.Release.ID, stage.Snapshot.Spec.EnvironmentID, false)
	request, err := source.signed(requestID)
	if err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return c.refuse(ctx, stage.Snapshot)
		}
		return err
	}
	if err := source.accepted(ctx, request); err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return nil
		}
		return err
	}
	if stage.Protected {
		decision, err := source.signed(decisionID)
		if err != nil {
			if errors.Is(err, ErrFreshDecisionRequired) {
				return c.refuse(ctx, stage.Snapshot)
			}
			return err
		}
		if err := source.accepted(ctx, decision); err != nil {
			if errors.Is(err, ErrFreshDecisionRequired) {
				return nil
			}
			return err
		}
		if stage.DecisionAction == "reject" {
			return c.refuse(ctx, stage.Snapshot)
		}
	}
	current, err := source.LoadAutoApplySnapshot(ctx, stage.Snapshot.Spec.ServiceID, stage.Snapshot.Spec.EnvironmentID)
	if errors.Is(err, ErrFreshDecisionRequired) || (err == nil && !sameSnapshot(current, stage.Snapshot)) {
		return c.refuse(ctx, stage.Snapshot)
	}
	if err != nil {
		return err
	}
	pinned := pinnedLLMSource{source: source, bound: stage.Snapshot}
	e = c.executor(pinned, LLMGatewayEffect{Gateway: c.Gateway, Source: source})
	_, err = e.AutoRemediateDesiredState(ctx, stage.Snapshot.Spec.ServiceID, stage.Snapshot.Spec.EnvironmentID, nil)
	if errors.Is(err, ErrFreshDecisionRequired) {
		return c.refuse(ctx, stage.Snapshot)
	}
	return err
}

type pinnedLLMSource struct {
	source *LLMCanonicalSource
	bound  ApplySnapshot
}

func (s pinnedLLMSource) LoadAutoApplySnapshot(ctx context.Context, routeID, envID uuid.UUID) (ApplySnapshot, error) {
	current, err := s.source.LoadAutoApplySnapshot(ctx, routeID, envID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if !sameSnapshot(current, s.bound) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	return current, nil
}
func (s pinnedLLMSource) ResolvePinnedSecrets(ctx context.Context, snapshot ApplySnapshot) (map[string]string, func(), error) {
	return s.source.ResolvePinnedSecrets(ctx, snapshot)
}

func (c *LLMCanonicalController) refuse(ctx context.Context, snapshot ApplySnapshot) error {
	e := c.executor(nil, nil)
	digest, err := snapshot.digest()
	if err != nil {
		return err
	}
	if prior, err := e.readReceipt(snapshot.IntentID); err != nil {
		return err
	} else if prior != nil {
		if prior.Digest != digest {
			return ErrFreshDecisionRequired
		}
		if prior.Phase == "refused" {
			return e.deliverCheckpoint(ctx, prior.Checkpoint)
		}
		return e.refuse(ctx, prior, ErrFreshDecisionRequired)
	}
	receipt, err := e.checkpoint(ctx, snapshot, digest, "", "refused", "")
	if err != nil {
		return err
	}
	return e.deliverCheckpoint(ctx, receipt.Checkpoint)
}

var _ controlplane.LLMCanonicalLifecycle = (*LLMCanonicalController)(nil)
