package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

type autoSnapshotStub struct {
	mu       sync.Mutex
	snapshot ApplySnapshot
	resolve  error
	loadErr  error
}

func (s *autoSnapshotStub) LoadAutoApplySnapshot(_ context.Context, _, _ uuid.UUID) (ApplySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, s.loadErr
}

func (s *autoSnapshotStub) ResolvePinnedSecrets(_ context.Context, _ ApplySnapshot) (map[string]string, func(), error) {
	if s.resolve != nil {
		return nil, nil, s.resolve
	}
	return map[string]string{}, func() {}, nil
}

type autoEffectStub struct {
	applied            bool
	applyCalls         int
	panicPostApplyRead bool
}

type processCrashEffect struct {
	marker string
	mode   string
}

func (e processCrashEffect) Observe(context.Context, ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	if _, err := os.Stat(e.marker); errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	} else if err != nil {
		return false, nil, err
	}
	return true, &domain.RuntimeObservation{ID: uuid.MustParse("00000000-0000-0000-0000-000000000778")}, nil
}
func (e processCrashEffect) Apply(context.Context, ApplySnapshot, map[string]string) error {
	if _, err := os.Stat(e.marker); err == nil {
		return errors.New("external effect repeated")
	}
	if err := os.WriteFile(e.marker, []byte("one effect"), 0o600); err != nil {
		return err
	}
	if e.mode == "effect" {
		os.Exit(23)
	}
	return nil
}

func TestCanonicalAutoApplyProcessCrashReplay(t *testing.T) {
	if mode := os.Getenv("BAHIA_AUTO_APPLY_CRASH_HELPER"); mode != "" {
		path := os.Getenv("BAHIA_AUTO_APPLY_OUTBOX")
		marker := os.Getenv("BAHIA_AUTO_APPLY_MARKER")
		var snapshot ApplySnapshot
		require.NoError(t, json.Unmarshal([]byte(os.Getenv("BAHIA_AUTO_APPLY_SNAPSHOT")), &snapshot))
		key, err := nostr.SecretKeyFromHex(os.Getenv("BAHIA_AUTO_APPLY_KEY"))
		require.NoError(t, err)
		outbox, err := localstore.OpenOutbox(path)
		require.NoError(t, err)
		lock, err := service.NewRuntimeApplyLock(path)
		require.NoError(t, err)
		executor := &CanonicalAutoApplyExecutor{
			Source: &autoSnapshotStub{snapshot: snapshot}, Effect: processCrashEffect{marker: marker, mode: mode},
			Lock: lock, Outbox: outbox, Signer: keyer.NewPlainKeySigner(key),
			Family: os.Getenv("BAHIA_AUTO_APPLY_FAMILY"),
			Enqueue: func(_ context.Context, event nostr.Event) error {
				if mode == "publish" {
					var content struct {
						Phase string `json:"phase"`
					}
					if json.Unmarshal([]byte(event.Content), &content) == nil && content.Phase == "effect_observed" {
						os.Exit(24)
					}
				}
				_, err := outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "default"})
				return err
			},
		}
		_, err = executor.AutoRemediateDesiredState(context.Background(), snapshot.Spec.ServiceID, snapshot.Spec.EnvironmentID, nil)
		t.Fatalf("helper did not crash at %s boundary: %v", mode, err)
	}
	for _, tc := range []struct {
		mode   string
		code   int
		family string
	}{
		{"effect", 23, autoApplyReceiptFamily},
		{"publish", 24, autoApplyReceiptFamily},
		{"effect", 23, toolBuildReceiptFamily},
		{"publish", 24, toolBuildReceiptFamily},
		{"effect", 23, toolDeployReceiptFamily},
		{"publish", 24, toolDeployReceiptFamily},
		{"effect", 23, llmRouteReceiptFamily},
		{"publish", 24, llmRouteReceiptFamily},
	} {
		t.Run(tc.family+"-"+tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			path, marker := filepath.Join(dir, "outbox.bolt"), filepath.Join(dir, "effect.marker")
			key := nostr.Generate()
			serviceID, envID := uuid.New(), uuid.New()
			snapshot := ApplySnapshot{IntentID: uuid.New(), DecisionEventID: nostr.Generate().Public().Hex(),
				DecisionActor: key.Public().Hex(), ServiceEventID: "service-v1", StateEventID: "state-v1",
				EnvironmentEventID: "env-v1", ArtifactEventID: "artifact-v1", IntentEventID: "intent-v1",
				Service: domain.Service{ID: serviceID}, Environment: domain.Environment{ID: envID},
				Spec: domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "sha256:desired"}}
			if tc.family == toolBuildReceiptFamily || tc.family == toolDeployReceiptFamily {
				snapshot.Tool = &ToolEffectInput{RequestEventID: "signed-tool-request", Step: tc.family}
			}
			if tc.family == llmRouteReceiptFamily {
				snapshot.LLM = &LLMEffectInput{RequestEventID: "signed-llm-request", GatewayRef: "gateway"}
			}
			raw, err := json.Marshal(snapshot)
			require.NoError(t, err)
			cmd := exec.Command(os.Args[0], "-test.run=^TestCanonicalAutoApplyProcessCrashReplay$")
			cmd.Env = append(os.Environ(),
				"BAHIA_AUTO_APPLY_CRASH_HELPER="+tc.mode, "BAHIA_AUTO_APPLY_OUTBOX="+path,
				"BAHIA_AUTO_APPLY_FAMILY="+tc.family,
				"BAHIA_AUTO_APPLY_MARKER="+marker, "BAHIA_AUTO_APPLY_KEY="+key.Hex(),
				"BAHIA_AUTO_APPLY_SNAPSHOT="+string(raw))
			err = cmd.Run()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited)
			require.Equal(t, tc.code, exited.ExitCode())
			require.FileExists(t, marker)
			outbox, err := localstore.OpenOutbox(path)
			require.NoError(t, err)
			defer outbox.Close()
			lock, err := service.NewRuntimeApplyLock(path)
			require.NoError(t, err)
			executor := &CanonicalAutoApplyExecutor{
				Source: &autoSnapshotStub{snapshot: snapshot}, Effect: processCrashEffect{marker: marker},
				Lock: lock, Outbox: outbox, Signer: keyer.NewPlainKeySigner(key), Family: tc.family,
				Enqueue: func(_ context.Context, event nostr.Event) error {
					_, err := outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "default"})
					return err
				},
			}
			require.NoError(t, executor.ReconcileReceipts(context.Background()))
			receipt, err := executor.readReceipt(snapshot.IntentID)
			require.NoError(t, err)
			require.Equal(t, "terminal", receipt.Phase)
			require.Equal(t, "one effect", string(mustReadAutoMarker(t, marker)))
		})
	}
}

func mustReadAutoMarker(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

func (e *autoEffectStub) Observe(_ context.Context, _ ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	if e.applied && e.panicPostApplyRead {
		panic("kill between effect and checkpoint")
	}
	if !e.applied {
		return false, nil, nil
	}
	return true, &domain.RuntimeObservation{ID: uuid.MustParse("00000000-0000-0000-0000-000000000777")}, nil
}

func (e *autoEffectStub) Apply(_ context.Context, _ ApplySnapshot, _ map[string]string) error {
	e.applyCalls++
	e.applied = true
	return nil
}

type autoApplyFixture struct {
	source   *autoSnapshotStub
	effect   *autoEffectStub
	outbox   *localstore.Outbox
	executor *CanonicalAutoApplyExecutor
	enqueued []nostr.Event
}

func newAutoApplyFixture(t *testing.T) *autoApplyFixture {
	t.Helper()
	outboxPath := filepath.Join(t.TempDir(), "outbox.bolt")
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	lock, err := service.NewRuntimeApplyLock(outboxPath)
	require.NoError(t, err)
	key := nostr.Generate()
	serviceID, envID, intentID := uuid.New(), uuid.New(), uuid.New()
	source := &autoSnapshotStub{snapshot: ApplySnapshot{
		IntentID: intentID, DecisionEventID: nostr.Generate().Public().Hex(),
		DecisionCoordinate: "deployment:" + intentID.String(), DecisionActor: key.Public().Hex(),
		ServiceEventID: "service-v1", StateEventID: "state-v1", IntentEventID: "intent-v1",
		EnvironmentEventID: "environment-v1", ArtifactEventID: "artifact-v1",
		ReleaseEventID: "release-v1", Service: domain.Service{ID: serviceID},
		Environment: domain.Environment{ID: envID}, Spec: domain.DesiredServiceSpec{
			ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "sha256:desired",
		},
	}}
	f := &autoApplyFixture{source: source, effect: &autoEffectStub{}, outbox: outbox}
	f.executor = &CanonicalAutoApplyExecutor{
		Source: source, Effect: f.effect, Lock: lock, Outbox: outbox,
		Signer: keyer.NewPlainKeySigner(key),
		Enqueue: func(_ context.Context, ev nostr.Event) error {
			f.enqueued = append(f.enqueued, ev)
			_, err := outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "default"})
			return err
		},
	}
	return f
}

func (f *autoApplyFixture) run() (*domain.RuntimeObservation, error) {
	s := f.source.snapshot
	return f.executor.AutoRemediateDesiredState(context.Background(), s.Spec.ServiceID, s.Spec.EnvironmentID, nil)
}

func TestCanonicalAutoApplyReplayAfterEffectBeforeCheckpoint(t *testing.T) {
	f := newAutoApplyFixture(t)
	f.effect.panicPostApplyRead = true
	require.Panics(t, func() { _, _ = f.run() })
	require.Equal(t, 1, f.effect.applyCalls)
	receipt, err := f.executor.readReceipt(f.source.snapshot.IntentID)
	require.NoError(t, err)
	require.Equal(t, "effect_started", receipt.Phase)

	// A new executor instance shares only the durable outbox and live runtime
	// observation. It does not need PostgreSQL or an in-memory old process.
	f.effect.panicPostApplyRead = false
	restarted := *f.executor
	f.executor = &restarted
	obs, err := f.run()
	require.NoError(t, err)
	require.NotNil(t, obs)
	require.Equal(t, 1, f.effect.applyCalls, "a successful external effect must not be repeated")
	receipt, err = f.executor.readReceipt(f.source.snapshot.IntentID)
	require.NoError(t, err)
	require.Equal(t, "terminal", receipt.Phase)
	_, err = f.run()
	require.NoError(t, err)
	require.Equal(t, 1, f.effect.applyCalls)
}

func TestCanonicalAutoApplyReplayAfterCheckpointBeforePublish(t *testing.T) {
	f := newAutoApplyFixture(t)
	original := f.executor.Enqueue
	f.executor.Enqueue = func(ctx context.Context, ev nostr.Event) error {
		var content struct {
			Phase string `json:"phase"`
		}
		require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
		if content.Phase == "effect_observed" {
			return errors.New("kill between checkpoint and publish")
		}
		return original(ctx, ev)
	}
	_, err := f.run()
	require.ErrorContains(t, err, "kill between checkpoint and publish")
	require.Equal(t, 1, f.effect.applyCalls)
	receipt, err := f.executor.readReceipt(f.source.snapshot.IntentID)
	require.NoError(t, err)
	require.Equal(t, "effect_observed", receipt.Phase)
	signedID := receipt.Checkpoint.ID
	f.executor.Enqueue = original
	_, err = f.run()
	require.NoError(t, err)
	require.Equal(t, 1, f.effect.applyCalls)
	entry, found, err := f.outbox.Get(signedID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, signedID, entry.Event.ID, "recovery publishes the same signed event")
}

func TestCanonicalAutoApplyStartupReconcilesTerminalWithoutAnotherEffect(t *testing.T) {
	f := newAutoApplyFixture(t)
	original := f.executor.Enqueue
	f.executor.Enqueue = func(ctx context.Context, ev nostr.Event) error {
		var content struct {
			Phase string `json:"phase"`
		}
		require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
		if content.Phase == "terminal" {
			return errors.New("kill after terminal receipt before outbox admission")
		}
		return original(ctx, ev)
	}
	_, err := f.run()
	require.ErrorContains(t, err, "before outbox admission")
	require.Equal(t, 1, f.effect.applyCalls)
	receipt, err := f.executor.readReceipt(f.source.snapshot.IntentID)
	require.NoError(t, err)
	require.Equal(t, "terminal", receipt.Phase)
	_, found, err := f.outbox.Get(receipt.Checkpoint.ID)
	require.NoError(t, err)
	require.False(t, found)

	f.executor.Enqueue = original
	require.NoError(t, f.executor.ReconcileReceipts(context.Background()))
	entry, found, err := f.outbox.Get(receipt.Checkpoint.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt.Checkpoint.ID, entry.Event.ID)
	require.Equal(t, 1, f.effect.applyCalls)
}

func TestCanonicalAutoApplyRefusesEachMovedRevisionAndSecretPin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ApplySnapshot)
	}{
		{"service", func(s *ApplySnapshot) { s.ServiceEventID = "service-v2" }},
		{"state", func(s *ApplySnapshot) { s.StateEventID = "state-v2" }},
		{"intent", func(s *ApplySnapshot) { s.IntentEventID = "intent-v2" }},
		{"environment", func(s *ApplySnapshot) { s.EnvironmentEventID = "environment-v2" }},
		{"release", func(s *ApplySnapshot) { s.ReleaseEventID = "release-v2" }},
		{"route", func(s *ApplySnapshot) { s.RouteEventID = "route-v2" }},
		{"decision", func(s *ApplySnapshot) { s.DecisionEventID = "decision-v2" }},
		{"secret version", func(s *ApplySnapshot) {
			s.SecretPins = []ApplySecretPin{{SecretID: uuid.New(), EnvVar: "TOKEN", CredentialID: "cred", Version: 2}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutoApplyFixture(t)
			// Leave an accepted receipt without an effect, as if observation
			// failed before the first external step.
			digest, err := f.source.snapshot.digest()
			require.NoError(t, err)
			receipt, err := f.executor.checkpoint(context.Background(), f.source.snapshot, digest, "", "accepted", "")
			require.NoError(t, err)
			require.NoError(t, f.executor.deliverCheckpoint(context.Background(), receipt.Checkpoint))
			tc.change(&f.source.snapshot)
			_, err = f.run()
			require.ErrorIs(t, err, ErrFreshDecisionRequired)
			require.Zero(t, f.effect.applyCalls)
		})
	}
}

func TestCanonicalAutoApplyRefusesMovedSignetVersionBeforeEffect(t *testing.T) {
	f := newAutoApplyFixture(t)
	f.source.resolve = fmt.Errorf("%w: version mismatch", ErrFreshDecisionRequired)
	_, err := f.run()
	require.ErrorIs(t, err, ErrFreshDecisionRequired)
	require.Zero(t, f.effect.applyCalls)
}

func TestCanonicalAutoApplyPublishesFreshDecisionForRejectedCanonicalWinner(t *testing.T) {
	f := newAutoApplyFixture(t)
	digest, err := f.source.snapshot.digest()
	require.NoError(t, err)
	_, err = f.executor.checkpoint(context.Background(), f.source.snapshot, digest, "", "accepted", "")
	require.NoError(t, err)
	f.source.loadErr = ErrFreshDecisionRequired
	_, err = f.run()
	require.ErrorIs(t, err, ErrFreshDecisionRequired)
	require.Zero(t, f.effect.applyCalls)
	receipt, err := f.executor.readReceipt(f.source.snapshot.IntentID)
	require.NoError(t, err)
	require.Equal(t, "refused", receipt.Phase)
	require.Equal(t, "rejected", tagValue(receipt.Checkpoint.Tags, "status"))
}
