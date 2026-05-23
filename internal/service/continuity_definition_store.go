package service

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ContinuityDefinitionStore stores latest replaceable continuity definitions by service key.
type ContinuityDefinitionStore interface {
	StoreProfile(profile domain.ServiceContinuityProfile) (bool, error)
	GetProfile(serviceKey string) (domain.ServiceContinuityProfile, bool)
	ListProfiles() []domain.ServiceContinuityProfile
	StoreReplicationPolicy(policy domain.ReplicationPolicy) (bool, error)
	GetReplicationPolicy(serviceKey string) (domain.ReplicationPolicy, bool)
	StoreRecipe(recipe domain.ContinuityRecipe) (bool, error)
	GetRecipe(serviceKey string, kind domain.ContinuityRecipeKind) (domain.ContinuityRecipe, bool)
	ListRecipesForService(serviceKey string) []domain.ContinuityRecipe
}

// InMemoryContinuityDefinitionStore is an RWMutex-protected latest-value store for continuity definitions.
type InMemoryContinuityDefinitionStore struct {
	mu                  sync.RWMutex
	profiles            map[string]domain.ServiceContinuityProfile
	replicationPolicies map[string]domain.ReplicationPolicy
	recipes             map[string]map[domain.ContinuityRecipeKind]domain.ContinuityRecipe
}

// NewInMemoryContinuityDefinitionStore returns an empty continuity definition store.
func NewInMemoryContinuityDefinitionStore() *InMemoryContinuityDefinitionStore {
	return &InMemoryContinuityDefinitionStore{
		profiles:            make(map[string]domain.ServiceContinuityProfile),
		replicationPolicies: make(map[string]domain.ReplicationPolicy),
		recipes:             make(map[string]map[domain.ContinuityRecipeKind]domain.ContinuityRecipe),
	}
}

// StoreProfile stores profile when it is newer than the current replaceable definition.
func (s *InMemoryContinuityDefinitionStore) StoreProfile(profile domain.ServiceContinuityProfile) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("continuity definition store is nil")
	}
	profile = cloneServiceContinuityProfile(profile)
	if err := profile.Validate(); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	current, ok := s.profiles[profile.ServiceKey]
	if ok && !replaceableDefinitionNewer(profile.UpdatedAt, profile.SourceEventID, current.UpdatedAt, current.SourceEventID) {
		return false, nil
	}
	s.profiles[profile.ServiceKey] = profile
	return true, nil
}

// GetProfile returns the latest service continuity profile for serviceKey.
func (s *InMemoryContinuityDefinitionStore) GetProfile(serviceKey string) (domain.ServiceContinuityProfile, bool) {
	if s == nil {
		return domain.ServiceContinuityProfile{}, false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	s.mu.RLock()
	profile, ok := s.profiles[serviceKey]
	s.mu.RUnlock()
	if !ok {
		return domain.ServiceContinuityProfile{}, false
	}
	return cloneServiceContinuityProfile(profile), true
}

// ListProfiles returns all latest service continuity profiles in deterministic service-key order.
func (s *InMemoryContinuityDefinitionStore) ListProfiles() []domain.ServiceContinuityProfile {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	profiles := make([]domain.ServiceContinuityProfile, 0, len(s.profiles))
	for _, profile := range s.profiles {
		profiles = append(profiles, cloneServiceContinuityProfile(profile))
	}
	s.mu.RUnlock()
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ServiceKey < profiles[j].ServiceKey })
	return profiles
}

// StoreReplicationPolicy stores policy when it is newer than the current replaceable definition.
func (s *InMemoryContinuityDefinitionStore) StoreReplicationPolicy(policy domain.ReplicationPolicy) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("continuity definition store is nil")
	}
	policy = cloneReplicationPolicy(policy)
	policy.ServiceKey = strings.TrimSpace(policy.ServiceKey)
	policy.SourceEventID = strings.TrimSpace(policy.SourceEventID)
	if err := domain.ValidateRequiredString(policy.ServiceKey, "service_key"); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	current, ok := s.replicationPolicies[policy.ServiceKey]
	if ok && !replaceableDefinitionNewer(policy.UpdatedAt, policy.SourceEventID, current.UpdatedAt, current.SourceEventID) {
		return false, nil
	}
	s.replicationPolicies[policy.ServiceKey] = policy
	return true, nil
}

// GetReplicationPolicy returns the latest replication policy for serviceKey.
func (s *InMemoryContinuityDefinitionStore) GetReplicationPolicy(serviceKey string) (domain.ReplicationPolicy, bool) {
	if s == nil {
		return domain.ReplicationPolicy{}, false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	s.mu.RLock()
	policy, ok := s.replicationPolicies[serviceKey]
	s.mu.RUnlock()
	if !ok {
		return domain.ReplicationPolicy{}, false
	}
	return cloneReplicationPolicy(policy), true
}

// StoreRecipe stores recipe when it is newer than the current recipe for service and kind.
func (s *InMemoryContinuityDefinitionStore) StoreRecipe(recipe domain.ContinuityRecipe) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("continuity definition store is nil")
	}
	recipe = cloneContinuityRecipe(recipe)
	if err := recipe.Validate(); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	byKind := s.recipes[recipe.ServiceKey]
	if byKind == nil {
		byKind = make(map[domain.ContinuityRecipeKind]domain.ContinuityRecipe)
		s.recipes[recipe.ServiceKey] = byKind
	}
	current, ok := byKind[recipe.Kind]
	if ok && !replaceableDefinitionNewer(recipe.UpdatedAt, recipe.SourceEventID, current.UpdatedAt, current.SourceEventID) {
		return false, nil
	}
	byKind[recipe.Kind] = recipe
	return true, nil
}

// GetRecipe returns the latest recipe for serviceKey and kind.
func (s *InMemoryContinuityDefinitionStore) GetRecipe(serviceKey string, kind domain.ContinuityRecipeKind) (domain.ContinuityRecipe, bool) {
	if s == nil {
		return domain.ContinuityRecipe{}, false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	s.mu.RLock()
	byKind := s.recipes[serviceKey]
	recipe, ok := byKind[kind]
	s.mu.RUnlock()
	if !ok {
		return domain.ContinuityRecipe{}, false
	}
	return cloneContinuityRecipe(recipe), true
}

// ListRecipesForService returns all latest recipes for serviceKey in deterministic kind/name order.
func (s *InMemoryContinuityDefinitionStore) ListRecipesForService(serviceKey string) []domain.ContinuityRecipe {
	if s == nil {
		return nil
	}
	serviceKey = strings.TrimSpace(serviceKey)
	s.mu.RLock()
	byKind := s.recipes[serviceKey]
	recipes := make([]domain.ContinuityRecipe, 0, len(byKind))
	for _, recipe := range byKind {
		recipes = append(recipes, cloneContinuityRecipe(recipe))
	}
	s.mu.RUnlock()
	sort.Slice(recipes, func(i, j int) bool {
		if recipes[i].Kind == recipes[j].Kind {
			return recipes[i].Name < recipes[j].Name
		}
		return recipes[i].Kind < recipes[j].Kind
	})
	return recipes
}

func (s *InMemoryContinuityDefinitionStore) ensureMapsLocked() {
	if s.profiles == nil {
		s.profiles = make(map[string]domain.ServiceContinuityProfile)
	}
	if s.replicationPolicies == nil {
		s.replicationPolicies = make(map[string]domain.ReplicationPolicy)
	}
	if s.recipes == nil {
		s.recipes = make(map[string]map[domain.ContinuityRecipeKind]domain.ContinuityRecipe)
	}
}

func replaceableDefinitionNewer(incomingAt time.Time, incomingEventID string, currentAt time.Time, currentEventID string) bool {
	if incomingAt.After(currentAt) {
		return true
	}
	if incomingAt.Before(currentAt) {
		return false
	}
	return strings.TrimSpace(incomingEventID) > strings.TrimSpace(currentEventID)
}

func cloneServiceContinuityProfile(profile domain.ServiceContinuityProfile) domain.ServiceContinuityProfile {
	profile.ServiceKey = strings.TrimSpace(profile.ServiceKey)
	profile.PrimaryWorkerPubKey = strings.TrimSpace(profile.PrimaryWorkerPubKey)
	profile.SourceEventID = strings.TrimSpace(profile.SourceEventID)
	if profile.Profiles != nil {
		profiles := make(map[domain.ContinuityMode]domain.ContinuityProfileSpec, len(profile.Profiles))
		for mode, spec := range profile.Profiles {
			profiles[mode] = cloneContinuityProfileSpec(spec)
		}
		profile.Profiles = profiles
	}
	return profile
}

func cloneContinuityProfileSpec(spec domain.ContinuityProfileSpec) domain.ContinuityProfileSpec {
	spec.Requires = cloneStringSlice(spec.Requires)
	spec.Disables = cloneStringSlice(spec.Disables)
	spec.Limits = cloneStringMap(spec.Limits)
	spec.Attributes = cloneStringMap(spec.Attributes)
	return spec
}

func cloneReplicationPolicy(policy domain.ReplicationPolicy) domain.ReplicationPolicy {
	policy.ServiceKey = strings.TrimSpace(policy.ServiceKey)
	policy.SourceEventID = strings.TrimSpace(policy.SourceEventID)
	if policy.Targets != nil {
		targets := make([]domain.ReplicationTarget, len(policy.Targets))
		copy(targets, policy.Targets)
		for i := range targets {
			targets[i].WorkerPubKey = strings.TrimSpace(targets[i].WorkerPubKey)
			targets[i].Strategy = strings.TrimSpace(targets[i].Strategy)
			targets[i].RequiredForModes = append([]domain.ContinuityMode(nil), targets[i].RequiredForModes...)
		}
		policy.Targets = targets
	}
	return policy
}

func cloneContinuityRecipe(recipe domain.ContinuityRecipe) domain.ContinuityRecipe {
	recipe.Name = strings.TrimSpace(recipe.Name)
	recipe.ServiceKey = strings.TrimSpace(recipe.ServiceKey)
	recipe.SourceEventID = strings.TrimSpace(recipe.SourceEventID)
	if recipe.Trigger != nil {
		trigger := *recipe.Trigger
		trigger.Type = strings.TrimSpace(trigger.Type)
		trigger.Target = strings.TrimSpace(trigger.Target)
		recipe.Trigger = &trigger
	}
	if recipe.Steps != nil {
		steps := make([]domain.RecipeStep, len(recipe.Steps))
		copy(steps, recipe.Steps)
		for i := range steps {
			steps[i].Name = strings.TrimSpace(steps[i].Name)
			steps[i].Action = strings.TrimSpace(steps[i].Action)
			steps[i].Params = cloneStringMap(steps[i].Params)
		}
		recipe.Steps = steps
	}
	return recipe
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
