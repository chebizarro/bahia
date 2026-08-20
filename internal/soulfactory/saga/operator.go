package saga

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// OperatorCommand is a transport-neutral command accepted by an authenticated operator surface.
type OperatorCommand string

const (
	CommandInspect   OperatorCommand = "inspect"
	CommandRetry     OperatorCommand = "retry"
	CommandReconcile OperatorCommand = "reconcile"
	CommandSafeAbort OperatorCommand = "safe-abort"
)

// Command carries no credentials or free-form metadata and is safe to audit.
type Command struct {
	Operation OperatorCommand `json:"operation"`
	RequestID string          `json:"request_id"`
	DryRun    bool            `json:"dry_run"`
}

// Operator executes inspect/retry/reconcile/safe-abort against authoritative persisted state.
type Operator struct{ engine *Engine }

func NewOperator(engine *Engine) (*Operator, error) {
	if engine == nil {
		return nil, errors.New("saga engine is required")
	}
	return &Operator{engine: engine}, nil
}

func (o *Operator) Execute(ctx context.Context, command Command) (*Report, error) {
	command.RequestID = strings.TrimSpace(command.RequestID)
	if command.RequestID == "" {
		return nil, errors.New("request id is required")
	}
	switch command.Operation {
	case CommandInspect:
		return o.engine.Inspect(ctx, command.RequestID)
	case CommandRetry:
		return o.engine.Retry(ctx, command.RequestID, command.DryRun)
	case CommandReconcile:
		return o.engine.Reconcile(ctx, command.RequestID, command.DryRun)
	case CommandSafeAbort:
		return o.engine.SafeAbort(ctx, command.RequestID, command.DryRun)
	default:
		return nil, fmt.Errorf("unsupported saga operator command %q", command.Operation)
	}
}
