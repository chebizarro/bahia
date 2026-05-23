package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	SurvivabilitySurvivable    = "survivable"
	SurvivabilityDegradedOnly  = "degraded_only"
	SurvivabilityEmergencyOnly = "emergency_only"
	SurvivabilityUnsatisfied   = "unsatisfied"

	ReplicationFreshnessFresh   = "fresh"
	ReplicationFreshnessStale   = "stale"
	ReplicationFreshnessUnknown = "unknown"

	StandbyHealthHealthy = "healthy"
	StandbyHealthStale   = "stale"
	StandbyHealthOffline = "offline"
	StandbyHealthNone    = "none"
)

// ContinuityAssessment describes the current survivability envelope for one managed service.
type ContinuityAssessment struct {
	ServiceKey           string
	Survivability        string
	AvailableProfiles    []domain.ContinuityMode
	MissingDependencies  []string
	SelectedStandby      string
	ReplicationFreshness string
	StandbyHealth        string
}

// WorkerReader exposes the worker continuity read model needed by the graph evaluator.
type WorkerReader interface {
	ListWorkers() []domain.Worker
}

// ContinuityGraph evaluates service survivability from existing continuity read models.
type ContinuityGraph struct {
	definitions ContinuityDefinitionStore
	heartbeats  HeartbeatMonitor
	statuses    ContinuityStatusReader
	workers     WorkerReader
	clock       func() time.Time
}

// NewContinuityGraph constructs a side-effect-free continuity assessment service.
func NewContinuityGraph(definitions ContinuityDefinitionStore, heartbeats HeartbeatMonitor, statuses ContinuityStatusReader, workers WorkerReader) (*ContinuityGraph, error) {
	if definitions == nil {
		return nil, fmt.Errorf("continuity definition store is required")
	}
	if heartbeats == nil {
		return nil, fmt.Errorf("heartbeat monitor is required")
	}
	if statuses == nil {
		return nil, fmt.Errorf("continuity status store is required")
	}
	if workers == nil {
		return nil, fmt.Errorf("worker reader is required")
	}
	return &ContinuityGraph{
		definitions: definitions,
		heartbeats:  heartbeats,
		statuses:    statuses,
		workers:     workers,
		clock:       func() time.Time { return time.Now().UTC() },
	}, nil
}

// AssessService evaluates one service's current continuity survivability.
func (g *ContinuityGraph) AssessService(serviceKey string) ContinuityAssessment {
	return g.assessService(serviceKey, "")
}

// AssessAll evaluates every service known to the continuity profile or status read models.
func (g *ContinuityGraph) AssessAll() []ContinuityAssessment {
	if g == nil {
		return nil
	}
	serviceKeys := g.managedServiceKeys()
	assessments := make([]ContinuityAssessment, 0, len(serviceKeys))
	for _, serviceKey := range serviceKeys {
		assessments = append(assessments, g.assessService(serviceKey, ""))
	}
	return assessments
}

// SimulateFailure evaluates survivability if the supplied worker is unavailable.
func (g *ContinuityGraph) SimulateFailure(workerPubKey string) []ContinuityAssessment {
	if g == nil {
		return nil
	}
	failedWorker := strings.TrimSpace(workerPubKey)
	serviceKeys := g.managedServiceKeys()
	assessments := make([]ContinuityAssessment, 0, len(serviceKeys))
	for _, serviceKey := range serviceKeys {
		assessments = append(assessments, g.assessService(serviceKey, failedWorker))
	}
	return assessments
}

func (g *ContinuityGraph) assessService(serviceKey string, failedWorker string) ContinuityAssessment {
	serviceKey = strings.TrimSpace(serviceKey)
	assessment := ContinuityAssessment{
		ServiceKey:           serviceKey,
		Survivability:        SurvivabilityUnsatisfied,
		ReplicationFreshness: ReplicationFreshnessUnknown,
		StandbyHealth:        StandbyHealthNone,
	}
	if g == nil || serviceKey == "" {
		assessment.MissingDependencies = []string{"service_key"}
		return assessment
	}

	profile, ok := g.definitions.GetProfile(serviceKey)
	if !ok {
		assessment.MissingDependencies = []string{"continuity_profile"}
		return assessment
	}
	policy, hasPolicy := g.definitions.GetReplicationPolicy(serviceKey)
	currentWorker := g.currentWorker(profile)
	candidates := g.standbyCandidates(serviceKey, currentWorker, failedWorker)
	if len(candidates) == 0 {
		assessment.MissingDependencies = []string{"standby_worker"}
		return assessment
	}

	best := g.selectBestCandidate(profile, policy, hasPolicy, candidates)
	assessment.SelectedStandby = best.worker.PubKey
	assessment.StandbyHealth = best.health
	assessment.ReplicationFreshness = best.replicationFreshness
	assessment.AvailableProfiles = append([]domain.ContinuityMode(nil), best.availableProfiles...)
	assessment.MissingDependencies = sortedStrings(best.missingDependencies.values())
	assessment.Survivability = survivabilityForProfiles(assessment.AvailableProfiles)
	return assessment
}

func (g *ContinuityGraph) managedServiceKeys() []string {
	seen := map[string]struct{}{}
	for _, profile := range g.definitions.ListProfiles() {
		serviceKey := strings.TrimSpace(profile.ServiceKey)
		if serviceKey != "" {
			seen[serviceKey] = struct{}{}
		}
	}
	for _, status := range g.statuses.ListAllStatuses() {
		serviceKey := strings.TrimSpace(status.ServiceKey)
		if serviceKey != "" {
			seen[serviceKey] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for serviceKey := range seen {
		keys = append(keys, serviceKey)
	}
	sort.Strings(keys)
	return keys
}

func (g *ContinuityGraph) currentWorker(profile domain.ServiceContinuityProfile) string {
	if status, ok := g.statuses.GetServiceStatus(profile.ServiceKey); ok {
		if active := strings.TrimSpace(status.ActiveWorkerPubKey); active != "" {
			return active
		}
	}
	return strings.TrimSpace(profile.PrimaryWorkerPubKey)
}

func (g *ContinuityGraph) standbyCandidates(serviceKey string, currentWorker string, failedWorker string) []continuityStandbyCandidate {
	workers := g.workers.ListWorkers()
	candidates := make([]continuityStandbyCandidate, 0)
	for i := range workers {
		worker := workers[i]
		worker.PubKey = strings.TrimSpace(worker.PubKey)
		if worker.PubKey == "" || worker.PubKey == currentWorker || worker.PubKey == failedWorker {
			continue
		}
		assignment, ok := standbyAssignmentForService(worker, serviceKey)
		if !ok {
			continue
		}
		health := g.standbyHealth(worker.PubKey)
		candidates = append(candidates, continuityStandbyCandidate{worker: worker, assignment: assignment, health: health})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if continuityHealthRank(candidates[i].health) != continuityHealthRank(candidates[j].health) {
			return continuityHealthRank(candidates[i].health) > continuityHealthRank(candidates[j].health)
		}
		if continuityTierRank(candidates[i].assignment.Tier) != continuityTierRank(candidates[j].assignment.Tier) {
			return continuityTierRank(candidates[i].assignment.Tier) > continuityTierRank(candidates[j].assignment.Tier)
		}
		if continuityAssignmentProfileRank(candidates[i].assignment) != continuityAssignmentProfileRank(candidates[j].assignment) {
			return continuityAssignmentProfileRank(candidates[i].assignment) > continuityAssignmentProfileRank(candidates[j].assignment)
		}
		return candidates[i].worker.PubKey < candidates[j].worker.PubKey
	})
	return candidates
}

func (g *ContinuityGraph) standbyHealth(pubkey string) string {
	snapshot, ok := g.heartbeats.Snapshot(pubkey)
	if !ok {
		return StandbyHealthOffline
	}
	switch snapshot.Status {
	case domain.HeartbeatStatusFresh:
		return StandbyHealthHealthy
	case domain.HeartbeatStatusStale:
		return StandbyHealthStale
	case domain.HeartbeatStatusExpired, domain.HeartbeatStatusUnknown:
		return StandbyHealthOffline
	default:
		return StandbyHealthOffline
	}
}

func (g *ContinuityGraph) selectBestCandidate(profile domain.ServiceContinuityProfile, policy domain.ReplicationPolicy, hasPolicy bool, candidates []continuityStandbyCandidate) continuityCandidateEvaluation {
	evaluations := make([]continuityCandidateEvaluation, 0, len(candidates))
	for _, candidate := range candidates {
		evaluations = append(evaluations, g.evaluateCandidate(profile, policy, hasPolicy, candidate))
	}
	sort.SliceStable(evaluations, func(i, j int) bool {
		if continuityProfileSetRank(evaluations[i].availableProfiles) != continuityProfileSetRank(evaluations[j].availableProfiles) {
			return continuityProfileSetRank(evaluations[i].availableProfiles) > continuityProfileSetRank(evaluations[j].availableProfiles)
		}
		if continuityHealthRank(evaluations[i].health) != continuityHealthRank(evaluations[j].health) {
			return continuityHealthRank(evaluations[i].health) > continuityHealthRank(evaluations[j].health)
		}
		if continuityReplicationRank(evaluations[i].replicationFreshness) != continuityReplicationRank(evaluations[j].replicationFreshness) {
			return continuityReplicationRank(evaluations[i].replicationFreshness) > continuityReplicationRank(evaluations[j].replicationFreshness)
		}
		if continuityTierRank(evaluations[i].assignment.Tier) != continuityTierRank(evaluations[j].assignment.Tier) {
			return continuityTierRank(evaluations[i].assignment.Tier) > continuityTierRank(evaluations[j].assignment.Tier)
		}
		return evaluations[i].worker.PubKey < evaluations[j].worker.PubKey
	})
	return evaluations[0]
}

func (g *ContinuityGraph) evaluateCandidate(profile domain.ServiceContinuityProfile, policy domain.ReplicationPolicy, hasPolicy bool, candidate continuityStandbyCandidate) continuityCandidateEvaluation {
	missing := stringSet{}
	capabilities := workerCapabilitySet(candidate.worker)
	available := make([]domain.ContinuityMode, 0, len(profile.Profiles))
	replicationFreshness := ReplicationFreshnessUnknown

	for _, mode := range orderedAssessmentModes(profile.Profiles) {
		spec := profile.Profiles[mode]
		modeMissing := stringSet{}
		if !standbySupportsProfile(candidate.assignment, mode) {
			modeMissing.add("profile:" + string(mode))
		}
		for _, requirement := range spec.Requires {
			requirement = strings.TrimSpace(requirement)
			if requirement == "" {
				continue
			}
			if !capabilities.has(requirement) {
				modeMissing.add(requirement)
			}
		}
		freshness, replicationOK := g.replicationFreshnessForMode(policy, hasPolicy, candidate.worker.PubKey, mode)
		replicationFreshness = strongerReplicationFreshness(replicationFreshness, freshness)
		if !replicationOK {
			modeMissing.add(replicationDependencyName(freshness, candidate.worker.PubKey))
		}
		if candidate.health != StandbyHealthHealthy {
			modeMissing.add("standby_health:" + candidate.worker.PubKey)
		}
		if len(modeMissing) == 0 {
			available = append(available, mode)
			continue
		}
		for _, dependency := range modeMissing.values() {
			missing.add(dependency)
		}
	}

	return continuityCandidateEvaluation{
		continuityStandbyCandidate: candidate,
		availableProfiles:          available,
		missingDependencies:        missing,
		replicationFreshness:       replicationFreshness,
	}
}

func (g *ContinuityGraph) replicationFreshnessForMode(policy domain.ReplicationPolicy, hasPolicy bool, workerPubKey string, mode domain.ContinuityMode) (string, bool) {
	if !hasPolicy {
		return ReplicationFreshnessUnknown, false
	}
	for _, target := range policy.Targets {
		if strings.TrimSpace(target.WorkerPubKey) != workerPubKey {
			continue
		}
		if !replicationTargetSupportsMode(target, mode) {
			return ReplicationFreshnessUnknown, false
		}
		if target.MaxStaleness <= 0 || policy.UpdatedAt.IsZero() {
			return ReplicationFreshnessUnknown, false
		}
		if g.now().Sub(policy.UpdatedAt) > target.MaxStaleness {
			return ReplicationFreshnessStale, false
		}
		return ReplicationFreshnessFresh, true
	}
	return ReplicationFreshnessUnknown, false
}

func (g *ContinuityGraph) now() time.Time {
	if g.clock != nil {
		return g.clock()
	}
	return time.Now().UTC()
}

type continuityStandbyCandidate struct {
	worker     domain.Worker
	assignment domain.WorkerStandbyAssignment
	health     string
}

type continuityCandidateEvaluation struct {
	continuityStandbyCandidate
	availableProfiles    []domain.ContinuityMode
	missingDependencies  stringSet
	replicationFreshness string
}

type stringSet map[string]struct{}

func (s stringSet) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	s[value] = struct{}{}
}

func (s stringSet) values() []string {
	values := make([]string, 0, len(s))
	for value := range s {
		values = append(values, value)
	}
	return values
}

func standbyAssignmentForService(worker domain.Worker, serviceKey string) (domain.WorkerStandbyAssignment, bool) {
	serviceKey = strings.TrimSpace(serviceKey)
	for _, assignment := range worker.StandbyAssignments {
		if strings.TrimSpace(assignment.ServiceKey) == serviceKey {
			return assignment, true
		}
	}
	return domain.WorkerStandbyAssignment{}, false
}

func standbySupportsProfile(assignment domain.WorkerStandbyAssignment, mode domain.ContinuityMode) bool {
	for _, supported := range assignment.SupportedProfiles {
		if supported == mode {
			return true
		}
	}
	return false
}

func replicationTargetSupportsMode(target domain.ReplicationTarget, mode domain.ContinuityMode) bool {
	if len(target.RequiredForModes) == 0 {
		return true
	}
	for _, supported := range target.RequiredForModes {
		if supported == mode {
			return true
		}
	}
	return false
}

func replicationDependencyName(freshness string, workerPubKey string) string {
	workerPubKey = strings.TrimSpace(workerPubKey)
	if freshness == ReplicationFreshnessStale {
		return "replication_freshness:" + workerPubKey
	}
	return "replication_target:" + workerPubKey
}

func workerCapabilitySet(worker domain.Worker) continuityCapabilitySet {
	set := continuityCapabilitySet{values: map[string]struct{}{}}
	set.add(worker.Architecture)
	for _, value := range worker.Capabilities.WorkloadKinds {
		set.add(value)
	}
	for _, value := range worker.Capabilities.Runtimes {
		set.add(value)
	}
	for _, value := range worker.Capabilities.ArtifactFormats {
		set.add(value)
	}
	for _, value := range worker.Capabilities.Accelerators {
		set.add(value)
	}
	for _, value := range worker.Capabilities.Toolchains {
		set.add(value)
	}
	for _, value := range worker.Capabilities.Features {
		set.add(value)
	}
	for _, software := range worker.Software {
		set.add(software.Name)
	}
	for key, value := range worker.Labels {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		set.add(key + ":" + value)
		set.add(key + "=" + value)
	}
	return set
}

type continuityCapabilitySet struct {
	values map[string]struct{}
}

func (s continuityCapabilitySet) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	s.values[value] = struct{}{}
	s.values[strings.ToLower(value)] = struct{}{}
}

func (s continuityCapabilitySet) has(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	_, ok := s.values[value]
	if ok {
		return true
	}
	_, ok = s.values[strings.ToLower(value)]
	return ok
}

func orderedAssessmentModes(profiles map[domain.ContinuityMode]domain.ContinuityProfileSpec) []domain.ContinuityMode {
	preferred := []domain.ContinuityMode{domain.ContinuityModeFull, domain.ContinuityModeDegraded, domain.ContinuityModeEmergency}
	modes := make([]domain.ContinuityMode, 0, len(profiles))
	seen := map[domain.ContinuityMode]struct{}{}
	for _, mode := range preferred {
		if _, ok := profiles[mode]; ok {
			modes = append(modes, mode)
			seen[mode] = struct{}{}
		}
	}
	extra := make([]domain.ContinuityMode, 0)
	for mode := range profiles {
		if mode == domain.ContinuityModeOffline {
			continue
		}
		if _, ok := seen[mode]; !ok {
			extra = append(extra, mode)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return string(extra[i]) < string(extra[j]) })
	return append(modes, extra...)
}

func survivabilityForProfiles(profiles []domain.ContinuityMode) string {
	if containsContinuityMode(profiles, domain.ContinuityModeFull) {
		return SurvivabilitySurvivable
	}
	if containsContinuityMode(profiles, domain.ContinuityModeDegraded) {
		return SurvivabilityDegradedOnly
	}
	if containsContinuityMode(profiles, domain.ContinuityModeEmergency) {
		return SurvivabilityEmergencyOnly
	}
	return SurvivabilityUnsatisfied
}

func containsContinuityMode(values []domain.ContinuityMode, needle domain.ContinuityMode) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func continuityProfileSetRank(profiles []domain.ContinuityMode) int {
	switch survivabilityForProfiles(profiles) {
	case SurvivabilitySurvivable:
		return 3
	case SurvivabilityDegradedOnly:
		return 2
	case SurvivabilityEmergencyOnly:
		return 1
	default:
		return 0
	}
}

func continuityAssignmentProfileRank(assignment domain.WorkerStandbyAssignment) int {
	return continuityProfileSetRank(assignment.SupportedProfiles)
}

func continuityHealthRank(health string) int {
	switch health {
	case StandbyHealthHealthy:
		return 3
	case StandbyHealthStale:
		return 2
	case StandbyHealthOffline:
		return 1
	default:
		return 0
	}
}

func continuityReplicationRank(freshness string) int {
	switch freshness {
	case ReplicationFreshnessFresh:
		return 2
	case ReplicationFreshnessStale:
		return 1
	default:
		return 0
	}
}

func continuityTierRank(tier domain.StandbyTier) int {
	switch tier {
	case domain.StandbyTierHot:
		return 3
	case domain.StandbyTierWarm:
		return 2
	case domain.StandbyTierCold:
		return 1
	default:
		return 0
	}
}

func strongerReplicationFreshness(current string, incoming string) string {
	if continuityReplicationRank(incoming) > continuityReplicationRank(current) {
		return incoming
	}
	return current
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
