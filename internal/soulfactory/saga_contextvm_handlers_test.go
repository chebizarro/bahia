package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type sagaResponsePublisher struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (p *sagaResponsePublisher) Publish(_ context.Context, event nostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return 1, nil
}

func TestSagaContextVMFleetAuthorization(t *testing.T) {
	p, reactor, provisionEvent, _, generator := startOperatorFixture(t)
	requester := nostr.Generate()
	serviceKey := nostr.Generate()
	serviceSigner, err := controlplane.NewPrivateKeySigner(serviceKey.Hex())
	require.NoError(t, err)
	for _, method := range []string{ContextVMMethodSagaInspect, ContextVMMethodSagaRetry, ContextVMMethodSagaReconcile, ContextVMMethodSagaSafeAbort} {
		for _, auth := range []struct {
			name string
			gate *controlplane.FleetOperatorGate
			want string
		}{
			{"authorized", controlplane.NewFleetOperatorGate([]string{requester.Public().Hex()}), ""},
			{"unauthorized", controlplane.NewFleetOperatorGate([]string{serviceKey.Public().Hex()}), "requester is not an authorized fleet operator"},
			{"empty", controlplane.NewFleetOperatorGate(nil), "fleet operator authorization is not configured"},
			{"nil", nil, "fleet operator authorization is not configured"},
		} {
			t.Run(method+"/"+auth.name, func(t *testing.T) {
				publisher := &sagaResponsePublisher{}
				responder := controlplane.NewEncryptedResponder(publisher, serviceSigner, serviceKey.Hex(), zap.NewNop())
				// Global transport admission is deliberately broader than the fleet
				// gate, proving it cannot substitute for method authorization.
				transport := controlplane.NewEncryptedRequestTransport(nil, responder, []string{requester.Public().Hex()}, zap.NewNop())
				RegisterSagaContextVMHandlers(transport, reactor, auth.gate)
				event := &nostr.Event{Kind: controlplane.KindContextVMMessage, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", serviceKey.Public().Hex()}}, Content: fmt.Sprintf(`{"jsonrpc":"2.0","id":"operator","method":%q,"params":{"request_id":%q,"requester":%q}}`, method, provisionEvent.ID.Hex(), serviceKey.Public().Hex())}
				require.NoError(t, event.Sign(requester))
				before, err := p.store.Load(t.Context(), provisionEvent.ID.Hex())
				require.NoError(t, err)
				transport.HandleContextVMEvent(t.Context(), event)
				after, err := p.store.Load(t.Context(), provisionEvent.ID.Hex())
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Equal(t, 1, generator.calls)
				publisher.mu.Lock()
				defer publisher.mu.Unlock()
				require.NotEmpty(t, publisher.events)
				var response controlplane.ContextVMJSONRPCResponse
				require.NoError(t, json.Unmarshal([]byte(publisher.events[len(publisher.events)-1].Content), &response))
				if auth.want != "" {
					require.Len(t, publisher.events, 1, "denied operations must not emit a progress acknowledgement")
					require.NotNil(t, response.Error)
					require.Equal(t, auth.want, response.Error.Message)
				} else {
					require.Nil(t, response.Error)
					require.NotNil(t, response.Result)
				}
			})
		}
	}
}

func TestSagaContextVMExplicitMutationAndValidation(t *testing.T) {
	p, _, event, _, generator := startOperatorFixture(t)
	handler := sagaContextVMHandler(p, saga.CommandRetry)
	for _, params := range []string{`{`, `{}`, `null`, `{"request_id":" "}`} {
		_, err := handler(t.Context(), contextVMTestRequest(t, ContextVMMethodSagaRetry, params))
		require.Error(t, err)
	}
	_, err := handler(t.Context(), contextVMTestRequest(t, ContextVMMethodSagaRetry, fmt.Sprintf(`{"request_id":%q,"dry_run":false,"operation":"safe-abort"}`, event.ID.Hex())))
	require.Error(t, err) // dependency remains unavailable, but retry actually ran
	require.Equal(t, 2, generator.calls)
	run, err := p.store.Load(t.Context(), event.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, saga.StageFailedRecoverable, run.Stage, "payload must not change the method's operation")
}
