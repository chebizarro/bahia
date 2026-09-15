package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

// OrderedStep is one step of the governed provisioning order. The order is
// authoritative and mirrors saga.forwardStages: Bahia deployment always
// precedes Soul Factory activation, and the active kind-31951 Soul is the
// last, gated step.
type OrderedStep string

const (
	// StepReserveIdentity reserves the agent identity and spec only. It must
	// never generate, rotate, or persist agent key material.
	StepReserveIdentity OrderedStep = "reserve_identity"
	// StepRegisterServiceUnit registers the Bahia service and its deployment
	// unit so a later deployment has an owned runtime boundary.
	StepRegisterServiceUnit OrderedStep = "register_service_unit"
	// StepSelectRuntimeRelease selects and binds a verified runtime release
	// (#4 AgentRuntimeReleaseService) before any deployment intent exists.
	StepSelectRuntimeRelease OrderedStep = "select_runtime_release"
	// StepDeployViaBahia creates the release-backed deployment intent and
	// drives the deployment through Bahia.
	StepDeployViaBahia OrderedStep = "deploy_via_bahia"
	// StepVerifyIdentity configures and verifies the agent identity.
	StepVerifyIdentity OrderedStep = "configure_verify_identity"
	// StepVerifyRelay configures and verifies the relay path.
	StepVerifyRelay OrderedStep = "configure_verify_relay"
	// StepVerifyModel verifies the model/inference path.
	StepVerifyModel OrderedStep = "verify_model"
	// StepVerifyReadiness verifies runtime readiness.
	StepVerifyReadiness OrderedStep = "verify_readiness"
	// StepPublishActiveSoul publishes the active kind-31951 Soul. It is only
	// reachable after every earlier step has been durably checkpointed.
	StepPublishActiveSoul OrderedStep = "publish_active_soul"
)

// stepBinding maps one ordered step to its durable saga stage and lineage.
type stepBinding struct {
	Step   OrderedStep
	Stage  saga.Stage
	System string
	Kind   string
	Rank   saga.CompensationRank
}

// governedOrder is the canonical, ordered binding of steps to saga stages.
var governedOrder = []stepBinding{
	{StepReserveIdentity, saga.StageIdentityReserved, "signet", "identity_reservation", saga.CompensateSignetPolicy},
	{StepRegisterServiceUnit, saga.StageServiceRegistered, "bahia_registry", "service_unit_registration", saga.CompensateServiceRegistration},
	{StepSelectRuntimeRelease, saga.StageReleaseSelected, "bahia_registry", "runtime_release_selection", saga.CompensateReleaseSelection},
	{StepDeployViaBahia, saga.StageRuntimeAllocated, "bahia_runtime", "bahia_deployment", saga.CompensateContainer},
	{StepVerifyIdentity, saga.StageSignerEnrolled, "signet", "identity_verification", saga.CompensateCredentials},
	{StepVerifyRelay, saga.StageNostrConfigured, "relay", "relay_verification", saga.CompensateCredentials},
	{StepVerifyModel, saga.StageLLMVerified, "model", "model_verification", saga.CompensateCredentials},
	{StepVerifyReadiness, saga.StageDMVerified, "readiness", "readiness_verification", saga.CompensateCredentials},
}

// OrderedSteps returns the exact governed provisioning order, ending with the
// gated active Soul publication.
func OrderedSteps() []OrderedStep {
	steps := make([]OrderedStep, 0, len(governedOrder)+1)
	for _, binding := range governedOrder {
		steps = append(steps, binding.Step)
	}
	return append(steps, StepPublishActiveSoul)
}

// GovernedProvisioningRequest is the immutable root identity of one governed
// provisioning run plus the runtime target the saga must select and deploy.
type GovernedProvisioningRequest struct {
	RequestID string
	RunID     string
	AgentID   string
	SpecHash  string
	Runtime   domain.RuntimeTarget
}

func (r GovernedProvisioningRequest) validate() error {
	if strings.TrimSpace(r.RequestID) == "" || strings.TrimSpace(r.RunID) == "" || strings.TrimSpace(r.AgentID) == "" || strings.TrimSpace(r.SpecHash) == "" {
		return errors.New("governed provisioning request requires request, run, agent, and spec identity")
	}
	switch r.Runtime {
	case domain.RuntimeTargetOpenClaw, domain.RuntimeTargetMetiq:
		return nil
	default:
		return fmt.Errorf("governed provisioning requires a supported runtime target, got %q", r.Runtime)
	}
}

// ProvisioningSpec is the secret-free identity presented to provisioning ports.
type ProvisioningSpec struct {
	RequestID string
	RunID     string
	AgentID   string
	SpecHash  string
	Runtime   domain.RuntimeTarget
}

// ObservedResource is one external resource as observed by a provisioning port.
// Ports must return the real owner and spec hash so the saga can distinguish a
// replay (adopt) from an ownership/spec conflict, and so adopted resources are
// never compensated.
type ObservedResource struct {
	Kind               string
	ExternalID         string
	Ownership          saga.Ownership
	OwnerRunID         string
	SpecHash           string
	CorrelationID      string
	AuthoritativeStage saga.Stage
}

// ProvisioningStepPort is the narrow integration boundary for the ordered
// provisioning steps. Implementations inspect before mutating, are idempotent
// under the supplied idempotency key, and fail closed. A single port backs all
// non-terminal steps so adapters can delegate each step to the existing
// machinery rather than reimplementing it:
//
//	StepReserveIdentity       reserve-only identity surface (NO key generation)
//	StepRegisterServiceUnit   BahiaIntegration.RegisterSoulAsService + the
//	                          agent environment's deployment unit
//	StepSelectRuntimeRelease  service.AgentRuntimeReleaseService.BindRelease then
//	                          RegistryService.CreateDeploymentIntentForRuntimeRelease /
//	                          SubmitPromotionIntent (#4/#5 release-backed intent)
//	StepDeployViaBahia        RegistryService deployment intent/run + observed
//	                          environment state (deploy precedes activation)
//	StepVerify*               existing Signet/relay/model/OpenClaw readiness verification
//
// The port is injected per provisioning run so a production adapter can carry
// the resolved soul, org, environment, and runtime-release context that the
// secret-free saga checkpoint intentionally does not persist.
type ProvisioningStepPort interface {
	Observe(ctx context.Context, spec ProvisioningSpec, step OrderedStep) ([]ObservedResource, error)
	Ensure(ctx context.Context, spec ProvisioningSpec, step OrderedStep, idempotencyKey string) error
	Remove(ctx context.Context, spec ProvisioningSpec, step OrderedStep, resource saga.Resource, idempotencyKey string) error
}

// ActiveSoulProjectionPort publishes and inspects the correlated kind-7950
// provisioning result and kind-31951 Soul projection. The active Soul may only
// be published when terminalStage is saga.StageRunning.
type ActiveSoulProjectionPort interface {
	Observe(ctx context.Context, spec ProvisioningSpec, terminalStage saga.Stage, failure *saga.Failure) ([]ObservedResource, error)
	Publish(ctx context.Context, spec ProvisioningSpec, terminalStage saga.Stage, failure *saga.Failure, idempotencyKey string) error
	Remove(ctx context.Context, spec ProvisioningSpec, terminalStage saga.Stage, idempotencyKey string) error
}

// GovernedProvisioner drives the ordered provisioning saga for one agent. It is
// a thin composition over the existing saga engine, store, and operator, so
// durability, idempotency, compensation, and restart recovery are inherited.
type GovernedProvisioner struct {
	engine  *saga.Engine
	store   saga.Store
	request GovernedProvisioningRequest
}

// NewGovernedProvisioner builds the ordered saga. Optional saga.Option values
// (clock, retention) are forwarded to the engine.
func NewGovernedProvisioner(store saga.Store, request GovernedProvisioningRequest, steps ProvisioningStepPort, projection ActiveSoulProjectionPort, opts ...saga.Option) (*GovernedProvisioner, error) {
	if store == nil {
		return nil, errors.New("governed provisioning store is required")
	}
	if steps == nil || projection == nil {
		return nil, errors.New("governed provisioning ports are required")
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	drivers := make([]saga.StageDriver, 0, len(governedOrder)+1)
	for _, binding := range governedOrder {
		drivers = append(drivers, &orderedStepDriver{binding: binding, port: steps, request: request})
	}
	drivers = append(drivers, &activeSoulDriver{port: projection, request: request})
	engine, err := saga.NewEngine(store, drivers, opts...)
	if err != nil {
		return nil, err
	}
	return &GovernedProvisioner{engine: engine, store: store, request: request}, nil
}

func (g *GovernedProvisioner) Start(ctx context.Context) (*saga.Run, error) {
	return g.engine.Start(ctx, g.request.RequestID, g.request.RunID, g.request.AgentID, g.request.SpecHash)
}

func (g *GovernedProvisioner) Reconcile(ctx context.Context, dryRun bool) (*saga.Report, error) {
	return g.engine.Reconcile(ctx, g.request.RequestID, dryRun)
}

func (g *GovernedProvisioner) Retry(ctx context.Context, dryRun bool) (*saga.Report, error) {
	return g.engine.Retry(ctx, g.request.RequestID, dryRun)
}

func (g *GovernedProvisioner) Inspect(ctx context.Context) (*saga.Report, error) {
	return g.engine.Inspect(ctx, g.request.RequestID)
}

func (g *GovernedProvisioner) SafeAbort(ctx context.Context, dryRun bool) (*saga.Report, error) {
	return g.engine.SafeAbort(ctx, g.request.RequestID, dryRun)
}

// Run loads the durable checkpoint so callers can inspect saga status after a
// process restart.
func (g *GovernedProvisioner) Run(ctx context.Context) (*saga.Run, error) {
	return g.store.Load(ctx, g.request.RequestID)
}

func (g *GovernedProvisioner) Operator() (*saga.Operator, error) {
	return saga.NewOperator(g.engine)
}

// orderedStepDriver adapts one ordered step onto the saga StageDriver contract.
type orderedStepDriver struct {
	binding stepBinding
	port    ProvisioningStepPort
	request GovernedProvisioningRequest
}

func (d *orderedStepDriver) Stage() saga.Stage { return d.binding.Stage }

func (d *orderedStepDriver) spec(snap saga.Snapshot) ProvisioningSpec {
	return ProvisioningSpec{
		RequestID: snap.RequestID,
		RunID:     snap.RunID,
		AgentID:   snap.AgentID,
		SpecHash:  snap.SpecHash,
		Runtime:   d.request.Runtime,
	}
}

func (d *orderedStepDriver) Inspect(ctx context.Context, snap saga.Snapshot, target *saga.Resource) (saga.Observation, error) {
	observed, err := d.port.Observe(ctx, d.spec(snap), d.binding.Step)
	if err != nil {
		return saga.Observation{}, err
	}
	return stepObservation(d.binding, snap, observed, target), nil
}

func (d *orderedStepDriver) Apply(ctx context.Context, snap saga.Snapshot, key string) error {
	return d.port.Ensure(ctx, d.spec(snap), d.binding.Step, key)
}

func (d *orderedStepDriver) Compensate(ctx context.Context, snap saga.Snapshot, resource saga.Resource, key string) error {
	return d.port.Remove(ctx, d.spec(snap), d.binding.Step, resource, key)
}

// activeSoulDriver is the last, gated step. It is the saga's TerminalDriver so
// the correlated 7950 result and 31951 Soul projection always reflect terminal
// state; an active Soul is published only for saga.StageRunning.
type activeSoulDriver struct {
	port    ActiveSoulProjectionPort
	request GovernedProvisioningRequest
}

func (d *activeSoulDriver) Stage() saga.Stage { return saga.StageRunning }

func (d *activeSoulDriver) spec(snap saga.Snapshot) ProvisioningSpec {
	return ProvisioningSpec{
		RequestID: snap.RequestID,
		RunID:     snap.RunID,
		AgentID:   snap.AgentID,
		SpecHash:  snap.SpecHash,
		Runtime:   d.request.Runtime,
	}
}

func (d *activeSoulDriver) Inspect(ctx context.Context, snap saga.Snapshot, target *saga.Resource) (saga.Observation, error) {
	observed, err := d.port.Observe(ctx, d.spec(snap), saga.StageRunning, nil)
	if err != nil {
		return saga.Observation{}, err
	}
	return projectionObservation(snap, saga.StageRunning, observed, target), nil
}

func (d *activeSoulDriver) Apply(ctx context.Context, snap saga.Snapshot, key string) error {
	return d.port.Publish(ctx, d.spec(snap), saga.StageRunning, nil, key)
}

func (d *activeSoulDriver) Compensate(ctx context.Context, snap saga.Snapshot, resource saga.Resource, key string) error {
	return d.port.Remove(ctx, d.spec(snap), resource.Stage, key)
}

func (d *activeSoulDriver) InspectTerminal(ctx context.Context, snap saga.Snapshot, stage saga.Stage, failure *saga.Failure) (saga.Observation, error) {
	observed, err := d.port.Observe(ctx, d.spec(snap), stage, failure)
	if err != nil {
		return saga.Observation{}, err
	}
	return projectionObservation(snap, stage, observed, nil), nil
}

func (d *activeSoulDriver) PublishTerminal(ctx context.Context, snap saga.Snapshot, stage saga.Stage, failure *saga.Failure, key string) error {
	return d.port.Publish(ctx, d.spec(snap), stage, failure, key)
}

func stepObservation(binding stepBinding, snap saga.Snapshot, observed []ObservedResource, target *saga.Resource) saga.Observation {
	if len(observed) == 0 {
		return saga.Observation{Reality: saga.RealityAbsent}
	}
	resources := make([]saga.Resource, 0, len(observed))
	for _, item := range observed {
		resources = append(resources, buildResource(binding.System, binding.Stage, binding.Kind, binding.Rank, snap, item))
	}
	return matchTarget(resources, target)
}

func projectionObservation(snap saga.Snapshot, stage saga.Stage, observed []ObservedResource, target *saga.Resource) saga.Observation {
	if len(observed) == 0 {
		return saga.Observation{Reality: saga.RealityAbsent}
	}
	resources := make([]saga.Resource, 0, len(observed))
	for _, item := range observed {
		resource := buildResource(saga.SystemBahiaProjection, stage, item.Kind, saga.CompensateProjection, snap, item)
		resource.AuthoritativeStage = stage
		resources = append(resources, resource)
	}
	return matchTarget(resources, target)
}

func buildResource(system string, stage saga.Stage, defaultKind string, rank saga.CompensationRank, snap saga.Snapshot, item ObservedResource) saga.Resource {
	kind := strings.TrimSpace(item.Kind)
	if kind == "" {
		kind = defaultKind
	}
	ownership := item.Ownership
	if ownership == "" {
		ownership = saga.OwnershipCreated
	}
	return saga.Resource{
		Stage:              stage,
		System:             system,
		Kind:               kind,
		ExternalID:         item.ExternalID,
		SpecHash:           firstNonEmpty(item.SpecHash, snap.SpecHash),
		Ownership:          ownership,
		OwnerRunID:         item.OwnerRunID,
		CorrelationID:      firstNonEmpty(item.CorrelationID, snap.RequestID),
		AuthoritativeStage: item.AuthoritativeStage,
		CompensationOrder:  rank,
	}
}

func matchTarget(resources []saga.Resource, target *saga.Resource) saga.Observation {
	if target == nil {
		return saga.Observation{Reality: saga.RealityMatching, Resources: resources}
	}
	for _, resource := range resources {
		if saga.PublicResourceRef(resource.System, resource.Kind, resource.ExternalID) == target.ExternalID {
			return saga.Observation{Reality: saga.RealityMatching, Resources: []saga.Resource{resource}}
		}
	}
	return saga.Observation{Reality: saga.RealityAbsent}
}
