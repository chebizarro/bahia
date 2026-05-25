package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	CleanupModeReclaimableOnly = "reclaimable_only"
	CleanupModeAggressive      = "aggressive"
	CleanupModeRecommendOnly   = "recommend_only"

	DefaultWorkerCleanupCooldown = 30 * time.Minute
	DefaultCleanupTargetFreeGB   = 40
)

var (
	ErrWorkerCleanupInFlight          = errors.New("worker cleanup already in-flight")
	ErrWorkerCleanupCooldown          = errors.New("worker cleanup cooldown active")
	ErrWorkerCleanupInvalidMode       = errors.New("invalid worker cleanup mode")
	ErrWorkerCleanupCapacityRejected  = errors.New("worker cleanup rejected because worker queue is full")
	ErrWorkerCleanupAdmissionRejected = errors.New("worker cleanup rejected by admission policy")
	ErrWorkerCleanupCapabilityMissing = errors.New("worker cleanup capability requirement not satisfied")
	ErrWorkerCleanupPaymentRequired   = errors.New("worker cleanup payment token required")
)

type WorkerCleanupConfig struct {
	Mode               string
	Cooldown           time.Duration
	TargetFreeGB       int
	PaymentToken       string
	RequiredSoftware   []string
	PressureThresholds WorkerPressureThresholds
}

type CleanupExecution struct {
	WorkerPubKey     string            `json:"worker_pubkey"`
	Mode             string            `json:"cleanup_mode"`
	Reason           string            `json:"reason,omitempty"`
	LoomJobID        string            `json:"loom_job_id,omitempty"`
	ProtectedRefs    []string          `json:"protected_refs,omitempty"`
	TargetFreeGB     int               `json:"target_free_gb"`
	StartedAt        time.Time         `json:"started_at"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
	Status           string            `json:"status"`
	CapacityRejected bool              `json:"capacity_rejected,omitempty"`
	Error            string            `json:"error,omitempty"`
	JobStatus        *CleanupJobStatus `json:"job_status,omitempty"`
}

type CleanupAssignmentReader interface {
	GetAssignmentState(ctx context.Context, workerPubKey string) (*domain.WorkerAssignmentState, error)
}

type CleanupJobRequest struct {
	ID           string
	Type         string
	WorkerPubkey string
	Cmd          string
	Args         []string
	Env          map[string]string
	PaymentToken string
}

type CleanupJobStatus struct {
	JobID        string `json:"job_id"`
	Status       string `json:"status"`
	Success      *bool  `json:"success,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Duration     *int   `json:"duration,omitempty"`
	WorkerPubkey string `json:"worker_pubkey,omitempty"`
	StdoutURL    string `json:"stdout_url,omitempty"`
	StderrURL    string `json:"stderr_url,omitempty"`
	ChangeToken  string `json:"change_token,omitempty"`
	Error        string `json:"error,omitempty"`
	LogOutput    string `json:"log_output,omitempty"`
}

type CleanupStatusCallback func(status *CleanupJobStatus)

type cleanupLoomClient interface {
	SubmitCleanupJob(ctx context.Context, job CleanupJobRequest) (string, error)
	PollCleanupJobStatusFromWorker(ctx context.Context, jobEventID string, expectedWorkerPubkey string, callbacks ...CleanupStatusCallback) (*CleanupJobStatus, error)
}

type WorkerCleanupOrchestrator struct {
	workers     repository.WorkerRepository
	assignments CleanupAssignmentReader
	loom        cleanupLoomClient
	publisher   events.Publisher
	config      WorkerCleanupConfig
	logger      *zap.Logger

	mu                  sync.Mutex
	activeByWorker      map[string]*CleanupExecution
	lastAttemptByWorker map[string]time.Time
}

func NewWorkerCleanupOrchestrator(workers repository.WorkerRepository, assignments CleanupAssignmentReader, loomClient cleanupLoomClient, publisher events.Publisher, cfg WorkerCleanupConfig, logger *zap.Logger) *WorkerCleanupOrchestrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	if cfg.Mode == "" {
		cfg.Mode = CleanupModeRecommendOnly
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultWorkerCleanupCooldown
	}
	if cfg.TargetFreeGB <= 0 {
		cfg.TargetFreeGB = DefaultCleanupTargetFreeGB
	}
	cfg.PaymentToken = strings.TrimSpace(cfg.PaymentToken)
	cfg.RequiredSoftware = normalizeCleanupRequiredSoftware(cfg.RequiredSoftware)
	cfg.PressureThresholds = EffectiveWorkerPressureThresholds(cfg.PressureThresholds)
	return &WorkerCleanupOrchestrator{workers: workers, assignments: assignments, loom: loomClient, publisher: publisher, config: cfg, logger: logger, activeByWorker: map[string]*CleanupExecution{}, lastAttemptByWorker: map[string]time.Time{}}
}

func (o *WorkerCleanupOrchestrator) AutoModeEnabled() bool {
	return o != nil && strings.EqualFold(strings.TrimSpace(o.config.Mode), "auto")
}

func (o *WorkerCleanupOrchestrator) RequestCleanup(ctx context.Context, workerPubKey, mode, reason string) (*CleanupExecution, error) {
	if o == nil {
		return nil, fmt.Errorf("worker cleanup orchestrator is not configured")
	}
	workerPubKey = strings.TrimSpace(workerPubKey)
	mode = normalizeCleanupMode(mode)
	if mode == "" || mode == CleanupModeRecommendOnly {
		return nil, ErrWorkerCleanupInvalidMode
	}
	if o.workers == nil {
		return nil, fmt.Errorf("worker repository is not configured")
	}
	if o.loom == nil {
		return nil, fmt.Errorf("loom client is not configured")
	}
	if workerPubKey == "" {
		return nil, fmt.Errorf("worker_pubkey is required")
	}
	worker, err := o.workers.GetByPubKey(ctx, workerPubKey)
	if err != nil {
		return nil, fmt.Errorf("lookup worker: %w", err)
	}
	if worker == nil {
		return nil, fmt.Errorf("worker not found")
	}
	if err := o.validateAdmission(worker); err != nil {
		return nil, err
	}
	if err := o.validateCleanupCapabilities(worker); err != nil {
		return nil, err
	}
	if err := o.validateCleanupPayment(worker); err != nil {
		return nil, err
	}
	if mode == CleanupModeAggressive && strings.EqualFold(worker.Labels["bahia.cleanup.protect"], "true") {
		return nil, fmt.Errorf("worker label bahia.cleanup.protect=true rejects aggressive cleanup")
	}
	protectedRefs, err := o.protectedRefs(ctx, worker)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	exec := &CleanupExecution{WorkerPubKey: workerPubKey, Mode: mode, Reason: strings.TrimSpace(reason), ProtectedRefs: protectedRefs, TargetFreeGB: o.config.TargetFreeGB, StartedAt: now, Status: "submitting"}
	if err := o.reserve(workerPubKey, exec, now); err != nil {
		return nil, err
	}

	jobID, err := o.loom.SubmitCleanupJob(ctx, CleanupJobRequest{
		ID:           "cleanup:" + workerPubKey + ":" + now.Format("20060102T150405Z"),
		Type:         "cleanup",
		WorkerPubkey: workerPubKey,
		Cmd:          "bash",
		Args:         []string{"-c", buildCleanupScript(mode)},
		Env:          cleanupEnv(mode, reason, protectedRefs, o.config.TargetFreeGB),
		PaymentToken: o.config.PaymentToken,
	})
	if err != nil {
		exec.Status = "failed"
		exec.Error = err.Error()
		o.finish(ctx, workerPubKey, exec, false)
		return exec, err
	}
	exec.LoomJobID = jobID
	exec.Status = "dispatched"
	dispatch := cloneCleanupExecution(exec)
	o.publish(ctx, events.EventWorkerCleanupRequested, dispatch)
	o.watchCleanupJob(ctx, workerPubKey, exec, jobID)
	return dispatch, nil
}

func (o *WorkerCleanupOrchestrator) watchCleanupJob(ctx context.Context, workerPubKey string, exec *CleanupExecution, jobID string) {
	watchCtx := context.Background()
	if ctx != nil {
		watchCtx = context.WithoutCancel(ctx)
	}
	go func() {
		status, err := o.loom.PollCleanupJobStatusFromWorker(watchCtx, jobID, workerPubKey)
		exec.JobStatus = status
		if err != nil {
			exec.Status = "failed"
			exec.Error = err.Error()
			o.finish(watchCtx, workerPubKey, exec, false)
			return
		}
		if status == nil || status.Success == nil || !*status.Success {
			exec.Status = "failed"
			if status != nil {
				exec.Error = strings.TrimSpace(status.Error)
			}
			if exec.Error == "" {
				exec.Error = "cleanup job failed"
			}
			if isLoomCapacityRejection(status, exec.Error) {
				exec.CapacityRejected = true
				o.finish(watchCtx, workerPubKey, exec, true)
				return
			}
			o.finish(watchCtx, workerPubKey, exec, false)
			return
		}
		exec.Status = "completed"
		o.finish(watchCtx, workerPubKey, exec, false)
	}()
}

func (o *WorkerCleanupOrchestrator) validateAdmission(worker *domain.Worker) error {
	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeCleanup, Worker: worker, PressureThresholds: o.config.PressureThresholds})
	if decision.Eligible {
		return nil
	}
	return fmt.Errorf("%w: %s: %s", ErrWorkerCleanupAdmissionRejected, decision.Code, decision.Reason)
}

func (o *WorkerCleanupOrchestrator) validateCleanupCapabilities(worker *domain.Worker) error {
	if worker == nil {
		return fmt.Errorf("%w: worker is required", ErrWorkerCleanupCapabilityMissing)
	}
	missing := make([]string, 0)
	for _, required := range o.config.RequiredSoftware {
		if !workerAdvertisesCleanupSoftware(*worker, required) {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrWorkerCleanupCapabilityMissing, strings.Join(missing, ","))
	}
	return nil
}

func (o *WorkerCleanupOrchestrator) validateCleanupPayment(worker *domain.Worker) error {
	if !workerRequiresPayment(*worker) || o.config.PaymentToken != "" {
		return nil
	}
	return fmt.Errorf("%w: worker advertises paid pricing but worker_cleanup.payment_token is not configured", ErrWorkerCleanupPaymentRequired)
}

func (o *WorkerCleanupOrchestrator) reserve(workerPubKey string, exec *CleanupExecution, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if active := o.activeByWorker[workerPubKey]; active != nil {
		return fmt.Errorf("%w: loom_job_id=%s", ErrWorkerCleanupInFlight, active.LoomJobID)
	}
	if last := o.lastAttemptByWorker[workerPubKey]; !last.IsZero() && now.Sub(last) < o.config.Cooldown {
		return fmt.Errorf("%w: last_attempt_at=%s cooldown=%s", ErrWorkerCleanupCooldown, last.Format(time.RFC3339), o.config.Cooldown)
	}
	o.activeByWorker[workerPubKey] = exec
	o.lastAttemptByWorker[workerPubKey] = now
	return nil
}

func (o *WorkerCleanupOrchestrator) finish(ctx context.Context, workerPubKey string, exec *CleanupExecution, capacityRejected bool) {
	now := time.Now().UTC()
	exec.CompletedAt = &now
	o.mu.Lock()
	delete(o.activeByWorker, workerPubKey)
	if capacityRejected {
		delete(o.lastAttemptByWorker, workerPubKey)
	}
	o.mu.Unlock()
	if exec.Status == "completed" {
		o.publish(ctx, events.EventWorkerCleanupCompleted, exec)
	} else {
		o.publish(ctx, events.EventWorkerCleanupFailed, exec)
	}
}

func cloneCleanupExecution(exec *CleanupExecution) *CleanupExecution {
	if exec == nil {
		return nil
	}
	out := *exec
	out.ProtectedRefs = append([]string(nil), exec.ProtectedRefs...)
	if exec.JobStatus != nil {
		status := *exec.JobStatus
		out.JobStatus = &status
	}
	return &out
}

func (o *WorkerCleanupOrchestrator) publish(ctx context.Context, eventType events.EventType, exec *CleanupExecution) {
	if o.publisher == nil || exec == nil {
		return
	}
	o.publisher.Publish(ctx, events.Event{Type: eventType, EntityID: exec.WorkerPubKey, Data: events.WorkerCleanupEvent{WorkerPubKey: exec.WorkerPubKey, CleanupMode: exec.Mode, Reason: exec.Reason, LoomJobID: exec.LoomJobID, ProtectedRefs: append([]string(nil), exec.ProtectedRefs...), TargetFreeGB: exec.TargetFreeGB, Status: exec.Status, CapacityRejected: exec.CapacityRejected, Error: exec.Error, StartedAt: exec.StartedAt, CompletedAt: exec.CompletedAt}})
}

func (o *WorkerCleanupOrchestrator) protectedRefs(ctx context.Context, worker *domain.Worker) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		seen[ref] = struct{}{}
	}
	if o.assignments != nil {
		state, err := o.assignments.GetAssignmentState(ctx, worker.PubKey)
		if err != nil {
			return nil, fmt.Errorf("read worker assignments for cleanup keep-list: %w", err)
		}
		if state != nil {
			for _, assignment := range state.ActiveAssignments {
				if assignment.Metadata != nil {
					add(stringFromAssignmentMetadata(assignment.Metadata["image_ref"]))
				}
			}
		}
	}
	for _, standby := range worker.StandbyAssignments {
		add(standby.ArtifactRef)
	}
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func normalizeCleanupRequiredSoftware(required []string) []string {
	if len(required) == 0 {
		required = []string{"bash", "docker"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(required))
	for _, item := range required {
		item = normalizeCleanupCapabilityToken(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func workerAdvertisesCleanupSoftware(worker domain.Worker, required string) bool {
	required = normalizeCleanupCapabilityToken(required)
	if required == "" {
		return true
	}
	for _, software := range worker.Software {
		if normalizeCleanupCapabilityToken(software.Name) == required {
			return true
		}
	}
	for _, candidate := range worker.Capabilities.Runtimes {
		if normalizeCleanupCapabilityToken(candidate) == required {
			return true
		}
	}
	for _, candidate := range worker.Capabilities.Toolchains {
		if normalizeCleanupCapabilityToken(candidate) == required {
			return true
		}
	}
	for _, candidate := range worker.Capabilities.Features {
		if normalizeCleanupCapabilityToken(candidate) == required {
			return true
		}
	}
	return false
}

func workerRequiresPayment(worker domain.Worker) bool {
	for _, pricing := range worker.Pricing {
		if pricing.PricePerSecond > 0 {
			return true
		}
	}
	return false
}

func normalizeCleanupCapabilityToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeCleanupMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case CleanupModeReclaimableOnly:
		return CleanupModeReclaimableOnly
	case CleanupModeAggressive:
		return CleanupModeAggressive
	case CleanupModeRecommendOnly:
		return CleanupModeRecommendOnly
	default:
		return ""
	}
}

func cleanupEnv(mode, reason string, protectedRefs []string, targetFreeGB int) map[string]string {
	return map[string]string{
		"BAHIA_JOB_FAMILY":             "cleanup",
		"BAHIA_CLEANUP_MODE":           mode,
		"BAHIA_CLEANUP_REASON":         strings.TrimSpace(reason),
		"BAHIA_CLEANUP_PROTECTED_REFS": strings.Join(protectedRefs, ","),
		"BAHIA_CLEANUP_TARGET_FREE_GB": fmt.Sprintf("%d", targetFreeGB),
	}
}

func buildCleanupScript(mode string) string {
	base := `set -euo pipefail
printf 'bahia cleanup mode=%s target_free_gb=%s\n' "${BAHIA_CLEANUP_MODE}" "${BAHIA_CLEANUP_TARGET_FREE_GB}"
docker system df || true
docker builder prune -af
docker container prune -f
docker image prune -f
`
	if mode != CleanupModeAggressive {
		return base + "docker system df || true\n"
	}
	return base + `protected_file="$(mktemp)"
trap 'rm -f "$protected_file"' EXIT
printf '%s' "${BAHIA_CLEANUP_PROTECTED_REFS}" | tr ',' '\n' | sed '/^$/d' > "$protected_file"
docker image ls --format '{{.Repository}}:{{.Tag}} {{.ID}}' | while read -r ref image_id; do
  [ -n "$ref" ] || continue
  [ "$ref" = "<none>:<none>" ] && continue
  if grep -Fxq "$ref" "$protected_file"; then
    printf 'protecting image %s\n' "$ref"
    continue
  fi
  docker image rm "$image_id" || true
done
docker system df || true
`
}

func isLoomCapacityRejection(status *CleanupJobStatus, fallback string) bool {
	text := strings.ToLower(strings.TrimSpace(fallback))
	if status != nil {
		text += " " + strings.ToLower(status.Error)
		text += " " + strings.ToLower(status.LogOutput)
	}
	return strings.Contains(text, "queue is full") || strings.Contains(text, "queue full") || strings.Contains(text, "max_concurrent_jobs")
}

func stringFromAssignmentMetadata(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}
