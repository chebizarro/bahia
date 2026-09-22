package router

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestVirtualizationRoutesRegisteredReadOnlyAndFailClosed(t *testing.T) {
	handler := NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, RouterDeps{})
	for _, path := range []string{"virtualization-hosts", "vm-images", "persistent-vms", "execution-planes", "vm-checkpoints", "vm-exports", "vm-operations"} {
		target := "/api/v1/" + path + "/" + uuid.New().String() + "?org_id=" + uuid.New().String()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 401 {
			t.Fatalf("%s must require identity with auth disabled: %d %s", path, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", target, nil))
		if w.Code != 405 {
			t.Fatalf("REST mutation registered: %s %d", path, w.Code)
		}
	}
}
