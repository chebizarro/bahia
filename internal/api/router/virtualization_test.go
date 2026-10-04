package router

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// virtTestServiceRepo satisfies the ServiceRepository interface for route-
// mounting tests. None of its methods are called — only its non-nil-ness
// matters so the dbGate middleware passes.
type virtTestServiceRepo struct{ repository.ServiceRepository }

func TestVirtualizationRoutesRegisteredReadOnlyAndFailClosed(t *testing.T) {
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	handler := NewWithDeps(registry, zap.NewNop(), config.CORSConfig{}, nil, RouterDeps{
		Services: virtTestServiceRepo{},
	})
	for _, path := range []string{"virtualization-hosts", "vm-images", "persistent-vms", "execution-planes", "vm-checkpoints", "vm-exports", "vm-operations"} {
		target := "/api/v1/" + path + "/" + uuid.New().String() + "?org_id=" + uuid.New().String()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 401 {
			t.Fatalf("%s must require identity with auth disabled: %d %s", path, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", target, nil))
		if w.Code != 404 {
			t.Fatalf("REST mutation registered: %s %d", path, w.Code)
		}
	}
}
