package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

const (
	ContextVMMethodSagaInspect   = "soul-factory/saga/inspect"
	ContextVMMethodSagaRetry     = "soul-factory/saga/retry"
	ContextVMMethodSagaReconcile = "soul-factory/saga/reconcile"
	ContextVMMethodSagaSafeAbort = "soul-factory/saga/safe-abort"
)

type provisioningOperator interface {
	ExecuteProvisioningCommand(context.Context, saga.Command) (*saga.Report, error)
}

// RegisterSagaContextVMHandlers uses the transport's fleet-operator registration
// so authorization precedes replay storage, progress acknowledgments, and work.
func RegisterSagaContextVMHandlers(transport *controlplane.EncryptedRequestTransport, reactor *Reactor, gate *controlplane.FleetOperatorGate) {
	if transport == nil || reactor == nil {
		return
	}
	operator, ok := reactor.provisioner.(provisioningOperator)
	if !ok {
		return
	}
	for method, operation := range map[string]saga.OperatorCommand{
		ContextVMMethodSagaInspect: saga.CommandInspect, ContextVMMethodSagaRetry: saga.CommandRetry,
		ContextVMMethodSagaReconcile: saga.CommandReconcile, ContextVMMethodSagaSafeAbort: saga.CommandSafeAbort,
	} {
		transport.RegisterOperatorContextVMHandler(method, sagaContextVMHandler(operator, operation), gate)
	}
}

func sagaContextVMHandler(operator provisioningOperator, operation saga.OperatorCommand) controlplane.ContextVMHandler {
	return func(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
		// The operation comes from the method, never from client-supplied fields.
		var params struct {
			RequestID string `json:"request_id"`
			DryRun    *bool  `json:"dry_run"`
		}
		if err := json.Unmarshal(request.RPC.Params, &params); err != nil {
			return nil, fmt.Errorf("decode saga command: %w", err)
		}
		if strings.TrimSpace(params.RequestID) == "" {
			return nil, fmt.Errorf("request_id is required")
		}
		// Mutation requires an explicit false; omitted/null defaults to inspection.
		dryRun := params.DryRun == nil || *params.DryRun
		return operator.ExecuteProvisioningCommand(ctx, saga.Command{Operation: operation, RequestID: params.RequestID, DryRun: dryRun})
	}
}
