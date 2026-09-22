package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type planeRelayFixture struct {
	events      chan *nostr.Event
	eose        chan struct{}
	closed      chan nostrAdapter.RelayClosed
	filters     [][]nostr.Filter
	contexts    []context.Context
	published   []nostr.Event
	results     []nostrAdapter.PublishResult
	authCalls   int
	authError   error
	onAuth      func()
	onSubscribe func(int)
	onPublish   func(nostr.Event)
}

func newPlaneRelayFixture() *planeRelayFixture {
	return &planeRelayFixture{events: make(chan *nostr.Event, 16), eose: make(chan struct{}), closed: make(chan nostrAdapter.RelayClosed, 4), results: []nostrAdapter.PublishResult{{RelayURL: "wss://fixture.invalid", Accepted: true}}}
}
func (f *planeRelayFixture) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	f.contexts = append(f.contexts, ctx)
	f.filters = append(f.filters, filters)
	if f.onSubscribe != nil {
		f.onSubscribe(len(f.filters))
	}
	return &nostrAdapter.MergedSubscription{Events: f.events, EndOfStoredEvents: f.eose, Closed: f.closed}, nil
}
func (f *planeRelayFixture) PublishWithResults(_ context.Context, event nostr.Event) ([]nostrAdapter.PublishResult, error) {
	f.published = append(f.published, event)
	if f.onPublish != nil {
		f.onPublish(event)
	}
	return f.results, nil
}
func (f *planeRelayFixture) AuthenticateRelay(context.Context, string) error {
	f.authCalls++
	if f.onAuth != nil {
		f.onAuth()
	}
	return f.authError
}
func planeClientFixture(t *testing.T, pool *planeRelayFixture, e domain.ExecutionPlaneEndpoint, now time.Time) *PlaneClient {
	t.Helper()
	key, _ := generatedKeyPair(t)
	c, err := NewPlaneClient(pool, HexKeyCanonicalSigner{PrivateKey: key}, []domain.ExecutionPlaneEndpoint{e})
	require.NoError(t, err)
	c.now = func() time.Time { return now }
	c.backoff = time.Nanosecond
	return c
}
func planeAckEvent(t *testing.T, e domain.ExecutionPlaneEndpoint, key string, now time.Time, request nostr.Event, accepted bool) *nostr.Event {
	t.Helper()
	var rpc cascontextvm.Request
	require.NoError(t, json.Unmarshal([]byte(request.Content), &rpc))
	var id string
	require.NoError(t, json.Unmarshal(rpc.ID, &id))
	op, err := uuid.Parse(id)
	require.NoError(t, err)
	body, err := json.Marshal(cascontextvm.NewResponse(rpc.ID, domain.ExecutionPlaneAcknowledgment{OperationID: op, Accepted: accepted}))
	require.NoError(t, err)
	return signLoomEvent(t, key, kinds.ContextVMMessage, now, append(planeTags(e), nostr.Tag{"p", request.PubKey.Hex()}, nostr.Tag{"e", request.ID.Hex()}), string(body))
}
func planeSupportEvent(t *testing.T, e domain.ExecutionPlaneEndpoint, key string, now time.Time) *nostr.Event {
	t.Helper()
	support := domain.ExecutionPlaneSupport{ProtocolVersion: 1, Tools: []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool}, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}}
	b, _ := json.Marshal(support)
	return signLoomEvent(t, key, kinds.ContextVMServerAnnouncement, now, append(planeTags(e), nostr.Tag{"d", e.EndpointRef.String()}), string(b))
}

func TestPlaneDiscoveryEOSEMissingUnsupportedAndIdentity(t *testing.T) {
	e, _, _, key, now := planeFixture(t)
	t.Run("missing means unavailable", func(t *testing.T) {
		pool := newPlaneRelayFixture()
		close(pool.eose)
		c := planeClientFixture(t, pool, e, now)
		_, err := c.Discover(t.Context(), e)
		require.Error(t, err)
		require.Empty(t, pool.published)
		require.ErrorIs(t, pool.contexts[0].Err(), context.Canceled)
	})
	t.Run("drains history before EOSE", func(t *testing.T) {
		pool := newPlaneRelayFixture()
		pool.events <- planeSupportEvent(t, e, key, now)
		close(pool.eose)
		c := planeClientFixture(t, pool, e, now)
		s, err := c.Discover(t.Context(), e)
		require.NoError(t, err)
		require.True(t, SupportsPlaneClasses(s, []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}))
		require.Equal(t, e.Author, pool.filters[0][0].Authors[0].Hex())
		require.Equal(t, []string{e.EndpointRef.String()}, pool.filters[0][0].Tags["d"])
	})
	t.Run("no endpoint fallback", func(t *testing.T) {
		pool := newPlaneRelayFixture()
		c := planeClientFixture(t, pool, e, now)
		e.HostID = uuid.New()
		_, err := c.Discover(t.Context(), e)
		require.Error(t, err)
		require.Empty(t, pool.filters)
	})
}

func TestPlaneContextVMSubscribesBeforePublishAndRequiresEvidence(t *testing.T) {
	e, p, o, key, now := planeFixture(t)
	for _, tool := range []string{"inspect", "probe", "apply"} {
		t.Run(tool, func(t *testing.T) {
			pool := newPlaneRelayFixture()
			c := planeClientFixture(t, pool, e, now)
			pool.onPublish = func(request nostr.Event) {
				require.Len(t, pool.filters, 1, "REQ must precede EVENT")
				var rpc cascontextvm.Request
				require.NoError(t, json.Unmarshal([]byte(request.Content), &rpc))
				if tool == "probe" {
					var req domain.ExecutionPlaneProbeRequest
					require.NoError(t, json.Unmarshal(rpc.Params, &req))
					o.SessionID = req.SessionID
					o.Probe.SessionID = req.SessionID
					o.Sequence = req.Sequence
					o.Probe.Sequence = req.Sequence
				}
				// Observation may precede its acknowledgement; neither may replace OK.
				if tool != "apply" {
					pool.events <- planeObservationEvent(t, e, o, key, now, nostr.Tag{"e", request.ID.Hex()})
				}
				pool.events <- planeAckEvent(t, e, key, now, request, true)
			}
			switch tool {
			case "inspect":
				result, err := c.Inspect(t.Context(), e, p.ID)
				require.NoError(t, err)
				require.Equal(t, o.PlaneID, result.PlaneID)
			case "probe":
				result, err := c.Probe(t.Context(), e, domain.ExecutionPlaneProbeRequest{PlaneID: p.ID, Generation: 1, SessionID: uuid.New(), Sequence: 5, LifecycleClasses: p.Desired.LifecycleClasses})
				require.NoError(t, err)
				require.True(t, result.Successful)
			case "apply":
				ack, err := c.Apply(t.Context(), e, domain.ExecutionPlaneApplyRequest{SchemaVersion: 1, PlaneID: p.ID, HostID: p.HostID, Generation: 1, OperationID: uuid.New(), Desired: p.Desired})
				require.NoError(t, err)
				require.True(t, ack.Accepted)
			}
			require.Len(t, pool.published, 1)
			require.Equal(t, nostr.Kind(kinds.ContextVMMessage), pool.published[0].Kind)
			require.ErrorIs(t, pool.contexts[0].Err(), context.Canceled)
		})
	}
}

func TestPlaneTransportRejectsOKAndAcknowledgmentFailures(t *testing.T) {
	e, p, _, key, now := planeFixture(t)
	for name, results := range map[string][]nostrAdapter.PublishResult{
		"OK false": {{Reason: "blocked: denied"}}, "duplicate false is not acceptance": {{Reason: "duplicate: seen"}}, "zero relays": {}, "transport error": {{Error: errors.New("connection lost")}},
	} {
		t.Run(name, func(t *testing.T) {
			pool := newPlaneRelayFixture()
			pool.results = results
			c := planeClientFixture(t, pool, e, now)
			pool.onPublish = func(req nostr.Event) { pool.events <- planeAckEvent(t, e, key, now, req, true) }
			_, err := c.Apply(t.Context(), e, domain.ExecutionPlaneApplyRequest{SchemaVersion: 1, PlaneID: p.ID, HostID: p.HostID, Generation: 1, OperationID: uuid.New(), Desired: p.Desired})
			require.Error(t, err)
		})
	}
	t.Run("application rejection", func(t *testing.T) {
		pool := newPlaneRelayFixture()
		c := planeClientFixture(t, pool, e, now)
		pool.onPublish = func(req nostr.Event) { pool.events <- planeAckEvent(t, e, key, now, req, false) }
		_, err := c.Apply(t.Context(), e, domain.ExecutionPlaneApplyRequest{SchemaVersion: 1, PlaneID: p.ID, HostID: p.HostID, Generation: 1, OperationID: uuid.New(), Desired: p.Desired})
		require.Error(t, err)
	})
	t.Run("EOSE without live evidence cannot complete inspect", func(t *testing.T) {
		pool := newPlaneRelayFixture()
		c := planeClientFixture(t, pool, e, now)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		pool.onPublish = func(req nostr.Event) {
			pool.events <- planeAckEvent(t, e, key, now, req, true)
			close(pool.eose)
			cancel()
		}
		result, err := c.Inspect(ctx, e, p.ID)
		require.Error(t, err)
		require.Nil(t, result)
	})
}

func TestPlaneAUTHReREQAndFailure(t *testing.T) {
	e, _, _, key, now := planeFixture(t)
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			pool := newPlaneRelayFixture()
			if fail {
				pool.authError = errors.New("signer unavailable")
			}
			pool.closed <- nostrAdapter.RelayClosed{RelayURL: "wss://fixture.invalid", Reason: "auth-required: challenge"}
			pool.onSubscribe = func(n int) {
				if n == 2 {
					pool.events <- planeSupportEvent(t, e, key, now)
					close(pool.eose)
				}
			}
			c := planeClientFixture(t, pool, e, now)
			_, err := c.Discover(t.Context(), e)
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, pool.filters, 2)
			}
			require.Equal(t, 1, pool.authCalls)
		})
	}
}

type planeRecordingObserver struct {
	observation func(domain.ExecutionPlaneObservation) error
	eose        func() error
	unavailable func() error
	diagnostic  func(domain.VMDiagnostic) error
}

func (o planeRecordingObserver) OnObservation(_ context.Context, v domain.ExecutionPlaneObservation) error {
	if o.observation != nil {
		return o.observation(v)
	}
	return nil
}
func (o planeRecordingObserver) OnEOSE(context.Context) error {
	if o.eose != nil {
		return o.eose()
	}
	return nil
}
func (o planeRecordingObserver) OnUnavailable(_ context.Context, diagnostic domain.VMDiagnostic) error {
	if o.diagnostic != nil {
		return o.diagnostic(diagnostic)
	}
	if o.unavailable != nil {
		return o.unavailable()
	}
	return nil
}

func TestPlaneObserveRetractsBeforeAuthentication(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			e, p, o, key, now := planeFixture(t)
			pool := newPlaneRelayFixture()
			pool.events <- planeObservationEvent(t, e, o, key, now)
			client := planeClientFixture(t, pool, e, now)
			eligible, retracted := false, false
			stop := errors.New("stop after authentication")
			pool.onAuth = func() {
				require.True(t, retracted)
				require.False(t, eligible, "eligibility must be gone even if AUTH blocks")
			}
			if failure {
				pool.authError = stop
			}
			pool.onSubscribe = func(n int) {
				if n == 2 {
					require.True(t, retracted)
					close(pool.eose)
				}
			}
			observer := planeRecordingObserver{
				observation: func(domain.ExecutionPlaneObservation) error {
					eligible = true
					pool.closed <- nostrAdapter.RelayClosed{RelayURL: "wss://fixture.invalid", Reason: "auth-required: challenge"}
					return nil
				},
				unavailable: func() error { eligible = false; retracted = true; return nil },
				eose:        func() error { require.False(t, eligible); return stop },
			}
			require.ErrorIs(t, client.Observe(t.Context(), e, p.ID, observer), stop)
			require.Equal(t, 1, pool.authCalls)
		})
	}
}

func TestPlaneObserveAuditsAndRejectsWindowsProbe(t *testing.T) {
	e, p, observation, key, now := planeFixture(t)
	observation.Probe.Capabilities = []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSWindows, Architecture: "amd64", AgentProtocolVersion: "3"}}
	event := planeObservationEvent(t, e, observation, key, now)
	pool := newPlaneRelayFixture()
	pool.events <- event
	client := planeClientFixture(t, pool, e, now)
	var evidence []domain.VMDiagnostic
	observer := planeRecordingObserver{observation: func(domain.ExecutionPlaneObservation) error {
		t.Fatal("Windows probe reached observation consumer")
		return nil
	}, diagnostic: func(d domain.VMDiagnostic) error { evidence = append(evidence, d); return nil }}
	require.Error(t, client.Observe(t.Context(), e, p.ID, observer))
	require.Contains(t, evidence, domain.VMDiagnostic{Code: domain.VMErrorIntegrity, EvidenceDigest: "sha256:" + event.ID.Hex()})
}

func TestPlaneObserveDedupReorderReconnectAndCleanup(t *testing.T) {
	e, p, o, key, now := planeFixture(t)
	pool := newPlaneRelayFixture()
	first := planeObservationEvent(t, e, o, key, now)
	pool.events <- first
	pool.onSubscribe = func(n int) {
		if n == 2 {
			pool.events = make(chan *nostr.Event, 16)
			pool.eose = make(chan struct{})
			pool.closed = make(chan nostrAdapter.RelayClosed, 1)
			pool.events <- first
			older := o
			older.Sequence--
			older.Probe = nil
			pool.events <- planeObservationEvent(t, e, older, key, now)
			next := o
			next.Sequence++
			pool.events <- planeObservationEvent(t, e, next, key, now)
		}
	}
	c := planeClientFixture(t, pool, e, now)
	count, lost, caught := 0, 0, 0
	stop := errors.New("fixture complete")
	observer := planeRecordingObserver{observation: func(v domain.ExecutionPlaneObservation) error {
		count++
		if count == 1 {
			close(pool.eose)
			return nil
		}
		return stop
	}, eose: func() error {
		caught++
		pool.closed <- nostrAdapter.RelayClosed{RelayURL: "wss://fixture.invalid", Reason: "error: reconnect"}
		return nil
	}, unavailable: func() error { lost++; return nil }}
	err := c.Observe(t.Context(), e, p.ID, observer)
	require.ErrorIs(t, err, stop)
	require.Equal(t, 2, count)
	require.Equal(t, 1, caught)
	require.GreaterOrEqual(t, lost, 1)
	require.Len(t, pool.filters, 2)
	for _, ctx := range pool.contexts {
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	}
}

type planeWorkerSource struct {
	verified []domain.VerifiedExecutionPlaneCapabilities
	err      error
}

func (s planeWorkerSource) VerifiedCapabilities(context.Context, string, time.Time) ([]domain.VerifiedExecutionPlaneCapabilities, error) {
	return s.verified, s.err
}

type planeWorkerRepo struct {
	repository.WorkerRepository
	worker domain.Worker
}

func (r planeWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	w := r.worker
	return &w, nil
}
func (r planeWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return []domain.Worker{r.worker}, nil
}

func TestPlaneWorkerEligibilityCannotUseAdvertisementOrExplicitTargetBypass(t *testing.T) {
	_, p, _, _, _ := planeFixture(t)
	capability := p.Desired.ExpectedCapabilities[0]
	w := domain.Worker{PubKey: p.WorkerPubKey, Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, MaxConcurrentJobs: 2, Capabilities: domain.WorkerCapabilities{WorkloadKinds: []string{string(domain.VMLifecycleLoomQEMU)}}}
	job := JobRequest{WorkerPubkey: w.PubKey, RequiredExecutionPlane: &capability}
	client := &Client{privateKey: "configured", workerRepo: planeWorkerRepo{worker: w}}
	_, err := client.SubmitJob(t.Context(), job)
	require.ErrorContains(t, err, "source unavailable")
	client.planeCapabilities = planeWorkerSource{}
	_, err = client.SubmitJob(t.Context(), job)
	require.ErrorContains(t, err, "no eligible verified")
	verified := domain.VerifiedExecutionPlaneCapabilities{PlaneID: p.ID, Generation: 1, SessionID: uuid.New(), ProbeSequence: 1, ExpiresAt: time.Now().Add(time.Minute), Capabilities: []domain.ExecutionPlaneCapability{capability}}
	client.planeCapabilities = planeWorkerSource{verified: []domain.VerifiedExecutionPlaneCapabilities{verified}}
	selected, err := client.selectWorker(t.Context(), job)
	require.NoError(t, err)
	require.Equal(t, w.PubKey, selected)
	capability.OS = domain.VMOSWindows
	_, err = client.selectWorker(t.Context(), job)
	require.Error(t, err)
	job.RequiredExecutionPlane = nil
	job.RequiredWorkloads = []string{string(domain.VMLifecycleLoomQEMU)}
	client.planeCapabilities = planeWorkerSource{}
	_, err = client.selectWorker(t.Context(), job)
	require.Error(t, err)
}
