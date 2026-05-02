package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestLLMHandlerRegisterHostUpsertsRuntimeTarget(t *testing.T) {
	repo := &llmHostWorkerRepo{}
	h := NewLLMHandler(nil, repo)
	body := `{"pubkey":"host-pubkey","name":"T7920 L40S","resources":{"memory_gb":128},"accelerators":[{"vendor":"nvidia","model":"L40S","count":1,"memory_gb":48}],"runtime_target":{"type":"docker","endpoint_ref":"t7920","public_base_url":"http://t7920.local"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/llm/hosts", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.RegisterHost(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if repo.worker == nil || repo.worker.RuntimeTarget == nil || repo.worker.RuntimeTarget.EndpointRef != "t7920" {
		t.Fatalf("runtime target not persisted: %#v", repo.worker)
	}
	if len(repo.worker.Accelerators) != 1 || repo.worker.Accelerators[0].Model != "L40S" {
		t.Fatalf("accelerators not parsed: %#v", repo.worker.Accelerators)
	}
}

type llmHostWorkerRepo struct{ worker *domain.Worker }

func (r *llmHostWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	cp := *w
	r.worker = &cp
	return nil
}
func (r *llmHostWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return r.worker, nil
}
func (r *llmHostWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	if r.worker == nil {
		return nil, nil
	}
	return []domain.Worker{*r.worker}, nil
}
func (r *llmHostWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}
