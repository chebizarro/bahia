package router_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// staticPaymentView is a PaymentCanonicalView over a fixed record set, as the
// local event store would answer with no PostgreSQL at all.
type staticPaymentView []domain.PaymentRecord

func (v staticPaymentView) ListPaymentRecords(context.Context) ([]domain.PaymentRecord, error) {
	return append([]domain.PaymentRecord(nil), v...), nil
}

// Payment reads are canonical cp-state served from the local event store
// (audit B-31), so a DB-less router (nil registry and nil Services, which
// turns every PostgreSQL-gated route into a 503) still answers both payment
// routes from the canonical view (bahia-u5whr).
func TestPaymentRoutesServeWithoutDatabase(t *testing.T) {
	runID := uuid.New()
	payments := service.NewPaymentService(nil, zap.NewNop())
	payments.SetCanonicalView(staticPaymentView{{
		ID: uuid.New(), DeploymentRunID: runID, WorkerPubkey: "worker-a", MintURL: "https://mint.example",
		AmountSats: 21, TokenHash: "abc", Direction: domain.PaymentDirectionPayment, Status: domain.PaymentStatusSent,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}})
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Payments: payments})

	for _, tt := range []struct {
		path string
		want int
	}{
		{path: "/api/v1/payments/history?worker=worker-a", want: http.StatusOK},
		{path: "/api/v1/deployments/runs/" + runID.String() + "/cost", want: http.StatusOK},
		// A PostgreSQL-gated read on the same router still answers 503.
		{path: "/api/v1/vm-images", want: http.StatusServiceUnavailable},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("GET %s = %d, want %d: %s", tt.path, rec.Code, tt.want, rec.Body.String())
			}
			if tt.want != http.StatusOK {
				return
			}
			var body struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Data) == 0 {
				t.Fatalf("GET %s: want a data envelope, got %s (err %v)", tt.path, rec.Body.String(), err)
			}
		})
	}
}
