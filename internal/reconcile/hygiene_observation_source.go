package reconcile

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

const (
	defaultHygienePendingLimit = 4096
	defaultHygienePendingTTL   = 2 * time.Hour
)

type hygienePendingRequest struct {
	correlation controlplane.MaintenanceRequestCorrelation
	sequence    uint64
	expires     time.Time
}

type hygieneWorkerObservation struct {
	scanCandidates      []HygieneCandidate
	scanTotalCandidates int
	scanTruncated       bool
	scanObservedAt      time.Time
	scanSequence        uint64
	pressure            []HygieneMountPressure
	pressureObservedAt  time.Time
	pressureSequence    uint64
}

// ContextVMHygieneObservationSource correlates authenticated NIP-59 worker
// responses with exact scan/pressure rumors emitted by Bahia. State is
// intentionally process-local: after restart, responses to requests this
// process did not register are rejected and the next reconcile pass refreshes
// the cache.
type ContextVMHygieneObservationSource struct {
	mu            sync.Mutex
	servicePubKey string
	pending       map[string]hygienePendingRequest
	workers       map[string]hygieneWorkerObservation
	nextSequence  uint64
	pendingLimit  int
	pendingTTL    time.Duration
	now           func() time.Time
	logger        *zap.Logger
}

func NewContextVMHygieneObservationSource(servicePubKey string, logger *zap.Logger) (*ContextVMHygieneObservationSource, error) {
	servicePubKey = strings.ToLower(strings.TrimSpace(servicePubKey))
	if !validHexIdentifier(servicePubKey, 32) {
		return nil, fmt.Errorf("hygiene observation source requires a valid Bahia service pubkey")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ContextVMHygieneObservationSource{
		servicePubKey: servicePubKey,
		pending:       make(map[string]hygienePendingRequest),
		workers:       make(map[string]hygieneWorkerObservation),
		pendingLimit:  defaultHygienePendingLimit,
		pendingTTL:    defaultHygienePendingTTL,
		now:           time.Now,
		logger:        logger.Named("hygiene-observations"),
	}, nil
}

// RegisterMaintenanceRequest records the exact finalized request rumor before
// it is published, closing the response-before-Publish-return race.
func (s *ContextVMHygieneObservationSource) RegisterMaintenanceRequest(c controlplane.MaintenanceRequestCorrelation) (func(), error) {
	if s == nil {
		return nil, fmt.Errorf("hygiene observation source is not configured")
	}
	c.Method = strings.TrimSpace(c.Method)
	if c.Method != controlplane.ContextVMMethodMaintenanceScan && c.Method != controlplane.ContextVMMethodMaintenancePressure {
		return nil, fmt.Errorf("unsupported hygiene observation method %q", c.Method)
	}
	c.WorkerPubKey = strings.ToLower(strings.TrimSpace(c.WorkerPubKey))
	c.RequestPubKey = strings.ToLower(strings.TrimSpace(c.RequestPubKey))
	c.RequestEventID = strings.ToLower(strings.TrimSpace(c.RequestEventID))
	c.DTag = strings.TrimSpace(c.DTag)
	if !validHexIdentifier(c.WorkerPubKey, 32) {
		return nil, fmt.Errorf("invalid maintenance worker pubkey")
	}
	if c.RequestPubKey != s.servicePubKey {
		return nil, fmt.Errorf("maintenance request was not authored by this Bahia service")
	}
	if !validHexIdentifier(c.RequestEventID, 32) {
		return nil, fmt.Errorf("invalid maintenance request rumor id")
	}
	if c.DTag == "" {
		return nil, fmt.Errorf("maintenance request JSON-RPC id is required")
	}

	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	if len(s.pending) >= s.pendingLimit {
		return nil, fmt.Errorf("hygiene request correlation capacity exhausted")
	}
	if _, exists := s.pending[c.RequestEventID]; exists {
		return nil, fmt.Errorf("maintenance request rumor is already registered")
	}
	s.nextSequence++
	entry := hygienePendingRequest{
		correlation: c,
		sequence:    s.nextSequence,
		expires:     now.Add(s.pendingTTL),
	}
	s.pending[c.RequestEventID] = entry

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if current, ok := s.pending[c.RequestEventID]; ok && current.sequence == entry.sequence {
				delete(s.pending, c.RequestEventID)
			}
			s.mu.Unlock()
		})
	}, nil
}

// HandleContextVMResponse consumes only standards-conformant NIP-59 responses
// from the worker and request Bahia registered. Rejections are intentionally
// silent on the wire and never log response payloads or host paths.
func (s *ContextVMHygieneObservationSource) HandleContextVMResponse(_ context.Context, response controlplane.ContextVMResponseEnvelope) {
	if s == nil || response.Event == nil || response.EnvelopeFormat != cascontextvm.EnvelopeFormatNIP59 {
		return
	}
	requestID, ok := soleTagValue(response.Event.Tags, "e")
	if !ok {
		return
	}
	requestID = strings.ToLower(strings.TrimSpace(requestID))

	now := s.now().UTC()
	s.mu.Lock()
	s.pruneExpiredLocked(now)
	pending, ok := s.pending[requestID]
	s.mu.Unlock()
	if !ok {
		return
	}

	// Do not let an unauthenticated or misaddressed event consume a legitimate
	// pending request. For NIP-59 rumors Event.PubKey is authenticated by the
	// signed seal verified in UnwrapAny.
	if strings.ToLower(response.Event.PubKey.Hex()) != pending.correlation.WorkerPubKey {
		return
	}
	recipient, ok := soleTagValue(response.Event.Tags, "p")
	if !ok || strings.ToLower(strings.TrimSpace(recipient)) != pending.correlation.RequestPubKey {
		return
	}

	// A response from the expected worker is terminal even when its JSON-RPC or
	// canonical result payload is invalid. Accepting a corrected second result
	// for one request would weaken replay/idempotency semantics.
	s.mu.Lock()
	current, stillPending := s.pending[requestID]
	if !stillPending || current.sequence != pending.sequence {
		s.mu.Unlock()
		return
	}
	delete(s.pending, requestID)
	s.mu.Unlock()

	if response.JSONRPC != cascontextvm.JSONRPCVersion || response.MethodPresent || !response.IDPresent {
		s.logRejected(pending, "invalid JSON-RPC envelope")
		return
	}
	var responseID string
	if err := json.Unmarshal(response.ID, &responseID); err != nil || responseID != pending.correlation.DTag {
		s.logRejected(pending, "JSON-RPC id mismatch")
		return
	}
	if response.ErrorPresent || !response.ResultPresent || string(response.Result) == "null" {
		s.logRejected(pending, "worker returned an error or empty result")
		return
	}
	receivedAt := response.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = now
	}

	switch pending.correlation.Method {
	case controlplane.ContextVMMethodMaintenanceScan:
		s.consumeScan(pending, response.Result, receivedAt)
	case controlplane.ContextVMMethodMaintenancePressure:
		s.consumePressure(pending, response.Result, receivedAt)
	}
}

func (s *ContextVMHygieneObservationSource) consumeScan(pending hygienePendingRequest, raw json.RawMessage, receivedAt time.Time) {
	var payload cascadia.CascadiaMaintenanceScanResultV1Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		s.logRejected(pending, "scan result is not valid JSON")
		return
	}
	if err := payload.Validate(); err != nil {
		s.logRejected(pending, "scan result failed canonical validation")
		return
	}
	if payload.TotalCandidates < len(payload.Candidates) || (!payload.Truncated && payload.TotalCandidates != len(payload.Candidates)) || (payload.Truncated && payload.TotalCandidates <= len(payload.Candidates)) {
		s.logRejected(pending, "scan result candidate totals are inconsistent")
		return
	}
	observedAt, err := effectiveObservationTime(payload.ScannedAt, receivedAt)
	if err != nil {
		s.logRejected(pending, "scan result timestamp is invalid")
		return
	}
	candidates := make([]HygieneCandidate, 0, len(payload.Candidates))
	if !payload.Truncated {
		for _, candidate := range payload.Candidates {
			candidates = append(candidates, HygieneCandidate{
				ID: candidate.Id, Path: candidate.Path, Class: candidate.Class,
				Reason: candidate.Reason, Canonical: candidate.Canonical, Blocked: candidate.Blocked,
			})
		}
	}

	s.mu.Lock()
	state := s.workers[pending.correlation.WorkerPubKey]
	if pending.sequence > state.scanSequence {
		state.scanCandidates = candidates
		state.scanTotalCandidates = payload.TotalCandidates
		state.scanTruncated = payload.Truncated
		state.scanObservedAt = observedAt
		state.scanSequence = pending.sequence
		s.workers[pending.correlation.WorkerPubKey] = state
	}
	s.mu.Unlock()
}

func (s *ContextVMHygieneObservationSource) consumePressure(pending hygienePendingRequest, raw json.RawMessage, receivedAt time.Time) {
	var payload cascadia.CascadiaMaintenancePressureResultV1Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		s.logRejected(pending, "pressure result is not valid JSON")
		return
	}
	if err := payload.Validate(); err != nil {
		s.logRejected(pending, "pressure result failed canonical validation")
		return
	}
	observedAt, err := effectiveObservationTime(payload.SampledAt, receivedAt)
	if err != nil {
		s.logRejected(pending, "pressure result timestamp is invalid")
		return
	}
	pressure := make([]HygieneMountPressure, 0, len(payload.Mounts))
	for _, mount := range payload.Mounts {
		if mount.FreeBytes > mount.TotalBytes || mount.FreeInodes > mount.TotalInodes {
			s.logRejected(pending, "pressure result free capacity exceeds total capacity")
			return
		}
		pressure = append(pressure, HygieneMountPressure{
			Path: mount.Path, UsedPct: mount.UsedPct,
			TotalInodes: uint64(mount.TotalInodes), FreeInodes: uint64(mount.FreeInodes),
		})
	}

	s.mu.Lock()
	state := s.workers[pending.correlation.WorkerPubKey]
	if pending.sequence > state.pressureSequence {
		state.pressure = pressure
		state.pressureObservedAt = observedAt
		state.pressureSequence = pending.sequence
		s.workers[pending.correlation.WorkerPubKey] = state
	}
	s.mu.Unlock()
}

func (s *ContextVMHygieneObservationSource) Latest(_ context.Context, workerPubKey string) (*HygieneObservation, error) {
	if s == nil {
		return nil, nil
	}
	workerPubKey = strings.ToLower(strings.TrimSpace(workerPubKey))
	s.mu.Lock()
	state, ok := s.workers[workerPubKey]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}
	observedAt := state.scanObservedAt
	if state.pressureObservedAt.After(observedAt) {
		observedAt = state.pressureObservedAt
	}
	return &HygieneObservation{
		WorkerPubKey:       workerPubKey,
		Candidates:         append([]HygieneCandidate(nil), state.scanCandidates...),
		Pressure:           append([]HygieneMountPressure(nil), state.pressure...),
		ObservedAt:         observedAt,
		ScanObservedAt:     state.scanObservedAt,
		PressureObservedAt: state.pressureObservedAt,
		ScanTruncated:      state.scanTruncated,
		TotalCandidates:    state.scanTotalCandidates,
	}, nil
}

func (s *ContextVMHygieneObservationSource) pruneExpiredLocked(now time.Time) {
	for id, pending := range s.pending {
		if !now.Before(pending.expires) {
			delete(s.pending, id)
		}
	}
}

func (s *ContextVMHygieneObservationSource) logRejected(pending hygienePendingRequest, reason string) {
	s.logger.Warn("rejected correlated hygiene response",
		zap.String("worker", pending.correlation.WorkerPubKey),
		zap.String("method", pending.correlation.Method),
		zap.String("reason", reason),
	)
}

func effectiveObservationTime(value string, receivedAt time.Time) (time.Time, error) {
	producerAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, err
	}
	producerAt = producerAt.UTC()
	if receivedAt.Before(producerAt) {
		return receivedAt.UTC(), nil
	}
	return producerAt, nil
}

func soleTagValue(tags nostr.Tags, name string) (string, bool) {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		count++
		if count > 1 || len(tag) < 2 || strings.TrimSpace(tag[1]) == "" {
			return "", false
		}
		value = tag[1]
	}
	return value, count == 1
}

func validHexIdentifier(value string, bytes int) bool {
	if len(value) != bytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}
