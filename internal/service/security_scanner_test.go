package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	securityadapter "github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSecurityScannerSBOMScanVerifiesHashQueriesOSVPersistsAndPublishes(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"demo","packages":[{"name":"lodash","versionInfo":"4.17.21","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:npm/lodash@4.17.21"}]},{"name":"lodash","versionInfo":"4.17.21","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:npm/lodash@4.17.21"}]},{"name":"openssl","versionInfo":"3.0.0","externalRefs":[{"referenceCategory":"SECURITY","referenceType":"cpe23Type","referenceLocator":"cpe:2.3:a:openssl:openssl:3.0.0:*:*:*:*:*:*:*"}]}]}`)
	hash := sha256String(payload)
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: "sha256:artifact"}
	target, err := domain.NewSBOMSecurityTarget(subject, domain.SBOMFormatSPDX, hash, "sbom:ref:test")
	require.NoError(t, err)
	target.ID = uuid.New()
	target.Metadata = map[string]any{"reference_d_tag": "sbom:ref:test", "payload_sha256": hash, "format": string(domain.SBOMFormatSPDX), "storage": string(domain.SBOMStorageBlossom), "location_uri": "https://blossom.example/" + hash + ".json"}
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerSBOMObservable, PublishState: domain.SecurityPublicationPending, UnsupportedReasons: map[string]int{}, Metadata: map[string]any{}}
	repo := newMemorySecurityRepo(target, run)
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: true}}}
	osv := &recordingOSVClient{results: []securityadapter.OSVQueryResult{{Vulnerabilities: []securityadapter.Vulnerability{{ID: "GHSA-1", Summary: "vuln", Severity: "HIGH", CVE: "CVE-1"}}}}}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, Storage: sbomadapter.NewStorageResolver(fakeBlossom{payloads: map[string][]byte{hash: payload}}, nil, nil, slog.Default()), OSV: osv, Publisher: publisher, Logger: zap.NewNop(), Pubkey: publisher.pubkey(t), FindingChunkSize: 10})

	err = scanner.executeRun(ctx, run.ID)

	require.NoError(t, err)
	require.Len(t, osv.queries, 1, "duplicate PURL coordinates should be deduped before OSV")
	require.Equal(t, "pkg:npm/lodash@4.17.21", osv.queries[0].PURL)
	storedRun := repo.runs[run.ID]
	require.Equal(t, domain.SecurityScanCompleted, storedRun.Status)
	require.Equal(t, 1, storedRun.OSVQueryCount)
	require.Equal(t, 1, storedRun.FindingCount)
	require.Equal(t, 1, storedRun.SeverityCounts.High)
	require.Equal(t, 1, storedRun.UnsupportedReasons["unsupported_coordinate"])
	require.Len(t, repo.findings, 1)
	require.Equal(t, "GHSA-1", repo.findings[0].OSVID)
	require.NotNil(t, repo.latest[target.TargetKeyHash])
	require.True(t, publisher.hasKind(KindSecurityStatus))
	require.True(t, publisher.hasKind(KindSecuritySummary))
	require.True(t, publisher.hasKind(KindSecurityFinding))
	require.True(t, publisher.hasKind(KindSecurityAudit))
}

func TestSecurityScannerSBOMHashMismatchFailsBeforeOSV(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"demo","packages":[]}`)
	actualHash := sha256String(payload)
	wrongHash := strings.Repeat("a", 64)
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: "sha256:artifact"}
	target, err := domain.NewSBOMSecurityTarget(subject, domain.SBOMFormatSPDX, wrongHash, "sbom:ref:test")
	require.NoError(t, err)
	target.ID = uuid.New()
	target.Metadata = map[string]any{"reference_d_tag": "sbom:ref:test", "payload_sha256": wrongHash, "format": string(domain.SBOMFormatSPDX), "storage": string(domain.SBOMStorageBlossom), "location_uri": "https://blossom.example/" + actualHash + ".json"}
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerManual, PublishState: domain.SecurityPublicationPending, UnsupportedReasons: map[string]int{}, Metadata: map[string]any{}}
	repo := newMemorySecurityRepo(target, run)
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: true}}}
	osv := &recordingOSVClient{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, Storage: sbomadapter.NewStorageResolver(fakeBlossom{payloads: map[string][]byte{actualHash: payload}}, nil, nil, slog.Default()), OSV: osv, Publisher: publisher, Logger: zap.NewNop(), Pubkey: publisher.pubkey(t)})

	err = scanner.executeRun(ctx, run.ID)

	require.Error(t, err)
	require.Contains(t, err.Error(), "sha256 mismatch")
	require.Empty(t, osv.queries)
	require.Equal(t, domain.SecurityScanFailed, repo.runs[run.ID].Status)
	require.Contains(t, repo.runs[run.ID].Error, "sha256 mismatch")
}

func TestSecurityScannerPublicationRetryStateWhenRelayRejects(t *testing.T) {
	ctx := context.Background()
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	target.ID = uuid.New()
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerManual, PublishState: domain.SecurityPublicationPending, UnsupportedReasons: map[string]int{}, Metadata: map[string]any{}}
	repo := newMemorySecurityRepo(target, run)
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: false, Reason: "rate limited"}}}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: &recordingOSVClient{}, Publisher: publisher, Logger: zap.NewNop()})

	err = scanner.executeRun(ctx, run.ID)

	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCompleted, repo.runs[run.ID].Status)
	require.Equal(t, domain.SecurityPublicationFailedRetryable, repo.runs[run.ID].PublishState)
	require.True(t, repo.hasPublicationState(domain.SecurityPublicationFailedRetryable))
}

func TestSecurityScannerCancelRunMarksTerminal(t *testing.T) {
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	target.ID = uuid.New()
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanRunning, Trigger: domain.SecurityTriggerManual, PublishState: domain.SecurityPublicationPending, UnsupportedReasons: map[string]int{}, Metadata: map[string]any{}}
	repo := newMemorySecurityRepo(target, run)
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: true}}}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, OSV: &recordingOSVClient{}, Publisher: publisher, Logger: zap.NewNop(), Pubkey: publisher.pubkey(t)})

	err = scanner.cancelRun(context.Background(), run.ID, "operator cancelled")

	require.NoError(t, err)
	require.Equal(t, domain.SecurityScanCancelled, repo.runs[run.ID].Status)
	require.Equal(t, "operator cancelled", repo.runs[run.ID].Error)
}

func TestSecurityScannerCompletionUpdatesCompatibilityAndDispatchesNewBreachOnly(t *testing.T) {
	ctx := context.Background()
	artifactID := uuid.New()
	payload := []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"demo","packages":[{"name":"lodash","versionInfo":"4.17.21","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:npm/lodash@4.17.21"}]}]}`)
	hash := sha256String(payload)
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String(), Digest: "sha256:artifact"}
	target, err := domain.NewSBOMSecurityTarget(subject, domain.SBOMFormatSPDX, hash, "sbom:ref:test")
	require.NoError(t, err)
	target.ID = uuid.New()
	target.Metadata = map[string]any{"reference_d_tag": "sbom:ref:test", "payload_sha256": hash, "format": string(domain.SBOMFormatSPDX), "storage": string(domain.SBOMStorageBlossom), "location_uri": "https://blossom.example/" + hash + ".json"}
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerManual, PublishState: domain.SecurityPublicationPending, UnsupportedReasons: map[string]int{}, Metadata: map[string]any{}}
	repo := newMemorySecurityRepo(target, run)
	publisher := &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay", Accepted: true}}}
	eventPub := events.NewInProcessPublisher(zap.NewNop())
	var dispatched []events.Event
	dispatchedCh := make(chan events.Event, 2)
	eventPub.Subscribe(events.EventSecurityPolicyBreached, func(_ context.Context, e events.Event) {
		dispatched = append(dispatched, e)
		dispatchedCh <- e
	})
	policy := domain.DeploymentPolicy{ID: uuid.New(), Name: "block-high", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, UpdatedAt: time.Now().UTC(), Rules: []domain.PolicyRule{{Type: domain.RuleMaxHighVulns, Params: map[string]any{"max": 0}}}}
	compat := &recordingSBOMCompatibilityUpdater{}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: repo, SBOMs: compat, Policies: staticSecurityPolicyProvider{policies: []domain.DeploymentPolicy{policy}}, Events: eventPub, Storage: sbomadapter.NewStorageResolver(fakeBlossom{payloads: map[string][]byte{hash: payload}}, nil, nil, slog.Default()), OSV: &recordingOSVClient{results: []securityadapter.OSVQueryResult{{Vulnerabilities: []securityadapter.Vulnerability{{ID: "GHSA-1", Severity: "HIGH"}}}}}, Publisher: publisher, Logger: zap.NewNop(), Pubkey: publisher.pubkey(t)})

	require.NoError(t, scanner.executeRun(ctx, run.ID))
	<-dispatchedCh
	require.Equal(t, artifactID, compat.artifactID)
	require.Equal(t, hash, compat.payloadSHA256)
	require.Equal(t, 1, compat.total)
	require.Equal(t, 1, compat.counts.High)
	require.Len(t, dispatched, 1)
	firstFingerprint := repo.breaches[policy.ID.String()+":"+target.TargetKeyHash].Fingerprint

	run2 := *run
	run2.ID = uuid.New()
	run2.Status = domain.SecurityScanAccepted
	repo.runs[run2.ID] = &run2
	require.NoError(t, scanner.executeRun(ctx, run2.ID))
	require.Len(t, dispatched, 1, "unchanged breach fingerprint must not dispatch again")
	require.Equal(t, firstFingerprint, repo.breaches[policy.ID.String()+":"+target.TargetKeyHash].Fingerprint)
}

func TestSecuritySchedulerClaimsDueAndSkipsActiveDuplicate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	target.ID = uuid.New()
	active := &domain.SecurityScanRun{ID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Status: domain.SecurityScanRunning, Trigger: domain.SecurityTriggerScheduled}
	repo := newMemorySecurityRepo(target, active)
	scheduleID := uuid.New()
	repo.schedules[scheduleID] = &domain.SecurityScanSchedule{ID: scheduleID, PolicyID: uuid.New(), TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: now.Add(-time.Minute)}
	scanner := &recordingScheduledScanner{}
	scheduler := NewSecurityScheduler(SecuritySchedulerConfig{Repo: repo, Scanner: scanner, Now: func() time.Time { return now }, WorkerID: "test-scheduler"})

	require.NoError(t, scheduler.Tick(context.Background()))
	require.Empty(t, scanner.requests, "active duplicate run should suppress new scheduled scan")
	require.Len(t, repo.dispatched, 1)
	require.Equal(t, active.ID, *repo.dispatched[0].LastRunID)
	require.Equal(t, now.Add(time.Hour), repo.dispatched[0].NextDueAt)
}

func TestSecurityScannerSubscriptionHandlesEOSEClosedAUTH(t *testing.T) {
	sub := &scriptedSecuritySubscriber{sub: &scriptedSecuritySubscription{messages: []SecuritySubscriptionMessage{{EOSE: true}, {Closed: SecurityRelayClosed{RelayURL: "wss://relay", SubscriptionID: "sub", Reason: "auth-required: sign in"}}}}}
	scanner := NewSecurityScanner(SecurityScannerConfig{Repo: newMemorySecurityRepo(domain.SecurityTarget{}, nil), OSV: &recordingOSVClient{}, Publisher: &recordingSecurityPublisher{secret: "1111111111111111111111111111111111111111111111111111111111111111", results: []sbomadapter.PublishOKResult{{Accepted: true}}}, Subscriber: sub, Logger: zap.NewNop()})

	err := scanner.subscribe(context.Background())

	require.NoError(t, err)
	require.Equal(t, []string{"wss://relay"}, sub.authenticated)
	require.True(t, sub.sub.closed)
}

type memorySecurityRepo struct {
	mu           sync.Mutex
	targets      map[string]*domain.SecurityTarget
	runs         map[uuid.UUID]*domain.SecurityScanRun
	findings     []domain.SecurityOSVFinding
	latest       map[string]*domain.SecurityTargetLatest
	publications map[uuid.UUID]*domain.SecurityObservablePublication
	schedules    map[uuid.UUID]*domain.SecurityScanSchedule
	breaches     map[string]*domain.SecurityPolicyBreach
	dispatched   []domain.SecurityScanSchedule
}

func newMemorySecurityRepo(target domain.SecurityTarget, run *domain.SecurityScanRun) *memorySecurityRepo {
	r := &memorySecurityRepo{targets: map[string]*domain.SecurityTarget{}, runs: map[uuid.UUID]*domain.SecurityScanRun{}, latest: map[string]*domain.SecurityTargetLatest{}, publications: map[uuid.UUID]*domain.SecurityObservablePublication{}, schedules: map[uuid.UUID]*domain.SecurityScanSchedule{}, breaches: map[string]*domain.SecurityPolicyBreach{}}
	if target.TargetKeyHash != "" {
		copyTarget := target
		r.targets[target.TargetKeyHash] = &copyTarget
	}
	if run != nil {
		copyRun := *run
		r.runs[run.ID] = &copyRun
	}
	return r
}

func (r *memorySecurityRepo) UpsertSecurityTarget(_ context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if target.ID == uuid.Nil {
		target.ID = uuid.New()
	}
	copyTarget := *target
	r.targets[target.TargetKeyHash] = &copyTarget
	return &copyTarget, nil
}
func (r *memorySecurityRepo) GetSecurityTargetByHash(_ context.Context, targetKeyHash string) (*domain.SecurityTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.targets[targetKeyHash]; t != nil {
		c := *t
		return &c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) ListSecurityTargets(context.Context, domain.SecurityTargetType, int) ([]domain.SecurityTarget, error) {
	return nil, nil
}
func (r *memorySecurityRepo) CreateSecurityScanRun(_ context.Context, run *domain.SecurityScanRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	c := *run
	r.runs[run.ID] = &c
	return nil
}
func (r *memorySecurityRepo) GetSecurityScanRun(_ context.Context, id uuid.UUID) (*domain.SecurityScanRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run := r.runs[id]; run != nil {
		c := *run
		return &c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) GetActiveSecurityScanRunByTargetHash(_ context.Context, targetKeyHash string) (*domain.SecurityScanRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.runs {
		if run.TargetKeyHash == targetKeyHash && !run.Status.IsTerminal() {
			c := *run
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) ListSecurityScanRuns(context.Context, string, int) ([]domain.SecurityScanRun, error) {
	return nil, nil
}
func (r *memorySecurityRepo) ListSecurityScanRunsByStatus(_ context.Context, statuses []domain.SecurityScanStatus, _ int) ([]domain.SecurityScanRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[domain.SecurityScanStatus]struct{}{}
	for _, status := range statuses {
		want[status] = struct{}{}
	}
	out := []domain.SecurityScanRun{}
	for _, run := range r.runs {
		if _, ok := want[run.Status]; ok {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (r *memorySecurityRepo) MarkSecurityScanRunStarted(_ context.Context, id uuid.UUID, startedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil {
		return repository.ErrNotFound
	}
	run.Status = domain.SecurityScanRunning
	run.StartedAt = &startedAt
	return nil
}
func (r *memorySecurityRepo) CompleteSecurityScanRun(_ context.Context, run *domain.SecurityScanRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := *run
	r.runs[run.ID] = &c
	return nil
}
func (r *memorySecurityRepo) UpdateSecurityScanRunStatus(_ context.Context, id uuid.UUID, status domain.SecurityScanStatus, errorMessage string, finishedAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil {
		return repository.ErrNotFound
	}
	run.Status = status
	run.Error = errorMessage
	run.FinishedAt = finishedAt
	return nil
}
func (r *memorySecurityRepo) UpsertSecurityTargetLatest(_ context.Context, latest *domain.SecurityTargetLatest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := *latest
	r.latest[latest.TargetKeyHash] = &c
	return nil
}
func (r *memorySecurityRepo) GetSecurityTargetLatestByHash(_ context.Context, targetKeyHash string) (*domain.SecurityTargetLatest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if latest := r.latest[targetKeyHash]; latest != nil {
		c := *latest
		return &c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) GetLatestSecurityTargetLatestForArtifact(_ context.Context, artifactID uuid.UUID) (*domain.SecurityTargetLatest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, target := range r.targets {
		if target.Subject != nil && target.Subject.Type == domain.SBOMSubjectArtifact && target.Subject.ID == artifactID.String() {
			if latest := r.latest[hash]; latest != nil {
				c := *latest
				return &c, nil
			}
		}
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) UpsertSecurityFindings(_ context.Context, findings []domain.SecurityOSVFinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findings = append(r.findings, findings...)
	return nil
}
func (r *memorySecurityRepo) ListSecurityFindings(context.Context, uuid.UUID) ([]domain.SecurityOSVFinding, error) {
	return nil, nil
}
func (r *memorySecurityRepo) ListSecurityFindingsFiltered(context.Context, repository.SecurityFindingFilter) ([]domain.SecurityOSVFinding, error) {
	return nil, nil
}
func (r *memorySecurityRepo) UpsertSecurityScanSchedule(_ context.Context, schedule *domain.SecurityScanSchedule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if schedule.ID == uuid.Nil {
		schedule.ID = uuid.New()
	}
	if existing := r.schedules[schedule.ID]; existing != nil {
		existing.TargetID = schedule.TargetID
		existing.TargetKeyHash = schedule.TargetKeyHash
		existing.Enabled = schedule.Enabled
		existing.IntervalSeconds = schedule.IntervalSeconds
		existing.Metadata = schedule.Metadata
		return nil
	}
	for _, existing := range r.schedules {
		if existing.PolicyID == schedule.PolicyID && existing.TargetKeyHash == schedule.TargetKeyHash {
			existing.TargetID = schedule.TargetID
			existing.Enabled = schedule.Enabled
			existing.IntervalSeconds = schedule.IntervalSeconds
			existing.Metadata = schedule.Metadata
			return nil
		}
	}
	c := *schedule
	r.schedules[schedule.ID] = &c
	return nil
}
func (r *memorySecurityRepo) ListSecurityScanSchedulesFiltered(context.Context, repository.SecurityScheduleFilter) ([]domain.SecurityScanSchedule, error) {
	return nil, nil
}
func (r *memorySecurityRepo) ClaimDueSecurityScanSchedules(_ context.Context, now time.Time, _ int, leasedBy string, leaseUntil time.Time) ([]domain.SecurityScanSchedule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.SecurityScanSchedule{}
	for _, schedule := range r.schedules {
		if schedule.Enabled && !schedule.NextDueAt.After(now) && (schedule.LeaseUntil == nil || !schedule.LeaseUntil.After(now)) {
			c := *schedule
			c.LeasedBy = leasedBy
			c.LeaseUntil = &leaseUntil
			schedule.LeasedBy = leasedBy
			schedule.LeaseUntil = &leaseUntil
			out = append(out, c)
		}
	}
	return out, nil
}
func (r *memorySecurityRepo) MarkSecurityScheduleDispatched(_ context.Context, id uuid.UUID, runID uuid.UUID, dispatchedAt time.Time, nextDueAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if schedule := r.schedules[id]; schedule != nil {
		schedule.LastRunID = &runID
		schedule.LastDispatchedAt = &dispatchedAt
		schedule.NextDueAt = nextDueAt
		schedule.LeaseUntil = nil
		schedule.LeasedBy = ""
		r.dispatched = append(r.dispatched, *schedule)
		return nil
	}
	return repository.ErrNotFound
}
func (r *memorySecurityRepo) DisableSecurityScanSchedulesForPolicy(_ context.Context, policyID uuid.UUID, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, schedule := range r.schedules {
		if schedule.PolicyID == policyID {
			schedule.Enabled = false
		}
	}
	return nil
}
func (r *memorySecurityRepo) RecordSecurityPolicyBreach(_ context.Context, breach *domain.SecurityPolicyBreach) (domain.SecurityBreachRecordResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := breach.PolicyID.String() + ":" + breach.TargetKeyHash
	if existing := r.breaches[key]; existing != nil {
		result := domain.SecurityBreachRecordUnchanged
		if existing.Fingerprint != breach.Fingerprint {
			result = domain.SecurityBreachRecordChanged
			breach.PreviousFingerprint = existing.Fingerprint
		}
		breach.ID = existing.ID
		c := *breach
		r.breaches[key] = &c
		return result, nil
	}
	if breach.ID == uuid.Nil {
		breach.ID = uuid.New()
	}
	c := *breach
	r.breaches[key] = &c
	return domain.SecurityBreachRecordNew, nil
}
func (r *memorySecurityRepo) ResolveSecurityPolicyBreach(_ context.Context, policyID uuid.UUID, targetKeyHash string, resolvedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if breach := r.breaches[policyID.String()+":"+targetKeyHash]; breach != nil {
		breach.ResolvedAt = &resolvedAt
		return nil
	}
	return repository.ErrNotFound
}
func (r *memorySecurityRepo) GetActiveSecurityPolicyBreach(_ context.Context, policyID uuid.UUID, targetKeyHash string) (*domain.SecurityPolicyBreach, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if breach := r.breaches[policyID.String()+":"+targetKeyHash]; breach != nil && breach.ResolvedAt == nil {
		c := *breach
		return &c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) UpsertOSVVulnerabilityCache(context.Context, *domain.OSVVulnerabilityCache) error {
	return nil
}
func (r *memorySecurityRepo) GetOSVVulnerabilityCache(context.Context, string, time.Time) (*domain.OSVVulnerabilityCache, error) {
	return nil, repository.ErrNotFound
}
func (r *memorySecurityRepo) PruneExpiredOSVVulnerabilityCache(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (r *memorySecurityRepo) UpsertSecurityPublication(_ context.Context, publication *domain.SecurityObservablePublication) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publications[publication.ID] = publication
	return nil
}
func (r *memorySecurityRepo) UpdateSecurityPublicationState(_ context.Context, id uuid.UUID, state domain.SecurityPublicationState, eventID, lastError string, nextRetryAt *time.Time, publishedAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pub := r.publications[id]; pub != nil {
		pub.PublishState = state
		pub.EventID = eventID
		pub.LastError = lastError
		pub.NextRetryAt = nextRetryAt
		pub.PublishedAt = publishedAt
	}
	return nil
}
func (r *memorySecurityRepo) ListRetryableSecurityPublications(context.Context, time.Time, int) ([]domain.SecurityObservablePublication, error) {
	return nil, nil
}

func (r *memorySecurityRepo) hasPublicationState(state domain.SecurityPublicationState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, publication := range r.publications {
		if publication.PublishState == state {
			return true
		}
	}
	return false
}

type recordingOSVClient struct {
	queries []securityadapter.OSVQuery
	results []securityadapter.OSVQueryResult
}

func (c *recordingOSVClient) QueryBatch(_ context.Context, queries []securityadapter.OSVQuery) ([]securityadapter.OSVQueryResult, error) {
	c.queries = append(c.queries, queries...)
	if len(c.results) > 0 {
		out := make([]securityadapter.OSVQueryResult, len(queries))
		for i := range queries {
			out[i] = c.results[0]
			out[i].Query = queries[i]
		}
		return out, nil
	}
	return make([]securityadapter.OSVQueryResult, len(queries)), nil
}

type recordingSecurityPublisher struct {
	secret  string
	results []sbomadapter.PublishOKResult
	events  []nostr.Event
}

func (p *recordingSecurityPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	secret, err := nostr.SecretKeyFromHex(p.secret)
	if err != nil {
		return nil, err
	}
	if err := ev.Sign(secret); err != nil {
		return nil, err
	}
	p.events = append(p.events, *ev)
	return p.results, nil
}
func (p *recordingSecurityPublisher) hasKind(kind int) bool {
	for _, ev := range p.events {
		if int(ev.Kind) == kind {
			return true
		}
	}
	return false
}
func (p *recordingSecurityPublisher) pubkey(t *testing.T) string {
	secret, err := nostr.SecretKeyFromHex(p.secret)
	require.NoError(t, err)
	ev := nostr.Event{CreatedAt: nostr.Now(), Kind: 1}
	require.NoError(t, ev.Sign(secret))
	return ev.PubKey.Hex()
}

type fakeBlossom struct{ payloads map[string][]byte }

func (f fakeBlossom) Download(_ context.Context, sha256Hash string) ([]byte, error) {
	if data := f.payloads[sha256Hash]; data != nil {
		return data, nil
	}
	return nil, fmt.Errorf("missing blob %s", sha256Hash)
}
func (f fakeBlossom) Upload(context.Context, []byte, string) (*blossom.BlobDescriptor, error) {
	return nil, fmt.Errorf("upload not used")
}

type scriptedSecuritySubscriber struct {
	sub           *scriptedSecuritySubscription
	authenticated []string
}

func (s *scriptedSecuritySubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (SecuritySubscription, error) {
	return s.sub, nil
}
func (s *scriptedSecuritySubscriber) AuthenticateRelay(_ context.Context, relayURL string) error {
	s.authenticated = append(s.authenticated, relayURL)
	return nil
}

type scriptedSecuritySubscription struct {
	messages []SecuritySubscriptionMessage
	index    int
	closed   bool
}

func (s *scriptedSecuritySubscription) Next(context.Context) (SecuritySubscriptionMessage, bool, error) {
	if s.index >= len(s.messages) {
		return SecuritySubscriptionMessage{}, false, nil
	}
	msg := s.messages[s.index]
	s.index++
	return msg, true, nil
}
func (s *scriptedSecuritySubscription) Close() { s.closed = true }

type staticSecurityPolicyProvider struct{ policies []domain.DeploymentPolicy }

func (p staticSecurityPolicyProvider) SecurityPoliciesForTarget(context.Context, *domain.SecurityTarget) ([]domain.DeploymentPolicy, error) {
	return p.policies, nil
}

type recordingSBOMCompatibilityUpdater struct {
	artifactID    uuid.UUID
	payloadSHA256 string
	counts        domain.SecuritySeverityCounts
	total         int
}

func (r *recordingSBOMCompatibilityUpdater) CreateManifest(context.Context, *domain.SBOMManifest) error {
	return nil
}
func (r *recordingSBOMCompatibilityUpdater) ProjectManifest(context.Context, *domain.SBOMManifest, []domain.SBOMManifestPackage) error {
	return nil
}
func (r *recordingSBOMCompatibilityUpdater) UpdateCompatibilityVulnerabilityCounts(_ context.Context, artifactID uuid.UUID, payloadSHA256 string, counts domain.SecuritySeverityCounts, total int) error {
	r.artifactID = artifactID
	r.payloadSHA256 = payloadSHA256
	r.counts = counts
	r.total = total
	return nil
}
func (r *recordingSBOMCompatibilityUpdater) GetManifestByID(context.Context, uuid.UUID) (*domain.SBOMManifest, error) {
	return nil, repository.ErrNotFound
}
func (r *recordingSBOMCompatibilityUpdater) ListManifestsBySubject(context.Context, domain.SBOMSubject, int) ([]domain.SBOMManifest, error) {
	return nil, nil
}
func (r *recordingSBOMCompatibilityUpdater) UpdateManifestPublishState(context.Context, uuid.UUID, domain.SBOMPublishState, string, string, string) error {
	return nil
}
func (r *recordingSBOMCompatibilityUpdater) CreateManifestPackages(context.Context, []domain.SBOMManifestPackage) error {
	return nil
}
func (r *recordingSBOMCompatibilityUpdater) ListPackagesByManifest(context.Context, uuid.UUID) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}
func (r *recordingSBOMCompatibilityUpdater) SearchManifestPackagesByName(context.Context, string, int) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}

type recordingScheduledScanner struct{ requests []SecurityScanRequest }

func (s *recordingScheduledScanner) SubmitScan(_ context.Context, req SecurityScanRequest) (*SecurityScanAccepted, error) {
	s.requests = append(s.requests, req)
	return &SecurityScanAccepted{Status: "accepted", RunID: uuid.New(), TargetKeyHash: req.Target.Package.Name}, nil
}

func sha256String(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
