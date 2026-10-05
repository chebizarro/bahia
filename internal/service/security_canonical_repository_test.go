package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	securityadapter "github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type memoryCanonicalSecurityStore struct {
	mu              sync.Mutex
	targets         map[string]domain.SecurityTarget
	runs            map[uuid.UUID]domain.SecurityScanRun
	schedules       map[uuid.UUID]domain.SecurityScanSchedule
	findings        map[string]domain.SecurityOSVFinding
	reject          map[string]error
	publishAttempts map[string]int
}

func newMemoryCanonicalSecurityStore() *memoryCanonicalSecurityStore {
	return &memoryCanonicalSecurityStore{
		targets: map[string]domain.SecurityTarget{}, runs: map[uuid.UUID]domain.SecurityScanRun{},
		schedules: map[uuid.UUID]domain.SecurityScanSchedule{}, findings: map[string]domain.SecurityOSVFinding{},
		reject: map[string]error{}, publishAttempts: map[string]int{},
	}
}

func (s *memoryCanonicalSecurityStore) publish(kind string, apply func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishAttempts[kind]++
	if err := s.reject[kind]; err != nil {
		return err
	}
	apply()
	return nil
}
func (s *memoryCanonicalSecurityStore) PublishTarget(_ context.Context, v *domain.SecurityTarget) error {
	copyValue := *v
	return s.publish("target", func() { s.targets[v.TargetKeyHash] = copyValue })
}
func (s *memoryCanonicalSecurityStore) PublishRun(_ context.Context, v *domain.SecurityScanRun) error {
	copyValue := *v
	return s.publish("run", func() { s.runs[v.ID] = copyValue })
}
func (s *memoryCanonicalSecurityStore) PublishSchedule(_ context.Context, v *domain.SecurityScanSchedule) error {
	copyValue := *v
	return s.publish("schedule", func() { s.schedules[v.ID] = copyValue })
}
func (s *memoryCanonicalSecurityStore) PublishFinding(_ context.Context, v domain.SecurityOSVFinding) error {
	return s.publish("finding", func() { s.findings[v.FindingKeyHash] = v })
}
func (s *memoryCanonicalSecurityStore) PublishFindingDetail(context.Context, domain.SecurityOSVFinding) error {
	return s.publish("finding-detail", func() {})
}
func (s *memoryCanonicalSecurityStore) ListSecurityTargets(context.Context) ([]domain.SecurityTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityTarget, 0, len(s.targets))
	for _, v := range s.targets {
		out = append(out, v)
	}
	return out, nil
}
func (s *memoryCanonicalSecurityStore) ListSecurityRuns(context.Context) ([]domain.SecurityScanRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityScanRun, 0, len(s.runs))
	for _, v := range s.runs {
		out = append(out, v)
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
func (s *memoryCanonicalSecurityStore) ListSecurityFindings(context.Context) ([]domain.SecurityOSVFinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SecurityOSVFinding, 0, len(s.findings))
	for _, v := range s.findings {
		out = append(out, v)
	}
	return out, nil
}

type failingSecurityIndex struct {
	*memorySecurityRepo
	mu            sync.Mutex
	targetWrites  int
	findingWrites int
}

func (i *failingSecurityIndex) UpsertSecurityTarget(context.Context, *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.targetWrites++
	return nil, errors.New("SQL down")
}
func (i *failingSecurityIndex) UpsertSecurityFindings(context.Context, []domain.SecurityOSVFinding) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.findingWrites++
	return errors.New("SQL down")
}

func TestCanonicalSecurityPublishesBeforeOptionalIndex(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	index := &failingSecurityIndex{memorySecurityRepo: newMemorySecurityRepo(domain.SecurityTarget{}, nil)}
	repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)

	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err, "optional SQL failure must not undo canonical admission")
	require.NotEqual(t, uuid.Nil, stored.ID)
	canonical.mu.Lock()
	canonicalTarget := canonical.targets[target.TargetKeyHash]
	canonical.mu.Unlock()
	require.Equal(t, stored.ID, canonicalTarget.ID)
	index.mu.Lock()
	require.Equal(t, 1, index.targetWrites)
	index.mu.Unlock()

	finding := domain.SecurityOSVFinding{RunID: uuid.New(), TargetKeyHash: target.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}
	require.NoError(t, repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding}))
	canonical.mu.Lock()
	canonicalFinding := canonical.findings["finding"]
	canonical.mu.Unlock()
	require.NotEqual(t, uuid.Nil, canonicalFinding.ID)
	require.False(t, canonicalFinding.CreatedAt.IsZero())
	index.mu.Lock()
	require.Equal(t, 1, index.findingWrites)
	index.mu.Unlock()
}

func TestCanonicalSecurityRejectionLeavesIndexUntouched(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	canonical.reject["target"] = errors.New("relay rejected")
	index := &failingSecurityIndex{memorySecurityRepo: newMemorySecurityRepo(domain.SecurityTarget{}, nil)}
	repo := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)

	_, err = repo.UpsertSecurityTarget(ctx, &target)
	require.ErrorContains(t, err, "relay rejected")
	index.mu.Lock()
	require.Zero(t, index.targetWrites)
	index.mu.Unlock()
}

type synchronizedOSVClient struct {
	mu    sync.Mutex
	calls int
}

func (c *synchronizedOSVClient) QueryBatch(_ context.Context, queries []securityadapter.OSVQuery) ([]securityadapter.OSVQueryResult, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return make([]securityadapter.OSVQueryResult, len(queries)), nil
}

func TestSecuritySchedulerConcurrentWakeupsClaimOneSignedRun(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	schedule := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Minute)}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &schedule))
	osv := &synchronizedOSVClient{}
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111"}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: osv, Publisher: publisher, Logger: zap.NewNop()})

	schedulers := []*SecurityScheduler{
		NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }, WorkerID: "wake-a"}),
		NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }, WorkerID: "wake-b"}),
	}
	errs := make(chan error, len(schedulers))
	var wakes sync.WaitGroup
	for _, scheduler := range schedulers {
		wakes.Add(1)
		go func(scheduler *SecurityScheduler) {
			defer wakes.Done()
			errs <- scheduler.Tick(ctx)
		}(scheduler)
	}
	wakes.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	scanner.sem.Wait()

	canonical.mu.Lock()
	require.Len(t, canonical.runs, 1, "deterministic signed claim must retain one run coordinate")
	canonical.mu.Unlock()
	osv.mu.Lock()
	require.Equal(t, 1, osv.calls, "only the winning claim may execute")
	osv.mu.Unlock()
}

func TestCanonicalSecurityRebuildsFreshIndex(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	stored, err := repo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	run := domain.SecurityScanRun{ID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Status: domain.SecurityScanCompleted, FinishedAt: &time.Time{}, CreatedAt: time.Now().UTC()}
	require.NoError(t, repo.CreateSecurityScanRun(ctx, &run))
	schedule := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600}
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, &schedule))
	require.NoError(t, repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{{RunID: run.ID, TargetKeyHash: stored.TargetKeyHash, FindingKeyHash: "finding", OSVID: "GHSA-1"}}))

	index := newMemorySecurityRepo(domain.SecurityTarget{}, nil)
	rebuilder := NewCanonicalSecurityRepository(index, canonical, zap.NewNop())
	require.NoError(t, rebuilder.RebuildIndex(ctx))
	index.mu.Lock()
	require.Len(t, index.targets, 1)
	require.Len(t, index.runs, 1)
	require.Len(t, index.schedules, 1)
	require.Len(t, index.findings, 1)
	index.mu.Unlock()
}

var _ repository.SecurityRepository = (*failingSecurityIndex)(nil)
