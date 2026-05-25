package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// DNSReconcileAdapter is the continuity boundary for triggering DNS reconciliation.
type DNSReconcileAdapter interface {
	ReconcileAll(ctx context.Context) error
}

// ContinuityDNSCutoverService connects continuity transitions to DNS mutation.
type ContinuityDNSCutoverService struct {
	dnsReconciler DNSReconcileAdapter
	publisher     events.Publisher
	logger        *zap.Logger

	mu           sync.Mutex
	lastStatuses map[string]ContinuityStatus
	seenRuns     map[string]struct{}
}

// NewContinuityDNSCutoverService subscribes continuity events to DNS reconciliation.
func NewContinuityDNSCutoverService(pub events.Publisher, dnsReconciler DNSReconcileAdapter, logger *zap.Logger) *ContinuityDNSCutoverService {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &ContinuityDNSCutoverService{
		dnsReconciler: dnsReconciler,
		publisher:     pub,
		logger:        logger.Named("continuity-dns-cutover"),
		lastStatuses:  make(map[string]ContinuityStatus),
		seenRuns:      make(map[string]struct{}),
	}
	if pub != nil {
		pub.Subscribe(EventContinuityStatusChanged, s.handleStatusChanged)
		pub.Subscribe(EventContinuityRecipeRunCompleted, s.handleRecipeRunCompleted)
		pub.Subscribe(events.EventType("continuity.recipe.action."+domain.RecipeActionPublishEndpoint), s.handlePublishEndpoint)
	}
	return s
}

// RestoreDNSRoutes satisfies ContinuityDNSAdapter for recovery recipes that restore routes.
func (s *ContinuityDNSCutoverService) RestoreDNSRoutes(ctx context.Context, _ string, _ map[string]string) error {
	return s.reconcile(ctx, "restore_dns_routes")
}

func (s *ContinuityDNSCutoverService) handleStatusChanged(ctx context.Context, e events.Event) {
	status, ok := continuityStatusFromEvent(e.Data)
	if !ok {
		s.logger.Warn("continuity DNS cutover ignored unsupported status payload", zap.String("event_type", string(e.Type)))
		return
	}
	status = normalizeContinuityStatus(status)
	if status.ServiceKey == "" {
		s.logger.Warn("continuity DNS cutover ignored status without service key")
		return
	}
	previous, hadPrevious := s.rememberStatus(status)
	if !hadPrevious || !continuityStatusCompletedTransition(previous, status) {
		return
	}
	if err := s.reconcile(ctx, "continuity_status_completed"); err != nil {
		s.logger.Warn("continuity status DNS reconcile failed", zap.String("service_key", status.ServiceKey), zap.Error(err))
	}
}

func (s *ContinuityDNSCutoverService) handleRecipeRunCompleted(ctx context.Context, e events.Event) {
	progress, ok := continuityRecipeProgressFromEvent(e.Data)
	if !ok {
		s.logger.Warn("continuity DNS cutover ignored unsupported recipe completion payload", zap.String("event_type", string(e.Type)))
		return
	}
	if progress.Status != ContinuityRecipeRunStatusCompleted {
		return
	}
	switch progress.RecipeKind {
	case domain.ContinuityRecipeKindFailover, domain.ContinuityRecipeKindRecovery:
	default:
		return
	}
	key := fmt.Sprintf("%s:%s:%s", e.Type, progress.RecipeKind, progress.RunID)
	if !s.markRunSeen(key) {
		return
	}
	if err := s.reconcile(ctx, "continuity_recipe_completed"); err != nil {
		s.logger.Warn("continuity recipe completion DNS reconcile failed", zap.String("run_id", progress.RunID), zap.String("recipe_kind", string(progress.RecipeKind)), zap.Error(err))
	}
}

func (s *ContinuityDNSCutoverService) handlePublishEndpoint(ctx context.Context, e events.Event) {
	progress, ok := continuityRecipeProgressFromEvent(e.Data)
	if !ok {
		s.logger.Warn("continuity DNS cutover ignored unsupported publish_endpoint payload", zap.String("event_type", string(e.Type)))
		return
	}
	key := fmt.Sprintf("%s:%s:%s", e.Type, progress.Action, progress.RunID)
	if !s.markRunSeen(key) {
		return
	}
	if err := s.reconcile(ctx, domain.RecipeActionPublishEndpoint); err != nil {
		s.logger.Warn("publish_endpoint DNS reconcile failed", zap.String("run_id", progress.RunID), zap.String("service_key", progress.ServiceKey), zap.Error(err))
	}
}

func (s *ContinuityDNSCutoverService) rememberStatus(status ContinuityStatus) (ContinuityStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.lastStatuses[status.ServiceKey]
	s.lastStatuses[status.ServiceKey] = status
	return previous, ok
}

func (s *ContinuityDNSCutoverService) markRunSeen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seenRuns[key]; ok {
		return false
	}
	s.seenRuns[key] = struct{}{}
	return true
}

func (s *ContinuityDNSCutoverService) reconcile(ctx context.Context, reason string) error {
	if s == nil || s.dnsReconciler == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.dnsReconciler.ReconcileAll(ctx); err != nil {
		return fmt.Errorf("%s DNS reconcile: %w", reason, err)
	}
	return nil
}

func continuityStatusCompletedTransition(previous, current ContinuityStatus) bool {
	if current.OperationState != ContinuityOperationSteady {
		return false
	}
	switch previous.OperationState {
	case ContinuityOperationFailoverInProgress:
		return current.ActiveProfile != domain.ContinuityModeFull
	case ContinuityOperationRecoveryInProgress:
		return current.ActiveProfile == domain.ContinuityModeFull
	default:
		return false
	}
}

func continuityRecipeProgressFromEvent(data any) (ContinuityRecipeProgressEvent, bool) {
	switch v := data.(type) {
	case ContinuityRecipeProgressEvent:
		return v, true
	case *ContinuityRecipeProgressEvent:
		if v == nil {
			return ContinuityRecipeProgressEvent{}, false
		}
		return *v, true
	default:
		return ContinuityRecipeProgressEvent{}, false
	}
}

var _ ContinuityDNSAdapter = (*ContinuityDNSCutoverService)(nil)
