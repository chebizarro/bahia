package reconcile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const hygieneTestServiceKey = "0000000000000000000000000000000000000000000000000000000000000011"
const hygieneTestWorkerKey = "0000000000000000000000000000000000000000000000000000000000000012"
const hygieneTestAttackerKey = "0000000000000000000000000000000000000000000000000000000000000013"

type hygieneProjectionRelay struct {
	t               *testing.T
	worker          casnostr.Signer
	responseSigner  casnostr.Signer
	servicePubKey   string
	transport       *controlplane.EncryptedRequestTransport
	scanResult      cascadia.CascadiaMaintenanceScanResultV1Payload
	pressureResult  cascadia.CascadiaMaintenancePressureResultV1Payload
	methods         []string
	plaintext       bool
	wrongRequestTag bool
	wrongResponseID bool
}

func (r *hygieneProjectionRelay) Publish(ctx context.Context, outer nostr.Event) (int, error) {
	inner, err := cascontextvm.UnwrapNIP59(ctx, r.worker, &outer)
	if err != nil {
		r.t.Fatalf("published maintenance event was not a worker-addressed NIP-59 request: %v", err)
	}
	var request controlplane.ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(inner.Content), &request); err != nil {
		r.t.Fatalf("decode maintenance request: %v", err)
	}
	r.methods = append(r.methods, request.Method)
	var result any
	switch request.Method {
	case controlplane.ContextVMMethodMaintenanceScan:
		result = r.scanResult
	case controlplane.ContextVMMethodMaintenancePressure:
		result = r.pressureResult
	default:
		return 1, nil
	}
	responseID := request.ID
	if r.wrongResponseID {
		responseID = json.RawMessage(`"wrong-id"`)
	}
	content, err := json.Marshal(cascontextvm.NewResponse(responseID, result))
	if err != nil {
		r.t.Fatal(err)
	}
	requestEventID := inner.ID.Hex()
	if r.wrongRequestTag {
		requestEventID = strings.Repeat("f", 64)
	}
	responseInner := &nostr.Event{
		Kind:      controlplane.KindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", r.servicePubKey}, {"e", requestEventID}},
		Content:   string(content),
	}
	if r.plaintext {
		if err := r.responseSigner.SignEvent(ctx, responseInner); err != nil {
			r.t.Fatal(err)
		}
		r.transport.HandleEvent(ctx, responseInner)
		return 1, nil
	}
	responseOuter, _, err := cascontextvm.WrapEventNIP59(ctx, r.responseSigner, r.servicePubKey, responseInner, cascontextvm.StoredGiftWrap)
	if err != nil {
		r.t.Fatalf("wrap maintenance response: %v", err)
	}
	r.transport.HandleEvent(ctx, responseOuter)
	return 1, nil
}

type hygieneIntegrationHarness struct {
	relay      *hygieneProjectionRelay
	source     *ContextVMHygieneObservationSource
	reconciler *HygieneReconciler
	workerKey  string
	now        time.Time
}

func newHygieneIntegrationHarness(t *testing.T, mutateRelay func(*hygieneProjectionRelay)) *hygieneIntegrationHarness {
	t.Helper()
	ctx := context.Background()
	serviceSigner, err := controlplane.NewPrivateKeySigner(hygieneTestServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	workerSigner, err := controlplane.NewPrivateKeySigner(hygieneTestWorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubKey, err := serviceSigner.GetPublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workerPubKey, err := workerSigner.GetPublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	relay := &hygieneProjectionRelay{
		t:              t,
		worker:         workerSigner,
		responseSigner: workerSigner,
		servicePubKey:  servicePubKey.Hex(),
		scanResult: cascadia.CascadiaMaintenanceScanResultV1Payload{
			Candidates: []cascadia.CascadiaMaintenanceScanResultV1Candidate{},
			ScannedAt:  now.Format(time.RFC3339Nano), TotalCandidates: 0,
		},
		pressureResult: cascadia.CascadiaMaintenancePressureResultV1Payload{
			Mounts:    []cascadia.CascadiaMaintenancePressureResultV1Mount{},
			SampledAt: now.Format(time.RFC3339Nano),
		},
	}
	if mutateRelay != nil {
		mutateRelay(relay)
	}
	responder := controlplane.NewEncryptedResponder(relay, serviceSigner, hygieneTestServiceKey, zap.NewNop())
	transport := controlplane.NewEncryptedRequestTransport(nil, responder, nil, zap.NewNop())
	relay.transport = transport
	source, err := NewContextVMHygieneObservationSource(servicePubKey.Hex(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return now }
	transport.RegisterContextVMResponseHandler(source.HandleContextVMResponse)
	publisher := controlplane.NewMaintenanceCommandPublisher(relay, serviceSigner, source)
	reconciler, err := NewHygieneReconciler(testHygienePolicy(nil), []string{workerPubKey.Hex()}, publisher, source, nil, time.Minute, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return now }
	return &hygieneIntegrationHarness{relay: relay, source: source, reconciler: reconciler, workerKey: workerPubKey.Hex(), now: now}
}

func TestHygieneObservationProjectionTriggersCandidateQuarantine(t *testing.T) {
	h := newHygieneIntegrationHarness(t, func(relay *hygieneProjectionRelay) {
		relay.scanResult = cascadia.CascadiaMaintenanceScanResultV1Payload{
			Candidates: []cascadia.CascadiaMaintenanceScanResultV1Candidate{{
				Id: "cruft-1", Path: "/srv/fleet/worktrees/cruft", Class: domain.HygieneClassCruft, Reason: "matches policy",
			}},
			ScannedAt:       relay.scanResult.ScannedAt,
			TotalCandidates: 1,
		}
	})

	result, err := h.reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !containsMethod(h.relay.methods, controlplane.ContextVMMethodMaintenanceQuarantine) || len(result.Actions) != 1 || result.Actions[0].Method != controlplane.ContextVMMethodMaintenanceQuarantine {
		t.Fatalf("canonical scan did not trigger quarantine: methods=%v actions=%+v", h.relay.methods, result.Actions)
	}
	if got := result.Actions[0].Paths; len(got) != 1 || got[0] != "/srv/fleet/worktrees/cruft" {
		t.Fatalf("quarantine paths = %v", got)
	}
}

func TestHygieneObservationProjectionTriggersPressureGC(t *testing.T) {
	h := newHygieneIntegrationHarness(t, func(relay *hygieneProjectionRelay) {
		relay.pressureResult = cascadia.CascadiaMaintenancePressureResultV1Payload{
			Mounts: []cascadia.CascadiaMaintenancePressureResultV1Mount{{
				Path: "/", TotalBytes: 1000, FreeBytes: 50, UsedPct: 95, TotalInodes: 100, FreeInodes: 50,
			}},
			SampledAt: relay.pressureResult.SampledAt,
		}
	})

	result, err := h.reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !containsMethod(h.relay.methods, controlplane.ContextVMMethodMaintenanceGC) || len(result.PressureAlerts) != 1 {
		t.Fatalf("canonical pressure did not trigger gc: methods=%v alerts=%v", h.relay.methods, result.PressureAlerts)
	}
}

func TestHygieneObservationRejectsForgedAndMismatchedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*hygieneProjectionRelay)
	}{
		{
			name: "wrong authenticated worker",
			mutate: func(relay *hygieneProjectionRelay) {
				attacker, err := controlplane.NewPrivateKeySigner(hygieneTestAttackerKey)
				if err != nil {
					relay.t.Fatal(err)
				}
				relay.responseSigner = attacker
			},
		},
		{name: "wrong request e tag", mutate: func(relay *hygieneProjectionRelay) { relay.wrongRequestTag = true }},
		{name: "wrong JSON-RPC id", mutate: func(relay *hygieneProjectionRelay) { relay.wrongResponseID = true }},
		{name: "plaintext response", mutate: func(relay *hygieneProjectionRelay) { relay.plaintext = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHygieneIntegrationHarness(t, func(relay *hygieneProjectionRelay) {
				relay.scanResult = cascadia.CascadiaMaintenanceScanResultV1Payload{
					Candidates: []cascadia.CascadiaMaintenanceScanResultV1Candidate{{Id: "cruft-1", Path: "/srv/cruft", Class: domain.HygieneClassCruft}},
					ScannedAt:  relay.scanResult.ScannedAt, TotalCandidates: 1,
				}
				tc.mutate(relay)
			})
			result, err := h.reconciler.ReconcileOnce(context.Background())
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if len(result.Actions) != 0 || containsMethod(h.relay.methods, controlplane.ContextVMMethodMaintenanceQuarantine) {
				t.Fatalf("rejected response triggered an action: methods=%v actions=%+v", h.relay.methods, result.Actions)
			}
		})
	}
}

func TestHygieneObservationTruncatedScanSuppressesQuarantineButAllowsGC(t *testing.T) {
	h := newHygieneIntegrationHarness(t, func(relay *hygieneProjectionRelay) {
		relay.scanResult = cascadia.CascadiaMaintenanceScanResultV1Payload{
			Candidates: []cascadia.CascadiaMaintenanceScanResultV1Candidate{{Id: "cruft-1", Path: "/srv/cruft", Class: domain.HygieneClassCruft}},
			ScannedAt:  relay.scanResult.ScannedAt, TotalCandidates: 2, Truncated: true,
		}
		relay.pressureResult = cascadia.CascadiaMaintenancePressureResultV1Payload{
			Mounts:    []cascadia.CascadiaMaintenancePressureResultV1Mount{{Path: "/", TotalBytes: 100, FreeBytes: 1, UsedPct: 99, TotalInodes: 100, FreeInodes: 50}},
			SampledAt: relay.pressureResult.SampledAt,
		}
	})

	result, err := h.reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if containsMethod(h.relay.methods, controlplane.ContextVMMethodMaintenanceQuarantine) {
		t.Fatalf("truncated candidate list triggered quarantine: %v", h.relay.methods)
	}
	if !containsMethod(h.relay.methods, controlplane.ContextVMMethodMaintenanceGC) || len(result.PressureAlerts) != 1 {
		t.Fatalf("independent pressure result did not trigger gc: methods=%v alerts=%v", h.relay.methods, result.PressureAlerts)
	}
	observation, err := h.source.Latest(context.Background(), h.workerKey)
	if err != nil {
		t.Fatal(err)
	}
	if observation == nil || !observation.ScanTruncated || observation.TotalCandidates != 2 || len(observation.Candidates) != 0 {
		t.Fatalf("truncated observation was not fail-closed: %+v", observation)
	}
}

func TestHygieneObservationSourceDoesNotRegressOnOutOfOrderResponses(t *testing.T) {
	serviceSecret, err := nostr.SecretKeyFromHex(hygieneTestServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	workerSecret, err := nostr.SecretKeyFromHex(hygieneTestWorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubKey := serviceSecret.Public().Hex()
	workerPubKey := workerSecret.Public()
	now := time.Now().UTC().Truncate(time.Second)
	source, err := NewContextVMHygieneObservationSource(servicePubKey, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return now }

	register := func(requestID, dTag string) {
		t.Helper()
		if _, err := source.RegisterMaintenanceRequest(controlplane.MaintenanceRequestCorrelation{
			Method: controlplane.ContextVMMethodMaintenanceScan, WorkerPubKey: workerPubKey.Hex(),
			RequestEventID: requestID, RequestPubKey: servicePubKey, DTag: dTag,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(requestID, dTag, path string) {
		t.Helper()
		result, err := json.Marshal(cascadia.CascadiaMaintenanceScanResultV1Payload{
			Candidates: []cascadia.CascadiaMaintenanceScanResultV1Candidate{{Id: path, Path: path, Class: domain.HygieneClassCruft}},
			ScannedAt:  now.Format(time.RFC3339Nano), TotalCandidates: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		source.HandleContextVMResponse(context.Background(), controlplane.ContextVMResponseEnvelope{
			Event:          &nostr.Event{Kind: controlplane.KindContextVMMessage, PubKey: workerPubKey, Tags: nostr.Tags{{"p", servicePubKey}, {"e", requestID}}},
			EnvelopeFormat: cascontextvm.EnvelopeFormatNIP59,
			ReceivedAt:     now,
			JSONRPC:        "2.0", ID: json.RawMessage(`"` + dTag + `"`), IDPresent: true,
			Result: result, ResultPresent: true,
		})
	}

	oldRequestID := strings.Repeat("a", 64)
	newRequestID := strings.Repeat("b", 64)
	register(oldRequestID, "old")
	register(newRequestID, "new")
	deliver(newRequestID, "new", "/srv/new")
	deliver(oldRequestID, "old", "/srv/old")

	observation, err := source.Latest(context.Background(), workerPubKey.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if observation == nil || len(observation.Candidates) != 1 || observation.Candidates[0].Path != "/srv/new" {
		t.Fatalf("older response regressed latest scan: %+v", observation)
	}
	observation.Candidates[0].Path = "/mutated"
	again, err := source.Latest(context.Background(), workerPubKey.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if again.Candidates[0].Path != "/srv/new" {
		t.Fatalf("Latest returned source-owned candidate slice: %+v", again)
	}
}

func TestHygieneObservationSourceBoundsAndExpiresPendingRequests(t *testing.T) {
	serviceSecret, err := nostr.SecretKeyFromHex(hygieneTestServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	workerSecret, err := nostr.SecretKeyFromHex(hygieneTestWorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	source, err := NewContextVMHygieneObservationSource(serviceSecret.Public().Hex(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	source.pendingLimit = 1
	source.pendingTTL = time.Minute
	source.now = func() time.Time { return now }
	correlation := func(id, dTag string) controlplane.MaintenanceRequestCorrelation {
		return controlplane.MaintenanceRequestCorrelation{
			Method: controlplane.ContextVMMethodMaintenanceScan, WorkerPubKey: workerSecret.Public().Hex(),
			RequestEventID: id, RequestPubKey: serviceSecret.Public().Hex(), DTag: dTag,
		}
	}
	if _, err := source.RegisterMaintenanceRequest(correlation(strings.Repeat("a", 64), "first")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RegisterMaintenanceRequest(correlation(strings.Repeat("b", 64), "second")); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity error = %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := source.RegisterMaintenanceRequest(correlation(strings.Repeat("b", 64), "second")); err != nil {
		t.Fatalf("expired entry did not free capacity: %v", err)
	}
	if len(source.pending) != 1 {
		t.Fatalf("pending correlations = %d, want 1", len(source.pending))
	}
}

func containsMethod(methods []string, method string) bool {
	for _, candidate := range methods {
		if candidate == method {
			return true
		}
	}
	return false
}
