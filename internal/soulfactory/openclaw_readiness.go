package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// OpenClawReadinessGate names the externally observable checks required before
// an OpenClaw soul may be projected as running.
type OpenClawReadinessGate string

const (
	ReadinessRuntime       OpenClawReadinessGate = "runtime_health"
	ReadinessRoute         OpenClawReadinessGate = "account_route"
	ReadinessSigner        OpenClawReadinessGate = "nip46_signer"
	ReadinessSubscriptions OpenClawReadinessGate = "nip17_subscriptions"
	ReadinessInference     OpenClawReadinessGate = "model_inference"
	ReadinessDMProbe       OpenClawReadinessGate = "dm_round_trip"
)

var openClawReadinessGates = []OpenClawReadinessGate{
	ReadinessRuntime, ReadinessRoute, ReadinessSigner,
	ReadinessSubscriptions, ReadinessInference, ReadinessDMProbe,
}

func OpenClawReadinessGateCount() int { return len(openClawReadinessGates) }

type OpenClawReadinessRequest struct {
	RequestID      string
	RunID          string
	AgentID        string
	AccountID      string
	RuntimeBinding string
	ManagedPubkey  string
	Provider       string
	Model          string
	RequiredRelays []string
}

type OpenClawReadinessProgress struct {
	Gate    OpenClawReadinessGate
	Current int
	Total   int
	Message string
}

type OpenClawReadinessEvidence struct {
	RequestID       string                          `json:"request_id"`
	RunID           string                          `json:"run_id"`
	Provider        string                          `json:"provider"`
	Model           string                          `json:"model"`
	VerifiedAt      time.Time                       `json:"verified_at"`
	TotalDurationMS int64                           `json:"total_duration_ms"`
	GateTimingsMS   map[OpenClawReadinessGate]int64 `json:"gate_timings_ms"`
	ProbeEventIDs   []string                        `json:"probe_event_ids"`
}

// OpenClawReadiness is injected into FullProvisioner. Production adapters may
// inspect real infrastructure; tests use deterministic doubles.
type OpenClawReadiness interface {
	Verify(context.Context, OpenClawReadinessRequest, func(OpenClawReadinessProgress) error) (*OpenClawReadinessEvidence, error)
}

type RuntimeReadinessObservation struct {
	ContainerHealthy bool
	GatewayHealthy   bool
}

type RouteReadinessObservation struct {
	AgentID         string
	AccountID       string
	OwnerRunID      string
	OwnerBinding    string
	ExclusiveOwners int
}

type SignerReadinessObservation struct {
	Connected      bool
	ManagedPubkey  string
	HandoffDeleted bool
}

type SubscriptionReadinessObservation struct {
	NIP17Active bool
	Relays      []string
}

type ModelReadinessObservation struct {
	Provider string
	Model    string
}

type InferenceReadinessObservation struct {
	Response string
	Duration time.Duration
}

type DMProbeReadinessObservation struct {
	ProbePubkey       string
	AuthorPubkey      string
	CorrelationID     string
	Disposable        bool
	Controlled        bool
	Authenticated     bool
	Received          bool
	Routed            bool
	LLMAnswered       bool
	Signed            bool
	RelayPublished    bool
	Decrypted         bool
	ContentVerified   bool
	SignatureVerified bool
	EventIDs          []string
	Duration          time.Duration
}

// OpenClawReadinessBackend is the infrastructure boundary. ProbeDM must use a
// disposable identity controlled independently from the provisioned runtime.
type OpenClawReadinessBackend interface {
	InspectRuntime(context.Context, OpenClawReadinessRequest) (RuntimeReadinessObservation, error)
	InspectRoute(context.Context, OpenClawReadinessRequest) (RouteReadinessObservation, error)
	InspectSigner(context.Context, OpenClawReadinessRequest) (SignerReadinessObservation, error)
	InspectSubscriptions(context.Context, OpenClawReadinessRequest) (SubscriptionReadinessObservation, error)
	InspectModel(context.Context, OpenClawReadinessRequest) (ModelReadinessObservation, error)
	Infer(context.Context, OpenClawReadinessRequest, string) (InferenceReadinessObservation, error)
	ProbeDM(context.Context, OpenClawReadinessRequest, string) (DMProbeReadinessObservation, error)
}

type OpenClawReadinessVerifier struct {
	backend          OpenClawReadinessBackend
	now              func() time.Time
	inferenceTimeout time.Duration
	probeTimeout     time.Duration
}

func NewOpenClawReadinessVerifier(backend OpenClawReadinessBackend, inferenceTimeout, probeTimeout time.Duration) (*OpenClawReadinessVerifier, error) {
	if backend == nil {
		return nil, fmt.Errorf("OpenClaw readiness backend is required")
	}
	if inferenceTimeout <= 0 || probeTimeout <= 0 {
		return nil, fmt.Errorf("positive inference and DM probe timeouts are required")
	}
	return &OpenClawReadinessVerifier{backend: backend, now: time.Now, inferenceTimeout: inferenceTimeout, probeTimeout: probeTimeout}, nil
}

type OpenClawReadinessError struct {
	Gate      OpenClawReadinessGate
	Code      string
	Retryable bool
}

func (e *OpenClawReadinessError) Error() string {
	if e == nil {
		return ""
	}
	messages := map[OpenClawReadinessGate]string{
		ReadinessRuntime:       "OpenClaw container or gateway is not healthy; inspect the dedicated runtime health check",
		ReadinessRoute:         "OpenClaw account route is missing, conflicting, or not exclusively owned by this run",
		ReadinessSigner:        "NIP-46 signer is not connected to the managed identity or the one-time handoff still exists",
		ReadinessSubscriptions: "expected NIP-17 subscriptions are not active on every required relay",
		ReadinessInference:     "selected provider/model is not active or bounded inference did not return a response",
		ReadinessDMProbe:       "independent encrypted DM round-trip did not complete and verify before the deadline",
	}
	if message := messages[e.Gate]; message != "" {
		return message
	}
	return "OpenClaw readiness verification failed"
}

func (v *OpenClawReadinessVerifier) Verify(ctx context.Context, req OpenClawReadinessRequest, progress func(OpenClawReadinessProgress) error) (*OpenClawReadinessEvidence, error) {
	if err := validateReadinessRequest(req); err != nil {
		return nil, err
	}
	started := v.now()
	evidence := &OpenClawReadinessEvidence{
		RequestID: req.RequestID, RunID: req.RunID, Provider: req.Provider, Model: req.Model,
		GateTimingsMS: make(map[OpenClawReadinessGate]int64, len(openClawReadinessGates)),
	}
	report := func(index int, gate OpenClawReadinessGate, message string) error {
		if progress == nil {
			return nil
		}
		return progress(OpenClawReadinessProgress{Gate: gate, Current: index + 1, Total: len(openClawReadinessGates), Message: message})
	}
	runGate := func(index int, gate OpenClawReadinessGate, message string, check func() error) error {
		if err := report(index, gate, message); err != nil {
			return err
		}
		gateStarted := v.now()
		if err := check(); err != nil {
			if safe, ok := err.(*OpenClawReadinessError); ok {
				return safe
			}
			return &OpenClawReadinessError{Gate: gate, Code: "inspection_failed", Retryable: true}
		}
		evidence.GateTimingsMS[gate] = nonNegativeMilliseconds(v.now().Sub(gateStarted))
		return nil
	}

	if err := runGate(0, ReadinessRuntime, "Checking dedicated container and gateway health…", func() error {
		obs, err := v.backend.InspectRuntime(ctx, req)
		if err != nil || !obs.ContainerHealthy || !obs.GatewayHealthy {
			return &OpenClawReadinessError{Gate: ReadinessRuntime, Code: "runtime_unhealthy", Retryable: true}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runGate(1, ReadinessRoute, "Verifying the exclusive agent/account route…", func() error {
		obs, err := v.backend.InspectRoute(ctx, req)
		if err != nil || obs.AgentID != req.AgentID || obs.AccountID != req.AccountID || obs.OwnerRunID != req.RunID || obs.OwnerBinding != req.RuntimeBinding || obs.ExclusiveOwners != 1 {
			return &OpenClawReadinessError{Gate: ReadinessRoute, Code: "route_conflict", Retryable: false}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runGate(2, ReadinessSigner, "Verifying durable NIP-46 connectivity and handoff deletion…", func() error {
		obs, err := v.backend.InspectSigner(ctx, req)
		if err != nil || !obs.Connected || !obs.HandoffDeleted || !strings.EqualFold(obs.ManagedPubkey, req.ManagedPubkey) {
			return &OpenClawReadinessError{Gate: ReadinessSigner, Code: "signer_disconnected", Retryable: true}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runGate(3, ReadinessSubscriptions, "Checking NIP-17 subscriptions on required relays…", func() error {
		obs, err := v.backend.InspectSubscriptions(ctx, req)
		if err != nil || !obs.NIP17Active || !containsEveryRelay(obs.Relays, req.RequiredRelays) {
			return &OpenClawReadinessError{Gate: ReadinessSubscriptions, Code: "subscriptions_incomplete", Retryable: true}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runGate(4, ReadinessInference, "Confirming selected provider/model with bounded inference…", func() error {
		model, err := v.backend.InspectModel(ctx, req)
		if err != nil || model.Provider != req.Provider || model.Model != req.Model {
			return &OpenClawReadinessError{Gate: ReadinessInference, Code: "model_mismatch", Retryable: false}
		}
		inferCtx, cancel := context.WithTimeout(ctx, v.inferenceTimeout)
		defer cancel()
		challenge := readinessCorrelation(req, "inference")
		result, err := v.backend.Infer(inferCtx, req, challenge)
		if err != nil || strings.TrimSpace(result.Response) == "" || result.Duration < 0 || result.Duration > v.inferenceTimeout {
			return &OpenClawReadinessError{Gate: ReadinessInference, Code: "inference_failed", Retryable: true}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := runGate(5, ReadinessDMProbe, "Running independent authenticated encrypted DM round-trip…", func() error {
		probeCtx, cancel := context.WithTimeout(ctx, v.probeTimeout)
		defer cancel()
		correlation := readinessCorrelation(req, "dm")
		obs, err := v.backend.ProbeDM(probeCtx, req, correlation)
		valid := err == nil && obs.Disposable && obs.Controlled && obs.Authenticated && obs.Received && obs.Routed && obs.LLMAnswered && obs.Signed && obs.RelayPublished && obs.Decrypted && obs.ContentVerified && obs.SignatureVerified &&
			obs.CorrelationID == correlation && strings.EqualFold(obs.AuthorPubkey, req.ManagedPubkey) && !strings.EqualFold(obs.ProbePubkey, req.ManagedPubkey) && obs.Duration >= 0 && obs.Duration <= v.probeTimeout && validPublicEventIDs(obs.EventIDs)
		if !valid {
			return &OpenClawReadinessError{Gate: ReadinessDMProbe, Code: "dm_probe_failed", Retryable: true}
		}
		evidence.ProbeEventIDs = append([]string(nil), obs.EventIDs...)
		return nil
	}); err != nil {
		return nil, err
	}

	evidence.VerifiedAt = v.now().UTC()
	evidence.TotalDurationMS = nonNegativeMilliseconds(v.now().Sub(started))
	return evidence, nil
}

func validateReadinessRequest(req OpenClawReadinessRequest) error {
	for name, value := range map[string]string{"request id": req.RequestID, "run id": req.RunID, "agent id": req.AgentID, "account id": req.AccountID, "runtime binding": req.RuntimeBinding, "managed pubkey": req.ManagedPubkey, "provider": req.Provider, "model": req.Model} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("OpenClaw readiness %s is required", name)
		}
	}
	if len(req.ManagedPubkey) != 64 {
		return fmt.Errorf("OpenClaw readiness managed pubkey is invalid")
	}
	return nil
}

func readinessCorrelation(req OpenClawReadinessRequest, purpose string) string {
	sum := sha256.Sum256([]byte("bahia-openclaw-readiness/v1\x00" + req.RequestID + "\x00" + req.RunID + "\x00" + purpose))
	return hex.EncodeToString(sum[:])
}

func containsEveryRelay(active, required []string) bool {
	set := make(map[string]struct{}, len(active))
	for _, relay := range active {
		if normalized := normalizeReadinessRelay(relay); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	for _, relay := range required {
		if _, ok := set[normalizeReadinessRelay(relay)]; !ok {
			return false
		}
	}
	return len(required) > 0
}

func normalizeReadinessRelay(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

func requiredReadinessRelays(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var relays []string
	for _, group := range groups {
		for _, relay := range group {
			normalized := normalizeReadinessRelay(relay)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			relays = append(relays, normalized)
		}
	}
	sort.Strings(relays)
	return relays
}

func validPublicEventIDs(ids []string) bool {
	if len(ids) < 2 {
		return false
	}
	for _, id := range ids {
		if len(id) != 64 {
			return false
		}
		if _, err := hex.DecodeString(id); err != nil {
			return false
		}
	}
	return true
}

func nonNegativeMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}
