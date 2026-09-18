package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/adapters/agentmemory"
	"github.com/openagentsinc/bahia/internal/adapters/qdrant"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

const (
	productionStateSchema       = "bahia.governed-provisioning-state.v1"
	productionReservationSchema = "bahia.governed-identity-reservation.v1"
	governedMetadataRequest     = "governed_request_id"
	governedMetadataRun         = "governed_run_id"
	governedMetadataSpec        = "governed_spec_hash"
)

var errProductionStateNotFound = errors.New("production provisioning state not found")

// ProductionGovernedProvisionerConfig wires the real Bahia persistence seams
// used by the governed provisioning port. StateDir contains both saga
// checkpoints and a secret-free adapter ledger. No key material or bunker URI
// is ever written there.
type ProductionGovernedProvisionerConfig struct {
	StateDir        string
	RuntimeReleases *service.AgentRuntimeReleaseService
	DeploymentUnits repository.DeploymentUnitRepository
}

// ProductionGovernedProvisioner is the reachable Reactor ProvisioningEngine.
// It resolves one immutable request, constructs production ports over the
// existing FullProvisioner machinery, and lets GovernedProvisioner drive every
// mutation in the accepted order.
type ProductionGovernedProvisioner struct {
	full     *FullProvisioner
	store    saga.Store
	states   *productionStateStore
	releases *service.AgentRuntimeReleaseService
	units    repository.DeploymentUnitRepository
}

// ownsTerminalFailureProjection tells Reactor that governed reconciliation
// durably publishes its own failed/rolled-back provisioning result. Success is
// intentionally still published by Reactor after StageRunning is confirmed.
func (*ProductionGovernedProvisioner) ownsTerminalFailureProjection() {}

// NewProductionGovernedProvisioner creates the durable production engine. The
// release and deployment-unit dependencies may be nil during database-degraded
// startup, but provisioning then fails closed before registry mutation.
func NewProductionGovernedProvisioner(full *FullProvisioner, cfg ProductionGovernedProvisionerConfig) (*ProductionGovernedProvisioner, error) {
	if full == nil || full.reactor == nil || full.bahiaIntegration == nil || full.bahiaIntegration.registry == nil {
		return nil, fmt.Errorf("production governed provisioning requires FullProvisioner, Reactor, and Bahia integration")
	}
	root := strings.TrimSpace(cfg.StateDir)
	if root == "" {
		return nil, fmt.Errorf("production governed provisioning state directory is required")
	}
	store, err := saga.NewFileStore(filepath.Join(root, "sagas"))
	if err != nil {
		return nil, fmt.Errorf("configure governed provisioning saga store: %w", err)
	}
	states, err := newProductionStateStore(filepath.Join(root, "adapters"))
	if err != nil {
		return nil, fmt.Errorf("configure governed provisioning adapter state: %w", err)
	}
	return &ProductionGovernedProvisioner{full: full, store: store, states: states, releases: cfg.RuntimeReleases, units: cfg.DeploymentUnits}, nil
}

// Provision is the live kind-5950 caller. An exact relay replay reuses the
// durable saga RunID and adapter ledger instead of allocating another identity
// or repeating a completed external mutation.
func (p *ProductionGovernedProvisioner) Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	if req == nil || run == nil {
		return nil, fmt.Errorf("governed provisioning requires request and run")
	}
	resolved, err := p.full.resolveProvisioningSpec(ctx, req)
	if err != nil {
		return nil, err
	}
	if resolved.Runtime.Target != domain.RuntimeTargetOpenClaw && resolved.Runtime.Target != domain.RuntimeTargetMetiq {
		return nil, fmt.Errorf("governed provisioning requires openclaw or metiq runtime, got %q", resolved.Runtime.Target)
	}
	if _, err := uuid.Parse(strings.TrimSpace(resolved.Runtime.RuntimeReleaseID)); err != nil {
		return nil, fmt.Errorf("governed provisioning requires a valid verified runtime_release_id: %w", err)
	}
	run.AgentID = resolved.AgentID
	run.DraftRef = resolved.DraftRef
	run.DraftEventID = resolved.DraftEventID
	run.SpecHash = resolved.SpecHash

	if prior, loadErr := p.states.load(ctx, run.RequestID); loadErr == nil {
		if prior.AgentID != resolved.AgentID || prior.SpecHash != resolved.SpecHash || prior.Runtime != resolved.Runtime.Target {
			return nil, fmt.Errorf("%w: durable production request differs from replay", saga.ErrConflict)
		}
		priorRunID, parseErr := uuid.Parse(prior.RunID)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid durable governed run id: %w", parseErr)
		}
		run.ID = priorRunID
	} else if !errors.Is(loadErr, errProductionStateNotFound) {
		return nil, loadErr
	}

	request := GovernedProvisioningRequest{
		RequestID: run.RequestID,
		RunID:     run.ID.String(),
		AgentID:   resolved.AgentID,
		SpecHash:  resolved.SpecHash,
		Runtime:   resolved.Runtime.Target,
	}
	port := &productionProvisioningPort{
		engine: p, request: req, resolved: resolved, run: run,
	}
	projection := productionProjectionPort{steps: port}
	governed, err := NewGovernedProvisioner(p.store, request, port, projection)
	if err != nil {
		return nil, err
	}
	if _, err := governed.Start(ctx); err != nil {
		return nil, err
	}
	report, err := governed.Reconcile(ctx, false)
	if err != nil {
		return nil, err
	}
	if report.Stage != saga.StageRunning {
		return nil, fmt.Errorf("governed provisioning stopped at stage %s", report.Stage)
	}
	state, err := p.states.load(ctx, run.RequestID)
	if err != nil {
		return nil, err
	}
	if state.Soul.Status != domain.SoulStatusActive || !state.ActiveSoulPublished {
		return nil, fmt.Errorf("governed provisioning reached running without an active Soul projection")
	}
	run.SoulID = &state.Soul.ID
	return cloneProductionSoul(&state.Soul), nil
}

type productionStepState struct {
	Complete  bool               `json:"complete"`
	Resources []ObservedResource `json:"resources,omitempty"`
}

type productionProvisioningState struct {
	Schema              string                              `json:"schema"`
	RequestID           string                              `json:"request_id"`
	RunID               string                              `json:"run_id"`
	AgentID             string                              `json:"agent_id"`
	SpecHash            string                              `json:"spec_hash"`
	Runtime             domain.RuntimeTarget                `json:"runtime"`
	Soul                domain.AgentSoul                    `json:"soul"`
	Prepared            bool                                `json:"prepared"`
	IdentityCreated     bool                                `json:"identity_created,omitempty"`
	RuntimeResult       *RuntimeControlResultEnvelope       `json:"runtime_result,omitempty"`
	Release             *domain.AgentRuntimeRelease         `json:"release,omitempty"`
	ReleaseSource       *domain.AgentRuntimeSource          `json:"release_source,omitempty"`
	ReleaseBinding      *domain.AgentServiceReleaseBinding  `json:"release_binding,omitempty"`
	ServiceID           uuid.UUID                           `json:"service_id,omitempty"`
	EnvironmentID       uuid.UUID                           `json:"environment_id,omitempty"`
	DeploymentUnitID    uuid.UUID                           `json:"deployment_unit_id,omitempty"`
	DeploymentIntentID  uuid.UUID                           `json:"deployment_intent_id,omitempty"`
	Steps               map[OrderedStep]productionStepState `json:"steps,omitempty"`
	ActiveSoulPublished bool                                `json:"active_soul_published,omitempty"`
	TerminalResultStage saga.Stage                          `json:"terminal_result_stage,omitempty"`
}

type productionIdentityReservation struct {
	Schema    string    `json:"schema"`
	AgentID   string    `json:"agent_id"`
	SpecHash  string    `json:"spec_hash"`
	RequestID string    `json:"request_id"`
	RunID     string    `json:"run_id"`
	CreatedAt time.Time `json:"created_at"`
}

type productionStateStore struct {
	dir string
	mu  sync.Mutex
}

func newProductionStateStore(dir string) (*productionStateStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "identities"), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &productionStateStore{dir: dir}, nil
}

func productionStateName(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *productionStateStore) requestPath(requestID string) string {
	return filepath.Join(s.dir, "requests", productionStateName("request", requestID))
}

func (s *productionStateStore) reservationPath(agentID string) string {
	return filepath.Join(s.dir, "identities", productionStateName("agent", agentID))
}

func (s *productionStateStore) load(ctx context.Context, requestID string) (*productionProvisioningState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var state productionProvisioningState
	if err := readProductionJSON(s.requestPath(requestID), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errProductionStateNotFound
		}
		return nil, err
	}
	if state.Schema != productionStateSchema || state.RequestID != requestID || state.AgentID == "" || state.RunID == "" || state.SpecHash == "" {
		return nil, fmt.Errorf("invalid production provisioning state")
	}
	if state.Steps == nil {
		state.Steps = map[OrderedStep]productionStepState{}
	}
	return &state, nil
}

func (s *productionStateStore) save(ctx context.Context, state *productionProvisioningState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if state == nil || state.RequestID == "" || state.RunID == "" || state.AgentID == "" || state.SpecHash == "" {
		return fmt.Errorf("incomplete production provisioning state")
	}
	state.Schema = productionStateSchema
	if state.Steps == nil {
		state.Steps = map[OrderedStep]productionStepState{}
	}
	state.Soul.BunkerURI = ""
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeProductionJSON(s.requestPath(state.RequestID), state)
}

func (s *productionStateStore) reservation(ctx context.Context, spec ProvisioningSpec, create bool) (*productionIdentityReservation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.reservationPath(spec.AgentID)
	var current productionIdentityReservation
	if err := readProductionJSON(path, &current); err == nil {
		if current.Schema != productionReservationSchema || current.AgentID != spec.AgentID {
			return nil, false, fmt.Errorf("invalid governed identity reservation")
		}
		return &current, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if !create {
		return nil, false, nil
	}
	current = productionIdentityReservation{
		Schema: productionReservationSchema, AgentID: spec.AgentID, SpecHash: spec.SpecHash,
		RequestID: spec.RequestID, RunID: spec.RunID, CreatedAt: time.Now().UTC(),
	}
	if err := writeProductionJSON(path, &current); err != nil {
		return nil, false, err
	}
	return &current, true, nil
}

func readProductionJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeProductionJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".governed-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

type productionProvisioningPort struct {
	engine   *ProductionGovernedProvisioner
	request  *domain.ProvisioningRequest
	resolved *resolvedProvisioningSpec
	run      *domain.ProvisioningRun
}

func (p *productionProvisioningPort) Observe(ctx context.Context, spec ProvisioningSpec, step OrderedStep) ([]ObservedResource, error) {
	if step == StepReserveIdentity {
		reservation, _, err := p.engine.states.reservation(ctx, spec, false)
		if err != nil || reservation == nil {
			return nil, err
		}
		return []ObservedResource{{
			Kind: "identity_reservation", ExternalID: "identity:" + spec.AgentID,
			Ownership: saga.OwnershipAdopted, SpecHash: reservation.SpecHash,
			CorrelationID: reservation.RequestID,
		}}, nil
	}
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if errors.Is(err, errProductionStateNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stage := state.Steps[step]
	if !stage.Complete {
		return nil, nil
	}
	// Live verification: the ledger's recorded resources are returned ONLY after
	// the live service/unit/release-binding/intent have been re-inspected and
	// their ownership, correlation, and governed spec metadata agree with this
	// replay. Any disagreement is an ownership/spec conflict, never an adoption.
	if err := p.inspectRealStep(ctx, spec, state, step); err != nil {
		return nil, err
	}
	return append([]ObservedResource(nil), stage.Resources...), nil
}

func (p *productionProvisioningPort) Ensure(ctx context.Context, spec ProvisioningSpec, step OrderedStep, _ string) error {
	switch step {
	case StepReserveIdentity:
		return p.ensureReservation(ctx, spec)
	case StepRegisterServiceUnit:
		return p.ensureServiceUnit(ctx, spec)
	case StepSelectRuntimeRelease:
		return p.ensureRuntimeRelease(ctx, spec)
	case StepDeployViaBahia:
		return p.ensureBahiaDeployment(ctx, spec)
	case StepVerifyIdentity:
		return p.ensureIdentity(ctx, spec)
	case StepVerifyRelay:
		return p.ensureRelay(ctx, spec)
	case StepVerifyModel:
		return p.ensureModel(ctx, spec)
	case StepVerifyReadiness:
		return p.ensureReadiness(ctx, spec)
	default:
		return fmt.Errorf("unsupported governed provisioning step %q", step)
	}
}

func (p *productionProvisioningPort) Remove(ctx context.Context, spec ProvisioningSpec, step OrderedStep, resource saga.Resource, _ string) error {
	if step == StepReserveIdentity {
		return nil
	}
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	stage := state.Steps[step]
	for i := range stage.Resources {
		observed := &stage.Resources[i]
		if saga.PublicResourceRef(resource.System, observed.Kind, observed.ExternalID) != resource.ExternalID {
			continue
		}
		if observed.Ownership != saga.OwnershipCreated || observed.OwnerRunID != spec.RunID {
			return nil
		}
		// Registry intents, release bindings, runtime identities, and accepted
		// runtime evidence are durable facts. Compensation safely relinquishes
		// ownership rather than deleting or re-keying them.
		observed.Ownership = saga.OwnershipAdopted
		observed.OwnerRunID = ""
		state.Steps[step] = stage
		return p.engine.states.save(ctx, state)
	}
	return nil
}

// ActiveSoulProjectionPort Observe implementation.
func (p *productionProvisioningPort) observeProjection(ctx context.Context, spec ProvisioningSpec, terminalStage saga.Stage) ([]ObservedResource, error) {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if errors.Is(err, errProductionStateNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if terminalStage == saga.StageRunning {
		if !state.ActiveSoulPublished {
			return nil, nil
		}
		current, err := p.engine.full.reactor.GetSoul(ctx, spec.AgentID)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Status != domain.SoulStatusActive || current.SpecHash != state.Soul.SpecHash {
			return nil, nil
		}
		return []ObservedResource{{
			Kind: saga.ResourceAgentSoul, ExternalID: firstNonEmpty(current.EventID, "soul:"+spec.AgentID),
			Ownership: saga.OwnershipCreated, OwnerRunID: spec.RunID,
			SpecHash: spec.SpecHash, CorrelationID: spec.RequestID,
			AuthoritativeStage: saga.StageRunning,
		}}, nil
	}
	if state.TerminalResultStage != terminalStage {
		return nil, nil
	}
	requestEvent, err := productionRequestEvent(p.run)
	if err != nil {
		return nil, err
	}
	result, err := p.engine.full.reactor.findExistingProvisioningResult(ctx, requestEvent)
	if err != nil {
		return nil, err
	}
	if result == nil || int(result.Kind) != kinds.SoulFactoryProvisioningResult {
		return nil, nil
	}
	return []ObservedResource{{
		Kind: saga.ResourceProvisioningResult, ExternalID: result.ID.Hex(),
		Ownership: saga.OwnershipCreated, OwnerRunID: spec.RunID,
		SpecHash: spec.SpecHash, CorrelationID: spec.RequestID,
		AuthoritativeStage: terminalStage,
	}}, nil
}

// Observe satisfies ActiveSoulProjectionPort. Go does not support overloads,
// so the projection boundary is provided by the embedded adapter below.
func (p *productionProvisioningPort) Publish(ctx context.Context, spec ProvisioningSpec, terminalStage saga.Stage, failure *saga.Failure, _ string) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	if terminalStage == saga.StageRunning {
		if !state.Steps[StepVerifyReadiness].Complete {
			return &saga.SafeError{Code: "policy_denied", Retryable: false}
		}
		state.Soul.Status = domain.SoulStatusActive
		now := time.Now().UTC()
		state.Soul.ProvisionedAt = &now
		if err := p.engine.full.reactor.PublishSoul(ctx, &state.Soul); err != nil {
			return &saga.SafeError{Code: "response_lost", Retryable: true, Cause: err}
		}
		state.ActiveSoulPublished = true
		return p.engine.states.save(ctx, state)
	}
	requestEvent, err := productionRequestEvent(p.run)
	if err != nil {
		return err
	}
	step := string(terminalStage)
	message := "governed provisioning did not reach running"
	if failure != nil {
		step = string(failure.Stage)
		message = failure.Message
	}
	if err := p.engine.full.reactor.publishError(ctx, requestEvent, step, message, spec.RunID); err != nil {
		return &saga.SafeError{Code: "response_lost", Retryable: true, Cause: err}
	}
	state.TerminalResultStage = terminalStage
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) RemoveProjection(context.Context, ProvisioningSpec, saga.Stage, string) error {
	return nil
}

// projectionPort resolves the Go method-name collision between the two Observe
// and Remove signatures while keeping one shared production adapter state.
type productionProjectionPort struct{ steps *productionProvisioningPort }

func (p productionProjectionPort) Observe(ctx context.Context, spec ProvisioningSpec, stage saga.Stage, _ *saga.Failure) ([]ObservedResource, error) {
	return p.steps.observeProjection(ctx, spec, stage)
}
func (p productionProjectionPort) Publish(ctx context.Context, spec ProvisioningSpec, stage saga.Stage, failure *saga.Failure, key string) error {
	return p.steps.Publish(ctx, spec, stage, failure, key)
}
func (p productionProjectionPort) Remove(ctx context.Context, spec ProvisioningSpec, stage saga.Stage, key string) error {
	return p.steps.RemoveProjection(ctx, spec, stage, key)
}

func (p *productionProvisioningPort) ensureReservation(ctx context.Context, spec ProvisioningSpec) error {
	reservation, created, err := p.engine.states.reservation(ctx, spec, true)
	if err != nil {
		return err
	}
	if reservation.SpecHash != spec.SpecHash {
		return &saga.SafeError{Code: "ownership_conflict", Retryable: false}
	}
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err == nil && state.Prepared {
		return nil
	}
	if err != nil && !errors.Is(err, errProductionStateNotFound) {
		return err
	}
	if state == nil {
		state = &productionProvisioningState{
			RequestID: spec.RequestID, RunID: spec.RunID, AgentID: spec.AgentID,
			SpecHash: p.resolved.SpecHash, Runtime: spec.Runtime,
			Steps: map[OrderedStep]productionStepState{},
		}
	}
	soul := &domain.AgentSoul{
		ID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia-governed-soul/v1\x00"+spec.RequestID)),
		AgentID: p.resolved.AgentID, Name: p.resolved.Name, Tier: p.resolved.Tier,
		Status: domain.SoulStatusProvisioning, CreatedAt: time.Now().UTC(),
	}
	p.resolved.applyToSoul(soul)
	output, err := p.generateSoulContent(ctx, soul)
	if err != nil {
		return err
	}
	if err := p.prepareAvatarAndWorkspace(ctx, soul, output); err != nil {
		return err
	}
	state.Soul = *soul
	state.Prepared = true
	resource := ObservedResource{
		Kind: "identity_reservation", ExternalID: "identity:" + spec.AgentID,
		Ownership: saga.OwnershipAdopted, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID,
	}
	state.Steps[StepReserveIdentity] = productionStepState{Complete: true, Resources: []ObservedResource{resource}}
	if !created {
		state.Steps[StepReserveIdentity] = productionStepState{Complete: true, Resources: []ObservedResource{resource}}
	}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) generateSoulContent(ctx context.Context, soul *domain.AgentSoul) (*domain.SoulGeneratorOutput, error) {
	var output *domain.SoulGeneratorOutput
	var err error
	if p.resolved.Draft != nil {
		draft := p.resolved.Draft.Content.MigrateToLatest()
		avatarPrompt := ""
		if draft.Avatar.Generation != nil {
			avatarPrompt = draft.Avatar.Generation.Prompt
		}
		output = &domain.SoulGeneratorOutput{SoulMD: draft.SoulMD, IdentityMD: draft.IdentityMD, AllowedKinds: append([]int(nil), draft.Permissions.AllowedKinds...), ToolGrants: append([]domain.ToolGrant(nil), draft.Permissions.ToolGrants...), AvatarPrompt: avatarPrompt}
	} else {
		output, err = p.engine.full.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{Template: p.resolved.Template, AgentID: p.resolved.AgentID, Name: p.resolved.Name, Brief: p.resolved.Brief, Tier: p.resolved.Tier})
		if err != nil {
			return nil, fmt.Errorf("generate soul: %w", err)
		}
	}
	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	if p.resolved.Draft != nil || len(p.resolved.Permissions.AllowedKinds) > 0 {
		soul.AllowedKinds = append([]int(nil), p.resolved.Permissions.AllowedKinds...)
	} else {
		soul.AllowedKinds = append([]int(nil), output.AllowedKinds...)
	}
	if p.resolved.Draft != nil || len(p.resolved.Permissions.ToolGrants) > 0 {
		soul.ToolGrants = append([]domain.ToolGrant(nil), p.resolved.Permissions.ToolGrants...)
	} else {
		soul.ToolGrants = append([]domain.ToolGrant(nil), output.ToolGrants...)
	}
	if soul.Name == "" {
		soul.Name = p.resolved.AgentID
	}
	soul.Purpose = p.resolved.Brief
	return output, nil
}

func (p *productionProvisioningPort) prepareAvatarAndWorkspace(ctx context.Context, soul *domain.AgentSoul, output *domain.SoulGeneratorOutput) error {
	full := p.engine.full
	if full.avatarGenerator != nil {
		avatar, err := full.avatarGenerator.Generate(ctx, output.AvatarPrompt, soul.AgentID)
		if err != nil {
			return fmt.Errorf("generate avatar: %w", err)
		}
		if full.blossomClient == nil {
			return fmt.Errorf("avatar storage is not configured")
		}
		stored, err := full.blossomClient.StoreAvatar(ctx, avatar.ImageData, avatar.ContentType, avatar.SourceURL)
		if err != nil {
			return fmt.Errorf("store avatar: %w", err)
		}
		soul.AvatarBlobHash, soul.AvatarURL, soul.Assets.AvatarRef = stored.Hash, stored.URL, stored.Ref
	}
	if full.workspaceManager != nil {
		repoURL, err := full.workspaceManager.InitWorkspace(ctx, soul)
		if err != nil {
			return fmt.Errorf("initialize workspace: %w", err)
		}
		soul.WorkspaceRepoURL = repoURL
	}
	return nil
}

func (p *productionProvisioningPort) ensureServiceUnit(ctx context.Context, spec ProvisioningSpec) error {
	if p.engine.units == nil {
		return fmt.Errorf("production governed provisioning deployment-unit repository is not configured")
	}
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	registry := p.engine.full.bahiaIntegration.registry
	serviceName := soulServiceName(spec.AgentID)
	existingService, err := registry.GetServiceByName(ctx, serviceName)
	if err != nil {
		return err
	}
	serviceID, err := p.engine.full.bahiaIntegration.RegisterSoulAsService(ctx, &state.Soul)
	if err != nil {
		return err
	}
	service, err := registry.GetService(ctx, serviceID)
	if err != nil || service == nil {
		return fmt.Errorf("inspect registered Soul service: %w", err)
	}
	orgID := p.engine.full.bahiaIntegration.OrganizationID()
	if orgID == uuid.Nil || service.OrgID != orgID {
		return fmt.Errorf("Soul service is not in the configured organization")
	}
	envID, err := p.engine.full.bahiaIntegration.EnsureAgentEnvironment(ctx)
	if err != nil {
		return err
	}
	unitKey := soulServiceName(spec.AgentID)
	existingUnit, err := p.engine.units.GetByEnvironmentKey(ctx, envID, unitKey)
	if err != nil {
		return err
	}
	unit := existingUnit
	if unit == nil {
		unit = &domain.DeploymentUnit{
			ID: uuid.New(), EnvironmentID: envID, Key: unitKey, DisplayName: state.Soul.Name,
			RuntimeType:   p.engine.full.bahiaIntegration.getRuntimeType(state.Soul.Tier),
			ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
			RuntimeConfig: map[string]any{"agent_id": spec.AgentID, governedMetadataSpec: spec.SpecHash},
			CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := p.engine.units.Create(ctx, unit); err != nil {
			concurrent, lookupErr := p.engine.units.GetByEnvironmentKey(ctx, envID, unitKey)
			if lookupErr != nil || concurrent == nil {
				return fmt.Errorf("create governed agent deployment unit: %w", err)
			}
			unit = concurrent
		}
	}
	state.ServiceID, state.EnvironmentID, state.DeploymentUnitID = serviceID, envID, unit.ID
	state.Soul.BahiaServiceID = &serviceID
	state.Steps[StepRegisterServiceUnit] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "service", serviceID.String(), existingService == nil),
		ownedResource(spec, "deployment_unit", unit.ID.String(), existingUnit == nil),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) ensureRuntimeRelease(ctx context.Context, spec ProvisioningSpec) error {
	if p.engine.releases == nil {
		return fmt.Errorf("production governed provisioning runtime release service is not configured")
	}
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	orgID := p.engine.full.bahiaIntegration.OrganizationID()
	releaseID, err := uuid.Parse(strings.TrimSpace(state.Soul.Runtime.RuntimeReleaseID))
	if err != nil {
		return err
	}
	release, err := p.engine.releases.GetRelease(ctx, orgID, releaseID)
	if err != nil {
		return err
	}
	if release == nil || release.VerifiedAt.IsZero() {
		return fmt.Errorf("verified runtime release %s not found", releaseID)
	}
	source, err := p.engine.releases.GetSource(ctx, orgID, release.SourceID)
	if err != nil {
		return err
	}
	if source == nil || strings.TrimSpace(source.ReleaseChannel) == "" {
		return fmt.Errorf("verified runtime release source is unavailable")
	}
	prior, err := p.engine.releases.GetServiceRelease(ctx, orgID, state.ServiceID, release.ID)
	if err != nil {
		return err
	}
	binding := &domain.AgentServiceReleaseBinding{
		OrgID: orgID, AgentID: spec.AgentID, ServiceID: state.ServiceID,
		ReleaseID: release.ID, ReleaseChannel: source.ReleaseChannel, SourceEventID: spec.RequestID,
	}
	if err := p.engine.releases.BindRelease(ctx, binding); err != nil {
		return err
	}
	bound, err := p.engine.releases.GetServiceRelease(ctx, orgID, state.ServiceID, release.ID)
	if err != nil || bound == nil {
		return fmt.Errorf("inspect bound runtime release: %w", err)
	}
	state.Release, state.ReleaseSource, state.ReleaseBinding = release, source, &bound.Binding
	state.Steps[StepSelectRuntimeRelease] = productionStepState{Complete: true, Resources: []ObservedResource{
		{Kind: "runtime_release", ExternalID: release.ID.String(), Ownership: saga.OwnershipPreExisting, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID},
		ownedResource(spec, "runtime_release_binding", bound.Binding.ID.String(), prior == nil),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) ensureBahiaDeployment(ctx context.Context, spec ProvisioningSpec) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	if state.Release == nil || state.ReleaseBinding == nil {
		return fmt.Errorf("runtime release must be selected before Bahia deployment")
	}
	registry := p.engine.full.bahiaIntegration.registry
	orgID := p.engine.full.bahiaIntegration.OrganizationID()
	prior, err := registry.GetDeploymentIntentForRuntimeRelease(ctx, orgID, state.ServiceID, state.Release.ID)
	if err != nil {
		return err
	}
	intent := &domain.DeploymentIntent{
		ID: uuid.New(), ServiceID: state.ServiceID, EnvironmentID: state.EnvironmentID,
		DeploymentUnitID: &state.DeploymentUnitID,
		RequestedBy:      "soul-factory", SourceKind: domain.SourceKindEventTriggered,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
		Metadata: map[string]any{
			"runtime_release_id": state.Release.ID.String(), governedMetadataRequest: spec.RequestID,
			governedMetadataRun: spec.RunID, governedMetadataSpec: spec.SpecHash, "agent_id": spec.AgentID,
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := registry.SubmitPromotionIntent(ctx, intent); err != nil {
		return err
	}
	deployed, err := registry.GetDeploymentIntentForRuntimeRelease(ctx, orgID, state.ServiceID, state.Release.ID)
	if err != nil || deployed == nil {
		return fmt.Errorf("inspect release-backed Bahia deployment intent: %w", err)
	}
	state.DeploymentIntentID = deployed.Intent.ID
	state.Soul.DeployStatus = "deploying"
	state.Steps[StepDeployViaBahia] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "bahia_deployment", deployed.Intent.ID.String(), prior == nil),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) ensureIdentity(ctx context.Context, spec ProvisioningSpec) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	full := p.engine.full
	var bunkerURI string
	if state.Soul.NostrPubkey == "" {
		pubkey, npub, bunker, err := full.reactor.signer.ProvisionAgent(ctx, spec.AgentID, state.Soul.AllowedKinds)
		if err != nil {
			return fmt.Errorf("configure Signet identity: %w", err)
		}
		state.Soul.NostrPubkey, state.Soul.NostrNpub = pubkey, npub
		state.IdentityCreated = true
		bunkerURI = bunker
		if full.signetEnrollment != nil && spec.Runtime == domain.RuntimeTargetOpenClaw {
			if err := full.signetEnrollment.StageHandoff(ctx, spec.AgentID, bunker); err != nil {
				return fmt.Errorf("protect OpenClaw one-time bunker handoff: %w", err)
			}
		}
		// Persist the public identity immediately. A retry never provisions a
		// second key even if runtime configuration fails afterward.
		if err := p.engine.states.save(ctx, state); err != nil {
			return err
		}
	}
	if err := p.assignMemberships(ctx, &state.Soul); err != nil {
		return err
	}
	state.Soul.BunkerURI = bunkerURI
	result, err := full.executeRuntimeProvision(ctx, &state.Soul, p.resolved, p.run)
	state.Soul.BunkerURI = ""
	if err != nil {
		return err
	}
	if result == nil || result.Status != "success" {
		return fmt.Errorf("runtime identity configuration did not return success")
	}
	state.RuntimeResult = result
	state.Steps[StepVerifyIdentity] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "identity_verification", state.Soul.NostrPubkey, state.IdentityCreated),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) assignMemberships(ctx context.Context, soul *domain.AgentSoul) error {
	full := p.engine.full
	if full.nip29MembershipErr != nil {
		return full.nip29MembershipErr
	}
	if full.nip29Membership != nil {
		if _, err := full.nip29Membership.Assign(ctx, soul.NostrPubkey); err != nil {
			return err
		}
	}
	if full.communikeysMembershipErr != nil {
		return full.communikeysMembershipErr
	}
	if full.communikeysMembership != nil {
		if _, err := full.communikeysMembership.Assign(ctx, soul.NostrPubkey); err != nil {
			return err
		}
	}
	if full.concordMembershipErr != nil {
		return full.concordMembershipErr
	}
	if full.concordMembership != nil {
		if _, err := full.concordMembership.Assign(ctx, soul.NostrPubkey); err != nil {
			return err
		}
	}
	return nil
}

func (p *productionProvisioningPort) ensureRelay(ctx context.Context, spec ProvisioningSpec) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	if state.RuntimeResult == nil || strings.TrimSpace(state.Soul.Runtime.RuntimeBinding) == "" || strings.TrimSpace(state.Soul.Runtime.RuntimePubkey) == "" {
		return fmt.Errorf("runtime relay/identity configuration is incomplete")
	}
	full := p.engine.full
	if full.nip05Manager != nil {
		relays, err := explicitNIP05Relays(full.nip05Relays)
		if err != nil {
			return err
		}
		state.Soul.NIP05 = full.nip05Manager.GetNIP05(state.Soul.AgentID)
		if err := full.nip05Manager.Register(ctx, state.Soul.AgentID, state.Soul.NostrPubkey, relays); err != nil {
			return fmt.Errorf("NIP-05 registration: %w", err)
		}
	}
	if err := full.publishProfile(ctx, &state.Soul); err != nil {
		return fmt.Errorf("publish profile: %w", err)
	}
	if full.qdrantClient != nil && full.qdrantClient.Configured() {
		state.Soul.QdrantCollection = state.Soul.AgentID
		if err := full.qdrantClient.CreateCollection(ctx, state.Soul.QdrantCollection, qdrant.DefaultCollectionConfig()); err != nil {
			return fmt.Errorf("create Qdrant collection: %w", err)
		}
	}
	if full.agentMemory != nil && full.agentMemory.Configured() {
		if err := full.agentMemory.RegisterAgent(ctx, state.Soul.AgentID, state.Soul.NostrNpub, map[string]interface{}{"tier": state.Soul.Tier, "status": "provisioning"}); err != nil {
			return fmt.Errorf("register agent memory: %w", err)
		}
		entries := agentmemory.CreateInitialMemory(state.Soul.AgentID, state.Soul.Name, state.Soul.Purpose, state.Soul.SoulMD)
		if err := full.agentMemory.SeedMemory(ctx, state.Soul.AgentID, entries); err != nil {
			return fmt.Errorf("seed agent memory: %w", err)
		}
	}
	state.Steps[StepVerifyRelay] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "relay_verification", state.Soul.Runtime.RuntimeBinding, true),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) ensureModel(ctx context.Context, spec ProvisioningSpec) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	if state.RuntimeResult == nil {
		return fmt.Errorf("runtime model verification evidence is missing")
	}
	model := strings.TrimSpace(stringResult(state.RuntimeResult.Result, "model"))
	provider := strings.TrimSpace(stringResult(state.RuntimeResult.Result, "provider"))
	if provider == "" {
		provider = providerFromModel(model)
	}
	if provider == "" || model == "" {
		return fmt.Errorf("runtime model verification requires provider and model evidence")
	}
	state.Soul.Runtime.Provider, state.Soul.Runtime.Model = provider, model
	state.Steps[StepVerifyModel] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "model_verification", provider+"/"+model, true),
	}}
	return p.engine.states.save(ctx, state)
}

func (p *productionProvisioningPort) ensureReadiness(ctx context.Context, spec ProvisioningSpec) error {
	state, err := p.engine.states.load(ctx, spec.RequestID)
	if err != nil {
		return err
	}
	full := p.engine.full
	if state.RuntimeResult == nil {
		return fmt.Errorf("runtime readiness evidence is missing")
	}
	if spec.Runtime == domain.RuntimeTargetOpenClaw && full.openClawReadiness != nil {
		evidence, err := full.openClawReadiness.Verify(ctx, OpenClawReadinessRequest{
			RequestID: spec.RequestID, RunID: spec.RunID, AgentID: spec.AgentID,
			AccountID: stringResult(state.RuntimeResult.Result, "account_id"), RuntimeBinding: state.Soul.Runtime.RuntimeBinding,
			ManagedPubkey: state.Soul.NostrPubkey, Provider: state.Soul.Runtime.Provider, Model: state.Soul.Runtime.Model,
			RequiredRelays: requiredReadinessRelays(state.Soul.RelayPolicy.Read, state.Soul.RelayPolicy.Write, state.Soul.RelayPolicy.Control),
		}, nil)
		if err != nil {
			return fmt.Errorf("verify OpenClaw readiness: %w", err)
		}
		gateTimings := make(map[string]int64, len(evidence.GateTimingsMS))
		for gate, duration := range evidence.GateTimingsMS {
			gateTimings[string(gate)] = duration
		}
		state.Soul.Readiness = &domain.SoulReadinessEvidence{RequestID: evidence.RequestID, RunID: evidence.RunID, VerifiedAt: evidence.VerifiedAt, TotalDurationMS: evidence.TotalDurationMS, GateTimingsMS: gateTimings, ProbeEventIDs: append([]string(nil), evidence.ProbeEventIDs...)}
	} else {
		verified, _ := state.RuntimeResult.Result["readiness_verified"].(bool)
		if !verified || stringResult(state.RuntimeResult.Result, "state") != "running" {
			return fmt.Errorf("signed runtime result does not contain readiness_verified running evidence")
		}
		state.Soul.Readiness = &domain.SoulReadinessEvidence{RequestID: spec.RequestID, RunID: spec.RunID, VerifiedAt: time.Now().UTC()}
	}
	state.Soul.Runtime.State = "running"
	if deployStatus, err := full.bahiaIntegration.SyncSoulStatus(ctx, &state.Soul); err != nil {
		return err
	} else if deployStatus != "" {
		state.Soul.DeployStatus = deployStatus
	}
	state.Steps[StepVerifyReadiness] = productionStepState{Complete: true, Resources: []ObservedResource{
		ownedResource(spec, "readiness_verification", state.Soul.Runtime.RuntimeBinding+":"+state.Soul.Readiness.VerifiedAt.UTC().Format(time.RFC3339Nano), true),
	}}
	return p.engine.states.save(ctx, state)
}

// governedConflict is a non-retryable ownership/spec conflict. The detail is
// carried in the wrapped message for logs and tests; the saga engine matches
// the SafeError code through errors.As.
func governedConflict(detail string) error {
	return fmt.Errorf("governed replay conflict: %s: %w", detail, &saga.SafeError{Code: "ownership_conflict", Retryable: false})
}

// ledgerResource returns the ledger's recorded resource of a kind for a step.
func ledgerResource(state *productionProvisioningState, step OrderedStep, kind string) (ObservedResource, bool) {
	for _, r := range state.Steps[step].Resources {
		if r.Kind == kind {
			return r, true
		}
	}
	return ObservedResource{}, false
}

// verifyGovernedMetadata compares a live resource's governed markers with the
// replay spec. Markers that are present must match exactly. When the ledger
// says THIS run created the resource, the spec marker must also be present:
// a created resource whose marker vanished was rewritten externally. Adopted
// or pre-existing resources may legitimately lack markers, but a present
// marker belonging to a different spec or agent is still a conflict.
func verifyGovernedMetadata(what string, meta map[string]any, spec ProvisioningSpec, ledger ObservedResource, requireRunMarkers bool) error {
	createdByThisRun := ledger.Ownership == saga.OwnershipCreated && ledger.OwnerRunID == spec.RunID
	get := func(key string) (string, bool) {
		if meta == nil {
			return "", false
		}
		v, ok := meta[key]
		if !ok || v == nil {
			return "", false
		}
		return strings.TrimSpace(fmt.Sprint(v)), true
	}
	if agent, ok := get("agent_id"); ok && agent != spec.AgentID {
		return governedConflict(fmt.Sprintf("%s is bound to agent %q, replay is %q", what, agent, spec.AgentID))
	}
	specHash, hasSpec := get(governedMetadataSpec)
	if hasSpec && specHash != spec.SpecHash {
		return governedConflict(fmt.Sprintf("%s live %s %q differs from replay %q", what, governedMetadataSpec, specHash, spec.SpecHash))
	}
	if createdByThisRun && !hasSpec {
		return governedConflict(fmt.Sprintf("%s was created by this run but no longer carries %s", what, governedMetadataSpec))
	}
	if requireRunMarkers && createdByThisRun {
		if reqID, ok := get(governedMetadataRequest); !ok || reqID != spec.RequestID {
			return governedConflict(fmt.Sprintf("%s %s does not match replay request %s", what, governedMetadataRequest, spec.RequestID))
		}
		if runID, ok := get(governedMetadataRun); !ok || runID != spec.RunID {
			return governedConflict(fmt.Sprintf("%s %s does not match replay run %s", what, governedMetadataRun, spec.RunID))
		}
	} else {
		if reqID, ok := get(governedMetadataRequest); ok && createdByThisRun && reqID != spec.RequestID {
			return governedConflict(fmt.Sprintf("%s %s %q differs from replay %s", what, governedMetadataRequest, reqID, spec.RequestID))
		}
	}
	return nil
}

// inspectRealStep re-inspects the LIVE resources behind a completed step and
// fails closed on any ownership, correlation, or governed-spec disagreement
// with the replay. It never trusts the private adapter ledger alone.
func (p *productionProvisioningPort) inspectRealStep(ctx context.Context, spec ProvisioningSpec, state *productionProvisioningState, step OrderedStep) error {
	registry := p.engine.full.bahiaIntegration.registry
	orgID := p.engine.full.bahiaIntegration.OrganizationID()
	switch step {
	case StepRegisterServiceUnit:
		if p.engine.units == nil {
			return fmt.Errorf("deployment-unit repository is unavailable")
		}
		serviceRecord, err := registry.GetService(ctx, state.ServiceID)
		if err != nil || serviceRecord == nil {
			return fmt.Errorf("registered service no longer exists")
		}
		if serviceRecord.Name != soulServiceName(spec.AgentID) || serviceRecord.ArtifactRepo != soulServiceArtifactRepo(spec.AgentID) {
			return governedConflict(fmt.Sprintf("live service %s identity (%q, %q) does not match agent %q", serviceRecord.ID, serviceRecord.Name, serviceRecord.ArtifactRepo, spec.AgentID))
		}
		if orgID != uuid.Nil && serviceRecord.OrgID != orgID {
			return governedConflict(fmt.Sprintf("live service %s belongs to organization %s, want %s", serviceRecord.ID, serviceRecord.OrgID, orgID))
		}
		unit, err := p.engine.units.GetByID(ctx, state.DeploymentUnitID)
		if err != nil || unit == nil {
			return fmt.Errorf("registered deployment unit no longer exists")
		}
		if unit.EnvironmentID != state.EnvironmentID || unit.Key != soulServiceName(spec.AgentID) {
			return governedConflict(fmt.Sprintf("live deployment unit %s identity (env %s, key %q) does not match governed state", unit.ID, unit.EnvironmentID, unit.Key))
		}
		unitLedger, _ := ledgerResource(state, step, "deployment_unit")
		if err := verifyGovernedMetadata("deployment unit "+unit.ID.String(), unit.RuntimeConfig, spec, unitLedger, false); err != nil {
			return err
		}
	case StepSelectRuntimeRelease:
		if p.engine.releases == nil {
			return fmt.Errorf("runtime release service is unavailable")
		}
		if state.Release == nil || state.ReleaseBinding == nil {
			return fmt.Errorf("selected runtime release state is missing")
		}
		bound, err := p.engine.releases.GetServiceRelease(ctx, orgID, state.ServiceID, state.Release.ID)
		if err != nil || bound == nil {
			return fmt.Errorf("runtime release binding no longer exists")
		}
		b := bound.Binding
		if b.AgentID != spec.AgentID || b.ServiceID != state.ServiceID || b.ReleaseID != state.Release.ID {
			return governedConflict(fmt.Sprintf("live release binding %s (agent %q, service %s, release %s) does not match governed state", b.ID, b.AgentID, b.ServiceID, b.ReleaseID))
		}
		if b.SourceEventID != spec.RequestID {
			return governedConflict(fmt.Sprintf("live release binding %s correlation %q is not this replay's request %s", b.ID, b.SourceEventID, spec.RequestID))
		}
		if state.ReleaseBinding.ReleaseChannel != "" && b.ReleaseChannel != state.ReleaseBinding.ReleaseChannel {
			return governedConflict(fmt.Sprintf("live release binding %s channel %q differs from governed %q", b.ID, b.ReleaseChannel, state.ReleaseBinding.ReleaseChannel))
		}
	case StepDeployViaBahia:
		if state.Release == nil {
			return fmt.Errorf("deployment release state is missing")
		}
		deployed, err := registry.GetDeploymentIntentForRuntimeRelease(ctx, orgID, state.ServiceID, state.Release.ID)
		if err != nil || deployed == nil {
			return fmt.Errorf("release-backed deployment intent no longer exists")
		}
		intent := deployed.Intent
		if intent.ID != state.DeploymentIntentID {
			return governedConflict(fmt.Sprintf("live deployment intent %s is not the governed intent %s", intent.ID, state.DeploymentIntentID))
		}
		if intent.ServiceID != state.ServiceID || intent.EnvironmentID != state.EnvironmentID ||
			intent.DeploymentUnitID == nil || *intent.DeploymentUnitID != state.DeploymentUnitID || deployed.Release.ID != state.Release.ID {
			return governedConflict(fmt.Sprintf("live deployment intent %s targets do not match governed state", intent.ID))
		}
		intentLedger, _ := ledgerResource(state, step, "bahia_deployment")
		if err := verifyGovernedMetadata("deployment intent "+intent.ID.String(), intent.Metadata, spec, intentLedger, true); err != nil {
			return err
		}
	case StepVerifyIdentity:
		if !validHexPublicKey(state.Soul.NostrPubkey) || state.RuntimeResult == nil || state.RuntimeResult.Status != "success" {
			return fmt.Errorf("identity verification evidence is missing")
		}
	case StepVerifyRelay:
		if state.Soul.Runtime.RuntimeBinding == "" || state.Soul.Runtime.RuntimePubkey == "" {
			return fmt.Errorf("relay verification evidence is missing")
		}
	case StepVerifyModel:
		if state.Soul.Runtime.Provider == "" || state.Soul.Runtime.Model == "" {
			return fmt.Errorf("model verification evidence is missing")
		}
	case StepVerifyReadiness:
		if state.Soul.Readiness == nil || state.Soul.Runtime.State != "running" {
			return fmt.Errorf("readiness verification evidence is missing")
		}
	}
	return nil
}

func ownedResource(spec ProvisioningSpec, kind, externalID string, created bool) ObservedResource {
	ownership := saga.OwnershipAdopted
	owner := ""
	if created {
		ownership, owner = saga.OwnershipCreated, spec.RunID
	}
	return ObservedResource{Kind: kind, ExternalID: externalID, Ownership: ownership, OwnerRunID: owner, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID}
}

func validHexPublicKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := nostr.PubKeyFromHex(value)
	return err == nil
}

func productionRequestEvent(run *domain.ProvisioningRun) (*nostr.Event, error) {
	if run == nil {
		return nil, fmt.Errorf("provisioning run is required")
	}
	id, err := nostr.IDFromHex(strings.TrimSpace(run.RequestID))
	if err != nil {
		return nil, err
	}
	pubkey, err := nostr.PubKeyFromHex(strings.TrimSpace(run.RequesterPubkey))
	if err != nil {
		return nil, err
	}
	return &nostr.Event{ID: id, PubKey: pubkey, Kind: nostr.Kind(kinds.SoulFactoryProvisioningRequest)}, nil
}

func cloneProductionSoul(in *domain.AgentSoul) *domain.AgentSoul {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		out := *in
		return &out
	}
	var out domain.AgentSoul
	if err := json.Unmarshal(data, &out); err != nil {
		copy := *in
		return &copy
	}
	return &out
}
