package soulfactory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type readinessBackendDouble struct {
	runtime       RuntimeReadinessObservation
	route         RouteReadinessObservation
	signer        SignerReadinessObservation
	subscriptions SubscriptionReadinessObservation
	model         ModelReadinessObservation
	inference     InferenceReadinessObservation
	probe         DMProbeReadinessObservation
	errGate       OpenClawReadinessGate
}

type acceptingOpenClawReadiness struct{}

func (acceptingOpenClawReadiness) Verify(_ context.Context, req OpenClawReadinessRequest, progress func(OpenClawReadinessProgress) error) (*OpenClawReadinessEvidence, error) {
	for index, gate := range openClawReadinessGates {
		if progress != nil {
			if err := progress(OpenClawReadinessProgress{Gate: gate, Current: index + 1, Total: len(openClawReadinessGates), Message: "test readiness"}); err != nil {
				return nil, err
			}
		}
	}
	return &OpenClawReadinessEvidence{
		RequestID: req.RequestID, RunID: req.RunID, Provider: req.Provider, Model: req.Model,
		VerifiedAt: time.Now().UTC(), ProbeEventIDs: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)},
		GateTimingsMS: map[OpenClawReadinessGate]int64{},
	}, nil
}

type rejectingOpenClawReadiness struct{ gate OpenClawReadinessGate }

func (r rejectingOpenClawReadiness) Verify(_ context.Context, _ OpenClawReadinessRequest, progress func(OpenClawReadinessProgress) error) (*OpenClawReadinessEvidence, error) {
	if progress != nil {
		if err := progress(OpenClawReadinessProgress{Gate: r.gate, Current: 1, Total: len(openClawReadinessGates), Message: "test readiness failure"}); err != nil {
			return nil, err
		}
	}
	return nil, &OpenClawReadinessError{Gate: r.gate, Code: "test_failure", Retryable: true}
}

func (d *readinessBackendDouble) gateError(gate OpenClawReadinessGate) error {
	if d.errGate == gate {
		return errors.New("nsec1-secret-provider-detail")
	}
	return nil
}
func (d *readinessBackendDouble) InspectRuntime(context.Context, OpenClawReadinessRequest) (RuntimeReadinessObservation, error) {
	return d.runtime, d.gateError(ReadinessRuntime)
}
func (d *readinessBackendDouble) InspectRoute(context.Context, OpenClawReadinessRequest) (RouteReadinessObservation, error) {
	return d.route, d.gateError(ReadinessRoute)
}
func (d *readinessBackendDouble) InspectSigner(context.Context, OpenClawReadinessRequest) (SignerReadinessObservation, error) {
	return d.signer, d.gateError(ReadinessSigner)
}
func (d *readinessBackendDouble) InspectSubscriptions(context.Context, OpenClawReadinessRequest) (SubscriptionReadinessObservation, error) {
	return d.subscriptions, d.gateError(ReadinessSubscriptions)
}
func (d *readinessBackendDouble) InspectModel(context.Context, OpenClawReadinessRequest) (ModelReadinessObservation, error) {
	return d.model, d.gateError(ReadinessInference)
}
func (d *readinessBackendDouble) Infer(context.Context, OpenClawReadinessRequest, string) (InferenceReadinessObservation, error) {
	return d.inference, d.gateError(ReadinessInference)
}
func (d *readinessBackendDouble) ProbeDM(_ context.Context, req OpenClawReadinessRequest, correlation string) (DMProbeReadinessObservation, error) {
	probe := d.probe
	probe.CorrelationID = correlation
	probe.AuthorPubkey = req.ManagedPubkey
	return probe, d.gateError(ReadinessDMProbe)
}

func readinessFixture() (OpenClawReadinessRequest, *readinessBackendDouble) {
	req := OpenClawReadinessRequest{
		RequestID: strings.Repeat("1", 64), RunID: "run-1", AgentID: "scout", AccountID: "scout-account",
		RuntimeBinding: "openclaw://agents/scout", ManagedPubkey: strings.Repeat("2", 64),
		Provider: "routstr", Model: "routstr/model-a", RequiredRelays: []string{"wss://relay-a.example", "wss://relay-b.example"},
	}
	backend := &readinessBackendDouble{
		runtime:       RuntimeReadinessObservation{ContainerHealthy: true, GatewayHealthy: true},
		route:         RouteReadinessObservation{AgentID: req.AgentID, AccountID: req.AccountID, OwnerRunID: req.RunID, OwnerBinding: req.RuntimeBinding, ExclusiveOwners: 1},
		signer:        SignerReadinessObservation{Connected: true, ManagedPubkey: req.ManagedPubkey, HandoffDeleted: true},
		subscriptions: SubscriptionReadinessObservation{NIP17Active: true, Relays: []string{"wss://relay-b.example/", "wss://relay-a.example"}},
		model:         ModelReadinessObservation{Provider: req.Provider, Model: req.Model},
		inference:     InferenceReadinessObservation{Response: "bounded response", Duration: 50 * time.Millisecond},
		probe: DMProbeReadinessObservation{
			ProbePubkey: strings.Repeat("3", 64), Disposable: true, Controlled: true, Authenticated: true,
			Received: true, Routed: true, LLMAnswered: true, Signed: true, RelayPublished: true,
			Decrypted: true, ContentVerified: true, SignatureVerified: true,
			EventIDs: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, Duration: 100 * time.Millisecond,
		},
	}
	return req, backend
}

func TestOpenClawReadinessVerifierRequiresEveryGateAndRecordsSanitizedEvidence(t *testing.T) {
	req, backend := readinessFixture()
	verifier, err := NewOpenClawReadinessVerifier(backend, time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var progress []OpenClawReadinessProgress
	evidence, err := verifier.Verify(t.Context(), req, func(update OpenClawReadinessProgress) error {
		progress = append(progress, update)
		return nil
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if len(progress) != OpenClawReadinessGateCount() {
		t.Fatalf("progress gates = %d", len(progress))
	}
	if evidence.RequestID != req.RequestID || evidence.RunID != req.RunID || evidence.Provider != req.Provider || evidence.Model != req.Model {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if len(evidence.ProbeEventIDs) != 2 || evidence.ProbeEventIDs[0] != strings.Repeat("a", 64) {
		t.Fatalf("probe evidence = %#v", evidence.ProbeEventIDs)
	}
}

func TestOpenClawReadinessVerifierFailsClosedForBrokenGateWithoutLeakingAdapterError(t *testing.T) {
	for _, gate := range openClawReadinessGates {
		t.Run(string(gate), func(t *testing.T) {
			req, backend := readinessFixture()
			backend.errGate = gate
			verifier, err := NewOpenClawReadinessVerifier(backend, time.Second, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := verifier.Verify(t.Context(), req, nil)
			if err == nil || evidence != nil {
				t.Fatalf("broken %s gate reported success", gate)
			}
			var safe *OpenClawReadinessError
			if !errors.As(err, &safe) || safe.Gate != gate {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), "nsec1") || strings.Contains(err.Error(), "provider-detail") {
				t.Fatalf("adapter secret leaked: %v", err)
			}
		})
	}
}

func TestOpenClawReadinessVerifierRejectsInternalOrUnverifiedDMProbe(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OpenClawReadinessRequest, *readinessBackendDouble)
	}{
		{"same identity", func(req *OpenClawReadinessRequest, backend *readinessBackendDouble) {
			backend.probe.ProbePubkey = req.ManagedPubkey
		}},
		{"not controlled", func(_ *OpenClawReadinessRequest, backend *readinessBackendDouble) { backend.probe.Controlled = false }},
		{"not decrypted", func(_ *OpenClawReadinessRequest, backend *readinessBackendDouble) { backend.probe.Decrypted = false }},
		{"bad signature", func(_ *OpenClawReadinessRequest, backend *readinessBackendDouble) {
			backend.probe.SignatureVerified = false
		}},
		{"bad event id", func(_ *OpenClawReadinessRequest, backend *readinessBackendDouble) {
			backend.probe.EventIDs[1] = "secret"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, backend := readinessFixture()
			tt.mutate(&req, backend)
			verifier, _ := NewOpenClawReadinessVerifier(backend, time.Second, time.Second)
			if evidence, err := verifier.Verify(t.Context(), req, nil); err == nil || evidence != nil {
				t.Fatal("invalid independent probe reported readiness")
			}
		})
	}
}

func TestOpenClawReadinessVerifierRejectsMissingRelayAndSlowInference(t *testing.T) {
	req, backend := readinessFixture()
	backend.subscriptions.Relays = backend.subscriptions.Relays[:1]
	verifier, _ := NewOpenClawReadinessVerifier(backend, time.Second, time.Second)
	if _, err := verifier.Verify(t.Context(), req, nil); err == nil {
		t.Fatal("missing required relay reported readiness")
	}

	req, backend = readinessFixture()
	backend.inference.Duration = 2 * time.Second
	verifier, _ = NewOpenClawReadinessVerifier(backend, time.Second, time.Second)
	if _, err := verifier.Verify(t.Context(), req, nil); err == nil {
		t.Fatal("unbounded inference reported readiness")
	}
}
