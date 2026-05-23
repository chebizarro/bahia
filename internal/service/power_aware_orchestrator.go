package service

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	powerEmergencyUPSRuntimeThreshold = 10 * time.Minute
	powerDegradedBatteryThreshold     = 20.0
)

// PowerAwareOrchestrator stores worker power observations and returns advisory continuity recommendations.
type PowerAwareOrchestrator interface {
	ObservePower(obs domain.PowerObservation)
	GetWorkerPowerState(pubkey string) (*domain.PowerObservation, bool)
	RecommendDegradation() []domain.PowerRecommendation
}

// PowerManagedService describes the service metadata needed for advisory power recommendations.
type PowerManagedService struct {
	ServiceKey   string
	WorkerPubKey string
	Critical     bool
	ComputeHeavy bool
}

// InMemoryPowerAwareOrchestrator is an RWMutex-protected advisory power orchestrator.
type InMemoryPowerAwareOrchestrator struct {
	mu           sync.RWMutex
	observations map[string]domain.PowerObservation
	services     map[string]PowerManagedService
	serviceKeys  []string
}

var _ PowerAwareOrchestrator = (*InMemoryPowerAwareOrchestrator)(nil)

// NewInMemoryPowerAwareOrchestrator returns an advisory power orchestrator for the supplied services.
func NewInMemoryPowerAwareOrchestrator(services []PowerManagedService) *InMemoryPowerAwareOrchestrator {
	o := &InMemoryPowerAwareOrchestrator{
		observations: make(map[string]domain.PowerObservation),
		services:     make(map[string]PowerManagedService),
	}
	o.SetManagedServices(services)
	return o
}

// SetManagedServices replaces the service metadata used when deriving recommendations.
func (o *InMemoryPowerAwareOrchestrator) SetManagedServices(services []PowerManagedService) {
	if o == nil {
		return
	}
	normalized := make(map[string]PowerManagedService)
	keys := make([]string, 0, len(services))
	for _, svc := range services {
		svc = normalizePowerManagedService(svc)
		if svc.ServiceKey == "" || svc.WorkerPubKey == "" {
			continue
		}
		if _, exists := normalized[svc.ServiceKey]; !exists {
			keys = append(keys, svc.ServiceKey)
		}
		normalized[svc.ServiceKey] = svc
	}
	sort.Strings(keys)

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.observations == nil {
		o.observations = make(map[string]domain.PowerObservation)
	}
	o.services = normalized
	o.serviceKeys = keys
}

// ObservePower records the latest observation for a worker by observation timestamp.
func (o *InMemoryPowerAwareOrchestrator) ObservePower(obs domain.PowerObservation) {
	if o == nil {
		return
	}
	obs = normalizePowerObservation(obs)
	if obs.WorkerPubKey == "" || obs.ObservedAt.IsZero() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.observations == nil {
		o.observations = make(map[string]domain.PowerObservation)
	}
	current, ok := o.observations[obs.WorkerPubKey]
	if ok && !obs.ObservedAt.After(current.ObservedAt) {
		return
	}
	o.observations[obs.WorkerPubKey] = obs
}

// GetWorkerPowerState returns the latest stored power observation for a worker.
func (o *InMemoryPowerAwareOrchestrator) GetWorkerPowerState(pubkey string) (*domain.PowerObservation, bool) {
	if o == nil {
		return nil, false
	}
	pubkey = strings.TrimSpace(pubkey)
	o.mu.RLock()
	obs, ok := o.observations[pubkey]
	o.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return &obs, true
}

// RecommendDegradation returns deterministic advisory recommendations derived from current power state.
func (o *InMemoryPowerAwareOrchestrator) RecommendDegradation() []domain.PowerRecommendation {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	observations := make(map[string]domain.PowerObservation, len(o.observations))
	for worker, obs := range o.observations {
		observations[worker] = obs
	}
	services := make(map[string]PowerManagedService, len(o.services))
	for serviceKey, svc := range o.services {
		services[serviceKey] = svc
	}
	serviceKeys := append([]string(nil), o.serviceKeys...)
	o.mu.RUnlock()

	recommendations := make([]domain.PowerRecommendation, 0)
	for _, serviceKey := range serviceKeys {
		svc := services[serviceKey]
		obs, ok := observations[svc.WorkerPubKey]
		if !ok {
			continue
		}
		if rec, ok := powerRecommendationForService(svc, obs); ok {
			recommendations = append(recommendations, rec)
		}
	}
	return recommendations
}

func powerRecommendationForService(svc PowerManagedService, obs domain.PowerObservation) (domain.PowerRecommendation, bool) {
	mode := domain.ContinuityMode("")
	reasons := make([]string, 0, 3)

	if obs.UPSRuntime < powerEmergencyUPSRuntimeThreshold && !svc.Critical {
		mode = domain.ContinuityModeEmergency
		reasons = append(reasons, "UPS runtime below 10m0s for non-critical service")
	}
	if obs.BatteryPercent < powerDegradedBatteryThreshold {
		if mode == "" {
			mode = domain.ContinuityModeDegraded
		}
		reasons = append(reasons, "battery below 20%")
	}
	if obs.ThermalState == domain.PowerThermalStateCritical && svc.ComputeHeavy {
		if mode == "" {
			mode = domain.ContinuityModeDegraded
		}
		reasons = append(reasons, "critical thermal state for compute-heavy service")
	}
	if mode == "" {
		return domain.PowerRecommendation{}, false
	}
	return domain.PowerRecommendation{
		ServiceKey:      svc.ServiceKey,
		RecommendedMode: mode,
		Reason:          strings.Join(reasons, "; "),
		AutoExecute:     false,
	}, true
}

func normalizePowerObservation(obs domain.PowerObservation) domain.PowerObservation {
	obs.Source = strings.TrimSpace(obs.Source)
	obs.WorkerPubKey = strings.TrimSpace(obs.WorkerPubKey)
	obs.ThermalState = strings.ToLower(strings.TrimSpace(obs.ThermalState))
	return obs
}

func normalizePowerManagedService(svc PowerManagedService) PowerManagedService {
	svc.ServiceKey = strings.TrimSpace(svc.ServiceKey)
	svc.WorkerPubKey = strings.TrimSpace(svc.WorkerPubKey)
	return svc
}
