package soulfactory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

// fakeStepResource models one durable external resource owned by a step.
type fakeStepResource struct {
	Kind          string
	ExternalID    string
	Ownership     saga.Ownership
	OwnerRunID    string
	SpecHash      string
	CorrelationID string
}

func (r fakeStepResource) observed() ObservedResource {
	return ObservedResource{
		Kind: r.Kind, ExternalID: r.ExternalID, Ownership: r.Ownership,
		OwnerRunID: r.OwnerRunID, SpecHash: r.SpecHash, CorrelationID: r.CorrelationID,
	}
}

// fakeProvisioningPort is an in-memory ProvisioningStepPort that records the
// exact order of side effects and can inject failures at any step.
type fakeProvisioningPort struct {
	mu           sync.Mutex
	resources    map[OrderedStep]map[string]fakeStepResource
	events       []string
	ensureCount  map[OrderedStep]int
	removeCount  map[OrderedStep]int
	failEnsure   map[OrderedStep]error
	inspectErr   map[OrderedStep]error
	reserveCount int
	keyGenSeen   bool
	runtimeSeen  []string
	readiness    []string
	releaseProv  string
	preseed      map[OrderedStep][]fakeStepResource
}

func newFakeProvisioningPort() *fakeProvisioningPort {
	return &fakeProvisioningPort{
		resources:   map[OrderedStep]map[string]fakeStepResource{},
		ensureCount: map[OrderedStep]int{},
		removeCount: map[OrderedStep]int{},
		failEnsure:  map[OrderedStep]error{},
		inspectErr:  map[OrderedStep]error{},
		preseed:     map[OrderedStep][]fakeStepResource{},
	}
}

func (p *fakeProvisioningPort) Observe(_ context.Context, spec ProvisioningSpec, step OrderedStep) ([]ObservedResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.inspectErr[step]; err != nil {
		return nil, err
	}
	stored := map[string]fakeStepResource{}
	for key, resource := range p.resources[step] {
		stored[key] = resource
	}
	for _, resource := range p.preseed[step] {
		stored[resource.ExternalID] = resource
	}
	keys := make([]string, 0, len(stored))
	for key := range stored {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ObservedResource, 0, len(keys))
	for _, key := range keys {
		out = append(out, stored[key].observed())
	}
	return out, nil
}

func (p *fakeProvisioningPort) Ensure(_ context.Context, spec ProvisioningSpec, step OrderedStep, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "ensure:"+string(step))
	p.ensureCount[step]++
	if err := p.failEnsure[step]; err != nil {
		return err
	}
	if p.resources[step] == nil {
		p.resources[step] = map[string]fakeStepResource{}
	}
	for _, resource := range p.stepResources(spec, step) {
		p.resources[step][resource.ExternalID] = resource
	}
	return nil
}

func (p *fakeProvisioningPort) Remove(_ context.Context, _ ProvisioningSpec, step OrderedStep, resource saga.Resource, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "remove:"+string(step))
	p.removeCount[step]++
	if step == StepReserveIdentity {
		// Identity reservations are preserved across compensation: never re-key.
		return nil
	}
	for key, stored := range p.resources[step] {
		if saga.PublicResourceRef(resource.System, stored.Kind, stored.ExternalID) == resource.ExternalID || stored.ExternalID == resource.ExternalID {
			delete(p.resources[step], key)
		}
	}
	return nil
}

func (p *fakeProvisioningPort) stepResources(spec ProvisioningSpec, step OrderedStep) []fakeStepResource {
	created := func(kind, id string) fakeStepResource {
		return fakeStepResource{Kind: kind, ExternalID: id, Ownership: saga.OwnershipCreated, OwnerRunID: spec.RunID, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID}
	}
	switch step {
	case StepReserveIdentity:
		p.reserveCount++
		return []fakeStepResource{{Kind: "identity_reservation", ExternalID: "identity:" + spec.AgentID, Ownership: saga.OwnershipAdopted, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID}}
	case StepRegisterServiceUnit:
		return []fakeStepResource{created("service", "service:"+spec.AgentID), created("deployment_unit", "unit:"+spec.AgentID)}
	case StepSelectRuntimeRelease:
		p.releaseProv = string(spec.Runtime) + "-hiveci"
		return []fakeStepResource{created("runtime_release_binding", "release:"+spec.AgentID), created("deployment_intent", "intent:"+spec.AgentID)}
	case StepDeployViaBahia:
		return []fakeStepResource{created("bahia_deployment", "deploy:"+spec.AgentID)}
	case StepVerifyIdentity, StepVerifyRelay, StepVerifyModel:
		return []fakeStepResource{created(string(step), "verify:"+string(step)+":"+spec.AgentID)}
	case StepVerifyReadiness:
		p.runtimeSeen = append(p.runtimeSeen, string(spec.Runtime))
		p.readiness = append(p.readiness, readinessGates(spec.Runtime)...)
		return []fakeStepResource{created("readiness_verification", "verify:"+string(step)+":"+spec.AgentID)}
	default:
		return nil
	}
}

func readinessGates(runtime domain.RuntimeTarget) []string {
	if runtime == domain.RuntimeTargetMetiq {
		return []string{"metiq_bridge", "metiq_runtime", "metiq_model", "metiq_dm"}
	}
	return []string{"openclaw_container", "openclaw_gateway", "openclaw_route", "openclaw_signer", "openclaw_subscriptions", "openclaw_model", "openclaw_dm"}
}

func (p *fakeProvisioningPort) resourceCount(step OrderedStep) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resources[step])
}

func (p *fakeProvisioningPort) count(step OrderedStep) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ensureCount[step]
}

// fakeSoulPublish records one active-Soul / terminal projection publication.
type fakeSoulPublish struct {
	Stage       saga.Stage
	Active      bool
	FailureCode string
	ExternalID  string
	Kind        string
}

type fakeProjectionPort struct {
	mu           sync.Mutex
	published    map[saga.Stage][]fakeStepResource
	souls        []fakeSoulPublish
	activeCount  int
	failActive   error
	failTerminal map[saga.Stage]error
}

func newFakeProjectionPort() *fakeProjectionPort {
	return &fakeProjectionPort{published: map[saga.Stage][]fakeStepResource{}, failTerminal: map[saga.Stage]error{}}
}

func (p *fakeProjectionPort) Observe(_ context.Context, _ ProvisioningSpec, stage saga.Stage, _ *saga.Failure) ([]ObservedResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stored := p.published[stage]
	out := make([]ObservedResource, 0, len(stored))
	for _, resource := range stored {
		observed := resource.observed()
		out = append(out, observed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

func (p *fakeProjectionPort) Publish(_ context.Context, spec ProvisioningSpec, stage saga.Stage, failure *saga.Failure, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stage == saga.StageRunning {
		if err := p.failActive; err != nil {
			return err
		}
	} else if err := p.failTerminal[stage]; err != nil {
		return err
	}
	active := stage == saga.StageRunning
	correlation := spec.RequestID
	result := fakeStepResource{Kind: saga.ResourceProvisioningResult, ExternalID: "result:" + spec.AgentID + ":" + string(stage), Ownership: saga.OwnershipCreated, OwnerRunID: spec.RunID, SpecHash: spec.SpecHash, CorrelationID: correlation}
	soulKind := saga.ResourceAgentSoul
	soulStatus := "active"
	if !active {
		soulStatus = "rolled_back"
		if failure != nil {
			soulStatus = "failed"
		}
	}
	soul := fakeStepResource{Kind: soulKind, ExternalID: "soul:" + spec.AgentID + ":" + soulStatus, Ownership: saga.OwnershipCreated, OwnerRunID: spec.RunID, SpecHash: spec.SpecHash, CorrelationID: correlation}
	p.published[stage] = []fakeStepResource{result, soul}
	if active {
		p.activeCount++
	}
	p.souls = append(p.souls, fakeSoulPublish{Stage: stage, Active: active, ExternalID: soul.ExternalID, Kind: soulKind})
	return nil
}

func (p *fakeProjectionPort) Remove(_ context.Context, _ ProvisioningSpec, stage saga.Stage, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.published, stage)
	return nil
}

func (p *fakeProjectionPort) activePublished() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeCount > 0
}

func openClawRequest(agent string) GovernedProvisioningRequest {
	return GovernedProvisioningRequest{
		RequestID: "request-" + agent, RunID: "run-" + agent, AgentID: agent,
		SpecHash: "sha256:" + agent, Runtime: domain.RuntimeTargetOpenClaw,
	}
}

func metiqRequest(agent string) GovernedProvisioningRequest {
	request := openClawRequest(agent)
	request.RequestID = "request-metiq-" + agent
	request.RunID = "run-metiq-" + agent
	request.SpecHash = "sha256:metiq:" + agent
	request.Runtime = domain.RuntimeTargetMetiq
	return request
}

func newGovernedProvisioner(t *testing.T, dir string, request GovernedProvisioningRequest, steps *fakeProvisioningPort, projection *fakeProjectionPort) (*GovernedProvisioner, saga.Store) {
	t.Helper()
	store, err := saga.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	provisioner, err := NewGovernedProvisioner(store, request, steps, projection)
	if err != nil {
		t.Fatal(err)
	}
	return provisioner, store
}

func performProvision(t *testing.T, provisioner *GovernedProvisioner) *saga.Report {
	t.Helper()
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := provisioner.Reconcile(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestGovernedProvisioningOrderIsExact(t *testing.T) {
	want := []OrderedStep{
		StepReserveIdentity, StepRegisterServiceUnit, StepSelectRuntimeRelease, StepDeployViaBahia,
		StepVerifyIdentity, StepVerifyRelay, StepVerifyModel, StepVerifyReadiness, StepPublishActiveSoul,
	}
	got := OrderedSteps()
	if strings.Join(stepStrings(got), ",") != strings.Join(stepStrings(want), ",") {
		t.Fatalf("ordered steps = %v", got)
	}

	steps := newFakeProvisioningPort()
	projection := newFakeProjectionPort()
	provisioner, _ := newGovernedProvisioner(t, t.TempDir(), openClawRequest("order"), steps, projection)
	report := performProvision(t, provisioner)
	if report.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	wantEvents := []string{
		"ensure:reserve_identity", "ensure:register_service_unit", "ensure:select_runtime_release",
		"ensure:deploy_via_bahia", "ensure:configure_verify_identity", "ensure:configure_verify_relay",
		"ensure:verify_model", "ensure:verify_readiness",
	}
	if strings.Join(steps.events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("side-effect order = %v", steps.events)
	}
	if len(projection.souls) != 1 || projection.souls[0].Stage != saga.StageRunning || !projection.souls[0].Active {
		t.Fatalf("active Soul publication = %#v", projection.souls)
	}
	if steps.count(StepRegisterServiceUnit) != 1 || steps.count(StepSelectRuntimeRelease) != 1 || steps.count(StepDeployViaBahia) != 1 {
		t.Fatal("Bahia steps were not executed exactly once")
	}
}

func stepStrings(steps []OrderedStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, string(step))
	}
	return out
}

func TestGovernedProvisioningPartialFailureNeverPublishesActiveSoul(t *testing.T) {
	for _, failed := range []OrderedStep{
		StepReserveIdentity, StepRegisterServiceUnit, StepSelectRuntimeRelease, StepDeployViaBahia,
		StepVerifyIdentity, StepVerifyRelay, StepVerifyModel, StepVerifyReadiness,
	} {
		failed := failed
		t.Run(string(failed), func(t *testing.T) {
			steps := newFakeProvisioningPort()
			steps.failEnsure[failed] = errors.New("injected step failure")
			projection := newFakeProjectionPort()
			provisioner, store := newGovernedProvisioner(t, t.TempDir(), openClawRequest("fail-"+string(failed)), steps, projection)
			ctx := context.Background()
			if _, err := provisioner.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := provisioner.Reconcile(ctx, false); err == nil {
				t.Fatal("expected injected failure")
			}
			if projection.activePublished() {
				t.Fatal("partial failure published an ACTIVE Soul")
			}
			run, err := store.Load(ctx, provisioner.request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Stage != saga.StageRolledBack {
				t.Fatalf("stage = %s", run.Stage)
			}
		})
	}
}

func TestGovernedProvisioningFailedActivePublishNeverActivatesSoul(t *testing.T) {
	steps := newFakeProvisioningPort()
	projection := newFakeProjectionPort()
	projection.failActive = errors.New("kind 31951 publish failed")
	provisioner, store := newGovernedProvisioner(t, t.TempDir(), openClawRequest("publish-fail"), steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected active publish failure")
	}
	if projection.activePublished() {
		t.Fatal("failed active publish still activated the Soul")
	}
	run, err := store.Load(ctx, provisioner.request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Stage != saga.StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	if !steps.allCreatedCompensated(run) {
		t.Fatal("created resources were orphaned by compensation")
	}
}

func TestGovernedProvisioningReplayAndRetryAreIdempotent(t *testing.T) {
	steps := newFakeProvisioningPort()
	projection := newFakeProjectionPort()
	request := openClawRequest("replay")
	provisioner, store := newGovernedProvisioner(t, t.TempDir(), request, steps, projection)
	ctx := context.Background()
	performProvision(t, provisioner)

	beforeEnsure := map[OrderedStep]int{}
	for _, step := range OrderedSteps() {
		beforeEnsure[step] = steps.count(step)
	}
	// Exact replay of the same request must not repeat any side effect.
	if _, err := provisioner.Reconcile(ctx, false); err != nil {
		t.Fatal(err)
	}
	for _, step := range OrderedSteps() {
		if steps.count(step) != beforeEnsure[step] {
			t.Fatalf("replay repeated step %s", step)
		}
	}
	if projection.activeCount != 1 {
		t.Fatalf("replay published %d active Souls", projection.activeCount)
	}
	if steps.resourceCount(StepRegisterServiceUnit) != 2 || steps.resourceCount(StepSelectRuntimeRelease) != 2 {
		t.Fatal("replay duplicated Bahia resources")
	}
	// Conflicting re-delivery of the same request identity is rejected.
	if _, err := provisioner.engine.Start(ctx, request.RequestID, "different-run", request.AgentID, request.SpecHash); !errors.Is(err, saga.ErrConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	run, _ := store.Load(ctx, request.RequestID)
	if run.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", run.Stage)
	}
}

func TestGovernedProvisioningCompensationPreservesIdentityWithoutRekey(t *testing.T) {
	steps := newFakeProvisioningPort()
	steps.failEnsure[StepDeployViaBahia] = errors.New("deploy failed closed")
	projection := newFakeProjectionPort()
	request := openClawRequest("compensate")
	provisioner, store := newGovernedProvisioner(t, t.TempDir(), request, steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected deploy failure")
	}
	run, _ := store.Load(ctx, request.RequestID)
	if run.Stage != saga.StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	// Identity reservation is preserved: never compensated, never re-keyed.
	if steps.removeCount[StepReserveIdentity] != 0 {
		t.Fatal("compensation removed the identity reservation")
	}
	if steps.reserveCount != 1 {
		t.Fatalf("identity was reserved %d times", steps.reserveCount)
	}
	if steps.keyGenSeen {
		t.Fatal("governed provisioning generated agent key material")
	}
	if steps.resourceCount(StepReserveIdentity) != 1 {
		t.Fatal("identity reservation did not survive rollback")
	}
	// Saga-created resources are compensated.
	if steps.resourceCount(StepRegisterServiceUnit) != 0 || steps.resourceCount(StepSelectRuntimeRelease) != 0 {
		t.Fatal("saga-created resources survived rollback")
	}
}

func TestGovernedProvisioningStatusSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	steps := newFakeProvisioningPort()
	steps.inspectErr[StepDeployViaBahia] = &saga.SafeError{Code: "stage_failed", Retryable: true}
	projection := newFakeProjectionPort()
	request := openClawRequest("restart")
	provisioner, store := newGovernedProvisioner(t, dir, request, steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected retryable deploy outage")
	}
	inflight, err := store.Load(ctx, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if inflight.Stage != saga.StageFailedRecoverable || inflight.ResumeStage != saga.StageRuntimeAllocated {
		t.Fatalf("in-flight checkpoint = %s/%s", inflight.Stage, inflight.ResumeStage)
	}
	if steps.count(StepReserveIdentity) != 1 || steps.count(StepRegisterServiceUnit) != 1 || steps.count(StepSelectRuntimeRelease) != 1 {
		t.Fatal("completed steps were not durably checkpointed before the outage")
	}

	// Simulated process restart: new store handle and new saga over the same directory.
	delete(steps.inspectErr, StepDeployViaBahia)
	restartedStore, err := saga.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGovernedProvisioner(restartedStore, request, steps, projection)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := restarted.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconstructed.Stage != saga.StageFailedRecoverable || reconstructed.Version != inflight.Version {
		t.Fatalf("restart lost durable status: %#v", reconstructed)
	}
	resumeEnsure := steps.count(StepDeployViaBahia)
	report, err := restarted.Retry(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	if steps.count(StepReserveIdentity) != 1 || steps.count(StepRegisterServiceUnit) != 1 || steps.count(StepSelectRuntimeRelease) != 1 {
		t.Fatal("restart replayed completed side effects")
	}
	if steps.count(StepDeployViaBahia) != resumeEnsure+1 {
		t.Fatal("restart did not resume the interrupted deploy step once")
	}
	if !projection.activePublished() {
		t.Fatal("restart did not publish the active Soul")
	}
}

func TestGovernedProvisioningRestartRejectsRuntimeTargetChange(t *testing.T) {
	dir := t.TempDir()
	steps := newFakeProvisioningPort()
	steps.inspectErr[StepDeployViaBahia] = &saga.SafeError{Code: "stage_failed", Retryable: true}
	projection := newFakeProjectionPort()
	request := openClawRequest("runtime-switch")
	provisioner, store := newGovernedProvisioner(t, dir, request, steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected interrupted OpenClaw deployment")
	}
	inflight, err := store.Load(ctx, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if inflight.Stage != saga.StageFailedRecoverable || inflight.ResumeStage != saga.StageRuntimeAllocated {
		t.Fatalf("in-flight checkpoint = %s/%s", inflight.Stage, inflight.ResumeStage)
	}
	if inflight.SpecHash == request.SpecHash {
		t.Fatal("durable spec hash did not bind the runtime target")
	}

	delete(steps.inspectErr, StepDeployViaBahia)
	switched := request
	switched.Runtime = domain.RuntimeTargetMetiq
	restartedStore, err := saga.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGovernedProvisioner(restartedStore, switched, steps, projection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Start(ctx); !errors.Is(err, saga.ErrConflict) {
		t.Fatalf("runtime-switch restart error = %v", err)
	}
	if _, err := restarted.Reconcile(ctx, false); !errors.Is(err, saga.ErrConflict) {
		t.Fatalf("runtime-switch reconcile error = %v", err)
	}
	if _, err := restarted.Retry(ctx, false); !errors.Is(err, saga.ErrConflict) {
		t.Fatalf("runtime-switch retry error = %v", err)
	}
	after, err := restartedStore.Load(ctx, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stage != inflight.Stage || after.ResumeStage != inflight.ResumeStage || after.Version != inflight.Version {
		t.Fatalf("runtime mismatch changed durable state: before=%#v after=%#v", inflight, after)
	}
	if steps.count(StepDeployViaBahia) != 0 {
		t.Fatal("runtime mismatch resumed deployment")
	}
	if steps.releaseProv != "openclaw-hiveci" {
		t.Fatalf("runtime release switched to %q", steps.releaseProv)
	}
	if projection.activePublished() {
		t.Fatal("runtime mismatch activated the Soul")
	}
}

func TestGovernedProvisioningOpenClawRuntimePath(t *testing.T) {
	steps := newFakeProvisioningPort()
	projection := newFakeProjectionPort()
	request := openClawRequest("openclaw")
	provisioner, _ := newGovernedProvisioner(t, t.TempDir(), request, steps, projection)
	report := performProvision(t, provisioner)
	if report.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	wantGates := strings.Join(readinessGates(domain.RuntimeTargetOpenClaw), ",")
	if strings.Join(steps.readiness, ",") != wantGates {
		t.Fatalf("openclaw readiness gates = %v", steps.readiness)
	}
	if len(steps.runtimeSeen) != 1 || steps.runtimeSeen[0] != string(domain.RuntimeTargetOpenClaw) {
		t.Fatalf("runtime path = %v", steps.runtimeSeen)
	}
	if steps.releaseProv != "openclaw-hiveci" {
		t.Fatalf("release provider = %q", steps.releaseProv)
	}
}

func TestGovernedProvisioningMetiqReleasePath(t *testing.T) {
	steps := newFakeProvisioningPort()
	projection := newFakeProjectionPort()
	request := metiqRequest("metiq")
	provisioner, store := newGovernedProvisioner(t, t.TempDir(), request, steps, projection)
	ctx := context.Background()
	report := performProvision(t, provisioner)
	if report.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	if steps.releaseProv != "metiq-hiveci" {
		t.Fatalf("metiq release provider = %q", steps.releaseProv)
	}
	if strings.Join(steps.readiness, ",") != strings.Join(readinessGates(domain.RuntimeTargetMetiq), ",") {
		t.Fatalf("metiq readiness gates = %v", steps.readiness)
	}
	if !projection.activePublished() {
		t.Fatal("metiq path did not publish the active Soul")
	}
	run, _ := store.Load(ctx, request.RequestID)
	if run.Stage != saga.StageRunning {
		t.Fatalf("stage = %s", run.Stage)
	}
}

func TestGovernedProvisioningUnverifiedReleaseFailsClosed(t *testing.T) {
	steps := newFakeProvisioningPort()
	steps.failEnsure[StepSelectRuntimeRelease] = errors.New("no verified runtime release for metiq")
	projection := newFakeProjectionPort()
	provisioner, _ := newGovernedProvisioner(t, t.TempDir(), metiqRequest("metiq-unverified"), steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected fail-closed release selection")
	}
	if projection.activePublished() {
		t.Fatal("unverified release activated a Soul")
	}
	if steps.count(StepDeployViaBahia) != 0 {
		t.Fatal("deployment proceeded without a verified release")
	}
}

func TestGovernedProvisioningSpecConflictRollsBackWithoutActivating(t *testing.T) {
	steps := newFakeProvisioningPort()
	steps.preseed[StepRegisterServiceUnit] = []fakeStepResource{{
		Kind: "service", ExternalID: "service:conflict", Ownership: saga.OwnershipCreated,
		OwnerRunID: "another-run", SpecHash: "sha256:other", CorrelationID: "another-request",
	}}
	projection := newFakeProjectionPort()
	request := openClawRequest("conflict")
	provisioner, _ := newGovernedProvisioner(t, t.TempDir(), request, steps, projection)
	ctx := context.Background()
	if _, err := provisioner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Reconcile(ctx, false); err == nil {
		t.Fatal("expected ownership/spec conflict")
	}
	if projection.activePublished() {
		t.Fatal("conflict activated a Soul")
	}
	if steps.count(StepRegisterServiceUnit) != 0 {
		t.Fatal("conflicting stage mutated external state")
	}
}

func (p *fakeProvisioningPort) allCreatedCompensated(_ *saga.Run) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resources[StepRegisterServiceUnit]) == 0 && len(p.resources[StepSelectRuntimeRelease]) == 0 && len(p.resources[StepDeployViaBahia]) == 0
}
