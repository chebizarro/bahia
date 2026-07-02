package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	securityadapter "github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	KindSecurityStatus  = 30315
	KindSecuritySummary = 30900
	KindSecurityFinding = 30078
	KindSecurityAudit   = 4903

	SecurityStatusSchema        = "bahia.status.security-scan.v1"
	SecurityScanSummarySchema   = "bahia.security.scan-summary.v1"
	SecurityTargetSummarySchema = "bahia.security.target-summary.v1"
	SecurityFindingsSchema      = "bahia.security.findings.v1"
	SecurityAuditSchema         = "bahia.audit.security.v1"
)

const (
	defaultSecurityRecoveryLimit      = 100
	defaultSecurityFindingChunkSize   = 100
	defaultSecurityMaxConcurrentScans = 4
	maxSecurityBackoff                = 30 * time.Second
)

type SecurityVerifiedPublisher interface {
	PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error)
}

type SecurityRelaySubscriber interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (SecuritySubscription, error)
	AuthenticateRelay(context.Context, string) error
}

type SecuritySubscription interface {
	Next(context.Context) (SecuritySubscriptionMessage, bool, error)
	Close()
}

type SecuritySubscriptionMessage struct {
	Event     *nostr.Event
	EOSE      bool
	RelayEOSE SecurityRelayEOSE
	Closed    SecurityRelayClosed
}

type SecurityRelayEOSE struct {
	RelayURL       string
	SubscriptionID string
}

type SecurityRelayClosed struct {
	RelayURL       string
	SubscriptionID string
	Reason         string
}

type SecurityOSVClient interface {
	QueryBatch(ctx context.Context, queries []securityadapter.OSVQuery) ([]securityadapter.OSVQueryResult, error)
}

type SecurityPolicyProvider interface {
	SecurityPoliciesForTarget(ctx context.Context, target *domain.SecurityTarget) ([]domain.DeploymentPolicy, error)
}

type SecurityScannerConfig struct {
	Repo               repository.SecurityRepository
	SBOMs              repository.SBOMManifestRepository
	Policies           SecurityPolicyProvider
	Events             events.Publisher
	Storage            *sbomadapter.StorageResolver
	OSV                SecurityOSVClient
	Publisher          SecurityVerifiedPublisher
	Subscriber         SecurityRelaySubscriber
	Pubkey             string
	Logger             *zap.Logger
	RecoveryLimit      int
	FindingChunkSize   int
	MaxConcurrentScans int
}

type SecurityScanner struct {
	repo       repository.SecurityRepository
	sboms      repository.SBOMManifestRepository
	policies   SecurityPolicyProvider
	events     events.Publisher
	storage    *sbomadapter.StorageResolver
	osv        SecurityOSVClient
	publisher  SecurityVerifiedPublisher
	subscriber SecurityRelaySubscriber
	pubkey     string
	logger     *zap.Logger

	recoveryLimit      int
	findingChunkSize   int
	maxConcurrentScans int

	sem sync.WaitGroup
	lim chan struct{}
}

type SecurityScanTargetInput struct {
	Type    domain.SecurityTargetType   `json:"type"`
	SBOM    *SecuritySBOMReferenceInput `json:"sbom,omitempty"`
	Package *SecurityPackageInput       `json:"package,omitempty"`
	PURL    string                      `json:"purl,omitempty"`
	Commit  *SecurityCommitInput        `json:"commit,omitempty"`
}

type SecuritySBOMReferenceInput struct {
	Subject       domain.SBOMSubject     `json:"subject"`
	Format        domain.SBOMFormat      `json:"format"`
	Storage       domain.SBOMStorageType `json:"storage"`
	LocationURI   string                 `json:"location_uri"`
	MediaType     string                 `json:"media_type,omitempty"`
	PayloadSHA256 string                 `json:"payload_sha256"`
	ReferenceDTag string                 `json:"reference_d_tag"`
}

type SecurityPackageInput struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

type SecurityCommitInput struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	CommitHash    string `json:"commit_hash"`
}

type SecurityScanRequest struct {
	Target         SecurityScanTargetInput    `json:"target"`
	Trigger        domain.SecurityTriggerKind `json:"trigger"`
	RequestedBy    string                     `json:"requested_by,omitempty"`
	RequestEventID string                     `json:"request_event_id,omitempty"`
	RequestDTag    string                     `json:"request_d_tag,omitempty"`
	Force          bool                       `json:"force,omitempty"`
}

type SecurityRescanRequest struct {
	TargetKeyHash  string `json:"target_key_hash"`
	RequestedBy    string `json:"requested_by,omitempty"`
	RequestEventID string `json:"request_event_id,omitempty"`
	RequestDTag    string `json:"request_d_tag,omitempty"`
}

type SecurityScanAccepted struct {
	Status        string                    `json:"status"`
	RunID         uuid.UUID                 `json:"run_id"`
	TargetKeyHash string                    `json:"target_key_hash"`
	TargetType    domain.SecurityTargetType `json:"target_type"`
	Duplicate     bool                      `json:"duplicate"`
	Skipped       bool                      `json:"skipped,omitempty"`
	Observables   SecurityObservableHint    `json:"observables"`
}

type SecurityObservableHint struct {
	Kinds []int             `json:"kinds"`
	Tags  map[string]string `json:"tags"`
}

type SecurityFindingsListRequest struct {
	RunID         *uuid.UUID              `json:"run_id,omitempty"`
	TargetKeyHash string                  `json:"target_key_hash,omitempty"`
	Severity      domain.SecuritySeverity `json:"severity,omitempty"`
	OSVID         string                  `json:"osv_id,omitempty"`
	Limit         int                     `json:"limit,omitempty"`
	Offset        int                     `json:"offset,omitempty"`
}

type SecurityFindingsListResult struct {
	Status   string                      `json:"status"`
	Findings []domain.SecurityOSVFinding `json:"findings"`
	Limit    int                         `json:"limit"`
	Offset   int                         `json:"offset"`
}

type SecuritySchedulesListRequest struct {
	PolicyID      *uuid.UUID `json:"policy_id,omitempty"`
	TargetKeyHash string     `json:"target_key_hash,omitempty"`
	EnabledOnly   bool       `json:"enabled_only,omitempty"`
	Limit         int        `json:"limit,omitempty"`
	Offset        int        `json:"offset,omitempty"`
}

type SecuritySchedulesListResult struct {
	Status    string                        `json:"status"`
	Schedules []domain.SecurityScanSchedule `json:"schedules"`
	Limit     int                           `json:"limit"`
	Offset    int                           `json:"offset"`
}

type scanCoordinate struct {
	query securityadapter.OSVQuery
	pkg   domain.SecurityPackage
	key   string
}

type scanOutcome struct {
	queries            []scanCoordinate
	findings           []domain.SecurityOSVFinding
	severityCounts     domain.SecuritySeverityCounts
	unsupportedReasons map[string]int
}

func NewSecurityScanner(cfg SecurityScannerConfig) *SecurityScanner {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	recoveryLimit := cfg.RecoveryLimit
	if recoveryLimit <= 0 {
		recoveryLimit = defaultSecurityRecoveryLimit
	}
	chunkSize := cfg.FindingChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultSecurityFindingChunkSize
	}
	concurrency := cfg.MaxConcurrentScans
	if concurrency <= 0 {
		concurrency = defaultSecurityMaxConcurrentScans
	}
	return &SecurityScanner{
		repo:               cfg.Repo,
		sboms:              cfg.SBOMs,
		policies:           cfg.Policies,
		events:             cfg.Events,
		storage:            cfg.Storage,
		osv:                cfg.OSV,
		publisher:          cfg.Publisher,
		subscriber:         cfg.Subscriber,
		pubkey:             strings.TrimSpace(cfg.Pubkey),
		logger:             logger.Named("security-scanner"),
		recoveryLimit:      recoveryLimit,
		findingChunkSize:   chunkSize,
		maxConcurrentScans: concurrency,
		lim:                make(chan struct{}, concurrency),
	}
}

func (s *SecurityScanner) Name() string { return "security-osv-scanner" }

func (s *SecurityScanner) Run(ctx context.Context) error {
	if err := s.ready(true); err != nil {
		return err
	}
	if err := s.recoverActiveRuns(ctx); err != nil {
		s.logger.Warn("security scan recovery failed", zap.Error(err))
	}
	backoff := time.Second
	for {
		err := s.subscribe(ctx)
		if ctx.Err() != nil {
			s.sem.Wait()
			return nil
		}
		s.logger.Warn("security SBOM subscription ended, reconnecting", zap.Error(err), zap.Duration("delay", backoff))
		select {
		case <-ctx.Done():
			s.sem.Wait()
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxSecurityBackoff {
			backoff = maxSecurityBackoff
		}
	}
}

func (s *SecurityScanner) SubmitScan(ctx context.Context, req SecurityScanRequest) (*SecurityScanAccepted, error) {
	if err := s.ready(false); err != nil {
		return nil, err
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = domain.SecurityTriggerManual
	}
	target, err := securityTargetFromInput(req.Target)
	if err != nil {
		return nil, err
	}
	stored, err := s.repo.UpsertSecurityTarget(ctx, &target)
	if err != nil {
		return nil, err
	}
	if !req.Force {
		if active, err := s.repo.GetActiveSecurityScanRunByTargetHash(ctx, stored.TargetKeyHash); err == nil {
			return acceptedResponse(active.ID, stored, true, false), nil
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		if trigger == domain.SecurityTriggerSBOMObservable {
			if latest, err := s.repo.GetSecurityTargetLatestByHash(ctx, stored.TargetKeyHash); err == nil && latest.Status == domain.SecurityScanCompleted {
				return acceptedResponse(latest.RunID, stored, true, true), nil
			} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
				return nil, err
			}
		}
	}
	run := &domain.SecurityScanRun{
		ID:             uuid.New(),
		TargetID:       stored.ID,
		TargetKeyHash:  stored.TargetKeyHash,
		Status:         domain.SecurityScanAccepted,
		Trigger:        trigger,
		RequestedBy:    strings.TrimSpace(req.RequestedBy),
		RequestEventID: strings.TrimSpace(req.RequestEventID),
		RequestDTag:    strings.TrimSpace(req.RequestDTag),
		PublishState:   domain.SecurityPublicationPending,
		Metadata:       map[string]any{"target_type": stored.Type},
	}
	if err := s.repo.CreateSecurityScanRun(ctx, run); err != nil {
		if active, activeErr := s.repo.GetActiveSecurityScanRunByTargetHash(ctx, stored.TargetKeyHash); activeErr == nil {
			return acceptedResponse(active.ID, stored, true, false), nil
		}
		return nil, err
	}
	if err := s.publishStatus(ctx, run, stored, domain.SecurityScanAccepted, "accepted", ""); err != nil {
		return nil, err
	}
	s.startRun(ctx, run.ID)
	return acceptedResponse(run.ID, stored, false, false), nil
}

func (s *SecurityScanner) Rescan(ctx context.Context, req SecurityRescanRequest) (*SecurityScanAccepted, error) {
	if err := s.ready(false); err != nil {
		return nil, err
	}
	target, err := s.repo.GetSecurityTargetByHash(ctx, strings.TrimSpace(req.TargetKeyHash))
	if err != nil {
		return nil, err
	}
	return s.SubmitScan(ctx, SecurityScanRequest{Target: targetInputFromStored(target), Trigger: domain.SecurityTriggerManual, RequestedBy: req.RequestedBy, RequestEventID: req.RequestEventID, RequestDTag: req.RequestDTag, Force: true})
}

func (s *SecurityScanner) ListFindings(ctx context.Context, req SecurityFindingsListRequest) (*SecurityFindingsListResult, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("security repository is not configured")
	}
	if (req.RunID == nil || *req.RunID == uuid.Nil) && strings.TrimSpace(req.TargetKeyHash) == "" {
		return nil, fmt.Errorf("security/findings-list requires run_id or target_key_hash")
	}
	limit, offset := boundedServiceLimit(req.Limit), req.Offset
	if offset < 0 {
		offset = 0
	}
	findings, err := s.repo.ListSecurityFindingsFiltered(ctx, repository.SecurityFindingFilter{RunID: req.RunID, TargetKeyHash: req.TargetKeyHash, Severity: req.Severity, OSVID: req.OSVID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	return &SecurityFindingsListResult{Status: "ok", Findings: findings, Limit: limit, Offset: offset}, nil
}

func (s *SecurityScanner) ListSchedules(ctx context.Context, req SecuritySchedulesListRequest) (*SecuritySchedulesListResult, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("security repository is not configured")
	}
	limit, offset := boundedServiceLimit(req.Limit), req.Offset
	if offset < 0 {
		offset = 0
	}
	schedules, err := s.repo.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{PolicyID: req.PolicyID, TargetKeyHash: req.TargetKeyHash, EnabledOnly: req.EnabledOnly, Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	return &SecuritySchedulesListResult{Status: "ok", Schedules: schedules, Limit: limit, Offset: offset}, nil
}

func (s *SecurityScanner) recoverActiveRuns(ctx context.Context) error {
	runs, err := s.repo.ListSecurityScanRunsByStatus(ctx, []domain.SecurityScanStatus{domain.SecurityScanAccepted, domain.SecurityScanRunning}, s.recoveryLimit)
	if err != nil {
		return err
	}
	for _, run := range runs {
		runID := run.ID
		s.startRun(ctx, runID)
	}
	return nil
}

func (s *SecurityScanner) startRun(ctx context.Context, runID uuid.UUID) {
	s.sem.Add(1)
	go func() {
		defer s.sem.Done()
		select {
		case s.lim <- struct{}{}:
			defer func() { <-s.lim }()
		case <-ctx.Done():
			_ = s.cancelRun(context.Background(), runID, "scanner shutdown before execution")
			return
		}
		if err := s.executeRun(ctx, runID); err != nil {
			s.logger.Warn("security scan execution failed", zap.String("run_id", runID.String()), zap.Error(err))
		}
	}()
}

func (s *SecurityScanner) executeRun(ctx context.Context, runID uuid.UUID) error {
	run, err := s.repo.GetSecurityScanRun(ctx, runID)
	if err != nil {
		return err
	}
	target, err := s.repo.GetSecurityTargetByHash(ctx, run.TargetKeyHash)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	if err := s.repo.MarkSecurityScanRunStarted(ctx, runID, started); err != nil {
		return err
	}
	run.Status = domain.SecurityScanRunning
	run.StartedAt = &started
	if err := s.publishStatus(ctx, run, target, domain.SecurityScanRunning, "preparing_target", ""); err != nil {
		s.logger.Warn("publish security running status failed", zap.Error(err))
	}
	if ctx.Err() != nil {
		return s.cancelRun(context.Background(), runID, "scan cancelled before provider query")
	}
	outcome, err := s.scanTarget(ctx, run, target)
	if err != nil {
		return s.failRun(ctx, run, target, err)
	}
	if err := s.repo.UpsertSecurityFindings(ctx, outcome.findings); err != nil {
		return s.failRun(ctx, run, target, err)
	}
	finished := time.Now().UTC()
	run.Status = domain.SecurityScanCompleted
	run.OSVQueryCount = len(outcome.queries)
	run.FindingCount = len(outcome.findings)
	run.SeverityCounts = outcome.severityCounts
	run.UnsupportedCount = unsupportedTotal(outcome.unsupportedReasons)
	run.UnsupportedReasons = outcome.unsupportedReasons
	run.FinishedAt = &finished
	run.PublishState = domain.SecurityPublicationPublished
	if err := s.publishCompletionObservables(ctx, run, target, outcome.findings); err != nil {
		run.PublishState = domain.SecurityPublicationFailedRetryable
		run.Error = "security observables publish failed: " + err.Error()
	}
	if err := s.repo.CompleteSecurityScanRun(ctx, run); err != nil {
		return err
	}
	latest := &domain.SecurityTargetLatest{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, RunID: run.ID, Status: run.Status, SeverityCounts: run.SeverityCounts, FindingCount: run.FindingCount, ScannedAt: finished, UpdatedAt: finished}
	if err := s.repo.UpsertSecurityTargetLatest(ctx, latest); err != nil {
		return err
	}
	if err := s.updateSBOMCompatibilityCounts(ctx, run, target); err != nil {
		s.logger.Warn("security SBOM compatibility aggregate update failed", zap.String("target_key_hash", target.TargetKeyHash), zap.Error(err))
	}
	if err := s.evaluatePolicyBreaches(ctx, run, target, outcome.findings); err != nil {
		s.logger.Warn("security policy breach evaluation failed", zap.String("target_key_hash", target.TargetKeyHash), zap.Error(err))
	}
	return nil
}

func (s *SecurityScanner) failRun(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, cause error) error {
	finished := time.Now().UTC()
	run.Status = domain.SecurityScanFailed
	run.Error = cause.Error()
	run.FinishedAt = &finished
	run.PublishState = domain.SecurityPublicationPublished
	if err := s.publishStatus(ctx, run, target, domain.SecurityScanFailed, "failed", cause.Error()); err != nil {
		run.PublishState = domain.SecurityPublicationFailedRetryable
	}
	if err := s.publishAudit(ctx, run, target, "security.scan.failed", cause.Error()); err != nil {
		run.PublishState = domain.SecurityPublicationFailedRetryable
	}
	if err := s.repo.CompleteSecurityScanRun(ctx, run); err != nil {
		return err
	}
	_ = s.repo.UpsertSecurityTargetLatest(ctx, &domain.SecurityTargetLatest{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, RunID: run.ID, Status: run.Status, SeverityCounts: run.SeverityCounts, FindingCount: run.FindingCount, ScannedAt: finished, UpdatedAt: finished})
	return cause
}

func (s *SecurityScanner) cancelRun(ctx context.Context, runID uuid.UUID, message string) error {
	run, err := s.repo.GetSecurityScanRun(ctx, runID)
	if err != nil || run.Status.IsTerminal() {
		return err
	}
	target, _ := s.repo.GetSecurityTargetByHash(ctx, run.TargetKeyHash)
	finished := time.Now().UTC()
	run.Status = domain.SecurityScanCancelled
	run.Error = message
	run.FinishedAt = &finished
	run.PublishState = domain.SecurityPublicationPending
	if target != nil {
		if err := s.publishStatus(ctx, run, target, domain.SecurityScanCancelled, "cancelled", message); err == nil {
			run.PublishState = domain.SecurityPublicationPublished
		}
	}
	if err := s.repo.CompleteSecurityScanRun(ctx, run); err != nil {
		return err
	}
	if target != nil {
		_ = s.repo.UpsertSecurityTargetLatest(ctx, &domain.SecurityTargetLatest{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, RunID: run.ID, Status: run.Status, SeverityCounts: run.SeverityCounts, FindingCount: run.FindingCount, ScannedAt: finished, UpdatedAt: finished})
	}
	return nil
}

func (s *SecurityScanner) scanTarget(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget) (scanOutcome, error) {
	outcome := scanOutcome{unsupportedReasons: map[string]int{}}
	var err error
	switch target.Type {
	case domain.SecurityTargetSBOM:
		outcome.queries, outcome.unsupportedReasons, err = s.sbomQueries(ctx, target)
	case domain.SecurityTargetPackage, domain.SecurityTargetPURL, domain.SecurityTargetCommit:
		outcome.queries, err = directTargetQueries(target)
	default:
		err = fmt.Errorf("unsupported security target type %q", target.Type)
	}
	if err != nil {
		return outcome, err
	}
	if ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	valid := make([]securityadapter.OSVQuery, 0, len(outcome.queries))
	for _, coordinate := range outcome.queries {
		valid = append(valid, coordinate.query)
	}
	if len(valid) == 0 {
		return outcome, nil
	}
	if err := s.publishStatus(ctx, run, target, domain.SecurityScanRunning, "querying_osv", ""); err != nil {
		s.logger.Warn("publish querying status failed", zap.Error(err))
	}
	results, err := s.osv.QueryBatch(ctx, valid)
	if err != nil {
		return outcome, err
	}
	for i, result := range results {
		if i >= len(outcome.queries) {
			break
		}
		coordinate := outcome.queries[i]
		for _, vuln := range result.Vulnerabilities {
			finding := findingFromVulnerability(run.ID, target.TargetKeyHash, coordinate, vuln)
			outcome.severityCounts = addSeverity(outcome.severityCounts, finding.Severity)
			outcome.findings = append(outcome.findings, finding)
		}
	}
	return outcome, nil
}

func (s *SecurityScanner) sbomQueries(ctx context.Context, target *domain.SecurityTarget) ([]scanCoordinate, map[string]int, error) {
	ref, err := sbomReferenceFromTarget(target)
	if err != nil {
		return nil, nil, err
	}
	if s.storage == nil {
		return nil, nil, fmt.Errorf("SBOM storage resolver is not configured")
	}
	if ref.Storage != domain.SBOMStorageBlossom {
		return nil, nil, fmt.Errorf("security scanner supports blossom SBOM storage, got %q", ref.Storage)
	}
	payload, err := s.storage.Resolve(ctx, sbomadapter.ResolveInput{Location: domain.SBOMLocation{Type: ref.Storage, URI: ref.LocationURI, MediaType: ref.MediaType}})
	if err != nil {
		return nil, nil, err
	}
	actual := securitySHA256Hex(payload)
	if !strings.EqualFold(actual, ref.PayloadSHA256) {
		return nil, nil, fmt.Errorf("SBOM payload sha256 mismatch: expected %s got %s", ref.PayloadSHA256, actual)
	}
	parsed, err := sbomadapter.ParseManifest(payload, ref.Subject)
	if err != nil {
		return nil, nil, err
	}
	coords, reasons := coordinatesFromPackages(parsed.Packages)
	return coords, reasons, nil
}

func directTargetQueries(target *domain.SecurityTarget) ([]scanCoordinate, error) {
	switch target.Type {
	case domain.SecurityTargetPackage:
		if target.Package == nil {
			return nil, fmt.Errorf("package target metadata is missing")
		}
		query := securityadapter.OSVQuery{Ecosystem: target.Package.Ecosystem, Name: target.Package.Name, Version: target.Package.Version}
		return []scanCoordinate{{query: query, pkg: *target.Package, key: target.TargetKey}}, nil
	case domain.SecurityTargetPURL:
		if strings.TrimSpace(target.PURL) == "" {
			return nil, fmt.Errorf("purl target metadata is missing")
		}
		pkg := domain.SecurityPackage{PURL: target.PURL}
		if target.Package != nil {
			pkg = *target.Package
		}
		return []scanCoordinate{{query: securityadapter.OSVQuery{PURL: target.PURL}, pkg: pkg, key: target.TargetKey}}, nil
	case domain.SecurityTargetCommit:
		if strings.TrimSpace(target.CommitHash) == "" {
			return nil, fmt.Errorf("commit target metadata is missing")
		}
		return []scanCoordinate{{query: securityadapter.OSVQuery{Commit: target.CommitHash}, pkg: domain.SecurityPackage{}, key: target.TargetKey}}, nil
	default:
		return nil, fmt.Errorf("unsupported direct target type %q", target.Type)
	}
}

func coordinatesFromPackages(packages []domain.SBOMManifestPackage) ([]scanCoordinate, map[string]int) {
	seen := map[string]struct{}{}
	reasons := map[string]int{}
	coords := make([]scanCoordinate, 0, len(packages))
	for _, pkg := range packages {
		var query securityadapter.OSVQuery
		var key string
		secPkg := domain.SecurityPackage{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version, PURL: pkg.PURL, CPE: pkg.CPE}
		if strings.TrimSpace(pkg.PURL) != "" {
			if target, err := domain.NewPURLSecurityTarget(pkg.PURL); err == nil {
				query = securityadapter.OSVQuery{PURL: target.PURL}
				key = target.TargetKey
				if target.Package != nil {
					secPkg = *target.Package
				}
			}
		}
		if key == "" && strings.TrimSpace(pkg.Ecosystem) != "" && strings.TrimSpace(pkg.Name) != "" {
			if target, err := domain.NewPackageSecurityTarget(pkg.Ecosystem, pkg.Name, pkg.Version); err == nil {
				query = securityadapter.OSVQuery{Ecosystem: target.Package.Ecosystem, Name: target.Package.Name, Version: target.Package.Version}
				key = target.TargetKey
				secPkg = *target.Package
			}
		}
		if key == "" {
			if strings.TrimSpace(pkg.CPE) != "" {
				reasons["unsupported_coordinate"]++
			} else {
				reasons["missing_coordinate"]++
			}
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		coords = append(coords, scanCoordinate{query: query, pkg: secPkg, key: key})
	}
	return coords, reasons
}

func (s *SecurityScanner) subscribe(ctx context.Context) error {
	merged, err := s.subscriber.SubscribeAllWithEOSE(ctx, securitySBOMFilters())
	if err != nil {
		return err
	}
	defer merged.Close()
	authAttempted := map[string]struct{}{}
	for {
		msg, ok, err := merged.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		switch {
		case msg.Event != nil:
			s.handleSBOMEvent(ctx, msg.Event)
		case msg.EOSE:
			s.logger.Info("security SBOM observable EOSE received")
		case msg.RelayEOSE.RelayURL != "" || msg.RelayEOSE.SubscriptionID != "":
			s.logger.Debug("relay sent security SBOM EOSE", zap.String("relay", msg.RelayEOSE.RelayURL), zap.String("subscription_id", msg.RelayEOSE.SubscriptionID))
		case msg.Closed.RelayURL != "" || msg.Closed.SubscriptionID != "" || msg.Closed.Reason != "":
			if s.handleRelayClosed(ctx, msg.Closed, authAttempted) {
				merged.Close()
				return nil
			}
		}
	}
}

func (s *SecurityScanner) handleRelayClosed(ctx context.Context, closed SecurityRelayClosed, authAttempted map[string]struct{}) bool {
	s.logger.Warn("relay closed security SBOM subscription", zap.String("relay", closed.RelayURL), zap.String("subscription_id", closed.SubscriptionID), zap.String("reason", closed.Reason))
	if !securityAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || s.subscriber == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := s.subscriber.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		s.logger.Warn("security SBOM subscription auth failed", zap.String("relay", closed.RelayURL), zap.Error(err))
		return false
	}
	return true
}

func (s *SecurityScanner) handleSBOMEvent(ctx context.Context, ev *nostr.Event) {
	if err := validateSecurityInboundEvent(ev, time.Now().UTC(), 10*time.Minute); err != nil {
		s.logger.Warn("dropping invalid SBOM observable before security scan", zap.Error(err))
		return
	}
	refs, err := securitySBOMReferencesFromEvent(ev)
	if err != nil {
		s.logger.Warn("dropping SBOM observable for security scan", zap.String("event_id", nostrutil.EventIDHex(ev)), zap.Error(err))
		return
	}
	for _, ref := range refs {
		_, err := s.SubmitScan(ctx, SecurityScanRequest{Target: SecurityScanTargetInput{Type: domain.SecurityTargetSBOM, SBOM: &ref}, Trigger: domain.SecurityTriggerSBOMObservable, RequestEventID: nostrutil.EventIDHex(ev), RequestDTag: securityTagValue(ev, "d")})
		if err != nil {
			s.logger.Warn("submit SBOM observable security scan failed", zap.String("event_id", nostrutil.EventIDHex(ev)), zap.Error(err))
		}
	}
}

func securitySBOMFilters() []nostr.Filter {
	return []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.Kind(sbomadapter.KindSBOMReference)}, Tags: nostr.TagMap{"domain": []string{"sbom"}, "schema": []string{"bahia.sbom.ref.v1"}}},
		{Kinds: []nostr.Kind{nostr.Kind(sbomadapter.KindSBOMAvailabilityList)}, Tags: nostr.TagMap{"domain": []string{"sbom"}, "schema": []string{"bahia.sbom.available-list.v1"}}},
	}
}

func securitySBOMReferencesFromEvent(ev *nostr.Event) ([]SecuritySBOMReferenceInput, error) {
	switch int(ev.Kind) {
	case sbomadapter.KindSBOMReference:
		if ignoreLegacySBOMReferenceWrapper(ev) {
			return nil, nil
		}
		att, err := sbomadapter.ParseAttestationFromEvent(ev)
		if err != nil {
			return nil, err
		}
		subject := domain.SBOMSubject{Type: domain.SBOMSubjectType(securityTagValue(ev, "subject_type")), Digest: securityTagValue(ev, "subject")}
		if len(att.Subject) > 0 {
			subject.ID = att.Subject[0].Name
		}
		return []SecuritySBOMReferenceInput{{Subject: subject, Format: att.Predicate.Format, Storage: att.Predicate.Location.Type, LocationURI: att.Predicate.Location.URI, MediaType: att.Predicate.Location.MediaType, PayloadSHA256: att.Predicate.Digest["sha256"], ReferenceDTag: securityTagValue(ev, "d")}}, nil
	case sbomadapter.KindSBOMAvailabilityList:
		idx, err := sbomadapter.ParseIndexFromEvent(ev)
		if err != nil {
			return nil, err
		}
		refs := make([]SecuritySBOMReferenceInput, 0, len(idx.Entries))
		for _, entry := range idx.Entries {
			if entry.ReferenceDTag == "" || entry.PayloadSHA256 == "" || entry.LocationURI == "" {
				continue
			}
			refs = append(refs, SecuritySBOMReferenceInput{Subject: domain.SBOMSubject{Type: domain.SBOMSubjectType(idx.SubjectType), ID: idx.SubjectID, Digest: entry.SubjectDigest}, Format: entry.Format, Storage: entry.StorageType, LocationURI: entry.LocationURI, PayloadSHA256: entry.PayloadSHA256, ReferenceDTag: entry.ReferenceDTag})
		}
		return refs, nil
	default:
		return nil, fmt.Errorf("unsupported SBOM observable kind %d", ev.Kind)
	}
}

func ignoreLegacySBOMReferenceWrapper(ev *nostr.Event) bool {
	if ev == nil || int(ev.Kind) != sbomadapter.KindSBOMReference {
		return false
	}
	var envelope struct {
		Type        string          `json:"_type"`
		LegacyEvent json.RawMessage `json:"legacy_event"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil {
		return false
	}
	// Migrated legacy wrappers are not in-toto attestations; they are envelopes
	// carrying prior event content under legacy_event and should not be scanned.
	return envelope.Type == "" && len(envelope.LegacyEvent) > 0
}

func securityTargetFromInput(input SecurityScanTargetInput) (domain.SecurityTarget, error) {
	switch input.Type {
	case domain.SecurityTargetSBOM:
		if input.SBOM == nil {
			return domain.SecurityTarget{}, fmt.Errorf("security scan SBOM target is required")
		}
		target, err := domain.NewSBOMSecurityTarget(input.SBOM.Subject, input.SBOM.Format, input.SBOM.PayloadSHA256, input.SBOM.ReferenceDTag)
		if err != nil {
			return target, err
		}
		target.Metadata = map[string]any{"reference_d_tag": input.SBOM.ReferenceDTag, "payload_sha256": strings.ToLower(strings.TrimSpace(input.SBOM.PayloadSHA256)), "format": string(input.SBOM.Format), "location_uri": strings.TrimSpace(input.SBOM.LocationURI), "storage": string(input.SBOM.Storage), "media_type": strings.TrimSpace(input.SBOM.MediaType)}
		return target, nil
	case domain.SecurityTargetPackage:
		if input.Package == nil {
			return domain.SecurityTarget{}, fmt.Errorf("security scan package target is required")
		}
		return domain.NewPackageSecurityTarget(input.Package.Ecosystem, input.Package.Name, input.Package.Version)
	case domain.SecurityTargetPURL:
		return domain.NewPURLSecurityTarget(input.PURL)
	case domain.SecurityTargetCommit:
		if input.Commit == nil {
			return domain.SecurityTarget{}, fmt.Errorf("security scan commit target is required")
		}
		return domain.NewCommitSecurityTarget(input.Commit.RepositoryURL, input.Commit.CommitHash)
	default:
		return domain.SecurityTarget{}, fmt.Errorf("security scan target type is required")
	}
}

func targetInputFromStored(target *domain.SecurityTarget) SecurityScanTargetInput {
	input := SecurityScanTargetInput{Type: target.Type}
	switch target.Type {
	case domain.SecurityTargetSBOM:
		ref, _ := sbomReferenceFromTarget(target)
		input.SBOM = &ref
	case domain.SecurityTargetPackage:
		if target.Package != nil {
			input.Package = &SecurityPackageInput{Ecosystem: target.Package.Ecosystem, Name: target.Package.Name, Version: target.Package.Version}
		}
	case domain.SecurityTargetPURL:
		input.PURL = target.PURL
	case domain.SecurityTargetCommit:
		input.Commit = &SecurityCommitInput{RepositoryURL: target.RepositoryURL, CommitHash: target.CommitHash}
	}
	return input
}

func sbomReferenceFromTarget(target *domain.SecurityTarget) (SecuritySBOMReferenceInput, error) {
	if target == nil || target.Subject == nil {
		return SecuritySBOMReferenceInput{}, fmt.Errorf("SBOM target metadata is missing subject")
	}
	metadata := target.Metadata
	ref := SecuritySBOMReferenceInput{Subject: *target.Subject, Format: domain.SBOMFormat(securityStringFromMap(metadata, "format")), Storage: domain.SBOMStorageType(securityStringFromMap(metadata, "storage")), LocationURI: securityStringFromMap(metadata, "location_uri"), MediaType: securityStringFromMap(metadata, "media_type"), PayloadSHA256: securityStringFromMap(metadata, "payload_sha256"), ReferenceDTag: securityStringFromMap(metadata, "reference_d_tag")}
	if ref.Storage == "" {
		ref.Storage = domain.SBOMStorageBlossom
	}
	if ref.PayloadSHA256 == "" || ref.ReferenceDTag == "" || ref.LocationURI == "" {
		return ref, fmt.Errorf("SBOM target location, payload hash, and reference d tag are required")
	}
	return ref, nil
}

func findingFromVulnerability(runID uuid.UUID, targetHash string, coordinate scanCoordinate, vuln securityadapter.Vulnerability) domain.SecurityOSVFinding {
	key := strings.Join([]string{targetHash, coordinate.key, vuln.ID}, ":")
	severity := normalizeSecuritySeverity(vuln.Severity)
	return domain.SecurityOSVFinding{ID: uuid.New(), RunID: runID, TargetKeyHash: targetHash, FindingKey: key, FindingKeyHash: domain.CanonicalTargetHash(key), OSVID: vuln.ID, CVE: vuln.CVE, Summary: vuln.Summary, Details: vuln.Details, Severity: severity, Package: coordinate.pkg, Aliases: append([]string(nil), vuln.Aliases...), References: append([]string(nil), vuln.References...), WithdrawnAt: parseOptionalTime(vuln.Withdrawn), RawModified: vuln.Modified, Metadata: map[string]any{"coordinate_key": coordinate.key}}
}

func (s *SecurityScanner) publishCompletionObservables(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, findings []domain.SecurityOSVFinding) error {
	var errs []string
	for _, publish := range []func(context.Context, *domain.SecurityScanRun, *domain.SecurityTarget) error{s.publishCompletedStatus, s.publishScanSummary, s.publishTargetSummary} {
		if err := publish(ctx, run, target); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := s.publishFindings(ctx, run, target, findings); err != nil {
		errs = append(errs, err.Error())
	}
	if err := s.publishAudit(ctx, run, target, "security.scan.completed", ""); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (s *SecurityScanner) publishCompletedStatus(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget) error {
	return s.publishStatus(ctx, run, target, domain.SecurityScanCompleted, "completed", "")
}

func (s *SecurityScanner) publishStatus(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, status domain.SecurityScanStatus, step, message string) error {
	content, _ := json.Marshal(map[string]any{"run_id": run.ID, "target_key_hash": target.TargetKeyHash, "target_type": target.Type, "status": status, "step": step, "message": message, "updated_at": time.Now().UTC()})
	tags := nostr.Tags{{"d", "security:scan:" + run.ID.String()}, {"domain", "security"}, {"schema", SecurityStatusSchema}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}, {"status", string(status)}, {"step", step}}
	if run.RequestEventID != "" {
		tags = append(tags, nostr.Tag{"e", run.RequestEventID})
	}
	ev := &nostr.Event{Kind: KindSecurityStatus, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	return s.publishObservable(ctx, run, target, nil, "scan_status", SecurityStatusSchema, "security:scan:"+run.ID.String(), ev)
}

func (s *SecurityScanner) publishScanSummary(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget) error {
	content, _ := json.Marshal(map[string]any{"run_id": run.ID, "target_key_hash": target.TargetKeyHash, "target_type": target.Type, "status": run.Status, "finding_count": run.FindingCount, "severity_counts": run.SeverityCounts, "unsupported_count": run.UnsupportedCount, "unsupported_reasons": run.UnsupportedReasons, "finished_at": run.FinishedAt})
	d := "security:scan-summary:" + run.ID.String()
	ev := &nostr.Event{Kind: KindSecuritySummary, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}, {"schema", SecurityScanSummarySchema}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}, {"status", string(run.Status)}}, Content: string(content)}
	return s.publishObservable(ctx, run, target, nil, "scan_summary", SecurityScanSummarySchema, d, ev)
}

func (s *SecurityScanner) publishTargetSummary(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget) error {
	content, _ := json.Marshal(map[string]any{"target_id": target.ID, "target_key_hash": target.TargetKeyHash, "target_type": target.Type, "latest_run_id": run.ID, "status": run.Status, "finding_count": run.FindingCount, "severity_counts": run.SeverityCounts, "scanned_at": run.FinishedAt})
	d := "security:target-summary:" + target.TargetKeyHash
	ev := &nostr.Event{Kind: KindSecuritySummary, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}, {"schema", SecurityTargetSummarySchema}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}, {"status", string(run.Status)}}, Content: string(content)}
	return s.publishObservable(ctx, run, target, nil, "target_summary", SecurityTargetSummarySchema, d, ev)
}

func (s *SecurityScanner) publishFindings(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, findings []domain.SecurityOSVFinding) error {
	if len(findings) == 0 {
		return nil
	}
	chunkSize := s.findingChunkSize
	chunkCount := (len(findings) + chunkSize - 1) / chunkSize
	for i := 0; i < chunkCount; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(findings) {
			end = len(findings)
		}
		content, _ := json.Marshal(map[string]any{"run_id": run.ID, "target_key_hash": target.TargetKeyHash, "chunk_index": i, "chunk_count": chunkCount, "findings": findings[start:end]})
		d := fmt.Sprintf("security:findings:%s:%d", run.ID, i)
		ev := &nostr.Event{Kind: KindSecurityFinding, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}, {"schema", SecurityFindingsSchema}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}, {"chunk_index", fmt.Sprint(i)}, {"chunk_count", fmt.Sprint(chunkCount)}}, Content: string(content)}
		if err := s.publishObservable(ctx, run, target, nil, "findings", SecurityFindingsSchema, d, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *SecurityScanner) publishAudit(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, action, message string) error {
	content, _ := json.Marshal(map[string]any{"action": action, "run_id": run.ID, "target_key_hash": target.TargetKeyHash, "target_type": target.Type, "message": message, "created_at": time.Now().UTC()})
	d := "security:audit:" + run.ID.String() + ":" + action

	ev := &nostr.Event{Kind: KindSecurityAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}, {"schema", SecurityAuditSchema}, {"type", "security-scan"}, {"action", action}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}}, Content: string(content)}
	return s.publishObservable(ctx, run, target, nil, "audit", SecurityAuditSchema, d, ev)
}

func (s *SecurityScanner) updateSBOMCompatibilityCounts(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget) error {
	if s.sboms == nil || target == nil || target.Type != domain.SecurityTargetSBOM || target.Subject == nil || target.Subject.Type != domain.SBOMSubjectArtifact {
		return nil
	}
	artifactID, err := uuid.Parse(target.Subject.ID)
	if err != nil {
		return nil
	}
	ref, err := sbomReferenceFromTarget(target)
	if err != nil {
		return err
	}
	return s.sboms.UpdateCompatibilityVulnerabilityCounts(ctx, artifactID, ref.PayloadSHA256, run.SeverityCounts, run.FindingCount)
}

func (s *SecurityScanner) evaluatePolicyBreaches(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, findings []domain.SecurityOSVFinding) error {
	if s.policies == nil || s.repo == nil || target == nil {
		return nil
	}
	policies, err := s.policies.SecurityPoliciesForTarget(ctx, target)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		violated := breachedSecurityRules(policy, run)
		if len(violated) == 0 {
			if active, err := s.repo.GetActiveSecurityPolicyBreach(ctx, policy.ID, target.TargetKeyHash); err == nil && active != nil {
				_ = s.repo.ResolveSecurityPolicyBreach(ctx, policy.ID, target.TargetKeyHash, time.Now().UTC())
			}
			continue
		}
		osvIDs := uniqueOSVIDs(findings)
		breach := &domain.SecurityPolicyBreach{PolicyID: policy.ID, TargetKeyHash: target.TargetKeyHash, Enforcement: string(policy.Enforcement), ViolatedRules: violated, SeverityCounts: run.SeverityCounts, OSVIDs: osvIDs, LastSeenAt: time.Now().UTC(), Metadata: map[string]any{"policy_name": policy.Name, "run_id": run.ID.String(), "target_type": string(target.Type)}}
		breach.Fingerprint = securityBreachFingerprint(policy, target.TargetKeyHash, breachedSecurityRules(policy, run), run.SeverityCounts, osvIDs)
		result, err := s.repo.RecordSecurityPolicyBreach(ctx, breach)
		if err != nil {
			return err
		}
		if result == domain.SecurityBreachRecordNew || result == domain.SecurityBreachRecordChanged {
			if err := s.publishPolicyBreachAudit(ctx, run, target, breach, result); err != nil {
				s.logger.Warn("security policy breach audit publish failed", zap.Error(err))
			}
			if s.events != nil {
				s.events.Publish(ctx, events.Event{Type: events.EventSecurityPolicyBreached, EntityID: breach.ID.String(), Data: map[string]any{"breach_id": breach.ID.String(), "policy_id": policy.ID.String(), "policy_name": policy.Name, "target_key_hash": target.TargetKeyHash, "fingerprint": breach.Fingerprint, "previous_fingerprint": breach.PreviousFingerprint, "enforcement": breach.Enforcement, "violated_rules": breach.ViolatedRules, "severity_counts": breach.SeverityCounts, "osv_ids": breach.OSVIDs, "record_result": string(result)}})
			}
		}
	}
	return nil
}

func breachedSecurityRules(policy domain.DeploymentPolicy, run *domain.SecurityScanRun) []string {
	out := []string{}
	for _, rule := range policy.Rules {
		switch rule.Type {
		case domain.RuleMaxCriticalVulns:
			if run.SeverityCounts.Critical > getIntParam(rule.Params, "max", 0) {
				out = append(out, string(rule.Type))
			}
		case domain.RuleMaxHighVulns:
			if run.SeverityCounts.High > getIntParam(rule.Params, "max", 0) {
				out = append(out, string(rule.Type))
			}
		case domain.RuleRequireScanStatus:
			if strings.ToLower(getStringParam(rule.Params, "status", "clean")) == "clean" && run.FindingCount > 0 {
				out = append(out, string(rule.Type))
			}
		case domain.RuleSecurityOSVScan:
			if !getBoolParam(rule.Params, "enabled", true) {
				continue
			}
			freshness := getIntParam(rule.Params, "freshness_seconds", 0)
			if run.Status != domain.SecurityScanCompleted || (freshness > 0 && run.FinishedAt != nil && time.Since(*run.FinishedAt) > time.Duration(freshness)*time.Second) {
				out = append(out, string(rule.Type))
			}
		}
	}
	sort.Strings(out)
	return out
}

func securityBreachFingerprint(policy domain.DeploymentPolicy, targetHash string, violated []string, counts domain.SecuritySeverityCounts, osvIDs []string) string {
	parts := []string{policy.ID.String(), targetHash, policy.UpdatedAt.UTC().Format(time.RFC3339Nano), string(policy.Enforcement), strings.Join(violated, ","), fmt.Sprintf("c=%d,h=%d,m=%d,l=%d,u=%d", counts.Critical, counts.High, counts.Moderate, counts.Low, counts.Unknown), strings.Join(osvIDs, ",")}
	return domain.CanonicalTargetHash(strings.Join(parts, "|"))
}

func uniqueOSVIDs(findings []domain.SecurityOSVFinding) []string {
	seen := map[string]struct{}{}
	for _, finding := range findings {
		if finding.OSVID == "" {
			continue
		}
		seen[finding.OSVID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *SecurityScanner) publishPolicyBreachAudit(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, breach *domain.SecurityPolicyBreach, result domain.SecurityBreachRecordResult) error {
	content, _ := json.Marshal(map[string]any{"action": "security.policy_breached", "run_id": run.ID, "target_key_hash": target.TargetKeyHash, "target_type": target.Type, "policy_id": breach.PolicyID, "breach_id": breach.ID, "fingerprint": breach.Fingerprint, "record_result": result, "violated_rules": breach.ViolatedRules, "severity_counts": breach.SeverityCounts, "osv_ids": breach.OSVIDs, "created_at": time.Now().UTC()})
	d := "security:audit:" + run.ID.String() + ":policy-breached:" + breach.PolicyID.String()
	ev := &nostr.Event{Kind: KindSecurityAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}, {"schema", SecurityAuditSchema}, {"type", "security-policy"}, {"action", "security.policy_breached"}, {"run_id", run.ID.String()}, {"target_type", string(target.Type)}, {"target_key_hash", target.TargetKeyHash}, {"policy_id", breach.PolicyID.String()}, {"breach_id", breach.ID.String()}}, Content: string(content)}
	return s.publishObservable(ctx, run, target, nil, "policy_breach_audit", SecurityAuditSchema, d, ev)
}

func (s *SecurityScanner) publishObservable(ctx context.Context, run *domain.SecurityScanRun, target *domain.SecurityTarget, findingID *uuid.UUID, observableType, schema, d string, ev *nostr.Event) error {
	publication := &domain.SecurityObservablePublication{ID: uuid.New(), ObservableType: observableType, EventKind: int(ev.Kind), DTag: d, Schema: schema, PublishState: domain.SecurityPublicationPending}
	if run != nil {
		publication.RunID = &run.ID
	}
	if target != nil {
		publication.TargetKeyHash = target.TargetKeyHash
	}
	publication.FindingID = findingID
	if s.repo != nil {
		_ = s.repo.UpsertSecurityPublication(ctx, publication)
	}
	results, err := s.publisher.PublishSignedEventWithResults(ctx, ev)
	if err != nil {
		if s.repo != nil {
			next := time.Now().UTC().Add(time.Minute)
			_ = s.repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationFailedRetryable, "", err.Error(), &next, nil)
		}
		return err
	}
	if len(results) == 0 {
		err = fmt.Errorf("publishing %s event: no relay OK results", observableType)
		if s.repo != nil {
			next := time.Now().UTC().Add(time.Minute)
			_ = s.repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationFailedRetryable, "", err.Error(), &next, nil)
		}
		return err
	}
	var rejections []string
	for _, result := range results {
		if result.Accepted {
			if s.pubkey != "" && !strings.EqualFold(ev.PubKey.Hex(), s.pubkey) {
				err = fmt.Errorf("publishing %s event: signed pubkey %s does not match configured publisher pubkey %s", observableType, ev.PubKey.Hex(), s.pubkey)
				if s.repo != nil {
					_ = s.repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationFailedTerminal, nostrutil.EventIDHex(ev), err.Error(), nil, nil)
				}
				return err
			}
			publishedAt := time.Now().UTC()
			if s.repo != nil {
				_ = s.repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationPublished, nostrutil.EventIDHex(ev), "", nil, &publishedAt)
			}
			return nil
		}
		relay := result.RelayURL
		if relay == "" {
			relay = "unknown relay"
		}
		if result.Reason != "" {
			rejections = append(rejections, relay+" rejected event: "+result.Reason)
		} else if result.Error != nil {
			rejections = append(rejections, fmt.Sprintf("%s publish error: %v", relay, result.Error))
		} else {
			rejections = append(rejections, relay+" returned OK accepted=false without reason")
		}
	}
	err = fmt.Errorf("publishing %s event: no relay accepted event: %s", observableType, strings.Join(rejections, "; "))
	if s.repo != nil {
		next := time.Now().UTC().Add(time.Minute)
		_ = s.repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationFailedRetryable, nostrutil.EventIDHex(ev), err.Error(), &next, nil)
	}
	return err
}

func acceptedResponse(runID uuid.UUID, target *domain.SecurityTarget, duplicate, skipped bool) *SecurityScanAccepted {
	return &SecurityScanAccepted{Status: "accepted", RunID: runID, TargetKeyHash: target.TargetKeyHash, TargetType: target.Type, Duplicate: duplicate, Skipped: skipped, Observables: SecurityObservableHint{Kinds: []int{KindSecurityStatus, KindSecuritySummary, KindSecurityFinding, KindSecurityAudit}, Tags: map[string]string{"domain": "security", "target_key_hash": target.TargetKeyHash}}}
}

func (s *SecurityScanner) ready(requireSubscriber bool) error {
	if s == nil {
		return fmt.Errorf("security scanner is nil")
	}
	if s.repo == nil {
		return fmt.Errorf("security repository is not configured")
	}
	if s.osv == nil {
		return fmt.Errorf("security OSV client is not configured")
	}
	if s.publisher == nil {
		return fmt.Errorf("security observable publisher is not configured")
	}
	if requireSubscriber && s.subscriber == nil {
		return fmt.Errorf("security SBOM subscriber is not configured")
	}
	return nil
}

func boundedServiceLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func unsupportedTotal(reasons map[string]int) int {
	total := 0
	for _, count := range reasons {
		total += count
	}
	return total
}

func addSeverity(counts domain.SecuritySeverityCounts, severity domain.SecuritySeverity) domain.SecuritySeverityCounts {
	switch severity {
	case domain.SecuritySeverityCritical:
		counts.Critical++
	case domain.SecuritySeverityHigh:
		counts.High++
	case domain.SecuritySeverityModerate:
		counts.Moderate++
	case domain.SecuritySeverityLow:
		counts.Low++
	default:
		counts.Unknown++
	}
	return counts
}

func normalizeSecuritySeverity(value string) domain.SecuritySeverity {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical":
		return domain.SecuritySeverityCritical
	case "high":
		return domain.SecuritySeverityHigh
	case "moderate", "medium":
		return domain.SecuritySeverityModerate
	case "low":
		return domain.SecuritySeverityLow
	default:
		return domain.SecuritySeverityUnknown
	}
}

func parseOptionalTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func securityTagValue(ev *nostr.Event, name string) string {
	if ev == nil {
		return ""
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func securityStringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key]; ok {
		switch value := raw.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		}
	}
	return ""
}

func securitySHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateSecurityInboundEvent(ev *nostr.Event, now time.Time, maxFutureSkew time.Duration) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if maxFutureSkew <= 0 {
		maxFutureSkew = 10 * time.Minute
	}
	if ev.ID.Hex() == "" || ev.PubKey.Hex() == "" {
		return fmt.Errorf("event id and pubkey are required")
	}
	if ev.CreatedAt <= 0 {
		return fmt.Errorf("created_at is required")
	}
	if ev.CreatedAt.Time().After(now.Add(maxFutureSkew)) {
		return fmt.Errorf("created_at too far in future")
	}
	for i, tag := range ev.Tags {
		if len(tag) == 0 || tag[0] == "" {
			return fmt.Errorf("tag %d has empty key", i)
		}
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	if !ev.VerifySignature() {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func securityAuthRequiredReason(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "auth-required") || strings.Contains(reason, "auth required") || strings.Contains(reason, "authentication required")
}
