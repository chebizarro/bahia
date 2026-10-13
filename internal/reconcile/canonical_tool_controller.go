package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const (
	toolDecisionFamily = "canonical-tool-decision-v1"
	toolPendingFamily  = "canonical-tool-admission-v1"
)

// ToolCanonicalController admits a signed decision with its exact effect
// inputs, then runs only after the normal intent status has a durable relay
// quorum proof. The pending row survives a crash between those two actions.
type ToolCanonicalController struct {
	Base             *CanonicalAutoApplySource
	Outbox           *localstore.Outbox
	DeliveryTarget   string
	AllowedAuthors   map[string]bool
	AllowedAuthor    func(string) bool
	ImageRepo        string
	InstallerVersion string
	Decryptor        interface {
		DecryptConfidential(context.Context, string, int, string, string) ([]byte, error)
	}
	Builder interface {
		CheckImageExists(context.Context, string) (string, bool, error)
		BuildImage(context.Context, build.BuildRequest) (*build.BuildResult, error)
	}
	DeployEffect AutoApplyEffect
	Lock         service.EnvironmentApplyTryLocker
	Signer       nostr.Signer
	Enqueue      func(context.Context, nostr.Event) error
	Ready        <-chan struct{}
	Logger       *zap.Logger
	wakeOnce     sync.Once
	wake         chan struct{}
}

type toolPending struct {
	RequestEventID  string        `json:"request_event_id"`
	ApprovalEventID string        `json:"approval_event_id"`
	Action          string        `json:"action"`
	Decision        ApplySnapshot `json:"decision"`
	Build           ApplySnapshot `json:"build,omitempty"`
	Deploy          ApplySnapshot `json:"deploy,omitempty"`
}

func (c *ToolCanonicalController) Name() string { return "canonical-tool-recovery" }

func (c *ToolCanonicalController) Run(ctx context.Context) error {
	c.ensureWake()
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

func (c *ToolCanonicalController) reconcileAndLog(ctx context.Context) {
	if err := c.ReconcilePending(ctx); err != nil {
		logger := c.Logger
		if logger == nil {
			logger = zap.NewNop()
		}
		logger.Error("canonical tool recovery requires attention", zap.Error(err))
	}
}

func (c *ToolCanonicalController) AfterAccepted(_ context.Context, intent *controlplane.Intent) {
	if intent == nil || intent.Domain != "tool" || intent.Op != "approval-response" {
		return
	}
	c.ensureWake()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *ToolCanonicalController) ensureWake() {
	c.wakeOnce.Do(func() { c.wake = make(chan struct{}, 1) })
}

func (c *ToolCanonicalController) source(requestID, approvalID nostr.ID, step string, admission bool) *ToolCanonicalSource {
	return &ToolCanonicalSource{Base: c.Base, Outbox: c.Outbox, DeliveryTarget: c.DeliveryTarget,
		RequestEventID: requestID, ApprovalEventID: approvalID, Step: step,
		SkipApprovalProof: admission, ImageRepo: c.ImageRepo, InstallerVersion: c.InstallerVersion,
		AllowedAuthors: c.AllowedAuthors, AllowedAuthor: c.AllowedAuthor, Decryptor: c.Decryptor}
}

func (c *ToolCanonicalController) allowed(author string) bool {
	if c.AllowedAuthor != nil {
		return c.AllowedAuthor(author)
	}
	return c.AllowedAuthors[author]
}

func (c *ToolCanonicalController) AdmitRequest(_ context.Context, intent *controlplane.Intent) error {
	if c == nil || c.Base == nil || c.Outbox == nil || intent == nil || intent.Event == nil ||
		!c.allowed(intent.Actor) {
		return fmt.Errorf("bahia-4f10q.2.1: canonical tool request admission unavailable")
	}
	if err := controlplane.ValidateToolRequestSignedEnvelope(intent); err != nil {
		return err
	}
	_, err := c.source(intent.Event.ID, nostr.ZeroID, "decision", true).signed(intent.Event.ID)
	return err
}

func (c *ToolCanonicalController) Decide(ctx context.Context, intent *controlplane.Intent) error {
	if c == nil || c.Base == nil || c.Outbox == nil || c.Lock == nil || c.Signer == nil || c.Enqueue == nil ||
		intent == nil || intent.Event == nil || !c.allowed(intent.Actor) {
		return fmt.Errorf("bahia-4f10q.2.1: canonical tool decision admission unavailable")
	}
	if err := controlplane.ValidateToolApprovalSignedEnvelope(intent); err != nil {
		return err
	}
	var decision toolApprovalWire
	if err := json.Unmarshal([]byte(intent.Event.Content), &decision); err != nil {
		return err
	}
	requestID, err := nostr.IDFromHex(decision.RequestEventID)
	if err != nil || requestID == nostr.ZeroID {
		return fmt.Errorf("bahia-4f10q.2.1: approval must cite the exact signed request event")
	}
	source := c.source(requestID, intent.Event.ID, "decision", true)
	request, err := source.signed(requestID)
	if err != nil {
		return err
	}
	if err := controlplane.ValidateToolRequestSignedEnvelope(request); err != nil || !c.allowed(request.Actor) {
		return fmt.Errorf("%w: request is not authorized: %v", ErrFreshDecisionRequired, err)
	}
	if err := source.acceptedIntent(ctx, request); err != nil {
		return err
	}
	var body toolRequestWire
	if err := json.Unmarshal([]byte(request.Event.Content), &body); err != nil {
		return err
	}
	if decision.IntentID != body.ID || decision.RequestEventID != requestID.Hex() ||
		intent.Coordinate != "tool-approval:"+body.ID.String() {
		return ErrFreshDecisionRequired
	}
	bound, err := source.LoadAutoApplySnapshot(ctx, body.ServiceID, body.EnvironmentID)
	if err != nil {
		return err
	}
	stage := toolPending{RequestEventID: requestID.Hex(), ApprovalEventID: intent.Event.ID.Hex(),
		Action: decision.Action, Decision: bound}
	if decision.Action == "approve" {
		stage.Build, err = c.source(requestID, intent.Event.ID, "build", true).LoadAutoApplySnapshot(ctx, body.ServiceID, body.EnvironmentID)
		if err != nil {
			return err
		}
		stage.Deploy, err = c.source(requestID, intent.Event.ID, "deploy", true).LoadAutoApplySnapshot(ctx, body.ServiceID, body.EnvironmentID)
		if err != nil {
			return err
		}
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		return err
	}
	_, err = c.Outbox.UpdateControlRecord(toolPendingFamily, body.ID.String(), func(previous []byte) ([]byte, error) {
		if previous != nil && !bytes.Equal(previous, raw) {
			return nil, fmt.Errorf("%w: a different tool decision is already admitted", ErrFreshDecisionRequired)
		}
		return raw, nil
	})
	return err
}

func (c *ToolCanonicalController) ReconcilePending(ctx context.Context) error {
	if c == nil || c.Outbox == nil {
		return fmt.Errorf("bahia-4f10q.2.1: tool recovery store unavailable")
	}
	rows, err := c.Outbox.ListControlRecords(toolPendingFamily)
	if err != nil {
		return err
	}
	var failures []error
	for _, raw := range rows {
		var stage toolPending
		if err := json.Unmarshal(raw, &stage); err != nil || stage.Decision.IntentID == uuid.Nil {
			failures = append(failures, fmt.Errorf("corrupt canonical tool admission"))
			continue
		}
		if err := c.reconcileOne(ctx, stage); err != nil {
			failures = append(failures, fmt.Errorf("tool %s: %w", stage.Decision.IntentID, err))
		}
	}
	return errors.Join(failures...)
}

func (c *ToolCanonicalController) reconcileOne(ctx context.Context, stage toolPending) error {
	// A completed deployment is final. Later canonical revisions require a
	// fresh request, but they cannot turn replay into a second effect or
	// rewrite the already checkpointed terminal result.
	if stage.Action == "approve" {
		terminal := c.executor(nil, nil, toolDeployReceiptFamily, "tool-deploy")
		if receipt, err := terminal.readReceipt(stage.Decision.IntentID); err != nil {
			return err
		} else if receipt != nil && receipt.Phase == "terminal" {
			return terminal.deliverCheckpoint(ctx, receipt.Checkpoint)
		}
	}
	requestID, err := nostr.IDFromHex(stage.RequestEventID)
	if err != nil {
		return err
	}
	approvalID, err := nostr.IDFromHex(stage.ApprovalEventID)
	if err != nil {
		return err
	}
	source := c.source(requestID, approvalID, "decision", false)
	approval, err := source.signed(approvalID)
	if err != nil {
		return c.refuseDecision(ctx, stage.Decision, err)
	}
	// No effect or final denial is published until the approval's ordinary
	// service-signed 30315 has its own durable relay quorum proof.
	if err := source.acceptedIntent(ctx, approval); err != nil {
		return nil
	}
	current, err := source.LoadAutoApplySnapshot(ctx, stage.Decision.Spec.ServiceID, stage.Decision.Spec.EnvironmentID)
	if err != nil || !sameSnapshot(current, stage.Decision) {
		return c.refuseDecision(ctx, stage.Decision, ErrFreshDecisionRequired)
	}
	if stage.Action == "reject" {
		return c.refuseDecision(ctx, stage.Decision, ErrFreshDecisionRequired)
	}
	buildSource := pinnedToolSource{source: c.source(requestID, approvalID, "build", false), bound: stage.Build}
	build := c.executor(buildSource, ToolBuildEffect{Builder: c.Builder}, toolBuildReceiptFamily, "tool-build")
	if _, err := build.AutoRemediateDesiredState(ctx, stage.Build.Spec.ServiceID, stage.Build.Spec.EnvironmentID, nil); err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return c.refuseDecision(ctx, stage.Decision, err)
		}
		return err
	}
	deploySource := pinnedToolSource{source: c.source(requestID, approvalID, "deploy", false), bound: stage.Deploy}
	deploy := c.executor(deploySource, c.DeployEffect, toolDeployReceiptFamily, "tool-deploy")
	_, err = deploy.AutoRemediateDesiredState(ctx, stage.Deploy.Spec.ServiceID, stage.Deploy.Spec.EnvironmentID, nil)
	if errors.Is(err, ErrFreshDecisionRequired) {
		return c.refuseDecision(ctx, stage.Decision, err)
	}
	return err
}

func sameSnapshot(left, right ApplySnapshot) bool {
	a, aErr := left.digest()
	b, bErr := right.digest()
	return aErr == nil && bErr == nil && a == b
}

type pinnedToolSource struct {
	source *ToolCanonicalSource
	bound  ApplySnapshot
}

func (s pinnedToolSource) LoadAutoApplySnapshot(ctx context.Context, serviceID, envID uuid.UUID) (ApplySnapshot, error) {
	current, err := s.source.LoadAutoApplySnapshot(ctx, serviceID, envID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if !sameSnapshot(current, s.bound) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	return current, nil
}

func (s pinnedToolSource) ResolvePinnedSecrets(ctx context.Context, snapshot ApplySnapshot) (map[string]string, func(), error) {
	return s.source.ResolvePinnedSecrets(ctx, snapshot)
}

func (c *ToolCanonicalController) executor(source AutoApplySnapshotSource, effect AutoApplyEffect, family, scope string) *CanonicalAutoApplyExecutor {
	return &CanonicalAutoApplyExecutor{Source: source, Effect: effect, Lock: c.Lock,
		Outbox: c.Outbox, Signer: c.Signer, Enqueue: c.Enqueue, Family: family, StatusScope: scope}
}

func (c *ToolCanonicalController) refuseDecision(ctx context.Context, snapshot ApplySnapshot, cause error) error {
	e := c.executor(nil, nil, toolDecisionFamily, "tool-decision")
	digest, err := snapshot.digest()
	if err != nil {
		return err
	}
	prior, err := e.readReceipt(snapshot.IntentID)
	if err != nil {
		return err
	}
	if prior != nil {
		if prior.Digest != digest || prior.Phase != "refused" {
			return fmt.Errorf("tool decision receipt conflicts with refusal")
		}
		return e.deliverCheckpoint(ctx, prior.Checkpoint)
	}
	receipt, err := e.checkpoint(ctx, snapshot, digest, "", "refused", "")
	if err != nil {
		return err
	}
	if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
		return err
	}
	_ = cause // The signed checkpoint binds the decision and refusal phase.
	return nil
}

var _ controlplane.ToolCanonicalExecutor = (*ToolCanonicalController)(nil)
