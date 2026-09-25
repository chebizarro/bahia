package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
	"github.com/stretchr/testify/require"
)

type unavailableSoulGenerator struct{ calls int }

func (g *unavailableSoulGenerator) Generate(context.Context, domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	g.calls++
	return nil, &saga.SafeError{Code: "dependency_unavailable", Retryable: true}
}

// Start at the same request adapter and reactor handler used by ContextVM,
// without seeding the saga store or calling saga.Engine in the fixture.
func startOperatorFixture(t *testing.T) (*ProductionGovernedProvisioner, *Reactor, *nostr.Event, string, *unavailableSoulGenerator) {
	t.Helper()
	signer := newFakeSigner(t)
	generator := &unavailableSoulGenerator{}
	reactor := NewReactor(Config{SoulFactoryPubkey: signer.pubkey}, generator, signer, slogDefaultLogger())
	capture := attachPublishCapture(reactor)
	reactor.findProvisioningResultFn = func(_ context.Context, requestID string) (*nostr.Event, error) {
		for i := len(capture.events) - 1; i >= 0; i-- {
			event := capture.events[i]
			if int(event.Kind) == domain.KindProvisioningResult && tagValue(event.Tags, tagEvent) == requestID {
				return event, nil
			}
		}
		return nil, nil
	}
	registry, _, _, _, _, _ := newSoulFactoryRegistryHarness()
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{}, slogDefaultLogger())
	require.NoError(t, err)
	full := &FullProvisioner{reactor: reactor, bahiaIntegration: integration}
	dir := t.TempDir()
	provisioner, err := NewProductionGovernedProvisioner(full, ProductionGovernedProvisionerConfig{StateDir: dir})
	require.NoError(t, err)
	require.NoError(t, reactor.InstallProvisioningEngine(provisioner))
	request := contextVMTestRequest(t, ContextVMMethodProvision, fmt.Sprintf(`{"agent_id":"saga-canary","brief":"Exercise durable recovery","spec_hash":"test-spec","runtime":{"target":"openclaw","runtime_release_id":%q}}`, uuid.NewString()))
	reactor.config.AuthorizedPubkeys = []string{request.Event.PubKey.Hex()}
	event, err := contextVMProvisioningEvent(request)
	require.NoError(t, err)
	reactor.handleProvisioningRequest(t.Context(), event)
	require.Equal(t, 1, generator.calls, "run: %+v", reactor.runs[event.ID.Hex()])
	return provisioner, reactor, event, dir, generator
}

func TestNormalProvisioningPopulatesSagaMetricsAndStuckAlert(t *testing.T) {
	p, _, event, dir, _ := startOperatorFixture(t)
	store, err := saga.NewFileStore(filepath.Join(dir, "sagas"))
	require.NoError(t, err)
	runs, err := store.List(t.Context())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	run := runs[0]
	require.Equal(t, event.ID.Hex(), run.RequestID)
	require.Equal(t, saga.StageFailedRecoverable, run.Stage)
	require.Equal(t, saga.StageIdentityReserved, run.ResumeStage)
	require.Greater(t, run.Version, uint64(1))
	require.NotEmpty(t, run.Failures)
	monitor, err := p.ProvisioningMonitor("bahia-test", "test")
	require.NoError(t, err)
	provider := telemetry.Setup(telemetry.Config{Enabled: true, ServiceName: "bahia-test"}, nil)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	provider.SetOpenClawSagaExporter(monitor.WritePrometheus)
	response := httptest.NewRecorder()
	provider.MetricsHandler()(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, response.Body.String(), `request_id="`+event.ID.Hex()+`"`)
	require.Contains(t, response.Body.String(), `stage="failed_recoverable"`)
	require.Contains(t, response.Body.String(), "bahia_openclaw_provisioning_retries{")

	// Advance observation time, not persisted history or a sleep. The unchanged
	// run exceeds StageStuck's >900s predicate throughout its five-minute hold.
	for _, age := range []time.Duration{16 * time.Minute, 21 * time.Minute} {
		aged, err := saga.NewMonitor(saga.MonitorConfig{Store: store, Instance: "test", Build: "test", Now: func() time.Time { return run.UpdatedAt.Add(age) }})
		require.NoError(t, err)
		snapshot, err := aged.Collect(t.Context())
		require.NoError(t, err)
		require.Len(t, snapshot.Runs, 1)
		require.Greater(t, snapshot.Runs[0].StageAge.Seconds(), float64(900))
		require.Equal(t, saga.StageFailedRecoverable, snapshot.Runs[0].Stage)
	}
}

func TestProductionOperatorRestartRetryAndSafeAbort(t *testing.T) {
	p, reactor, event, dir, generator := startOperatorFixture(t)
	var kinds []nostr.Kind
	publish := reactor.publishFn
	reactor.publishFn = func(ctx context.Context, event *nostr.Event, relays []string) error {
		kinds = append(kinds, event.Kind)
		return publish(ctx, event, relays)
	}
	restarted, err := NewProductionGovernedProvisioner(p.full, ProductionGovernedProvisionerConfig{StateDir: dir})
	require.NoError(t, err)
	requestID := event.ID.Hex()
	before, err := restarted.store.Load(t.Context(), requestID)
	require.NoError(t, err)
	for _, operation := range []saga.OperatorCommand{saga.CommandInspect, saga.CommandRetry, saga.CommandReconcile, saga.CommandSafeAbort} {
		report, err := restarted.ExecuteProvisioningCommand(t.Context(), saga.Command{RequestID: requestID, Operation: operation, DryRun: true})
		require.NoError(t, err)
		require.True(t, report.DryRun)
	}
	after, err := restarted.store.Load(t.Context(), requestID)
	require.NoError(t, err)
	require.Equal(t, before, after, "dry-run commands must not mutate checkpoints")
	require.Equal(t, 1, generator.calls)

	// The reservation already exists, but preparation did not complete. Retry
	// must resume preparation instead of treating that reservation as completion.
	_, err = restarted.ExecuteProvisioningCommand(t.Context(), saga.Command{RequestID: requestID, Operation: saga.CommandRetry})
	require.Error(t, err)
	require.Equal(t, 2, generator.calls, "retry skipped unfinished identity preparation")
	after, err = restarted.store.Load(t.Context(), requestID)
	require.NoError(t, err)
	require.Equal(t, before.RunID, after.RunID)
	require.Equal(t, saga.StageFailedRecoverable, after.Stage)
	require.Greater(t, after.Version, before.Version)

	report, err := restarted.ExecuteProvisioningCommand(t.Context(), saga.Command{RequestID: requestID, Operation: saga.CommandSafeAbort})
	require.NoError(t, err)
	require.Equal(t, saga.StageRolledBack, report.Stage)
	state, err := restarted.states.load(t.Context(), requestID)
	require.NoError(t, err)
	require.Equal(t, saga.StageRolledBack, state.TerminalResultStage)
	require.False(t, state.ActiveSoulPublished)
	require.Contains(t, kinds, nostr.Kind(cascadia.CAS_CP_STATE))
	require.Contains(t, kinds, nostr.Kind(cascadia.CAS_AUDIT))
	// A reserve-only identity record is intentionally retained, not a generated
	// key or an owned resource that abort is permitted to destroy.
	reservation, _, err := restarted.states.reservation(t.Context(), ProvisioningSpec{AgentID: state.AgentID}, false)
	require.NoError(t, err)
	require.Equal(t, before.RunID, reservation.RunID)
}

func TestProductionOperatorRefusesIdentityDriftAndConcurrentWork(t *testing.T) {
	p, _, event, dir, generator := startOperatorFixture(t)
	command := saga.Command{Operation: saga.CommandRetry, RequestID: event.ID.Hex()}
	other, err := NewProductionGovernedProvisioner(p.full, ProductionGovernedProvisionerConfig{StateDir: dir})
	require.NoError(t, err)
	unlock, err := p.states.lockRequest(t.Context(), command.RequestID)
	require.NoError(t, err)
	_, err = other.ExecuteProvisioningCommand(t.Context(), command)
	require.ErrorIs(t, err, saga.ErrConflict)
	unlock()
	state, err := p.states.load(t.Context(), command.RequestID)
	require.NoError(t, err)
	state.Runtime = domain.RuntimeTargetMetiq
	state.Resolved.Runtime.Target = domain.RuntimeTargetMetiq
	require.NoError(t, p.states.save(t.Context(), state))
	_, err = other.ExecuteProvisioningCommand(t.Context(), command)
	require.ErrorIs(t, err, saga.ErrConflict)
	require.Equal(t, 1, generator.calls)
}

func TestProductionReplayUsesImmutableResolvedInputs(t *testing.T) {
	p, reactor, event, _, generator := startOperatorFixture(t)
	before, err := p.store.Load(t.Context(), event.ID.Hex())
	require.NoError(t, err)
	// Resolution would now fail. A replay uses the captured request snapshot,
	// not a new mutable template, draft, or fleet config revision.
	reactor.getFleetConfigFn = func(context.Context) (*FleetConfigSnapshot, error) {
		return nil, fmt.Errorf("configuration unavailable")
	}
	reactor.handleProvisioningRequest(t.Context(), event)
	require.Equal(t, 2, generator.calls)
	after, err := p.store.Load(t.Context(), event.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, before.RunID, after.RunID)
	state, err := p.states.load(t.Context(), event.ID.Hex())
	require.NoError(t, err)
	state.Resolved.SignetIdentity = &OpenClawSignetIdentityContract{BunkerURL: "bunker://must-not-persist"}
	require.NoError(t, p.states.save(t.Context(), state))
	data, err := os.ReadFile(p.states.requestPath(event.ID.Hex()))
	require.NoError(t, err)
	require.NotContains(t, string(data), "bunker://")
}

func TestProductionUnpreparedReservationStillRejectsOtherRequest(t *testing.T) {
	p, _, event, _, generator := startOperatorFixture(t)
	request, err := ParseProvisioningRequestEvent(event)
	require.NoError(t, err)
	request.EventID = strings.Repeat("f", 64)
	run := &domain.ProvisioningRun{ID: uuid.New(), RequestID: request.EventID, RequesterPubkey: request.Requester}
	_, err = p.Provision(t.Context(), request, run)
	require.Error(t, err)
	require.Equal(t, 1, generator.calls, "a different request must not generate against the existing reservation")
}

func TestProductionSuccessDeliveryReplaysStableEvents(t *testing.T) {
	for _, failedKind := range []nostr.Kind{domain.KindProvisioningResult, nostr.Kind(cascadia.CAS_CP_STATE), nostr.Kind(cascadia.CAS_AUDIT)} {
		t.Run(fmt.Sprint(failedKind), func(t *testing.T) {
			p, reactor, event, dir, _ := startOperatorFixture(t)
			state, err := p.states.load(t.Context(), event.ID.Hex())
			require.NoError(t, err)
			// Isolate terminal delivery after the active-Soul commit point; the
			// normal-path test separately proves engine-generated checkpoints.
			state.Soul = domain.AgentSoul{AgentID: state.AgentID, Status: domain.SoulStatusActive, NostrPubkey: reactor.config.SoulFactoryPubkey}
			state.ActiveSoulPublished = true
			require.NoError(t, p.states.save(t.Context(), state))
			accepted := map[nostr.Kind]nostr.ID{}
			fail := true
			calls := 0
			reactor.publishFn = func(_ context.Context, e *nostr.Event, _ []string) error {
				calls++
				if prior, ok := accepted[e.Kind]; ok {
					require.Equal(t, prior, e.ID, "response-loss replay changed signed event identity")
				}
				accepted[e.Kind] = e.ID
				if fail && e.Kind == failedKind {
					return errors.New("relay accepted but OK was lost")
				}
				return nil
			}
			require.Error(t, p.deliverSuccess(t.Context(), state))
			restarted, err := NewProductionGovernedProvisioner(p.full, ProductionGovernedProvisionerConfig{StateDir: dir})
			require.NoError(t, err)
			state, err = restarted.states.load(t.Context(), event.ID.Hex())
			require.NoError(t, err)
			require.NotNil(t, state.SuccessResult)
			require.False(t, state.SuccessDelivered)
			fail = false
			require.NoError(t, restarted.deliverSuccess(t.Context(), state))
			require.True(t, state.SuccessDelivered)
			require.Len(t, accepted, 3)
			previousCalls := calls
			state, err = restarted.states.load(t.Context(), event.ID.Hex())
			require.NoError(t, err)
			require.NoError(t, restarted.deliverSuccess(t.Context(), state))
			require.Equal(t, previousCalls, calls, "delivered success must not publish again")
		})
	}
}
