package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/codec/betterbinary"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

// The adapter ledger (bahia-nfc95) is the production adapters' durable state
// of governed provisioning: per request, the resolved immutable input, the
// Soul projection, the registry identifiers and the per-step resource
// references a replay re-inspects; per agent id, the identity reservation
// that stops a second request from minting a second identity. Its authority
// is one replaceable kind-30900 record per request and per identity in the
// daemon's local event store (and on the relays, through the outbox):
//
//	d = soul-factory:adapter-request:<sha256(request id)>
//	d = soul-factory:adapter-identity:<sha256(agent id)>
//	t = soul-factory-adapter-ledger, legacy_kind = 32028
//
// Confidentiality: the content is the fleet-OCK envelope
// (bahia.confidential.aead.v1). Governed provisioning is fleet-operated (its
// saga operator commands are fleet-gated), and the record names internal
// registry identifiers, the resolved specification and the Soul's generated
// persona, so it is fleet-visible, never plain. The retained signed success
// result is service-only (service_inner, NIP-44 to the service key): whoever
// holds a signed, possibly undelivered event can deliver it, and delivery is
// the daemon's. The fleet-visible document names it by event id only. No
// field is a secret value: the bunker URI is never written (it is a one-time
// Signet handoff) and the Signet identity contract is excluded from the
// payload. The public tags carry only the coordinate, the family, the record
// kind and the version.
//
// Precedence on read is the saga store's: the highest version wins; on a tie
// the copy this process committed, then the canonical record, then the file
// cache. A tombstone at version v retires every copy at or below v. A daemon
// on a fresh host, with no file, resumes from the record alone; a daemon
// upgraded in place resumes from its pre-canonical file (version 0) once and
// publishes the record on the next save.
const (
	ledgerRequestSchema  = "bahia.state.soulfactory-adapter-request.v1"
	ledgerIdentitySchema = "bahia.state.soulfactory-adapter-identity.v1"
	ledgerDomain         = "soul-factory"
	ledgerEntity         = "adapter-ledger"
	ledgerTagRecord      = "record"
	ledgerTagVersion     = "version"
	ledgerRecordRequest  = "request"
	ledgerRecordIdentity = "identity"
	ledgerRequestDPrefix = "soul-factory:adapter-request:"
	ledgerIdentityPrefix = "soul-factory:adapter-identity:"
)

// LedgerEncryptor is the confidential cp-state seam the ledger publishes
// through. *controlplane.ConfidentialEncryptor satisfies it structurally.
type LedgerEncryptor interface {
	EncryptConfidential(ctx context.Context, orgID string, plaintext []byte, legacyKind int, dTag, topic string, serviceOnlyPlaintext []byte) (string, error)
	DecryptConfidential(ctx context.Context, content string, legacyKind int, dTag, topic string) ([]byte, error)
	DecryptServiceInner(ctx context.Context, content string) ([]byte, error)
}

// productionLedgerSeams are the daemon seams that make the ledger canonical.
// The zero value is the file-only journal tests and an identity-less daemon
// run with; a partial set fails closed.
type productionLedgerSeams struct {
	Reader        saga.EventReader
	Publisher     saga.EventPublisher
	Encryptor     LedgerEncryptor
	ServicePubkey string
	Logger        *slog.Logger
	Now           func() time.Time
}

func (s productionLedgerSeams) empty() bool {
	return s.Reader == nil && s.Publisher == nil && s.Encryptor == nil && strings.TrimSpace(s.ServicePubkey) == ""
}

// ledgerCommitted is one committed copy of a ledger payload and the
// created_at of the record that committed it, which the next publish must
// replace.
type ledgerCommitted[T any] struct {
	value     *T
	version   uint64
	createdAt nostr.Timestamp
	deleted   bool
}

// ledgerServiceOnly is the service-only layer of a request record.
type ledgerServiceOnly struct {
	SuccessResult *nostr.Event `json:"success_result,omitempty"`
}

type productionStateStore struct {
	dir       string
	reader    saga.EventReader
	publisher saga.EventPublisher
	encryptor LedgerEncryptor
	author    nostr.PubKey
	logger    *slog.Logger
	now       func() time.Time

	// mu serializes every read-modify-publish of both record kinds, so two
	// requests for one agent id cannot both observe an absent reservation.
	mu         sync.Mutex
	requests   map[string]ledgerCommitted[productionProvisioningState]
	identities map[string]ledgerCommitted[productionIdentityReservation]
}

func newProductionStateStore(dir string, seams productionLedgerSeams) (*productionStateStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "identities"), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	logger := seams.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := seams.Now
	if now == nil {
		now = time.Now
	}
	store := &productionStateStore{
		dir: dir, logger: logger.With("component", "soulfactory_adapter_ledger"), now: now,
		requests:   map[string]ledgerCommitted[productionProvisioningState]{},
		identities: map[string]ledgerCommitted[productionIdentityReservation]{},
	}
	if seams.empty() {
		return store, nil
	}
	if seams.Reader == nil || seams.Publisher == nil || seams.Encryptor == nil {
		return nil, errors.New("canonical adapter ledger records require the daemon's local event store, publisher and confidential encryptor")
	}
	author, err := nostr.PubKeyFromHex(strings.TrimSpace(seams.ServicePubkey))
	if err != nil {
		return nil, fmt.Errorf("canonical adapter ledger records require the daemon's service identity (nostr.private_key): %w", err)
	}
	store.reader, store.publisher, store.encryptor, store.author = seams.Reader, seams.Publisher, seams.Encryptor, author
	return store, nil
}

// canonical reports whether the ledger's authority is the event store.
func (s *productionStateStore) canonical() bool { return s.reader != nil }

func productionStateName(namespace, value string) string {
	return ledgerHash(namespace, value) + ".json"
}

func ledgerHash(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// ledgerRequestDTag is the coordinate of a request's ledger record.
func ledgerRequestDTag(requestID string) string {
	return ledgerRequestDPrefix + ledgerHash("request", requestID)
}

// ledgerIdentityDTag is the coordinate of an agent id's reservation record.
func ledgerIdentityDTag(agentID string) string {
	return ledgerIdentityPrefix + ledgerHash("agent", agentID)
}

func (s *productionStateStore) requestPath(requestID string) string {
	return filepath.Join(s.dir, "requests", productionStateName("request", requestID))
}

func (s *productionStateStore) reservationPath(agentID string) string {
	return filepath.Join(s.dir, "identities", productionStateName("agent", agentID))
}

func validateProductionState(state *productionProvisioningState, requestID string) error {
	if state.Schema != productionStateSchema || state.RequestID != requestID || state.AgentID == "" || state.RunID == "" || state.SpecHash == "" {
		return fmt.Errorf("invalid production provisioning state")
	}
	return nil
}

// load returns the request's state from whichever authority holds the
// highest version.
func (s *productionStateStore) load(ctx context.Context, requestID string) (*productionProvisioningState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, _, err := s.currentRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errProductionStateNotFound
	}
	state, err := ledgerClone(current)
	if err != nil {
		return nil, err
	}
	if state.Steps == nil {
		state.Steps = map[OrderedStep]productionStepState{}
	}
	return state, nil
}

// save commits the next version of the request's state: it is published
// (or durably queued) before the step that produced it is reported complete.
// state.Version must be the version the caller loaded; on success it is
// advanced to the committed version. A state whose version is not the
// committed one is a conflict, never silently replaced.
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
	current, replaces, err := s.currentRequest(ctx, state.RequestID)
	if err != nil {
		return err
	}
	switch {
	case current != nil && current.Version != state.Version:
		return fmt.Errorf("%w: adapter ledger holds version %d of request %s, caller has %d", saga.ErrConflict, current.Version, state.RequestID, state.Version)
	case current == nil && state.Version != 0:
		return fmt.Errorf("%w: adapter ledger has no live record of request %s for version %d", saga.ErrConflict, state.RequestID, state.Version)
	}
	next, err := ledgerClone(state)
	if err != nil {
		return err
	}
	next.Version = state.Version + 1
	next.SuccessResultID = ""
	if next.SuccessResult != nil {
		next.SuccessResultID = next.SuccessResult.ID.Hex()
	}
	createdAt, err := s.publishRequest(ctx, next, replaces, false)
	if err != nil {
		return err
	}
	s.requests[next.RequestID] = ledgerCommitted[productionProvisioningState]{value: next, version: next.Version, createdAt: createdAt}
	s.cacheRequest(next)
	state.Version, state.SuccessResultID = next.Version, next.SuccessResultID
	return nil
}

// remove tombstones the request's ledger record and drops its cache. It is
// idempotent: a request with no live record is already removed.
func (s *productionStateStore) remove(ctx context.Context, requestID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.currentRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	createdAt, err := s.publishRequest(ctx, current, replaces, true)
	if err != nil {
		return err
	}
	s.requests[requestID] = ledgerCommitted[productionProvisioningState]{version: current.Version, createdAt: createdAt, deleted: true}
	if err := os.Remove(s.requestPath(requestID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		if s.canonical() {
			s.logger.Warn("adapter ledger cache remove failed", "request_id", requestID, "error", err)
			return nil
		}
		return err
	}
	return nil
}

// reservation returns the agent id's identity reservation, creating it for
// spec when create is set and none exists. created reports a creation.
func (s *productionStateStore) reservation(ctx context.Context, spec ProvisioningSpec, create bool) (*productionIdentityReservation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.currentIdentity(ctx, spec.AgentID)
	if err != nil {
		return nil, false, err
	}
	if current != nil {
		if current.Schema != productionReservationSchema || current.AgentID != spec.AgentID {
			return nil, false, fmt.Errorf("invalid governed identity reservation")
		}
		copied := *current
		return &copied, false, nil
	}
	if !create {
		return nil, false, nil
	}
	next := &productionIdentityReservation{
		Schema: productionReservationSchema, AgentID: spec.AgentID, SpecHash: spec.SpecHash,
		RequestID: spec.RequestID, RunID: spec.RunID, CreatedAt: s.now().UTC(), Version: 1,
	}
	createdAt, err := s.publishIdentity(ctx, next, replaces, false)
	if err != nil {
		return nil, false, err
	}
	s.identities[spec.AgentID] = ledgerCommitted[productionIdentityReservation]{value: next, version: next.Version, createdAt: createdAt}
	if err := writeProductionJSON(s.reservationPath(spec.AgentID), next); err != nil {
		if !s.canonical() {
			return nil, false, err
		}
		s.logger.Warn("adapter ledger reservation cache write failed", "agent_id", spec.AgentID, "error", err)
	}
	copied := *next
	return &copied, true, nil
}

// removeIdentity tombstones the agent id's identity reservation and drops
// its cache, releasing the agent id for a later request. It is idempotent: an
// agent id with no live reservation is already released. Callers decide
// eligibility (ledgerPurgingStore.retireIdentity); this only commits it.
func (s *productionStateStore) removeIdentity(ctx context.Context, agentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, replaces, err := s.currentIdentity(ctx, agentID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	createdAt, err := s.publishIdentity(ctx, current, replaces, true)
	if err != nil {
		return err
	}
	s.identities[agentID] = ledgerCommitted[productionIdentityReservation]{version: current.Version, createdAt: createdAt, deleted: true}
	if err := os.Remove(s.reservationPath(agentID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		if s.canonical() {
			s.logger.Warn("adapter ledger reservation cache remove failed", "agent_id", agentID, "error", err)
			return nil
		}
		return err
	}
	return nil
}

// currentRequest is the request's live state from the highest-version
// authority and the created_at the next record must replace.
func (s *productionStateStore) currentRequest(ctx context.Context, requestID string) (*productionProvisioningState, nostr.Timestamp, error) {
	process, known := s.requests[requestID]
	var record *ledgerCommitted[productionProvisioningState]
	if s.canonical() {
		found, err := s.readRequestRecord(ctx, requestID)
		if err != nil {
			return nil, 0, err
		}
		record = found
	}
	var file productionProvisioningState
	cached, err := readProductionJSON(s.requestPath(requestID), &file)
	if err != nil {
		return nil, 0, err
	}
	var fileCopy *productionProvisioningState
	if cached {
		if err := validateProductionState(&file, requestID); err != nil {
			return nil, 0, err
		}
		fileCopy = &file
	}
	best, replaces := ledgerCurrent(process, known, record, fileCopy, func(st *productionProvisioningState) uint64 { return st.Version })
	return best, replaces, nil
}

// currentIdentity is the agent id's live reservation, as currentRequest.
func (s *productionStateStore) currentIdentity(ctx context.Context, agentID string) (*productionIdentityReservation, nostr.Timestamp, error) {
	process, known := s.identities[agentID]
	var record *ledgerCommitted[productionIdentityReservation]
	if s.canonical() {
		found, err := s.readIdentityRecord(ctx, agentID)
		if err != nil {
			return nil, 0, err
		}
		record = found
	}
	var file productionIdentityReservation
	cached, err := readProductionJSON(s.reservationPath(agentID), &file)
	if err != nil {
		return nil, 0, err
	}
	var fileCopy *productionIdentityReservation
	if cached {
		fileCopy = &file
	}
	best, replaces := ledgerCurrent(process, known, record, fileCopy, func(r *productionIdentityReservation) uint64 { return r.Version })
	return best, replaces, nil
}

// ledgerCurrent applies the precedence rule to the three copies of one
// payload: the copy this process committed (when known), the canonical
// record and the file cache. The highest version wins; a tie goes to the
// first of them; a tombstone at version v retires every copy at or below v,
// so neither a stale file nor an older in-process copy can resurrect a
// removed record. replaces is the newest created_at known.
func ledgerCurrent[T any](process ledgerCommitted[T], known bool, record *ledgerCommitted[T], file *T, version func(*T) uint64) (*T, nostr.Timestamp) {
	var (
		best       *T
		replaces   nostr.Timestamp
		tombstoned uint64
		hasTomb    bool
	)
	consider := func(copy ledgerCommitted[T]) {
		if copy.deleted {
			if !hasTomb || copy.version > tombstoned {
				tombstoned, hasTomb = copy.version, true
			}
			return
		}
		if best == nil || version(copy.value) > version(best) {
			best = copy.value
		}
	}
	if known {
		replaces = process.createdAt
		consider(process)
	}
	if record != nil {
		replaces = max(replaces, record.createdAt)
		consider(*record)
	}
	if file != nil {
		consider(ledgerCommitted[T]{value: file, version: version(file)})
	}
	if best != nil && hasTomb && version(best) <= tombstoned {
		best = nil
	}
	return best, replaces
}

// readRequestRecord reads the request's canonical record, decrypting its
// fleet-visible document and its service-only layer.
func (s *productionStateStore) readRequestRecord(ctx context.Context, requestID string) (*ledgerCommitted[productionProvisioningState], error) {
	dTag := ledgerRequestDTag(requestID)
	ev, err := s.latestRecord(ctx, dTag, ledgerRecordRequest)
	if err != nil || ev == nil {
		return nil, err
	}
	version, deleted, err := ledgerRecordMeta(ev)
	if err != nil {
		return nil, err
	}
	if deleted {
		return &ledgerCommitted[productionProvisioningState]{version: version, createdAt: ev.CreatedAt, deleted: true}, nil
	}
	document, err := s.encryptor.DecryptConfidential(ctx, ev.Content, int(kinds.CPStateFamilySoulFactoryAdapterLedger), dTag, kinds.CPStateTopicSoulFactoryAdapterLedger)
	if err != nil {
		return nil, fmt.Errorf("decrypt adapter ledger record of request %s: %w", requestID, err)
	}
	var state productionProvisioningState
	if err := json.Unmarshal(document, &state); err != nil {
		return nil, fmt.Errorf("decode adapter ledger record of request %s: %w", requestID, err)
	}
	if err := validateProductionState(&state, requestID); err != nil {
		return nil, fmt.Errorf("adapter ledger record at %s: %w", dTag, err)
	}
	if state.Version != version {
		return nil, fmt.Errorf("adapter ledger record at %s carries version %d but is tagged %d", dTag, state.Version, version)
	}
	serviceOnly, err := s.encryptor.DecryptServiceInner(ctx, ev.Content)
	if err != nil {
		return nil, fmt.Errorf("decrypt adapter ledger service layer of request %s: %w", requestID, err)
	}
	if len(serviceOnly) > 0 {
		var inner ledgerServiceOnly
		if err := json.Unmarshal(serviceOnly, &inner); err != nil {
			return nil, fmt.Errorf("decode adapter ledger service layer of request %s: %w", requestID, err)
		}
		state.SuccessResult = inner.SuccessResult
	}
	if state.SuccessResultID != "" && (state.SuccessResult == nil || state.SuccessResult.ID.Hex() != state.SuccessResultID) {
		return nil, fmt.Errorf("adapter ledger record at %s names success result %s its service layer does not carry", dTag, state.SuccessResultID)
	}
	return &ledgerCommitted[productionProvisioningState]{value: &state, version: version, createdAt: ev.CreatedAt}, nil
}

// readIdentityRecord reads the agent id's canonical reservation record.
func (s *productionStateStore) readIdentityRecord(ctx context.Context, agentID string) (*ledgerCommitted[productionIdentityReservation], error) {
	dTag := ledgerIdentityDTag(agentID)
	ev, err := s.latestRecord(ctx, dTag, ledgerRecordIdentity)
	if err != nil || ev == nil {
		return nil, err
	}
	version, deleted, err := ledgerRecordMeta(ev)
	if err != nil {
		return nil, err
	}
	if deleted {
		return &ledgerCommitted[productionIdentityReservation]{version: version, createdAt: ev.CreatedAt, deleted: true}, nil
	}
	document, err := s.encryptor.DecryptConfidential(ctx, ev.Content, int(kinds.CPStateFamilySoulFactoryAdapterLedger), dTag, kinds.CPStateTopicSoulFactoryAdapterLedger)
	if err != nil {
		return nil, fmt.Errorf("decrypt adapter ledger reservation of agent %s: %w", agentID, err)
	}
	var reservation productionIdentityReservation
	if err := json.Unmarshal(document, &reservation); err != nil {
		return nil, fmt.Errorf("decode adapter ledger reservation of agent %s: %w", agentID, err)
	}
	if reservation.Schema != productionReservationSchema || reservation.AgentID != agentID || reservation.Version != version {
		return nil, fmt.Errorf("adapter ledger reservation at %s is not a %s record of agent %s", dTag, productionReservationSchema, agentID)
	}
	return &ledgerCommitted[productionIdentityReservation]{value: &reservation, version: version, createdAt: ev.CreatedAt}, nil
}

// latestRecord returns the newest record of this family and record kind the
// daemon authored at the coordinate, or nil. Ties on created_at go to the
// lowest id, as NIP-01 replacement does.
func (s *productionStateStore) latestRecord(ctx context.Context, dTag, record string) (*nostr.Event, error) {
	var latest *nostr.Event
	for ev := range s.reader.QueryEvents(nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{s.author},
		Tags: nostr.TagMap{kinds.CASControlStateTagD: {dTag}},
	}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != s.author || int(ev.Kind) != kinds.CASControlState {
			continue
		}
		if tagValue(ev.Tags, kinds.CASControlStateTagLegacyKind) != kinds.CPStateFamilySoulFactoryAdapterLedger.TagValue() {
			return nil, fmt.Errorf("adapter ledger coordinate %s holds a record of another family", dTag)
		}
		if tagValue(ev.Tags, ledgerTagRecord) != record {
			return nil, fmt.Errorf("adapter ledger coordinate %s holds a %q record, want %q", dTag, tagValue(ev.Tags, ledgerTagRecord), record)
		}
		if latest == nil || ev.CreatedAt > latest.CreatedAt || (ev.CreatedAt == latest.CreatedAt && ev.ID.Hex() < latest.ID.Hex()) {
			copied := ev
			latest = &copied
		}
	}
	return latest, nil
}

// ledgerRecordMeta reads the version and tombstone marker of a record.
func ledgerRecordMeta(ev *nostr.Event) (uint64, bool, error) {
	version, err := strconv.ParseUint(tagValue(ev.Tags, ledgerTagVersion), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("adapter ledger record %s has no version tag: %w", ev.ID.Hex(), err)
	}
	return version, tagValue(ev.Tags, kinds.CASControlStateTagDeleted) == "true", nil
}

// publishRequest publishes the request's record (or its tombstone) and
// writes the cache in file-only mode. The fleet-visible document is the
// state without the signed success result, which is the service-only layer.
func (s *productionStateStore) publishRequest(ctx context.Context, state *productionProvisioningState, replaces nostr.Timestamp, deleted bool) (nostr.Timestamp, error) {
	if !s.canonical() {
		if deleted {
			return 0, nil
		}
		return 0, writeProductionJSON(s.requestPath(state.RequestID), state)
	}
	content := ""
	if !deleted {
		document := *state
		document.SuccessResult = nil
		plaintext, err := json.Marshal(&document)
		if err != nil {
			return 0, fmt.Errorf("encode adapter ledger record: %w", err)
		}
		var serviceOnly []byte
		if state.SuccessResult != nil {
			if serviceOnly, err = json.Marshal(ledgerServiceOnly{SuccessResult: state.SuccessResult}); err != nil {
				return 0, fmt.Errorf("encode adapter ledger service layer: %w", err)
			}
		}
		content, err = s.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, plaintext, int(kinds.CPStateFamilySoulFactoryAdapterLedger), ledgerRequestDTag(state.RequestID), kinds.CPStateTopicSoulFactoryAdapterLedger, serviceOnly)
		if err != nil {
			return 0, fmt.Errorf("encrypt adapter ledger record: %w", err)
		}
	}
	return s.publish(ctx, ledgerRequestDTag(state.RequestID), ledgerRequestSchema, ledgerRecordRequest, state.Version, content, replaces, deleted, "request "+state.RequestID)
}

// publishIdentity publishes the agent id's reservation record (or its
// tombstone).
func (s *productionStateStore) publishIdentity(ctx context.Context, reservation *productionIdentityReservation, replaces nostr.Timestamp, deleted bool) (nostr.Timestamp, error) {
	if !s.canonical() {
		return 0, nil
	}
	content := ""
	if !deleted {
		plaintext, err := json.Marshal(reservation)
		if err != nil {
			return 0, fmt.Errorf("encode adapter ledger reservation: %w", err)
		}
		content, err = s.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, plaintext, int(kinds.CPStateFamilySoulFactoryAdapterLedger), ledgerIdentityDTag(reservation.AgentID), kinds.CPStateTopicSoulFactoryAdapterLedger, nil)
		if err != nil {
			return 0, fmt.Errorf("encrypt adapter ledger reservation: %w", err)
		}
	}
	return s.publish(ctx, ledgerIdentityDTag(reservation.AgentID), ledgerIdentitySchema, ledgerRecordIdentity, reservation.Version, content, replaces, deleted, "agent "+reservation.AgentID)
}

// publish signs and publishes one ledger record outbox-first. Its created_at
// is strictly after the record it replaces, so a state saved twice within
// one second still replaces its predecessor. A publish the outbox keeps for
// retry counts as published. The content must fit the event store.
func (s *productionStateStore) publish(ctx context.Context, dTag, schema, record string, version uint64, content string, replaces nostr.Timestamp, deleted bool, what string) (nostr.Timestamp, error) {
	if len(content) > betterbinary.MaxContentSize {
		return 0, fmt.Errorf("adapter ledger record of %s is %d bytes, over the %d-byte event limit", what, len(content), betterbinary.MaxContentSize)
	}
	createdAt := nostr.Timestamp(s.now().Unix())
	if createdAt <= replaces {
		createdAt = replaces + 1
	}
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Content: content, Tags: nostr.Tags{
		{kinds.CASControlStateTagD, dTag},
		{kinds.CASControlStateTagDomain, ledgerDomain},
		{kinds.CASControlStateTagSchema, schema},
		{kinds.CASControlStateTagEntity, ledgerEntity},
		{"t", kinds.CPStateTopicSoulFactoryAdapterLedger},
		{kinds.CASControlStateTagLegacyKind, kinds.CPStateFamilySoulFactoryAdapterLedger.TagValue()},
		{kinds.CASControlStateTagDeleted, strconv.FormatBool(deleted)},
		{ledgerTagRecord, record},
		{ledgerTagVersion, strconv.FormatUint(version, 10)},
	}}
	if err := s.publisher.PublishSignedEvent(ctx, &event); err != nil && !nostrutil.IsPublishQueued(err) {
		return 0, fmt.Errorf("publish adapter ledger record of %s: %w", what, err)
	}
	return createdAt, nil
}

// cacheRequest writes the request's state to the file cache. In canonical
// mode a cache failure is logged, never reported: the record is durable.
func (s *productionStateStore) cacheRequest(state *productionProvisioningState) {
	if !s.canonical() {
		return
	}
	if err := writeProductionJSON(s.requestPath(state.RequestID), state); err != nil {
		s.logger.Warn("adapter ledger cache write failed", "request_id", state.RequestID, "error", err)
	}
}

// ledgerClone deep-copies a state through its JSON form, the form every
// authority holds it in.
func ledgerClone(in *productionProvisioningState) (*productionProvisioningState, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encode production provisioning state: %w", err)
	}
	var out productionProvisioningState
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode production provisioning state: %w", err)
	}
	return &out, nil
}

// readProductionJSON decodes the file at path into target, reporting false
// when there is no file.
func readProductionJSON(path string, target any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return true, nil
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

// soulLookup reads the agent id's current Soul projection, nil when there is
// none. *Reactor.GetSoul satisfies it; it fails closed on an incomplete relay
// read, and so does the identity retirement that asks it.
type soulLookup func(ctx context.Context, agentID string) (*domain.AgentSoul, error)

// ledgerPurgingStore is the saga store the engine and saga.PurgeExpired see:
// deleting a run also tombstones the request's adapter-ledger record and,
// when the rule below allows, the agent id's identity reservation, all under
// the request lock so a running workflow is never purged from under itself.
//
// Order: the ledger records are retired first and the saga run last. Every
// step is idempotent and the saga run is what the next retention pass lists,
// so a purge interrupted anywhere is completed by the next pass instead of
// leaking a ledger record whose run is already gone.
//
// Identity rule. The reservation names the request that reserved the agent
// id. It is retired with that request's run only; a run that lost the agent
// id to another request (ownership_conflict) leaves the winner's reservation
// alone. And it is retired only when no Soul projection of the agent id is
// live, that is GetSoul returns nothing or a revoked Soul: an identity
// outlives its request whenever the agent exists outside the governed path
// (legacy provisioning, adoption) or was re-bound after the run ended, and
// the reservation is what keeps a later request from minting a second
// identity for that live agent. A Soul read that fails, or no Soul seam at
// all, keeps the reservation: releasing an agent id is never done blind.
type ledgerPurgingStore struct {
	saga.Store
	states *productionStateStore
	souls  soulLookup
}

func (s ledgerPurgingStore) Delete(ctx context.Context, requestID string, expectedVersion uint64) error {
	unlock, err := s.states.lockRequest(ctx, requestID)
	if err != nil {
		return err
	}
	defer unlock()
	run, err := s.Store.Load(ctx, requestID)
	if err != nil {
		return err
	}
	if run.Version != expectedVersion {
		return saga.ErrConflict
	}
	// The Soul read is the one step that can fail closed; take it before any
	// record is touched so a failed read leaves the run whole for the next pass.
	release, err := s.releasesIdentity(ctx, run)
	if err != nil {
		return err
	}
	if err := s.states.remove(ctx, requestID); err != nil {
		return err
	}
	if release {
		if err := s.states.removeIdentity(ctx, run.AgentID); err != nil {
			return err
		}
		s.states.logger.Info("adapter ledger identity reservation released", "agent_id", run.AgentID, "request_id", run.RequestID)
	}
	return s.Store.Delete(ctx, requestID, expectedVersion)
}

// releasesIdentity applies the identity rule to the run being purged and
// reports whether the agent id's reservation is released with it.
func (s ledgerPurgingStore) releasesIdentity(ctx context.Context, run *saga.Run) (bool, error) {
	reservation, _, err := s.states.reservation(ctx, ProvisioningSpec{AgentID: run.AgentID}, false)
	if err != nil {
		return false, err
	}
	if reservation == nil || reservation.RequestID != run.RequestID {
		return false, nil
	}
	if s.souls == nil {
		s.states.logger.Warn("adapter ledger identity reservation kept: no Soul lookup to prove the agent id is free", "agent_id", run.AgentID, "request_id", run.RequestID)
		return false, nil
	}
	soul, err := s.souls(ctx, run.AgentID)
	if err != nil {
		return false, fmt.Errorf("inspect Soul of agent %s before releasing its identity reservation: %w", run.AgentID, err)
	}
	if soul != nil && soul.Status != domain.SoulStatusRevoked {
		s.states.logger.Info("adapter ledger identity reservation kept: agent has a live Soul", "agent_id", run.AgentID, "request_id", run.RequestID, "soul_status", string(soul.Status))
		return false, nil
	}
	return true, nil
}
