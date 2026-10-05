package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	securityadapter "github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// securityOpLog records, in order, every canonical publish and SQL index write
// of a test, so publish-first ordering can be asserted.
type securityOpLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *securityOpLog) add(op string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *securityOpLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

// memoryCanonicalSecurityStore is the canonical store of a service test: one
// record per coordinate, replaced on every accepted publish.
type memoryCanonicalSecurityStore struct {
	mu        sync.Mutex
	targets   map[string]domain.SecurityTarget
	runs      map[uuid.UUID]domain.SecurityScanRun
	schedules map[uuid.UUID]domain.SecurityScanSchedule
	findings  map[string]domain.SecurityOSVFinding
	reject    map[string]error
	attempts  map[string]int
	log       *securityOpLog
}

func newMemoryCanonicalSecurityStore() *memoryCanonicalSecurityStore {
	return &memoryCanonicalSecurityStore{
		targets: map[string]domain.SecurityTarget{}, runs: map[uuid.UUID]domain.SecurityScanRun{},
		schedules: map[uuid.UUID]domain.SecurityScanSchedule{}, findings: map[string]domain.SecurityOSVFinding{},
		reject: map[string]error{}, attempts: map[string]int{},
	}
}

func (s *memoryCanonicalSecurityStore) publish(kind string, apply func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[kind]++
	if err := s.reject[kind]; err != nil {
		return err
	}
	apply()
	s.log.add("publish:" + kind)
	return nil
}

func (s *memoryCanonicalSecurityStore) setReject(kind string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.reject, kind)
		return
	}
	s.reject[kind] = err
}

func (s *memoryCanonicalSecurityStore) PublishTarget(_ context.Context, v *domain.SecurityTarget) error {
	stored := *v
	return s.publish("target", func() { s.targets[v.TargetKeyHash] = stored })
}

func (s *memoryCanonicalSecurityStore) PublishRun(_ context.Context, v *domain.SecurityScanRun) error {
	stored := *v
	if v.Metadata != nil {
		stored.Metadata = make(map[string]any, len(v.Metadata))
		for key, value := range v.Metadata {
			stored.Metadata[key] = value
		}
	}
	return s.publish("run", func() { s.runs[v.ID] = stored })
}

func (s *memoryCanonicalSecurityStore) PublishSchedule(_ context.Context, v *domain.SecurityScanSchedule) error {
	stored := *v
	return s.publish("schedule", func() { s.schedules[v.ID] = stored })
}

func (s *memoryCanonicalSecurityStore) PublishFinding(_ context.Context, v domain.SecurityOSVFinding) error {
	return s.publish("finding", func() { s.findings[v.FindingKeyHash] = v })
}

func (s *memoryCanonicalSecurityStore) PublishFindingDetail(context.Context, domain.SecurityOSVFinding) error {
	return s.publish("finding-detail", func() {})
}

func (s *memoryCanonicalSecurityStore) ListSecurityTargets(_ context.Context, hash string) ([]domain.SecurityTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityTarget, 0, len(s.targets))
	for _, v := range s.targets {
		if hash == "" || v.TargetKeyHash == hash {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *memoryCanonicalSecurityStore) ListSecurityRuns(_ context.Context, id uuid.UUID, hash string) ([]domain.SecurityScanRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityScanRun, 0, len(s.runs))
	for _, v := range s.runs {
		if (id == uuid.Nil || v.ID == id) && (hash == "" || v.TargetKeyHash == hash) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *memoryCanonicalSecurityStore) ListSecuritySchedules(context.Context) ([]domain.SecurityScanSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityScanSchedule, 0, len(s.schedules))
	for _, v := range s.schedules {
		out = append(out, v)
	}
	return out, nil
}

func (s *memoryCanonicalSecurityStore) ListSecurityFindings(_ context.Context, runID uuid.UUID, hash string) ([]domain.SecurityOSVFinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityOSVFinding, 0, len(s.findings))
	for _, v := range s.findings {
		if (runID == uuid.Nil || v.RunID == runID) && (hash == "" || v.TargetKeyHash == hash) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *memoryCanonicalSecurityStore) counts() (targets, runs, schedules, findings int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.targets), len(s.runs), len(s.schedules), len(s.findings)
}

func (s *memoryCanonicalSecurityStore) attempted(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[kind]
}

func (s *memoryCanonicalSecurityStore) onlySchedule(t *testing.T) domain.SecurityScanSchedule {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.schedules, 1)
	for _, schedule := range s.schedules {
		return schedule
	}
	return domain.SecurityScanSchedule{}
}

// loggingSecurityIndex is the optional SQL index of a test: it logs its writes
// next to the canonical publishes and can be made to fail.
type loggingSecurityIndex struct {
	*memorySecurityRepo
	log    *securityOpLog
	failMu sync.Mutex
	fail   error
}

func newLoggingSecurityIndex(log *securityOpLog) *loggingSecurityIndex {
	return &loggingSecurityIndex{memorySecurityRepo: newMemorySecurityRepo(domain.SecurityTarget{}, nil), log: log}
}

func (i *loggingSecurityIndex) write(op string) error {
	i.log.add("index:" + op)
	i.failMu.Lock()
	defer i.failMu.Unlock()
	return i.fail
}

func (i *loggingSecurityIndex) UpsertSecurityTarget(ctx context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	if err := i.write("target"); err != nil {
		return nil, err
	}
	return i.memorySecurityRepo.UpsertSecurityTarget(ctx, target)
}

func (i *loggingSecurityIndex) CreateSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if err := i.write("run"); err != nil {
		return err
	}
	return i.memorySecurityRepo.CreateSecurityScanRun(ctx, run)
}

func (i *loggingSecurityIndex) UpsertSecurityScanSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if err := i.write("schedule"); err != nil {
		return err
	}
	return i.memorySecurityRepo.UpsertSecurityScanSchedule(ctx, schedule)
}

func (i *loggingSecurityIndex) UpsertSecurityFindings(ctx context.Context, findings []domain.SecurityOSVFinding) error {
	if err := i.write("findings"); err != nil {
		return err
	}
	return i.memorySecurityRepo.UpsertSecurityFindings(ctx, findings)
}

func (i *loggingSecurityIndex) rowCounts() (targets, runs, schedules, findings int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.targets), len(i.runs), len(i.schedules), len(i.findings)
}

var _ repository.SecurityRepository = (*loggingSecurityIndex)(nil)

// lockedSecurityPublisher is a concurrency-safe observable publisher: scans run
// on their own goroutines.
type lockedSecurityPublisher struct {
	mu     sync.Mutex
	secret nostr.SecretKey
	events []nostr.Event
	fail   error // when set, nothing reaches the outbox
}

func newLockedSecurityPublisher(t *testing.T) *lockedSecurityPublisher {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex("1111111111111111111111111111111111111111111111111111111111111111")
	require.NoError(t, err)
	return &lockedSecurityPublisher{secret: secret}
}

func (p *lockedSecurityPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ev.Sign(p.secret); err != nil {
		return nil, err
	}
	if p.fail != nil {
		return nil, p.fail
	}
	p.events = append(p.events, *ev)
	return []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: true}}, nil
}

func (p *lockedSecurityPublisher) DeliveryOutcome(context.Context, string) (nostrutil.DeliveryOutcome, error) {
	return nostrutil.DeliveryUnknown, nil
}

// countingOSVClient answers every query with the configured vulnerabilities.
type countingOSVClient struct {
	mu    sync.Mutex
	calls int
	vulns []securityadapter.Vulnerability
}

func (c *countingOSVClient) QueryBatch(_ context.Context, queries []securityadapter.OSVQuery) ([]securityadapter.OSVQueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	out := make([]securityadapter.OSVQueryResult, len(queries))
	for i := range queries {
		out[i] = securityadapter.OSVQueryResult{Query: queries[i], Vulnerabilities: append([]securityadapter.Vulnerability(nil), c.vulns...)}
	}
	return out, nil
}

func (c *countingOSVClient) setVulns(vulns ...securityadapter.Vulnerability) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vulns = vulns
}

func (c *countingOSVClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func lodashTarget(t *testing.T) domain.SecurityTarget {
	t.Helper()
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	return target
}

func lodashScanRequest() SecurityScanRequest {
	return SecurityScanRequest{Target: SecurityScanTargetInput{Type: domain.SecurityTargetPackage, Package: &SecurityPackageInput{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}}, Trigger: domain.SecurityTriggerManual}
}

func TestCanonicalSecurityPublishesBeforeOptionalIndex(t *testing.T) {
	ctx := context.Background()
	log := &securityOpLog{}
	canonical := newMemoryCanonicalSecurityStore()
	canonical.log = log
	index := newLoggingSecurityIndex(log)
	repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	target := lodashTarget(t)

	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, stored.ID, "identity is minted before anything is written")
	run := &domain.SecurityScanRun{TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Trigger: domain.SecurityTriggerManual}
	require.NoError(t, repo.CreateSecurityScanRun(ctx, run))
	schedule := &domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, schedule))
	require.NoError(t, repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{RunID: run.ID, TargetKeyHash: stored.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}}))

	require.Equal(t, []string{
		"publish:target", "index:target",
		"publish:run", "index:run",
		"publish:schedule", "index:schedule",
		"publish:finding", "publish:finding-detail", "index:findings",
	}, log.snapshot())
}

func TestCanonicalSecurityRecordsExistWhenSQLIndexFails(t *testing.T) {
	ctx := context.Background()
	log := &securityOpLog{}
	canonical := newMemoryCanonicalSecurityStore()
	index := newLoggingSecurityIndex(log)
	index.fail = errors.New("SQL down")
	repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	target := lodashTarget(t)

	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err, "a failed index write must not undo canonical admission")
	run := &domain.SecurityScanRun{TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Trigger: domain.SecurityTriggerManual}
	require.NoError(t, repo.CreateSecurityScanRun(ctx, run))
	schedule := &domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, schedule))
	require.NoError(t, repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{RunID: run.ID, TargetKeyHash: stored.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}}))

	targets, runs, schedules, findings := canonical.counts()
	require.Equal(t, []int{1, 1, 1, 1}, []int{targets, runs, schedules, findings})
	require.Equal(t, []string{"index:target", "index:run", "index:schedule", "index:findings"}, log.snapshot(), "each index write was attempted after its publish")
	targets, runs, schedules, findings = index.rowCounts()
	require.Equal(t, []int{0, 0, 0, 0}, []int{targets, runs, schedules, findings})

	// Reads come from canonical state, so they do not notice the index outage.
	read, err := repo.GetSecurityScanRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanAccepted, read.Status)
	listed, err := repo.ListSecurityFindings(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

func TestCanonicalSecurityRejectionIsSurfacedAndLeavesIndexUntouched(t *testing.T) {
	ctx := context.Background()
	rejected := errors.New("relay rejected")
	target := lodashTarget(t)
	seed := func(t *testing.T, repo *CanonicalSecurityRepository) *domain.SecurityTarget {
		t.Helper()
		copied := target
		stored, err := repo.UpsertSecurityTarget(ctx, &copied)
		require.NoError(t, err)
		return stored
	}
	cases := map[string]struct {
		kind  string
		write func(t *testing.T, repo *CanonicalSecurityRepository) error
	}{
		"target": {kind: "target", write: func(t *testing.T, repo *CanonicalSecurityRepository) error {
			copied := target
			_, err := repo.UpsertSecurityTarget(ctx, &copied)
			return err
		}},
		"run claim": {kind: "run", write: func(t *testing.T, repo *CanonicalSecurityRepository) error {
			stored := seed(t, repo)
			return repo.CreateSecurityScanRun(ctx, &domain.SecurityScanRun{TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash})
		}},
		"schedule": {kind: "schedule", write: func(t *testing.T, repo *CanonicalSecurityRepository) error {
			stored := seed(t, repo)
			return repo.UpsertSecurityScanSchedule(ctx, &domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 60})
		}},
		"finding": {kind: "finding", write: func(t *testing.T, repo *CanonicalSecurityRepository) error {
			return repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{RunID: uuid.New(), TargetKeyHash: target.TargetKeyHash, FindingKeyHash: "finding"}})
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := &securityOpLog{}
			canonical := newMemoryCanonicalSecurityStore()
			canonical.setReject(tc.kind, rejected)
			index := newLoggingSecurityIndex(log)
			repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())

			require.ErrorIs(t, tc.write(t, repo), rejected, "the publish failure is returned, not swallowed")
			for _, op := range log.snapshot() {
				require.NotContains(t, op, tc.kind, "SQL must not be written for a record whose publish was rejected")
			}
		})
	}
}

func TestSecurityScanRunsEndToEndWithoutSQLRepository(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	osv := &countingOSVClient{}
	osv.setVulns(securityadapter.Vulnerability{ID: "GHSA-1", Severity: "HIGH", Summary: "prototype pollution"})
	policy := domain.DeploymentPolicy{ID: uuid.New(), Name: "block-high", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, UpdatedAt: time.Now().UTC(), Rules: []domain.PolicyRule{{Type: domain.RuleMaxHighVulns, Params: map[string]any{"max": 0}}}}
	eventPub := events.NewInProcessPublisher(zap.NewNop())
	breaches := make(chan string, 8)
	eventPub.Subscribe(events.EventSecurityPolicyBreached, func(_ context.Context, e events.Event) {
		data, _ := e.Data.(map[string]any)
		result, _ := data["record_result"].(string)
		breaches <- result
	})
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Policies: staticSecurityPolicyProvider{policies: []domain.DeploymentPolicy{policy}}, Events: eventPub, Publisher: newLockedSecurityPublisher(t), Logger: zap.NewNop()})

	scan := func(t *testing.T) *domain.SecurityScanRun {
		t.Helper()
		req := lodashScanRequest()
		req.Force = true
		accepted, err := scanner.SubmitScan(ctx, req)
		require.NoError(t, err)
		scanner.sem.Wait()
		run, err := repo.GetSecurityScanRun(ctx, accepted.RunID)
		require.NoError(t, err)
		require.Equal(t, domain.SecurityScanCompleted, run.Status)
		return run
	}

	first := scan(t)
	require.Equal(t, "new", <-breaches)
	require.Equal(t, 1, first.FindingCount)
	require.Equal(t, 1, first.SeverityCounts.High)
	findings, err := scanner.ListFindings(ctx, SecurityFindingsListRequest{RunID: &first.ID})
	require.NoError(t, err)
	require.Len(t, findings.Findings, 1)
	require.Equal(t, "GHSA-1", findings.Findings[0].OSVID)
	latest, err := repo.GetSecurityTargetLatestByHash(ctx, first.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, first.ID, latest.RunID)

	// The same breach on the next scan is not announced again; a different
	// one is announced as changed. Both verdicts come from run records.
	second := scan(t)
	require.NotEqual(t, first.ID, second.ID)
	osv.setVulns(securityadapter.Vulnerability{ID: "GHSA-1", Severity: "HIGH"}, securityadapter.Vulnerability{ID: "GHSA-2", Severity: "HIGH"})
	scan(t)
	require.Equal(t, "changed", <-breaches)
	// A clean scan resolves the breach, so the next one is new again.
	osv.setVulns()
	scan(t)
	osv.setVulns(securityadapter.Vulnerability{ID: "GHSA-3", Severity: "HIGH"})
	scan(t)
	require.Equal(t, "new", <-breaches)
	require.Empty(t, breaches, "exactly three breach events: new, changed, new")
	_, runs, _, _ := canonical.counts()
	require.Equal(t, 5, runs)
}

// The run record is the claim. A legacy status observable that cannot be
// published must not strand the claimed run without an execution.
func TestSecurityScanExecutesAlthoughLegacyObservablesCannotBePublished(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	osv := &countingOSVClient{}
	publisher := newLockedSecurityPublisher(t)
	publisher.fail = errors.New("persist signed nostr event before publish: outbox unavailable")
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Publisher: publisher, Logger: zap.NewNop()})

	accepted, err := scanner.SubmitScan(ctx, lodashScanRequest())
	require.NoError(t, err)
	scanner.sem.Wait()
	run, err := repo.GetSecurityScanRun(ctx, accepted.RunID)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCompleted, run.Status)
	require.Equal(t, domain.SecurityPublicationFailedTerminal, run.PublishState, "the canonical run records that its observables were not published")
	require.Equal(t, 1, osv.callCount())
}

func TestSecuritySchedulerConcurrentWakeupsClaimOneSignedRun(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target := lodashTarget(t)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	schedule := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Minute)}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &schedule))
	osv := &countingOSVClient{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Publisher: newLockedSecurityPublisher(t), Logger: zap.NewNop()})

	// Two schedulers model two wakeups that do not share a lock: only the
	// signed run claim keeps them from scanning twice.
	schedulers := []*SecurityScheduler{
		NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }, WorkerID: "wake-a"}),
		NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }, WorkerID: "wake-b"}),
	}
	start := make(chan struct{})
	errs := make([]error, len(schedulers))
	var wakes sync.WaitGroup
	for i, scheduler := range schedulers {
		wakes.Add(1)
		go func(i int, scheduler *SecurityScheduler) {
			defer wakes.Done()
			<-start
			errs[i] = scheduler.Tick(ctx)
		}(i, scheduler)
	}
	close(start)
	wakes.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	scanner.sem.Wait()

	_, runs, _, _ := canonical.counts()
	require.Equal(t, 1, runs, "the deterministic claim keeps one run coordinate")
	require.Equal(t, 1, osv.callCount(), "only the winning claim executes")
	dispatched := canonical.onlySchedule(t)
	require.Equal(t, scheduledRunID(schedule), *dispatched.LastRunID)
	require.Equal(t, now.Add(time.Hour), dispatched.NextDueAt)

	// A later wakeup finds nothing due.
	require.NoError(t, schedulers[0].Tick(ctx))
	scanner.sem.Wait()
	require.Equal(t, 1, osv.callCount())
}

// A crash between the run claim and the schedule update leaves the schedule
// due. The restarted scheduler adopts the claimed run instead of scanning again.
func TestSecuritySchedulerRestartAdoptsExistingClaim(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target := lodashTarget(t)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	schedule := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Minute)}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &schedule))
	finished := now.Add(-time.Second)
	claimed := &domain.SecurityScanRun{ID: scheduledRunID(schedule), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Trigger: domain.SecurityTriggerScheduled}
	require.NoError(t, repo.CreateSecurityScanRun(ctx, claimed))
	claimed.Status, claimed.FinishedAt = domain.SecurityScanCompleted, &finished
	require.NoError(t, repo.CompleteSecurityScanRun(ctx, claimed))

	osv := &countingOSVClient{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Publisher: newLockedSecurityPublisher(t), Logger: zap.NewNop()})
	restarted := NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }})
	require.NoError(t, restarted.Tick(ctx))
	scanner.sem.Wait()

	_, runs, _, _ := canonical.counts()
	require.Equal(t, 1, runs)
	require.Zero(t, osv.callCount(), "the claimed scan is not repeated")
	dispatched := canonical.onlySchedule(t)
	require.Equal(t, claimed.ID, *dispatched.LastRunID)
	require.Equal(t, now.Add(time.Hour), dispatched.NextDueAt)
}

func TestSecuritySchedulerKeepsDispatchingPastAFailingSchedule(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target := lodashTarget(t)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	// The older schedule names a target the store does not hold.
	orphan := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: uuid.New(), TargetKeyHash: "missing-target", Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Hour)}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &orphan))
	healthy := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Minute)}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &healthy))
	osv := &countingOSVClient{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Publisher: newLockedSecurityPublisher(t), Logger: zap.NewNop()})
	scheduler := NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }})

	err = scheduler.Tick(ctx)
	require.ErrorIs(t, err, repository.ErrNotFound)
	require.ErrorContains(t, err, "missing-target")
	scanner.sem.Wait()
	require.Equal(t, 1, osv.callCount(), "the healthy schedule is dispatched although an earlier one failed")
	schedules, err := repo.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{TargetKeyHash: stored.TargetKeyHash})
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, now.Add(time.Hour), schedules[0].NextDueAt)
}

type staticSecurityPolicyView struct {
	mu       sync.Mutex
	policies []domain.DeploymentPolicy
}

func (v *staticSecurityPolicyView) ListSecurityPolicies(context.Context) ([]domain.DeploymentPolicy, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]domain.DeploymentPolicy(nil), v.policies...), nil
}

func (v *staticSecurityPolicyView) set(policies ...domain.DeploymentPolicy) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.policies = policies
}

// Schedules are derived from retained policy and target cp-state: no SQL
// policy repository and no SQL security repository are involved.
func TestSecuritySchedulesAreDerivedFromCanonicalPolicyAndTargetState(t *testing.T) {
	ctx := context.Background()
	// The scheduler's clock is ahead of the wall clock the store stamps a new
	// schedule's first due time with.
	now := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target := lodashTarget(t)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	policy := domain.DeploymentPolicy{ID: uuid.New(), Name: "daily-osv", Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"interval_seconds": float64(7200), "source_types": []any{string(domain.SecurityTargetPackage)}}}}}
	view := &staticSecurityPolicyView{}
	view.set(policy)
	policies := NewPolicyService(nil, nil, nil, zap.NewNop())
	policies.SetSecurityRepository(repo)
	policies.SetCanonicalPolicyView(view)
	osv := &countingOSVClient{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Policies: policies, Publisher: newLockedSecurityPublisher(t), Logger: zap.NewNop()})
	scheduler := NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Deriver: policies, Now: func() time.Time { return now }})

	scheduler.wake(ctx)
	scanner.sem.Wait()
	derived := canonical.onlySchedule(t)
	require.Equal(t, policy.ID, derived.PolicyID)
	require.Equal(t, stored.TargetKeyHash, derived.TargetKeyHash)
	require.Equal(t, 7200, derived.IntervalSeconds)
	require.Equal(t, 1, osv.callCount(), "a newly derived schedule is due at once")
	require.Equal(t, now.Add(2*time.Hour), derived.NextDueAt)

	// Deriving again keeps the schedule's progress: nothing becomes due.
	scheduler.wake(ctx)
	scanner.sem.Wait()
	require.Equal(t, 1, osv.callCount())
	require.Equal(t, now.Add(2*time.Hour), canonical.onlySchedule(t).NextDueAt)

	// Disabling the policy disables its schedule on the same coordinate.
	policy.Enabled = false
	view.set(policy)
	scheduler.wake(ctx)
	disabled := canonical.onlySchedule(t)
	require.False(t, disabled.Enabled)
	require.Equal(t, derived.ID, disabled.ID)
}

type readinessCheckingDeriver struct {
	ready   <-chan struct{}
	early   chan struct{}
	derived chan struct{}
}

func (d *readinessCheckingDeriver) DeriveSecurityScanSchedules(context.Context) error {
	select {
	case <-d.ready:
	default:
		close(d.early)
	}
	select {
	case d.derived <- struct{}{}:
	default:
	}
	return nil
}

func TestSecuritySchedulerWaitsForLocalStoreCatchUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	deriver := &readinessCheckingDeriver{ready: ready, early: make(chan struct{}), derived: make(chan struct{}, 1)}
	repo := NewCanonicalSecurityRepository(nil, newMemoryCanonicalSecurityStore(), zap.NewNop())
	scheduler := NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: &recordingScheduledScanner{}, Deriver: deriver, Ready: func() <-chan struct{} { return ready }})
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()

	close(ready)
	<-deriver.derived
	select {
	case <-deriver.early:
		t.Fatal("schedules were derived before the local store caught up")
	default:
	}
	cancel()
	require.NoError(t, <-done)
}

func TestCanonicalSecurityLatestSummariesComeFromRunRecords(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	artifactID := uuid.New()
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String(), Digest: "sha256:artifact"}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	finish := func(target *domain.SecurityTarget, at time.Time, high int) *domain.SecurityScanRun {
		run := &domain.SecurityScanRun{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, CreatedAt: at}
		require.NoError(t, repo.CreateSecurityScanRun(ctx, run))
		run.Status, run.FinishedAt = domain.SecurityScanCompleted, &at
		run.SeverityCounts.High, run.FindingCount = high, high
		require.NoError(t, repo.CompleteSecurityScanRun(ctx, run))
		return run
	}
	// One artifact, two SBOM payloads: the newer payload's scan is the latest.
	older, err := domain.NewSBOMSecurityTarget(subject, domain.SBOMFormatSPDX, sha256String([]byte("payload-old")), "sbom:ref:old")
	require.NoError(t, err)
	olderStored, err := repo.UpsertSecurityTarget(ctx, &older)
	require.NoError(t, err)
	finish(olderStored, base, 5)
	newer, err := domain.NewSBOMSecurityTarget(subject, domain.SBOMFormatSPDX, sha256String([]byte("payload-new")), "sbom:ref:new")
	require.NoError(t, err)
	newerStored, err := repo.UpsertSecurityTarget(ctx, &newer)
	require.NoError(t, err)
	newest := finish(newerStored, base.Add(10*time.Minute), 2)

	latest, err := repo.GetLatestSecurityTargetLatestForArtifact(ctx, artifactID)
	require.NoError(t, err)
	require.Equal(t, newest.ID, latest.RunID)
	require.Equal(t, 2, latest.SeverityCounts.High)

	// A scan in progress does not hide the last finished one.
	active := &domain.SecurityScanRun{TargetID: newerStored.ID, TargetKeyHash: newerStored.TargetKeyHash, CreatedAt: base.Add(20 * time.Minute)}
	require.NoError(t, repo.CreateSecurityScanRun(ctx, active))
	latest, err = repo.GetSecurityTargetLatestByHash(ctx, newerStored.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, newest.ID, latest.RunID)
	require.ErrorIs(t, repo.CreateSecurityScanRun(ctx, &domain.SecurityScanRun{TargetID: newerStored.ID, TargetKeyHash: newerStored.TargetKeyHash}), repository.ErrAlreadyExists, "a target has one active run")
	_, err = repo.GetLatestSecurityTargetLatestForArtifact(ctx, uuid.New())
	require.ErrorIs(t, err, repository.ErrNotFound)
}

func TestCanonicalSecurityRebuildsFreshIndexFromCanonicalState(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	writer := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target := lodashTarget(t)
	stored, err := writer.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	finished := time.Now().UTC()
	run := &domain.SecurityScanRun{TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash}
	require.NoError(t, writer.CreateSecurityScanRun(ctx, run))
	require.NoError(t, writer.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{RunID: run.ID, TargetKeyHash: stored.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}}))
	run.Status, run.FinishedAt, run.FindingCount = domain.SecurityScanCompleted, &finished, 1
	require.NoError(t, writer.CompleteSecurityScanRun(ctx, run))
	schedule := &domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600}
	require.NoError(t, writer.UpsertSecurityScanSchedule(ctx, schedule))
	publishesBefore := canonical.attempted("target") + canonical.attempted("run") + canonical.attempted("schedule") + canonical.attempted("finding")

	log := &securityOpLog{}
	index := newLoggingSecurityIndex(log)
	rebuilder := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	require.NoError(t, rebuilder.RebuildIndex(ctx))
	targets, runs, schedules, findings := index.rowCounts()
	require.Equal(t, []int{1, 1, 1, 1}, []int{targets, runs, schedules, findings})
	indexedRun, err := index.GetSecurityScanRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCompleted, indexedRun.Status)
	latest, err := index.GetSecurityTargetLatestByHash(ctx, stored.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, run.ID, latest.RunID)

	// Repeating the rebuild re-asserts targets and schedules but does not
	// rewrite a run, or its findings, that the index already holds.
	require.NoError(t, rebuilder.RebuildIndex(ctx))
	_, runs, _, findings = index.rowCounts()
	require.Equal(t, []int{1, 1}, []int{runs, findings})
	publishesAfter := canonical.attempted("target") + canonical.attempted("run") + canonical.attempted("schedule") + canonical.attempted("finding")
	require.Equal(t, publishesBefore, publishesAfter, "rebuilding the index never publishes")
}

type memoryBackfillMarker struct {
	mu      sync.Mutex
	records map[string][]byte
}

func (m *memoryBackfillMarker) GetControlRecord(family, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[family+"/"+id], nil
}

func (m *memoryBackfillMarker) PutControlRecord(family, id string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string][]byte{}
	}
	m.records[family+"/"+id] = value
	return nil
}

// State written before the domain became canonical exists only in SQL. It is
// published once, with its SQL ids, so gates and schedules keep working.
func TestCanonicalSecurityBackfillPublishesSQLEraStateOnce(t *testing.T) {
	ctx := context.Background()
	target := lodashTarget(t)
	target.ID = uuid.New()
	finished := time.Now().UTC().Add(-time.Hour)
	done := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanCompleted, FindingCount: 1, FinishedAt: &finished, CreatedAt: finished}
	index := newMemorySecurityRepo(target, done)
	inFlight := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanRunning, CreatedAt: finished.Add(time.Minute)}
	require.NoError(t, index.CreateSecurityScanRun(ctx, inFlight))
	require.NoError(t, index.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{ID: uuid.New(), RunID: done.ID, TargetKeyHash: target.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}}))
	schedule := &domain.SecurityScanSchedule{ID: uuid.New(), PolicyID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: finished.Add(time.Hour), LeasedBy: "old-worker"}
	require.NoError(t, index.UpsertSecurityScanSchedule(ctx, schedule))

	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	marker := &memoryBackfillMarker{}
	require.NoError(t, repo.BackfillFromIndex(ctx, marker))

	stored, err := repo.GetSecurityTargetByHash(ctx, target.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, target.ID, stored.ID, "SQL identities are kept")
	latest, err := repo.GetSecurityTargetLatestByHash(ctx, target.TargetKeyHash)
	require.NoError(t, err)
	require.Equal(t, done.ID, latest.RunID)
	_, err = repo.GetActiveSecurityScanRunByTargetHash(ctx, target.TargetKeyHash)
	require.ErrorIs(t, err, repository.ErrNotFound, "a scan in flight under SQL leases is not adopted")
	closed, err := index.GetSecurityScanRun(ctx, inFlight.ID)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCancelled, closed.Status, "its index row is closed so it cannot block the target's next run")
	findings, err := repo.ListSecurityFindings(ctx, done.ID)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	backfilled := canonical.onlySchedule(t)
	require.Equal(t, schedule.ID, backfilled.ID)
	require.Empty(t, backfilled.LeasedBy, "SQL leases are not carried into canonical state")

	attempts := canonical.attempted("target") + canonical.attempted("run") + canonical.attempted("schedule") + canonical.attempted("finding")
	require.NoError(t, repo.BackfillFromIndex(ctx, marker))
	require.Equal(t, attempts, canonical.attempted("target")+canonical.attempted("run")+canonical.attempted("schedule")+canonical.attempted("finding"), "the backfill runs once")
}
