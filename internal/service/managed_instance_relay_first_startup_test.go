package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type supervisorBackfillProbe struct {
	supervisorRepoFake
	backfillCalls int
}

func (p *supervisorBackfillProbe) BackfillFromIndex(context.Context) error {
	p.backfillCalls++
	return nil
}

type cancelingSupervisionSource struct{ cancel context.CancelFunc }

func (s cancelingSupervisionSource) SupervisionSpecs(context.Context) ([]SupervisionSpec, error) {
	s.cancel()
	return nil, nil
}

func TestManagedInstanceSupervisorStartupDoesNotPromoteSQLState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &supervisorBackfillProbe{}
	supervisor, err := NewManagedInstanceSupervisor(
		cancelingSupervisionSource{cancel: cancel}, state, nil, nil, time.Hour, zap.NewNop(),
	)
	require.NoError(t, err)
	require.NoError(t, supervisor.Run(ctx))
	require.Zero(t, state.backfillCalls, "normal startup must not promote SQL rows to canonical events")
}
