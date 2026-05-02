package nostr

import (
	"context"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type captureWorkerRepo struct{ worker *domain.Worker }

func (r *captureWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	cp := *w
	r.worker = &cp
	return nil
}
func (r *captureWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return nil, nil
}
func (r *captureWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, nil
}
func (r *captureWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

func TestProcessorWorkerAdvertisementParsesLLMRuntimeMetadata(t *testing.T) {
	repo := &captureWorkerRepo{}
	processor := NewProcessor(nil, repo, zap.NewNop())
	ev := &gonostr.Event{
		PubKey:    "worker-pubkey",
		Kind:      kindLoomWorkerAd,
		CreatedAt: gonostr.Now(),
		Content:   `{"name":"gpu-worker","resources":{"cpu_cores":32,"memory_gb":256,"disk_gb":1000},"accelerators":[{"vendor":"nvidia","model":"L40S","count":1,"memory_gb":48,"driver":"535"}],"runtime_target":{"type":"compose","endpoint_ref":"gpu-a","compose_dir":"/srv/llm","public_base_url":"https://gpu-a.example"}}`,
	}
	if err := processor.handleWorkerAdvertisement(context.Background(), ev); err != nil {
		t.Fatalf("handle worker ad: %v", err)
	}
	if repo.worker == nil || repo.worker.Resources == nil || repo.worker.Resources.MemoryGB != 256 {
		t.Fatalf("resources not parsed: %#v", repo.worker)
	}
	if len(repo.worker.Accelerators) != 1 || repo.worker.Accelerators[0].Model != "L40S" {
		t.Fatalf("accelerators not parsed: %#v", repo.worker.Accelerators)
	}
	if repo.worker.RuntimeTarget == nil || repo.worker.RuntimeTarget.EndpointRef != "gpu-a" || repo.worker.RuntimeTarget.PublicBaseURL != "https://gpu-a.example" {
		t.Fatalf("runtime target not parsed: %#v", repo.worker.RuntimeTarget)
	}
}
