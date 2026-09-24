package controlplane

import (
	"context"
	"fmt"
	"sync"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const (
	ContextVMMethodContinuityFailover = "continuity/failover"
	ContextVMMethodContinuityRecovery = "continuity/recovery"
)

// ContinuityRuntime hydrates the disposable definition store from relay history
// before admitting commands. The application runs it at the same tier as ContextVM.
type ContinuityRuntime struct {
	pool        continuityRelayPool
	gate        *FleetOperatorGate
	definitions service.ContinuityDefinitionStore
	executor    service.ContinuityRecipeExecutor
	logger      *zap.Logger
	ready       chan struct{}
	readyOnce   sync.Once
}

type continuityRelayPool interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
	AuthenticateRelay(context.Context, string) error
	RecordRelayClosed(string, string)
	RecordRelayReREQ()
}

// RegisterContinuityContextVMHandlers returns the definition runner as well as
// registering mutations: both halves are required for a usable continuity plane.
func RegisterContinuityContextVMHandlers(transport *EncryptedRequestTransport, gate *FleetOperatorGate, pool *nostradapter.RelayPool, definitions service.ContinuityDefinitionStore, executor service.ContinuityRecipeExecutor, logger *zap.Logger) *ContinuityRuntime {
	if logger == nil {
		logger = zap.NewNop()
	}
	h := &ContinuityRuntime{gate: gate, definitions: definitions, executor: executor, logger: logger, ready: make(chan struct{})}
	if pool != nil {
		h.pool = pool
	}
	if transport != nil {
		transport.RegisterContextVMHandler(ContextVMMethodContinuityFailover, gate.wrap(h.handleFailoverRequest))
		transport.RegisterContextVMHandler(ContextVMMethodContinuityRecovery, gate.wrap(h.handleRecoveryRequest))
	}
	return h
}

func (h *ContinuityRuntime) Name() string { return "continuity-definitions" }

func (h *ContinuityRuntime) definitionFilters() ([]nostr.Filter, error) {
	if h.gate == nil || len(h.gate.authorizedPubkeys) == 0 {
		return nil, fmt.Errorf("%s", fleetOperatorNotConfiguredError)
	}
	authors := make([]nostr.PubKey, 0, len(h.gate.authorizedPubkeys))
	for _, key := range h.gate.authorizedPubkeys {
		author, err := nostr.PubKeyFromHex(key)
		if err != nil {
			return nil, fmt.Errorf("invalid continuity operator public key: %w", err)
		}
		authors = append(authors, author)
	}
	// The inventory is discovered from these operator-authored definitions; #d
	// values are not known before backfill. No limit/cursor may truncate a cold
	// rebuild of the in-memory store. Since=1 includes all valid stored events
	// and leaves this same scoped REQ open for realtime replacements.
	return []nostr.Filter{{Kinds: []nostr.Kind{nostradapter.KindContinuityProfile, nostradapter.KindFailoverPolicy, nostradapter.KindReplicationPolicy, nostradapter.KindRecoveryWorkflow}, Authors: authors, Since: 1}}, nil
}

func (h *ContinuityRuntime) Run(ctx context.Context) error {
	filters, err := h.definitionFilters()
	if err != nil {
		return err
	}
	if h.pool == nil || h.definitions == nil {
		return fmt.Errorf("continuity definition runtime is not configured")
	}
	backoff := nostradapter.DefaultBackoff()
	for {
		merged, err := h.pool.SubscribeAllWithEOSE(ctx, filters)
		if err == nil {
			err = h.consumeDefinitions(ctx, merged, backoff)
		}
		if ctx.Err() != nil {
			return nil
		}
		h.logger.Warn("continuity subscription ended", zap.Error(err))
		// This timer is reconnect backoff, never backfill completion.
		if err := waitContinuityReconnect(ctx, backoff.Next()); err != nil {
			return nil
		}
		h.pool.RecordRelayReREQ()
	}
}
