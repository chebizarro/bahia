package hiveci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// CanonicalState is the daemon's canonical Hive-CI state: pipeline policies,
// the processing state of each signed result and the accepted-release ledger,
// published before any SQL index is written and read back from the local
// event store. *nostr.HiveCICanonicalPublisher satisfies it.
type CanonicalState interface {
	PublishPipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error
	ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
	PublishResultState(context.Context, domain.HiveCIResultState) error
	ListResultStates(ctx context.Context, resultEventID, runEventID string) ([]domain.HiveCIResultState, error)
	PublishAcceptedRelease(context.Context, domain.HiveCIAcceptedRelease) error
	ListAcceptedReleases(ctx context.Context, releaseIdentity string) ([]domain.HiveCIAcceptedRelease, error)
	PublishReleaseConflict(context.Context, domain.HiveCIReleaseConflict) error
}

// hiveCIPolicyNamespace derives the id of a pipeline policy that has no
// SQL-era id from its key, so every daemon names the same policy the same way.
var hiveCIPolicyNamespace = uuid.MustParse("4f0a7b8e-2d3c-4e5f-9a1b-6c7d8e9f0a1b")

// evidenceQueryLimit bounds how many signed events one read scans.
const evidenceQueryLimit = 1000

// CanonicalRepository is the Hive-CI store. Signed 5401 runs and
// 5402 results are the evidence: they live in the local event store, where the
// subscriber saves them, and every read decodes the signed event. What the
// daemon decides about a result (its processing state and retry count), the
// pipeline policies and the accepted-release ledger are the
// daemon's own canonical records, published through the outbox before the
// optional SQL index is written. SQL failures are logged and repaired by
// RebuildIndex; with no database the store is complete.
//
// It has the shape of repository.HiveCIRepository because that is what the
// subscriber and the pipeline bridge speak, and of ReleaseStore because that
// is what the release ingestor commits through.
type CanonicalRepository struct {
	events    EvidenceStore
	canonical CanonicalState
	index     repository.HiveCIRepository
	trusted   map[string]struct{}
	// mu serializes read-check-publish sequences on this process.
	mu     sync.Mutex
	logger *zap.Logger
	now    func() time.Time
}

var (
	_ repository.HiveCIRepository = (*CanonicalRepository)(nil)
	_ ReleaseStore                = (*CanonicalRepository)(nil)
)

// NewCanonicalRepository returns the canonical store over events and
// canonical. index may be nil. trustedCIPubkeys are the kind-5401 producers
// whose runs count; a run signed by anyone else is not a run.
func NewCanonicalRepository(events EvidenceStore, canonical CanonicalState, index repository.HiveCIRepository, trustedCIPubkeys []string, logger *zap.Logger) *CanonicalRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	trusted := make(map[string]struct{}, len(trustedCIPubkeys))
	for _, pk := range trustedCIPubkeys {
		if pk = strings.ToLower(strings.TrimSpace(pk)); pk != "" {
			trusted[pk] = struct{}{}
		}
	}
	return &CanonicalRepository{
		events: events, canonical: canonical, index: index, trusted: trusted,
		logger: logger.Named("hiveci-canonical-store"), now: func() time.Time { return time.Now().UTC() },
	}
}

func (r *CanonicalRepository) available() error {
	if r == nil || r.events == nil || r.canonical == nil {
		return errors.New("Hive-CI canonical store is not configured")
	}
	return nil
}

// mirror records the outcome of a derived SQL index write. The canonical
// record is already published, so a failure is logged, never returned.
func (r *CanonicalRepository) mirror(op string, err error) {
	if err != nil {
		r.logger.Warn("Hive-CI SQL index write failed; canonical state retained", zap.String("operation", op), zap.Error(err))
	}
}

// Runs ------------------------------------------------------------------------

// UpsertWorkflowRun mirrors an ingested run into the SQL index. The run is
// its signed event, which the subscriber has saved in the local event store.
func (r *CanonicalRepository) UpsertWorkflowRun(ctx context.Context, run domain.HiveCIWorkflowRun) error {
	if err := r.available(); err != nil {
		return err
	}
	if r.index != nil {
		r.mirror("run", r.index.UpsertWorkflowRun(ctx, run))
	}
	return nil
}

func (r *CanonicalRepository) runFromEvent(ev *nostr.Event) *domain.HiveCIWorkflowRun {
	if ev == nil {
		return nil
	}
	if _, ok := r.trusted[ev.PubKey.Hex()]; !ok {
		return nil
	}
	run, err := parseWorkflowRunEvent(ev)
	if err != nil {
		return nil
	}
	return &run
}

func (r *CanonicalRepository) GetRunByEventID(_ context.Context, eventID string) (*domain.HiveCIWorkflowRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	ev, err := storedEvent(r.events, eventID, kinds.HiveCIWorkflowRun)
	if err != nil {
		return nil, err
	}
	return r.runFromEvent(ev), nil
}

// FindWorkflowRun returns the newest trusted run for the hive-ci-protocol
// identity tuple, or nil.
func (r *CanonicalRepository) FindWorkflowRun(_ context.Context, repoCoordinate, commitSHA, workflowPath string) (*domain.HiveCIWorkflowRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.HiveCIWorkflowRun)}, Tags: nostr.TagMap{"a": []string{repoCoordinate}}, Limit: evidenceQueryLimit}
	for ev := range r.events.QueryEvents(filter) {
		candidate := ev
		run := r.runFromEvent(&candidate)
		if run == nil || !strings.EqualFold(run.CommitSHA, commitSHA) || run.WorkflowPath != workflowPath {
			continue
		}
		return run, nil
	}
	return nil, nil
}

// Results ---------------------------------------------------------------------

func (r *CanonicalRepository) resultState(ctx context.Context, resultEventID string) (*domain.HiveCIResultState, error) {
	states, err := r.canonical.ListResultStates(ctx, resultEventID, "")
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, nil
	}
	return &states[0], nil
}

// resultFromEvent decodes a signed result and applies the daemon's state
// record to it. A result without a state record was never ingested: nil.
func (r *CanonicalRepository) resultFromEvent(ctx context.Context, ev *nostr.Event) (*domain.HiveCIWorkflowResult, error) {
	if ev == nil {
		return nil, nil
	}
	result, _, err := parseWorkflowResultEvent(ev)
	if err != nil {
		return nil, nil
	}
	state, err := r.resultState(ctx, result.ResultEventID)
	if err != nil || state == nil {
		return nil, err
	}
	applyResultState(&result, *state)
	return &result, nil
}

func applyResultState(result *domain.HiveCIWorkflowResult, state domain.HiveCIResultState) {
	result.ProcessingState = state.ProcessingState
	result.ProcessingError = state.ProcessingError
	result.RetryCount = state.RetryCount
	result.LastRetryAt = state.LastRetryAt
	result.UpdatedAt = state.UpdatedAt
}

// UpsertWorkflowResult publishes the processing state of a newly ingested
// result (its signed event is already in the local event store). An ingested
// result keeps its state, except that a result stored while its run was
// missing moves to pending_result once the run is here.
func (r *CanonicalRepository) UpsertWorkflowResult(ctx context.Context, result domain.HiveCIWorkflowResult) error {
	if err := r.available(); err != nil {
		return err
	}
	state := result.ProcessingState
	if state == "" {
		state = domain.HiveCIProcessingStatePendingRun
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, err := r.resultState(ctx, result.ResultEventID)
	if err != nil {
		return err
	}
	switch {
	case existing == nil:
		if err := r.canonical.PublishResultState(ctx, domain.HiveCIResultState{
			ResultEventID: result.ResultEventID, RunEventID: result.RunEventID, ProcessingState: state, UpdatedAt: r.now(),
		}); err != nil {
			return fmt.Errorf("publish Hive-CI result state: %w", err)
		}
	case existing.ProcessingState == domain.HiveCIProcessingStatePendingRun && state == domain.HiveCIProcessingStatePendingResult:
		next := *existing
		next.ProcessingState = state
		next.UpdatedAt = r.now()
		if err := r.canonical.PublishResultState(ctx, next); err != nil {
			return fmt.Errorf("publish Hive-CI result state: %w", err)
		}
	}
	if r.index != nil {
		r.mirror("result", r.index.UpsertWorkflowResult(ctx, result))
	}
	return nil
}

func (r *CanonicalRepository) GetResultByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowResult, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	ev, err := storedEvent(r.events, eventID, kinds.HiveCIWorkflowResult)
	if err != nil {
		return nil, err
	}
	return r.resultFromEvent(ctx, ev)
}

// GetLatestResultByRunEventID returns the newest ingested result for the run.
func (r *CanonicalRepository) GetLatestResultByRunEventID(ctx context.Context, runEventID string) (*domain.HiveCIWorkflowResult, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.HiveCIWorkflowResult)}, Tags: nostr.TagMap{"e": []string{runEventID}}, Limit: evidenceQueryLimit}
	for ev := range r.events.QueryEvents(filter) {
		candidate := ev
		result, err := r.resultFromEvent(ctx, &candidate)
		if err != nil {
			return nil, err
		}
		if result != nil && result.RunEventID == runEventID {
			return result, nil
		}
	}
	return nil, nil
}

// resultsForStates loads the signed result of each state record, oldest
// event first.
func (r *CanonicalRepository) resultsForStates(ctx context.Context, states []domain.HiveCIResultState) ([]domain.HiveCIWorkflowResult, error) {
	out := make([]domain.HiveCIWorkflowResult, 0, len(states))
	for _, state := range states {
		ev, err := storedEvent(r.events, state.ResultEventID, kinds.HiveCIWorkflowResult)
		if err != nil {
			return nil, err
		}
		if ev == nil {
			// The state outlived its evidence (pruned, or the record was
			// backfilled from SQL before the relay replayed the event).
			continue
		}
		result, _, err := parseWorkflowResultEvent(ev)
		if err != nil {
			continue
		}
		applyResultState(&result, state)
		out = append(out, result)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EventCreatedAt.Before(out[j].EventCreatedAt) })
	return out, nil
}

// ListPendingResults returns every ingested result whose processing is not
// finished: what a restart resumes.
func (r *CanonicalRepository) ListPendingResults(ctx context.Context) ([]domain.HiveCIWorkflowResult, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	states, err := r.canonical.ListResultStates(ctx, "", "")
	if err != nil {
		return nil, err
	}
	pending := states[:0]
	for _, state := range states {
		if !state.ProcessingState.Terminal() && state.ProcessingState != domain.HiveCIProcessingStateVerified {
			pending = append(pending, state)
		}
	}
	return r.resultsForStates(ctx, pending)
}

func (r *CanonicalRepository) ListOrphanedResultsByRun(ctx context.Context, runEventID string) ([]domain.HiveCIWorkflowResult, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	states, err := r.canonical.ListResultStates(ctx, "", runEventID)
	if err != nil {
		return nil, err
	}
	orphans := states[:0]
	for _, state := range states {
		if state.ProcessingState == domain.HiveCIProcessingStatePendingRun {
			orphans = append(orphans, state)
		}
	}
	return r.resultsForStates(ctx, orphans)
}

// updateResultState publishes a changed state record for the result. apply
// returns false to leave the record as it is.
func (r *CanonicalRepository) updateResultState(ctx context.Context, resultEventID string, apply func(*domain.HiveCIResultState) bool) (*domain.HiveCIResultState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, err := r.resultState(ctx, resultEventID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	next := *current
	if !apply(&next) {
		return current, nil
	}
	next.UpdatedAt = r.now()
	if err := r.canonical.PublishResultState(ctx, next); err != nil {
		return nil, fmt.Errorf("publish Hive-CI result state: %w", err)
	}
	return &next, nil
}

func (r *CanonicalRepository) UpdateResultState(ctx context.Context, eventID string, newState domain.HiveCIProcessingState) error {
	if err := r.available(); err != nil {
		return err
	}
	var invalid error
	if _, err := r.updateResultState(ctx, eventID, func(state *domain.HiveCIResultState) bool {
		if state.ProcessingState == newState {
			return false
		}
		if !state.ProcessingState.CanTransitionTo(newState) {
			invalid = fmt.Errorf("invalid hiveci result state transition %s -> %s", state.ProcessingState, newState)
			return false
		}
		state.ProcessingState = newState
		return true
	}); err != nil {
		return err
	}
	if invalid != nil {
		return invalid
	}
	if r.index != nil {
		r.mirror("result state", r.index.UpdateResultState(ctx, eventID, newState))
	}
	return nil
}

func (r *CanonicalRepository) IncrementResultRetry(ctx context.Context, eventID string, at time.Time) (int, error) {
	if err := r.available(); err != nil {
		return 0, err
	}
	at = at.UTC()
	state, err := r.updateResultState(ctx, eventID, func(state *domain.HiveCIResultState) bool {
		state.RetryCount++
		state.LastRetryAt = &at
		return true
	})
	if err != nil || state == nil {
		return 0, err
	}
	if r.index != nil {
		_, indexErr := r.index.IncrementResultRetry(ctx, eventID, at)
		r.mirror("result retry", indexErr)
	}
	return state.RetryCount, nil
}

func (r *CanonicalRepository) MarkResultFailed(ctx context.Context, eventID, reason string) error {
	if err := r.available(); err != nil {
		return err
	}
	if _, err := r.updateResultState(ctx, eventID, func(state *domain.HiveCIResultState) bool {
		state.ProcessingState = domain.HiveCIProcessingStateFailed
		state.ProcessingError = reason
		return true
	}); err != nil {
		return err
	}
	if r.index != nil {
		r.mirror("result failed", r.index.MarkResultFailed(ctx, eventID, reason))
	}
	return nil
}

// Policies --------------------------------------------------------------------

func policyKeyOf(policy domain.HiveCIPipelinePolicy) string {
	return policy.RepoCoordinate + "\x00" + policy.WorkflowPath + "\x00" + policy.BranchPattern + "\x00" + policy.ServiceID.String() + "\x00" + policy.EnvironmentID.String()
}

func (r *CanonicalRepository) ListPolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	policies, err := r.canonical.ListPipelinePolicies(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(policies, func(i, j int) bool {
		a, b := policies[i], policies[j]
		if a.RepoCoordinate != b.RepoCoordinate {
			return a.RepoCoordinate < b.RepoCoordinate
		}
		if a.WorkflowPath != b.WorkflowPath {
			return a.WorkflowPath < b.WorkflowPath
		}
		return a.BranchPattern < b.BranchPattern
	})
	return policies, nil
}

// GetPolicyByRepoAndWorkflow returns the enabled policy for the repository
// workflow, preferring one without a branch pattern, then the newest.
func (r *CanonicalRepository) GetPolicyByRepoAndWorkflow(ctx context.Context, repo, workflow string) (*domain.HiveCIPipelinePolicy, error) {
	policies, err := r.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	var best *domain.HiveCIPipelinePolicy
	for i := range policies {
		policy := &policies[i]
		if !policy.Enabled || policy.RepoCoordinate != repo || policy.WorkflowPath != workflow {
			continue
		}
		if best == nil ||
			(best.BranchPattern != "" && policy.BranchPattern == "") ||
			((best.BranchPattern == "") == (policy.BranchPattern == "") && policy.UpdatedAt.After(best.UpdatedAt)) {
			best = policy
		}
	}
	if best == nil {
		return nil, nil
	}
	found := *best
	return &found, nil
}

// EnsurePipelinePolicy publishes the policy, keeping the id of the canonical
// record for the same key so releases accepted under it keep referring to it.
// The optional SQL index is never read to create canonical policy state.
func (r *CanonicalRepository) EnsurePipelinePolicy(ctx context.Context, policy domain.HiveCIPipelinePolicy) error {
	if err := r.available(); err != nil {
		return err
	}
	if policy.Metadata == nil {
		policy.Metadata = map[string]any{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := policyKeyOf(policy)
	retained, err := r.canonical.ListPipelinePolicies(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	policy.ID = uuid.Nil
	for _, existing := range retained {
		if policyKeyOf(existing) == key {
			policy.ID, policy.CreatedAt = existing.ID, existing.CreatedAt
			if policy.Enabled == existing.Enabled && samePolicyMetadata(policy.Metadata, existing.Metadata) {
				return nil
			}
			break
		}
	}
	if policy.ID == uuid.Nil {
		policy.ID = uuid.NewSHA1(hiveCIPolicyNamespace, []byte(key))
	}
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	policy.UpdatedAt = now
	if err := r.canonical.PublishPipelinePolicy(ctx, policy); err != nil {
		return fmt.Errorf("publish Hive-CI pipeline policy: %w", err)
	}
	if r.index != nil {
		r.mirror("policy", r.index.EnsurePipelinePolicy(ctx, policy))
	}
	return nil
}

func samePolicyMetadata(a, b map[string]any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// Accepted releases -------------------------------------------------------------

// acceptedRelease returns the ledger record for the release identity, or nil.
func (r *CanonicalRepository) acceptedRelease(ctx context.Context, releaseIdentity string) (*domain.HiveCIAcceptedRelease, error) {
	releases, err := r.canonical.ListAcceptedReleases(ctx, releaseIdentity)
	if err != nil {
		return nil, err
	}
	if len(releases) == 0 {
		return nil, nil
	}
	return &releases[0], nil
}

// CommitAcceptedRelease is the atomic accepted-release identity boundary over
// canonical state. The first attestation accepted for a release
// identity is published as the identity's ledger record; an attestation with
// the same content digest is an exact replay and commits nothing; one with
// different content is quarantined as a conflict record and rejected with
// repository.ErrHiveCIReleaseReplayConflict. The decision is made on the
// canonical ledger alone; the SQL accepted-release table is mirrored
// afterwards as an index and its failures are logged.
func (r *CanonicalRepository) CommitAcceptedRelease(ctx context.Context, release domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error) {
	if err := r.available(); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	if err := release.Validate(); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, err := r.acceptedRelease(ctx, release.Result.ReleaseIdentity)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("read Hive-CI accepted release: %w", err)
	}
	switch {
	case existing == nil:
		if err := r.canonical.PublishAcceptedRelease(ctx, release); err != nil {
			return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("publish Hive-CI accepted release: %w", err)
		}
		r.mirrorRelease(ctx, release, false)
		return domain.HiveCIReleaseCommitResult{Release: release}, nil
	case existing.ContentDigest == release.ContentDigest:
		r.mirrorRelease(ctx, release, false)
		return domain.HiveCIReleaseCommitResult{Release: release, Replay: true}, nil
	default:
		if err := r.canonical.PublishReleaseConflict(ctx, domain.HiveCIReleaseConflict{
			ReleaseIdentity: release.Result.ReleaseIdentity, AcceptedContentDigest: existing.ContentDigest,
			ConflictingContentDigest: release.ContentDigest, ResultEventID: release.ResultEventID,
			SignedEvent: release.SignedEvent, QuarantinedAt: r.now(),
		}); err != nil {
			return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("quarantine conflicting Hive-CI release: %w", err)
		}
		r.mirrorRelease(ctx, release, true)
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s", repository.ErrHiveCIReleaseReplayConflict, release.Result.ReleaseIdentity)
	}
}

// mirrorRelease writes the release through the SQL accepted-release store
// when the index is one. The SQL store applies the same replay and conflict
// rules: when the canonical ledger quarantined the release (conflict), the
// index's conflict is the same decision mirrored; otherwise an index conflict
// means a SQL-era row disagrees with canonical state and is logged like any
// other index failure.
func (r *CanonicalRepository) mirrorRelease(ctx context.Context, release domain.HiveCIAcceptedRelease, conflict bool) {
	index, ok := r.index.(repository.HiveCIReleaseRepository)
	if !ok || r.index == nil {
		return
	}
	_, err := index.CommitAcceptedRelease(ctx, release)
	if conflict && errors.Is(err, repository.ErrHiveCIReleaseReplayConflict) {
		return
	}
	r.mirror("accepted release", err)
}

// Index rebuild and migration ---------------------------------------------------

// RebuildIndex replays the canonical policies, result states and accepted
// releases into the optional SQL index. It never removes rows; one failed
// row does not stop the rest.
func (r *CanonicalRepository) RebuildIndex(ctx context.Context) error {
	if r == nil || r.index == nil {
		return nil
	}
	if err := r.available(); err != nil {
		return err
	}
	var failed []error
	policies, err := r.canonical.ListPipelinePolicies(ctx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if err := r.index.EnsurePipelinePolicy(ctx, policy); err != nil {
			failed = append(failed, fmt.Errorf("policy %s: %w", policy.ID, err))
		}
	}
	states, err := r.canonical.ListResultStates(ctx, "", "")
	if err != nil {
		return err
	}
	results, err := r.resultsForStates(ctx, states)
	if err != nil {
		return err
	}
	for _, result := range results {
		run, err := r.GetRunByEventID(ctx, result.RunEventID)
		if err != nil {
			failed = append(failed, fmt.Errorf("result %s run: %w", result.ResultEventID, err))
			continue
		}
		if run != nil {
			if err := r.index.UpsertWorkflowRun(ctx, *run); err != nil {
				failed = append(failed, fmt.Errorf("run %s: %w", run.RunEventID, err))
			}
		}
		if err := r.index.UpsertWorkflowResult(ctx, result); err != nil {
			failed = append(failed, fmt.Errorf("result %s: %w", result.ResultEventID, err))
			continue
		}
		if result.ProcessingState == domain.HiveCIProcessingStateFailed {
			if err := r.index.MarkResultFailed(ctx, result.ResultEventID, result.ProcessingError); err != nil {
				failed = append(failed, fmt.Errorf("result %s: %w", result.ResultEventID, err))
			}
			continue
		}
		if err := r.index.UpdateResultState(ctx, result.ResultEventID, result.ProcessingState); err != nil {
			failed = append(failed, fmt.Errorf("result %s state: %w", result.ResultEventID, err))
		}
	}
	if releaseIndex, ok := r.index.(repository.HiveCIReleaseRepository); ok {
		releases, err := r.canonical.ListAcceptedReleases(ctx, "")
		if err != nil {
			return err
		}
		for _, release := range releases {
			// The SQL store treats a release it already holds as an exact
			// replay; a conflict means a SQL-era row disagrees with canonical
			// state and is reported.
			if _, err := releaseIndex.CommitAcceptedRelease(ctx, release); err != nil {
				failed = append(failed, fmt.Errorf("release %s: %w", release.Result.ReleaseIdentity, err))
			}
		}
	}
	return errors.Join(failed...)
}

// BackfillMarker records that the one-time SQL-era backfill ran.
// *localstore.Outbox satisfies it.
type BackfillMarker interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}

const hiveCIBackfillMarker = "hiveci-canonical-v1"

// BackfillFromIndex publishes, once, the Hive-CI state that exists only in
// the SQL index because it was written before the store became canonical:
// every pipeline policy (keeping its SQL id) and the processing state of
// every unfinished result, so the relay replay of its signed event finds the
// state it had. The marker is written only after everything was admitted.
func (r *CanonicalRepository) BackfillFromIndex(ctx context.Context, marker BackfillMarker) error {
	if r == nil || r.index == nil || marker == nil {
		return nil
	}
	if err := r.available(); err != nil {
		return err
	}
	if done, err := marker.GetControlRecord("bootstrap", hiveCIBackfillMarker); err != nil || string(done) == "1" {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	retained, err := r.canonical.ListPipelinePolicies(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(retained))
	for _, policy := range retained {
		known[policyKeyOf(policy)] = struct{}{}
	}
	policies, err := r.index.ListPolicies(ctx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if _, ok := known[policyKeyOf(policy)]; ok {
			continue
		}
		if err := r.canonical.PublishPipelinePolicy(ctx, policy); err != nil {
			return fmt.Errorf("backfill Hive-CI policy %s: %w", policy.ID, err)
		}
	}
	pending, err := r.index.ListPendingResults(ctx)
	if err != nil {
		return err
	}
	for _, result := range pending {
		existing, err := r.resultState(ctx, result.ResultEventID)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		if err := r.canonical.PublishResultState(ctx, domain.HiveCIResultState{
			ResultEventID: result.ResultEventID, RunEventID: result.RunEventID, ProcessingState: result.ProcessingState,
			ProcessingError: result.ProcessingError, RetryCount: result.RetryCount, LastRetryAt: result.LastRetryAt, UpdatedAt: r.now(),
		}); err != nil {
			return fmt.Errorf("backfill Hive-CI result %s: %w", result.ResultEventID, err)
		}
	}
	return marker.PutControlRecord("bootstrap", hiveCIBackfillMarker, []byte("1"))
}
