package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testPaymentRepo struct {
	records []*domain.PaymentRecord
	byID    map[uuid.UUID]*domain.PaymentRecord
	byHash  map[string]*domain.PaymentRecord
}

func newTestPaymentRepo() *testPaymentRepo {
	return &testPaymentRepo{
		byID:   make(map[uuid.UUID]*domain.PaymentRecord),
		byHash: make(map[string]*domain.PaymentRecord),
	}
}

func (r *testPaymentRepo) Create(_ context.Context, rec *domain.PaymentRecord) error {
	if rec.ID == uuid.Nil {
		rec.ID = uuid.New()
	}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = rec.CreatedAt
	}
	copy := *rec
	r.records = append(r.records, &copy)
	r.byID[copy.ID] = &copy
	if copy.TokenHash != "" {
		r.byHash[copy.TokenHash] = &copy
	}
	return nil
}

func (r *testPaymentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.PaymentRecord, error) {
	return r.byID[id], nil
}

func (r *testPaymentRepo) ListByRun(_ context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	out := make([]domain.PaymentRecord, 0)
	for _, rec := range r.records {
		if rec.DeploymentRunID == runID {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (r *testPaymentRepo) ListByWorker(_ context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	out := make([]domain.PaymentRecord, 0)
	for _, rec := range r.records {
		if rec.WorkerPubkey != workerPubkey {
			continue
		}
		out = append(out, *rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *testPaymentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.PaymentStatus, errMsg string) error {
	if rec := r.byID[id]; rec != nil {
		rec.Status = status
		rec.ErrorMessage = errMsg
		rec.UpdatedAt = time.Date(2026, 5, 2, 12, 1, 0, 0, time.UTC)
	}
	return nil
}

func (r *testPaymentRepo) GetByTokenHash(_ context.Context, tokenHash string) (*domain.PaymentRecord, error) {
	return r.byHash[tokenHash], nil
}

type testPaymentWorkerRepo struct {
	workers map[string]*domain.Worker
}

func newTestPaymentWorkerRepo() *testPaymentWorkerRepo {
	return &testPaymentWorkerRepo{workers: make(map[string]*domain.Worker)}
}

func (r *testPaymentWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	r.workers[w.PubKey] = w
	return nil
}

func (r *testPaymentWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	return r.workers[pubkey], nil
}

func (r *testPaymentWorkerRepo) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	out := make([]domain.Worker, 0)
	for _, worker := range r.workers {
		if status == "" || string(worker.Status) == status {
			out = append(out, *worker)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (r *testPaymentWorkerRepo) UpdateStatus(_ context.Context, pubkey string, status domain.WorkerStatus) error {
	if worker := r.workers[pubkey]; worker != nil {
		worker.Status = status
	}
	return nil
}

func newTestMCPPaymentServer(t *testing.T) (*Server, *testPaymentRepo, uuid.UUID, string) {
	t.Helper()

	runID := uuid.New()
	workerPubkey := "worker-payment-pubkey"
	runRepo := newTestRunRepo()
	runRepo.runs[runID] = &domain.DeploymentRun{
		ID:                 runID,
		DeploymentIntentID: uuid.New(),
		WorkerPubkey:       workerPubkey,
		Status:             domain.RunStatusRunning,
		CreatedAt:          time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
	}

	workerRepo := newTestPaymentWorkerRepo()
	workerRepo.workers[workerPubkey] = &domain.Worker{
		PubKey:          workerPubkey,
		Name:            "payment-worker",
		MaxDurationSecs: 300,
		Pricing: []domain.WorkerPricing{{
			MintURL:        "https://mint.example",
			PricePerSecond: 4,
			Unit:           "sat",
		}},
		Status: domain.WorkerStatusOnline,
	}

	paymentRepo := newTestPaymentRepo()
	paymentSvc := service.NewPaymentService(paymentRepo, workerRepo, runRepo, zap.NewNop())
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{Payments: paymentSvc})
	return server, paymentRepo, runID, workerPubkey
}

func TestGetTools_IncludesPaymentTools(t *testing.T) {
	server, _, _, _ := newTestMCPPaymentServer(t)
	required := map[string]bool{
		"bahia_estimate_cost":       false,
		"bahia_get_run_cost":        false,
		"bahia_get_payment_history": false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
			if tool.InputSchema["required"] == nil {
				t.Fatalf("%s missing required schema", tool.Name)
			}
		}
	}

	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_EstimateCost(t *testing.T) {
	server, _, runID, workerPubkey := newTestMCPPaymentServer(t)

	result, err := server.CallTool(context.Background(), "bahia_estimate_cost", map[string]interface{}{
		"run_id":                  runID.String(),
		"estimated_duration_secs": float64(120),
	})
	if err != nil {
		t.Fatalf("estimate cost err: %v", err)
	}
	if result.IsError {
		t.Fatalf("estimate cost returned error: %s", result.Content[0].Text)
	}

	payload := decodeResultMap(t, result)
	if payload["worker_pubkey"] != workerPubkey {
		t.Fatalf("worker_pubkey = %v, want %s", payload["worker_pubkey"], workerPubkey)
	}
	if payload["mint_url"] != "https://mint.example" {
		t.Fatalf("mint_url = %v", payload["mint_url"])
	}
	if payload["estimated_cost_sats"] != float64(480) {
		t.Fatalf("estimated_cost_sats = %v, want 480", payload["estimated_cost_sats"])
	}
}

func TestCallTool_GetRunCostAndPaymentHistory(t *testing.T) {
	server, paymentRepo, runID, workerPubkey := newTestMCPPaymentServer(t)
	ctx := context.Background()

	if err := paymentRepo.Create(ctx, &domain.PaymentRecord{
		DeploymentRunID: runID,
		WorkerPubkey:    workerPubkey,
		MintURL:         "https://mint.example",
		AmountSats:      1000,
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusSent,
		TokenHash:       "token-hash-payment",
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if err := paymentRepo.Create(ctx, &domain.PaymentRecord{
		DeploymentRunID: runID,
		WorkerPubkey:    workerPubkey,
		MintURL:         "https://mint.example",
		AmountSats:      250,
		Direction:       domain.PaymentDirectionChange,
		Status:          domain.PaymentStatusRedeemed,
		TokenHash:       "token-hash-change",
	}); err != nil {
		t.Fatalf("create change: %v", err)
	}

	runCost, err := server.CallTool(ctx, "bahia_get_run_cost", map[string]interface{}{
		"run_id": runID.String(),
	})
	if err != nil {
		t.Fatalf("get run cost err: %v", err)
	}
	if runCost.IsError {
		t.Fatalf("get run cost returned error: %s", runCost.Content[0].Text)
	}
	runCostPayload := decodeResultMap(t, runCost)
	summary := runCostPayload["summary"].(map[string]interface{})
	if summary["total_paid_sats"] != float64(1000) {
		t.Fatalf("total_paid_sats = %v, want 1000", summary["total_paid_sats"])
	}
	if summary["total_change_sats"] != float64(250) {
		t.Fatalf("total_change_sats = %v, want 250", summary["total_change_sats"])
	}
	if summary["net_cost_sats"] != float64(750) {
		t.Fatalf("net_cost_sats = %v, want 750", summary["net_cost_sats"])
	}
	if len(runCostPayload["payments"].([]interface{})) != 2 {
		t.Fatalf("payments length = %d, want 2", len(runCostPayload["payments"].([]interface{})))
	}

	history, err := server.CallTool(ctx, "bahia_get_payment_history", map[string]interface{}{
		"worker_pubkey": workerPubkey,
		"limit":         float64(1),
	})
	if err != nil {
		t.Fatalf("get payment history err: %v", err)
	}
	if history.IsError {
		t.Fatalf("get payment history returned error: %s", history.Content[0].Text)
	}
	historyPayload := decodeResultMap(t, history)
	if historyPayload["worker_pubkey"] != workerPubkey {
		t.Fatalf("worker_pubkey = %v, want %s", historyPayload["worker_pubkey"], workerPubkey)
	}
	if historyPayload["total"] != float64(1) {
		t.Fatalf("history total = %v, want 1", historyPayload["total"])
	}
	payments := historyPayload["payments"].([]interface{})
	first := payments[0].(map[string]interface{})
	if first["amount_sats"] != float64(1000) {
		t.Fatalf("history amount_sats = %v, want 1000", first["amount_sats"])
	}
}

func TestCallTool_PaymentsValidationAndConfiguration(t *testing.T) {
	server, _, runID, _ := newTestMCPPaymentServer(t)

	invalidID, err := server.CallTool(context.Background(), "bahia_estimate_cost", map[string]interface{}{
		"run_id": "not-a-uuid",
	})
	if err != nil {
		t.Fatalf("invalid id call err: %v", err)
	}
	if !invalidID.IsError || !strings.Contains(invalidID.Content[0].Text, "invalid run_id") {
		t.Fatalf("expected invalid run_id error, got %#v", invalidID)
	}

	missingWorker, err := server.CallTool(context.Background(), "bahia_get_payment_history", map[string]interface{}{})
	if err != nil {
		t.Fatalf("missing worker call err: %v", err)
	}
	if !missingWorker.IsError || !strings.Contains(missingWorker.Content[0].Text, "worker_pubkey is required") {
		t.Fatalf("expected worker_pubkey error, got %#v", missingWorker)
	}

	unconfigured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	missingService, err := unconfigured.CallTool(context.Background(), "bahia_get_run_cost", map[string]interface{}{
		"run_id": runID.String(),
	})
	if err != nil {
		t.Fatalf("unconfigured call err: %v", err)
	}
	if !missingService.IsError || !strings.Contains(missingService.Content[0].Text, "payment tools are not configured") {
		t.Fatalf("expected unconfigured payment service error, got %#v", missingService)
	}
}
