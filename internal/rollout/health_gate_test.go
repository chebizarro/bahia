package rollout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type errorObserver struct{ err error }

func (o errorObserver) Observe(context.Context, uuid.UUID, uuid.UUID, string) (*domain.RuntimeObservation, error) {
	return nil, o.err
}

func TestHealthGateObserverErrorsTriggerFailureThreshold(t *testing.T) {
	gate := NewHealthGate(errorObserver{err: errors.New("observer offline")}, zap.NewNop())
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	result, err := gate.Check(ctx, uuid.New(), uuid.New(), "api", domain.HealthGateConfig{
		Interval:         time.Millisecond,
		Timeout:          time.Second,
		SuccessThreshold: 1,
		FailureThreshold: 2,
	})
	if err != nil {
		t.Fatalf("Check returned context/timeout error instead of fast failure: %v", err)
	}
	if result.Passed || result.TotalChecks != 2 || result.UnhealthyChecks != 2 {
		t.Fatalf("unexpected health result: %+v", result)
	}
	if !strings.Contains(result.Error, "observer offline") {
		t.Fatalf("health result error = %q, want observer failure", result.Error)
	}
}
