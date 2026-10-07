package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	securityadapter "github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// localSecurityOSV answers every query with one high-severity vulnerability
// whose detail text needs two detail parts.
type localSecurityOSV struct {
	mu    sync.Mutex
	calls int
}

func (c *localSecurityOSV) QueryBatch(_ context.Context, queries []securityadapter.OSVQuery) ([]securityadapter.OSVQueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	out := make([]securityadapter.OSVQueryResult, len(queries))
	for i := range queries {
		out[i] = securityadapter.OSVQueryResult{Query: queries[i], Vulnerabilities: []securityadapter.Vulnerability{{ID: "GHSA-local", Severity: "HIGH", Summary: "prototype pollution", Details: strings.Repeat("d", detailChunkSize+100)}}}
	}
	return out, nil
}

func (c *localSecurityOSV) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// localSecurityObservables stands in for the legacy observable publisher. The
// scanner publishes the "security.scan.completed" audit last, after the run's
// canonical record is terminal, so completed reports a finished scan.
type localSecurityObservables struct {
	secret    nostr.SecretKey
	completed chan string
}

func newLocalSecurityObservables() *localSecurityObservables {
	return &localSecurityObservables{secret: nostr.Generate(), completed: make(chan string, 16)}
}

func (p *localSecurityObservables) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	if err := ev.Sign(p.secret); err != nil {
		return nil, err
	}
	if tagValue(ev.Tags, "action") == "security.scan.completed" {
		p.completed <- tagValue(ev.Tags, "run_id")
	}
	return []sbomadapter.PublishOKResult{{RelayURL: cpRelayA, Accepted: true}}, nil
}

func (p *localSecurityObservables) DeliveryOutcome(context.Context, string) (nostrutil.DeliveryOutcome, error) {
	return nostrutil.DeliveryUnknown, nil
}

// signallingPolicyProvider reports each scan-time policy lookup. That lookup is
// the scan goroutine's last access to the local store, so once it is reported
// the scan has stopped touching state the test reads or closes.
type signallingPolicyProvider struct {
	policies  *service.PolicyService
	evaluated chan struct{}
}

func (p *signallingPolicyProvider) SecurityPoliciesForTarget(ctx context.Context, target *domain.SecurityTarget) ([]domain.DeploymentPolicy, error) {
	policies, err := p.policies.SecurityPoliciesForTarget(ctx, target)
	p.evaluated <- struct{}{}
	return policies, err
}

// localSecurityStack is the security domain wired the way app.go wires it,
// over one daemon's local event store and no SQL repository unless index is
// given.
type localSecurityStack struct {
	store       *service.CanonicalSecurityRepository
	policies    *service.PolicyService
	scanner     *service.SecurityScanner
	osv         *localSecurityOSV
	observables *localSecurityObservables
	evaluated   chan struct{}
}

// awaitScan waits until one scan has completed and finished its policy
// evaluation, and returns its run id.
func (s *localSecurityStack) awaitScan() string {
	runID := <-s.observables.completed
	<-s.evaluated
	return runID
}

func newLocalSecurityStack(d *localHistoryDaemon, index repository.SecurityRepository) *localSecurityStack {
	canonical := NewSecurityCanonicalPublisher(d.projector, &nonceConfidentialEncryptor{}, zap.NewNop())
	stack := &localSecurityStack{osv: &localSecurityOSV{}, observables: newLocalSecurityObservables(), evaluated: make(chan struct{}, 16)}
	stack.store = service.NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	stack.policies = service.NewPolicyService(nil, nil, nil, zap.NewNop())
	stack.policies.SetSecurityRepository(stack.store)
	stack.policies.SetCanonicalPolicyView(NewSecurityPolicyView(d.projector.history))
	stack.scanner = service.NewSecurityScanner(service.SecurityScannerConfig{Repo: stack.store, OSV: stack.osv, Policies: &signallingPolicyProvider{policies: stack.policies, evaluated: stack.evaluated}, Publisher: stack.observables, Logger: zap.NewNop()})
	return stack
}

func (s *localSecurityStack) scheduler(now time.Time) *service.SecurityScheduler {
	return service.NewSecurityScheduler(service.SecuritySchedulerConfig{Repo: s.store, Scanner: s.scanner, Deriver: s.policies, Now: func() time.Time { return now }})
}

// publishScanPolicy publishes a policy cp-state record shaped like the policy
// state publisher's (see controlplane.PolicyRegistryRecord), with a periodic
// OSV scan rule for package targets.
func publishScanPolicy(t *testing.T, d *localHistoryDaemon, policyID uuid.UUID, enabled bool) {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"id": policyID.String(), "name": "periodic-osv", "environment_id": nil, "enforcement": "block", "enabled": enabled, "deleted": false,
		"rules": []map[string]any{{"type": string(domain.RuleSecurityOSVScan), "params": map[string]any{"interval_seconds": 3600, "source_types": []string{string(domain.SecurityTargetPackage)}}}},
	})
	require.NoError(t, err)
	require.NoError(t, d.projector.publishControlState(context.Background(), KindPolicyRegistry, policyID.String(), false, nostr.Tags{{"policy", policyID.String()}}, string(content), "policy", &policyID))
}

func storedSecurityEvents(t *testing.T, store *localstore.Store, topic string) []nostr.Event {
	t.Helper()
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	var out []nostr.Event
	for ev := range store.QueryEvents(nostr.Filter{
		Kinds:   []nostr.Kind{KindCASControlState},
		Authors: []nostr.PubKey{nostr.MustPubKeyFromHex(servicePubkey)},
		Tags:    nostr.TagMap{"t": []string{topic}},
	}) {
		out = append(out, ev)
	}
	return out
}

func lodashSecurityTarget(t *testing.T) domain.SecurityTarget {
	t.Helper()
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	return target
}

// The whole scheduled-scan path with no SQL repository: schedules are derived
// from retained policy and target cp-state, two concurrent wakeups claim one
// signed run, and a restart reproduces schedules, runs and findings from the
// local event store without scanning or signing again.
func TestSecurityCanonicalDBLessScheduledScanSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	policyID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second).Add(time.Minute)

	first := startLocalHistoryDaemon(t, dir, script)
	stack := newLocalSecurityStack(first, nil)
	publishScanPolicy(t, first, policyID, true)
	target := lodashSecurityTarget(t)
	stored, err := stack.store.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	require.NoError(t, stack.policies.DeriveSecurityScanSchedules(ctx))
	schedules, err := stack.store.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	require.NoError(t, err)
	require.Len(t, schedules, 1, "the schedule is derived from the retained policy and target records")
	require.Equal(t, policyID, schedules[0].PolicyID)
	require.Equal(t, 3600, schedules[0].IntervalSeconds)

	// Two wakeups with no shared lock race for the due schedule.
	wakeups := []*service.SecurityScheduler{stack.scheduler(now), stack.scheduler(now)}
	start := make(chan struct{})
	errs := make([]error, len(wakeups))
	var wg sync.WaitGroup
	for i, scheduler := range wakeups {
		wg.Add(1)
		go func(i int, scheduler *service.SecurityScheduler) {
			defer wg.Done()
			<-start
			errs[i] = scheduler.Tick(ctx)
		}(i, scheduler)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	runID := stack.awaitScan()

	require.Equal(t, 1, stack.osv.callCount(), "one claim, one scan")
	require.Len(t, storedSecurityEvents(t, first.store, kinds.CPStateTopicSecurityRun), 1, "the claim and its progress replace one run coordinate")
	run, err := stack.store.GetSecurityScanRun(ctx, uuid.MustParse(runID))
	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCompleted, run.Status)
	require.Equal(t, domain.SecurityTriggerScheduled, run.Trigger)
	require.Equal(t, 1, run.SeverityCounts.High)
	schedules, err = stack.store.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, run.ID, *schedules[0].LastRunID)
	require.Equal(t, now.Add(time.Hour), schedules[0].NextDueAt)
	first.close()

	// Restart: fresh services over the reopened store, still no SQL.
	restarted := startLocalHistoryDaemon(t, dir, script)
	stack = newLocalSecurityStack(restarted, nil)
	signed := script.totalCalls()
	require.NoError(t, stack.policies.DeriveSecurityScanSchedules(ctx))
	replayed := lodashSecurityTarget(t)
	again, err := stack.store.UpsertSecurityTarget(ctx, &replayed)
	require.NoError(t, err)
	require.Equal(t, stored.ID, again.ID)
	require.NoError(t, stack.scheduler(now).Tick(ctx))
	require.Equal(t, signed, script.totalCalls(), "re-deriving unchanged state after a restart signs nothing")
	require.Zero(t, stack.osv.callCount(), "nothing is due, so nothing is scanned")

	schedules, err = stack.store.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, run.ID, *schedules[0].LastRunID)
	require.Equal(t, now.Add(time.Hour), schedules[0].NextDueAt)
	runs, err := stack.store.ListSecurityScanRuns(ctx, stored.TargetKeyHash, 0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, domain.SecurityScanCompleted, runs[0].Status)
	latest, err := stack.store.GetSecurityTargetLatestByHash(ctx, stored.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, run.ID, latest.RunID)
	findings, err := stack.scanner.ListFindings(ctx, service.SecurityFindingsListRequest{RunID: &run.ID})
	require.NoError(t, err)
	require.Len(t, findings.Findings, 1)
	require.Equal(t, "GHSA-local", findings.Findings[0].OSVID)
	require.Len(t, findings.Findings[0].Details, detailChunkSize+100, "the multi-part detail is reassembled from its records")

	// When the schedule comes due again, exactly one new run is claimed.
	later := now.Add(2 * time.Hour)
	require.NoError(t, stack.scheduler(later).Tick(ctx))
	secondRun := stack.awaitScan()
	require.NotEqual(t, runID, secondRun)
	require.Equal(t, 1, stack.osv.callCount())
	require.Len(t, storedSecurityEvents(t, restarted.store, kinds.CPStateTopicSecurityRun), 2)
	require.Len(t, storedSecurityEvents(t, restarted.store, kinds.CPStateTopicSecurityFinding), 1, "a finding keeps one coordinate across runs")
}

// A disabled policy disables its schedule on the same coordinate.
func TestSecurityCanonicalScheduleFollowsPolicyState(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	stack := newLocalSecurityStack(daemon, nil)
	policyID := uuid.New()
	publishScanPolicy(t, daemon, policyID, true)
	target := lodashSecurityTarget(t)
	_, err := stack.store.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	require.NoError(t, stack.policies.DeriveSecurityScanSchedules(ctx))

	publishScanPolicy(t, daemon, policyID, false)
	require.NoError(t, stack.policies.DeriveSecurityScanSchedules(ctx))
	enabled, err := stack.store.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	require.NoError(t, err)
	require.Empty(t, enabled)
	all, err := stack.store.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{PolicyID: &policyID})
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Len(t, storedSecurityEvents(t, daemon.store, kinds.CPStateTopicSecuritySchedule), 1)
}

// countingSecurityIndex is an optional SQL index that only counts target
// writes; the embedded nil repository panics on any other call.
type countingSecurityIndex struct {
	repository.SecurityRepository
	mu     sync.Mutex
	writes int
	err    error
}

func (i *countingSecurityIndex) UpsertSecurityTarget(_ context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.writes++
	if i.err != nil {
		return nil, i.err
	}
	return target, nil
}

func (i *countingSecurityIndex) writeCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.writes
}

func TestSecurityCanonicalRejectedPublishIsSurfacedAndLeavesIndexUntouched(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: maintenance"
	script.reject[cpRelayB] = "blocked: maintenance"
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	index := &countingSecurityIndex{}
	stack := newLocalSecurityStack(daemon, index)

	target := lodashSecurityTarget(t)
	_, err := stack.store.UpsertSecurityTarget(ctx, &target)
	require.ErrorIs(t, err, ErrPublishAbandoned, "a rejected canonical publish is returned to the caller")
	require.Zero(t, index.writeCount(), "SQL must stay untouched when the canonical publish is rejected")
	require.Empty(t, storedSecurityEvents(t, daemon.store, kinds.CPStateTopicSecurityTarget))
	_, err = stack.scanner.SubmitScan(ctx, service.SecurityScanRequest{Target: service.SecurityScanTargetInput{Type: domain.SecurityTargetPackage, Package: &service.SecurityPackageInput{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}}})
	require.ErrorIs(t, err, ErrPublishAbandoned, "a scan is not accepted when its target cannot be published")
	require.Zero(t, stack.osv.callCount())

	script.mu.Lock()
	script.reject = map[string]string{}
	script.mu.Unlock()
	retried := lodashSecurityTarget(t)
	stored, err := stack.store.UpsertSecurityTarget(ctx, &retried)
	require.NoError(t, err)
	require.Equal(t, 1, index.writeCount())
	require.Len(t, storedSecurityEvents(t, daemon.store, kinds.CPStateTopicSecurityTarget), 1)
	read, err := stack.store.GetSecurityTargetByHash(ctx, stored.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, stored.ID, read.ID)
}

// With every relay unreachable the signed record is durably queued and is the
// local state at once; the failing SQL index does not undo it.
func TestSecurityCanonicalQueuedPublishIsDurableAndIndexFailureIsNotFatal(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	index := &countingSecurityIndex{err: errors.New("SQL down")}
	stack := newLocalSecurityStack(daemon, index)

	target := lodashSecurityTarget(t)
	stored, err := stack.store.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	require.Equal(t, 1, index.writeCount())
	events := storedSecurityEvents(t, daemon.store, kinds.CPStateTopicSecurityTarget)
	require.Len(t, events, 1, "the canonical record exists although the SQL write failed")
	entry, held, err := daemon.outbox.Get(events[0].ID)
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, localstore.OutboxPending, entry.State)
	read, err := stack.store.GetSecurityTargetByHash(ctx, stored.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, stored.ID, read.ID)
}

// A finding's detail text may shrink from several parts to one record; the
// base coordinate hides the stale parts, and unchanged findings sign nothing.
func TestSecurityCanonicalFindingDetailsAreReassembledAndDeduped(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	stack := newLocalSecurityStack(daemon, nil)
	runID := uuid.New()
	created := time.Now().UTC().Truncate(time.Second)
	finding := domain.SecurityOSVFinding{RunID: runID, TargetKeyHash: "target-hash", FindingKey: "lodash:GHSA-1", FindingKeyHash: "finding-hash", OSVID: "GHSA-1", Severity: domain.SecuritySeverityHigh, CreatedAt: created, Details: strings.Repeat("x", detailChunkSize+100)}
	silent := domain.SecurityOSVFinding{RunID: runID, TargetKeyHash: "target-hash", FindingKey: "lodash:GHSA-2", FindingKeyHash: "silent-hash", OSVID: "GHSA-2", Severity: domain.SecuritySeverityLow, CreatedAt: created}

	require.NoError(t, stack.store.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding, silent}))
	signed := script.totalCalls()
	require.NoError(t, stack.store.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding, silent}))
	require.Equal(t, signed, script.totalCalls(), "re-asserting unchanged findings, parts and tombstones signs nothing")

	listed, err := stack.store.ListSecurityFindings(ctx, runID)
	require.NoError(t, err)
	details := map[string]string{}
	for _, f := range listed {
		details[f.FindingKeyHash] = f.Details
	}
	require.Len(t, details["finding-hash"], detailChunkSize+100)
	require.Empty(t, details["silent-hash"])

	finding.Details = "replacement detail"
	require.NoError(t, stack.store.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding}))
	high, err := stack.store.ListSecurityFindingsFiltered(ctx, repository.SecurityFindingFilter{TargetKeyHash: "target-hash", Severity: domain.SecuritySeverityHigh})
	require.NoError(t, err)
	require.Len(t, high, 1)
	require.Equal(t, "replacement detail", high[0].Details, "the base record hides stale multipart coordinates")
}

// A retired run is replaced on its coordinate by a tombstone carrying a
// NIP-40 expiration: readers drop it at once, the local store keeps one
// coordinate for it (bounded growth), and the store's expiry prune removes
// the tombstone once it has expired (bahia-u5whr item 6).
func TestSecurityCanonicalRetiredRunIsTombstonedAndExpires(t *testing.T) {
	ctx := context.Background()
	daemon := startLocalHistoryDaemon(t, t.TempDir(), newRelayScript())
	stack := newLocalSecurityStack(daemon, nil)
	canonical := NewSecurityCanonicalPublisher(daemon.projector, &nonceConfidentialEncryptor{}, zap.NewNop())
	finished := time.Now().UTC().Add(-time.Hour)
	run := domain.SecurityScanRun{ID: uuid.New(), TargetKeyHash: "target", Status: domain.SecurityScanCompleted, CreatedAt: finished.Add(-time.Minute), FinishedAt: &finished}
	require.NoError(t, stack.store.CreateSecurityScanRun(ctx, &run))
	runs, err := canonical.ListSecurityRuns(ctx, uuid.Nil, "target")
	require.NoError(t, err)
	require.Len(t, runs, 1)

	expiresAt := time.Now().UTC().Add(time.Hour)
	require.NoError(t, canonical.RetireRun(ctx, &run, expiresAt))
	runs, err = canonical.ListSecurityRuns(ctx, uuid.Nil, "target")
	require.NoError(t, err)
	require.Empty(t, runs, "a retired run is dropped by readers")
	var held []nostr.Event
	for ev := range daemon.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{KindCASControlState}, Tags: nostr.TagMap{"t": []string{kinds.CPStateTopicSecurityRun}}}) {
		held = append(held, ev)
	}
	require.Len(t, held, 1, "the run coordinate holds one event: the tombstone")
	require.True(t, isTombstoneTags(held[0].Tags))
	require.Equal(t, strconv.FormatInt(expiresAt.Unix(), 10), tagValue(held[0].Tags, "expiration"))

	removed, err := daemon.store.PruneExpiredEvents(expiresAt.Add(-time.Minute))
	require.NoError(t, err)
	require.Zero(t, removed, "the tombstone stays until it expires")
	removed, err = daemon.store.PruneExpiredEvents(expiresAt)
	require.NoError(t, err)
	require.Equal(t, 1, removed, "the expired tombstone leaves the store")
}
