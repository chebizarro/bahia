package service

import (
	"context"
	"fmt"
	"strings"

	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"go.uber.org/zap"
)

const (
	defaultSBOMAsyncQueueDepth = 128
	sbomAsyncOperationGenerate = "sbom/generate"
	sbomAsyncOperationImport   = "sbom/import"
)

type SBOMAcceptedAck struct {
	Accepted        bool   `json:"accepted"`
	Status          string `json:"status"`
	RunID           string `json:"run_id"`
	StatusDTag      string `json:"status_d_tag"`
	IDempotencyKey  string `json:"idempotencyKey"`
	ObservableKinds []int  `json:"observable_kinds"`
}

type SBOMAsyncResult struct {
	Operation      string
	IDempotencyKey string
	StatusDTag     string
	Run            *SBOMRunResult
	Err            error
}

type SBOMAsyncRunnerOption func(*SBOMAsyncRunner)

type SBOMAsyncRunner struct {
	orchestrator *SBOMOrchestrator
	jobs         chan sbomAsyncJob
	observer     func(SBOMAsyncResult)
	logger       *zap.Logger
}

type sbomAsyncJob struct {
	operation string
	generate  *SBOMGenerateRequest
	importReq *SBOMImportRequest
	ack       SBOMAcceptedAck
}

func NewSBOMAsyncRunner(orchestrator *SBOMOrchestrator, opts ...SBOMAsyncRunnerOption) *SBOMAsyncRunner {
	logger := zap.NewNop()
	if orchestrator != nil && orchestrator.Logger != nil {
		logger = orchestrator.Logger.Named("async-runner")
	}
	r := &SBOMAsyncRunner{orchestrator: orchestrator, jobs: make(chan sbomAsyncJob, defaultSBOMAsyncQueueDepth), logger: logger}
	for _, opt := range opts {
		opt(r)
	}
	if r.jobs == nil {
		r.jobs = make(chan sbomAsyncJob, defaultSBOMAsyncQueueDepth)
	}
	if r.logger == nil {
		r.logger = zap.NewNop()
	}
	return r
}

func WithSBOMAsyncRunnerQueueDepth(depth int) SBOMAsyncRunnerOption {
	return func(r *SBOMAsyncRunner) {
		if depth <= 0 {
			depth = defaultSBOMAsyncQueueDepth
		}
		r.jobs = make(chan sbomAsyncJob, depth)
	}
}

func WithSBOMAsyncResultObserver(observer func(SBOMAsyncResult)) SBOMAsyncRunnerOption {
	return func(r *SBOMAsyncRunner) {
		r.observer = observer
	}
}

func (r *SBOMAsyncRunner) Name() string { return "sbom-async-runner" }

func (r *SBOMAsyncRunner) Run(ctx context.Context) error {
	if r == nil || r.orchestrator == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case job := <-r.jobs:
			r.runJob(ctx, job)
		}
	}
}

func (r *SBOMAsyncRunner) EnqueueGenerate(ctx context.Context, req SBOMGenerateRequest) (SBOMAcceptedAck, error) {
	if err := r.readyFor(sbomAsyncOperationGenerate); err != nil {
		return SBOMAcceptedAck{}, err
	}
	ack, err := NewSBOMAcceptedAck(req.IDempotencyKey)
	if err != nil {
		return SBOMAcceptedAck{}, err
	}
	return ack, r.enqueue(ctx, sbomAsyncJob{operation: sbomAsyncOperationGenerate, generate: &req, ack: ack})
}

func (r *SBOMAsyncRunner) EnqueueImport(ctx context.Context, req SBOMImportRequest) (SBOMAcceptedAck, error) {
	if err := r.readyFor(sbomAsyncOperationImport); err != nil {
		return SBOMAcceptedAck{}, err
	}
	ack, err := NewSBOMAcceptedAck(req.IDempotencyKey)
	if err != nil {
		return SBOMAcceptedAck{}, err
	}
	return ack, r.enqueue(ctx, sbomAsyncJob{operation: sbomAsyncOperationImport, importReq: &req, ack: ack})
}

func NewSBOMAcceptedAck(idempotencyKey string) (SBOMAcceptedAck, error) {
	key := strings.TrimSpace(idempotencyKey)
	statusD, err := SBOMStatusDTag(key)
	if err != nil {
		return SBOMAcceptedAck{}, err
	}
	return SBOMAcceptedAck{
		Accepted:        true,
		Status:          "accepted",
		RunID:           key,
		StatusDTag:      statusD,
		IDempotencyKey:  key,
		ObservableKinds: []int{KindSBOMStatus, KindSBOMAudit, sbomadapter.KindSBOMReference, sbomadapter.KindSBOMAvailabilityList},
	}, nil
}

func (r *SBOMAsyncRunner) readyFor(operation string) error {
	if r == nil || r.orchestrator == nil {
		return fmt.Errorf("SBOM async runner is not configured")
	}
	if err := r.orchestrator.validateRuntimeConfigured(); err != nil {
		return err
	}
	if operation == sbomAsyncOperationGenerate && r.orchestrator.Generators == nil {
		return fmt.Errorf("SBOM generator registry is not configured")
	}
	return nil
}

func (r *SBOMAsyncRunner) enqueue(ctx context.Context, job sbomAsyncJob) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r.jobs <- job:
		return nil
	default:
		return fmt.Errorf("SBOM async runner queue is full")
	}
}

func (r *SBOMAsyncRunner) runJob(ctx context.Context, job sbomAsyncJob) {
	result := SBOMAsyncResult{Operation: job.operation, IDempotencyKey: job.ack.IDempotencyKey, StatusDTag: job.ack.StatusDTag}
	switch job.operation {
	case sbomAsyncOperationGenerate:
		result.Run, result.Err = r.orchestrator.Generate(ctx, *job.generate)
	case sbomAsyncOperationImport:
		result.Run, result.Err = r.orchestrator.Import(ctx, *job.importReq)
	default:
		result.Err = fmt.Errorf("unsupported SBOM async operation %q", job.operation)
	}
	if result.Err != nil && r.logger != nil {
		r.logger.Warn("SBOM async orchestration failed", zap.String("operation", result.Operation), zap.String("idempotency_key", result.IDempotencyKey), zap.Error(result.Err))
	}
	if r.observer != nil {
		r.observer(result)
	}
}
