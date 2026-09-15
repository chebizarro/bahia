package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// LegacyAgentReconciliationInputSchemaV1 identifies the secret-free, read-only
// snapshot schema consumed by the reconciliation classifier.
const LegacyAgentReconciliationInputSchemaV1 = "soulfactory-legacy-agent-reconciliation-input/v1"

const (
	legacyAgentReconciliationReportSchema  = "soulfactory-legacy-agent-reconciliation-report/v1"
	legacyAgentReconciliationReceiptSchema = "soulfactory-legacy-agent-reconciliation-receipt/v1"

	// legacyAgentReconcileActionLink is the only mutation the reconciliation
	// write path is authorized to perform: attach an already-existing Soul to
	// exactly one Bahia service without reprovisioning.
	LegacyAgentReconcileActionLink = "link_existing_soul_to_bahia_service"

	legacyReconcileStatusLinked    = "linked"
	legacyReconcileStatusUnlinked  = "unlinked"
	legacyReconcileStatusAmbiguous = "ambiguous"
	legacyReconcileStatusOrphaned  = "orphaned"
)

var (
	// ErrLegacyReconciliationAmbiguous is returned alongside a complete report
	// when any active Soul is ambiguous, so callers can refuse the workflow.
	ErrLegacyReconciliationAmbiguous = errors.New("legacy agent reconciliation classification is ambiguous")
	// ErrLegacyReconciliationRefused is returned when a link write is attempted
	// without approval or for a Soul that is not eligible to be linked.
	ErrLegacyReconciliationRefused = errors.New("legacy agent reconciliation refused")
)

// LegacyAgentReconciliationInput is a secret-free snapshot of existing Souls,
// running runtimes, and Bahia services. Collection and mutation are deliberately
// outside this classifier; the classifier only previews.
type LegacyAgentReconciliationInput struct {
	Schema            string                             `json:"schema"`
	Souls             []LegacyReconcileSoul              `json:"souls"`
	RuntimeAgents     []LegacyRunningAgent               `json:"running_agents"`
	Services          []LegacyReconcileService           `json:"services"`
	Approvals         []LegacyReconcileApproval          `json:"approvals,omitempty"`
	ReviewedPlacement map[string]LegacyReviewedPlacement `json:"operator_reviewed_placement,omitempty"`
}

// LegacyReviewedPlacement is the operator-reviewed Bahia placement for the
// already-running runtime. Reconciliation records it but never starts or moves
// the runtime.
type LegacyReviewedPlacement struct {
	Ref               string `json:"ref"`
	EnvironmentID     string `json:"environment_id"`
	DeploymentUnitKey string `json:"deployment_unit_key,omitempty"`
}

// LegacyReconcileSoul is the read-only projection of one kind:31951 Soul.
type LegacyReconcileSoul struct {
	EventID        string `json:"event_id"`
	AgentID        string `json:"agent_id"`
	Name           string `json:"name,omitempty"`
	Status         string `json:"status"`
	AgentPubkey    string `json:"agent_pubkey,omitempty"`
	RuntimeBinding string `json:"runtime_binding,omitempty"`
	RuntimeState   string `json:"runtime_state,omitempty"`
	RuntimeTarget  string `json:"runtime_target,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
	PersonaRef     string `json:"persona_ref,omitempty"`
	CustodyRef     string `json:"custody_ref,omitempty"`
	BahiaServiceID string `json:"bahia_service_id,omitempty"`
	AllowedKinds   []int  `json:"allowed_kinds,omitempty"`
	SourceRef      string `json:"source_ref"`
}

// LegacyReconcileService is the read-only projection of one Bahia service.
type LegacyReconcileService struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	ArtifactRepo  string                       `json:"artifact_repo"`
	RuntimeType   string                       `json:"runtime_type,omitempty"`
	SourceRef     string                       `json:"source_ref"`
	RuntimeConfig *domain.ServiceRuntimeConfig `json:"runtime_config,omitempty"`
}

// LegacyReconcileApproval is the explicit operator authorization required
// before any write. It must name the exact agent identity and action.
type LegacyReconcileApproval struct {
	AgentID         string          `json:"agent_id"`
	Action          string          `json:"action"`
	SoulEventID     string          `json:"soul_event_id"`
	SoulContentHash string          `json:"soul_content_hash"`
	ApprovedBy      string          `json:"approved_by"`
	ApprovalRef     string          `json:"approval_ref"`
	Principal       *auth.Principal `json:"-"`
}

// LegacyAgentReconciliationReport is the dry-run preview. It never mutates.
type LegacyAgentReconciliationReport struct {
	Schema          string                                    `json:"schema"`
	ReadOnly        bool                                      `json:"read_only"`
	MutationAllowed bool                                      `json:"mutation_allowed"`
	Classifications []LegacyAgentReconciliationClassification `json:"classifications"`
	Summary         LegacyAgentReconciliationSummary          `json:"summary"`
}

// LegacyAgentReconciliationSummary counts Souls by reconciliation state.
type LegacyAgentReconciliationSummary struct {
	Active    int `json:"active"`
	Linked    int `json:"linked"`
	Unlinked  int `json:"unlinked"`
	Ambiguous int `json:"ambiguous"`
	Orphaned  int `json:"orphaned"`
}

// LegacyAgentReconciliationClassification is the per-Soul reconciliation state.
type LegacyAgentReconciliationClassification struct {
	AgentID             string               `json:"agent_id"`
	SoulEventID         string               `json:"soul_event_id"`
	SoulContentHash     string               `json:"soul_content_hash,omitempty"`
	Status              string               `json:"status"`
	ReasonCode          string               `json:"reason_code"`
	ExistingServiceID   string               `json:"existing_service_id,omitempty"`
	CandidateRuntimeIDs []string             `json:"candidate_runtime_ids,omitempty"`
	CandidateServiceIDs []string             `json:"candidate_service_ids,omitempty"`
	MatchedRuntimeIDs   []string             `json:"matched_runtime_ids,omitempty"`
	MatchedEvidence     []string             `json:"matched_evidence,omitempty"`
	Plan                *LegacyReconcilePlan `json:"reconcile_plan,omitempty"`
}

// LegacyReconcilePlan is the operator-facing plan for an unlinked Soul.
type LegacyReconcilePlan struct {
	Action              string                  `json:"action"`
	OperatorApproval    string                  `json:"operator_approval_required"`
	AgentID             string                  `json:"agent_id"`
	ServiceName         string                  `json:"service_name"`
	ServiceArtifactRepo string                  `json:"service_artifact_repo"`
	ReuseServiceID      string                  `json:"reuse_service_id,omitempty"`
	Placement           LegacyReviewedPlacement `json:"operator_reviewed_placement"`
	PreservePubkey      string                  `json:"preserve_managed_pubkey,omitempty"`
	PreserveRuntime     string                  `json:"preserve_runtime_binding,omitempty"`
	PreserveWorkspace   string                  `json:"preserve_workspace,omitempty"`
	PreservePersonaRef  string                  `json:"preserve_persona_ref,omitempty"`
	CustodyEvidenceRef  string                  `json:"custody_evidence_ref,omitempty"`
	MatchedRuntimeIDs   []string                `json:"matched_runtime_ids"`
	IdentitySourceRefs  []string                `json:"identity_source_refs"`
	ProhibitedActions   []string                `json:"prohibited_actions"`
	Rollback            LegacyReconcileRollback `json:"rollback"`
}

// LegacyReconcileRollback describes the deterministic reversal of a link.
type LegacyReconcileRollback struct {
	PreviousSoulEventID string   `json:"previous_soul_event_id"`
	CreatedServiceID    string   `json:"created_service_id,omitempty"`
	Steps               []string `json:"steps"`
}

// LegacyAgentReconcileReceipt is returned by an approved link write.
type LegacyAgentReconcileReceipt struct {
	Schema                  string                  `json:"schema"`
	AgentID                 string                  `json:"agent_id"`
	Action                  string                  `json:"action"`
	NoOp                    bool                    `json:"no_op"`
	ServiceCreated          bool                    `json:"service_created"`
	ServiceID               uuid.UUID               `json:"service_id"`
	DeploymentUnitID        uuid.UUID               `json:"deployment_unit_id"`
	RuntimeAdopted          bool                    `json:"runtime_adopted"`
	SupersedingSoulEventID  string                  `json:"superseding_soul_event_id,omitempty"`
	PreviousSoulEventID     string                  `json:"previous_soul_event_id,omitempty"`
	PreservedRuntimeBinding string                  `json:"preserved_runtime_binding,omitempty"`
	PreservedAgentPubkey    string                  `json:"preserved_agent_pubkey,omitempty"`
	Rollback                LegacyReconcileRollback `json:"rollback"`
}

// ClassifyLegacyAgentReconciliation builds a deterministic dry-run preview of
// existing active Souls that lack a Bahia service link. If any Soul is
// ambiguous it returns the complete report together with
// ErrLegacyReconciliationAmbiguous so callers can refuse the write workflow.
func ClassifyLegacyAgentReconciliation(input LegacyAgentReconciliationInput) (LegacyAgentReconciliationReport, error) {
	if err := validateLegacyReconciliationInput(input); err != nil {
		return LegacyAgentReconciliationReport{}, err
	}

	souls := append([]LegacyReconcileSoul(nil), input.Souls...)
	sort.Slice(souls, func(i, j int) bool { return souls[i].AgentID < souls[j].AgentID })
	agents := append([]LegacyRunningAgent(nil), input.RuntimeAgents...)
	sort.Slice(agents, func(i, j int) bool { return agents[i].InventoryID < agents[j].InventoryID })
	services := append([]LegacyReconcileService(nil), input.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })

	report := LegacyAgentReconciliationReport{
		Schema:          legacyAgentReconciliationReportSchema,
		ReadOnly:        true,
		MutationAllowed: false,
	}
	ambiguous := false
	for _, soul := range souls {
		if !isLegacyReconcileActive(soul.Status) {
			continue
		}
		report.Summary.Active++
		classification := classifyLegacyReconcileSoul(soul, agents, services, input.ReviewedPlacement)
		report.Classifications = append(report.Classifications, classification)
		switch classification.Status {
		case legacyReconcileStatusLinked:
			report.Summary.Linked++
		case legacyReconcileStatusUnlinked:
			report.Summary.Unlinked++
		case legacyReconcileStatusAmbiguous:
			report.Summary.Ambiguous++
			ambiguous = true
		case legacyReconcileStatusOrphaned:
			report.Summary.Orphaned++
		}
	}
	if ambiguous {
		return report, ErrLegacyReconciliationAmbiguous
	}
	return report, nil
}

func classifyLegacyReconcileSoul(soul LegacyReconcileSoul, agents []LegacyRunningAgent, services []LegacyReconcileService, placement map[string]LegacyReviewedPlacement) LegacyAgentReconciliationClassification {
	classification := LegacyAgentReconciliationClassification{AgentID: soul.AgentID, SoulEventID: soul.EventID}

	var linkedService *LegacyReconcileService
	if strings.TrimSpace(soul.BahiaServiceID) != "" {
		linkedService = findLegacyServiceByID(services, soul.BahiaServiceID)
		if linkedService == nil {
			classification.Status = legacyReconcileStatusOrphaned
			classification.ReasonCode = "missing_service_reference"
			return classification
		}
		if linkedService.Name != soulServiceName(soul.AgentID) || linkedService.ArtifactRepo != soulServiceArtifactRepo(soul.AgentID) {
			classification.Status = legacyReconcileStatusAmbiguous
			classification.ReasonCode = "linked_service_identity_mismatch"
			classification.CandidateServiceIDs = []string{linkedService.ID}
			return classification
		}
	}

	nameMatches, conflicts := findLegacyServicesByName(services, soul.AgentID)
	if linkedService == nil && len(conflicts) > 0 {
		classification.Status = legacyReconcileStatusAmbiguous
		classification.ReasonCode = "service_identity_conflict"
		classification.CandidateServiceIDs = conflicts
		return classification
	}
	var reuseService *LegacyReconcileService
	if linkedService == nil {
		switch len(nameMatches) {
		case 0:
		case 1:
			reuseService = &nameMatches[0]
		default:
			classification.Status = legacyReconcileStatusAmbiguous
			classification.ReasonCode = "multiple_service_matches"
			for _, svc := range nameMatches {
				classification.CandidateServiceIDs = append(classification.CandidateServiceIDs, svc.ID)
			}
			return classification
		}
	}

	matches, runtimeConflicts := legacyReconcileRuntimeMatches(soul, agents)
	if len(runtimeConflicts) > 0 {
		classification.Status = legacyReconcileStatusAmbiguous
		classification.ReasonCode = "identity_evidence_conflict"
		classification.CandidateRuntimeIDs = runtimeConflicts
		return classification
	}
	if len(matches) > 1 {
		classification.Status = legacyReconcileStatusAmbiguous
		classification.ReasonCode = "multiple_authoritative_matches"
		for _, match := range matches {
			classification.CandidateRuntimeIDs = append(classification.CandidateRuntimeIDs, match.agent.InventoryID)
		}
		return classification
	}
	if len(matches) == 0 {
		classification.Status = legacyReconcileStatusOrphaned
		classification.ReasonCode = "no_runtime_match"
		if linkedService != nil {
			classification.ExistingServiceID = linkedService.ID
		} else if reuseService != nil {
			classification.ExistingServiceID = reuseService.ID
		}
		return classification
	}

	match := matches[0]
	classification.MatchedRuntimeIDs = []string{match.agent.InventoryID}
	classification.MatchedEvidence = match.fields
	if linkedService != nil {
		classification.ExistingServiceID = linkedService.ID
		if !legacyServiceAdoptsRuntime(*linkedService, match.agent) {
			classification.Status = legacyReconcileStatusOrphaned
			classification.ReasonCode = "linked_service_runtime_mismatch"
			return classification
		}
		classification.Status = legacyReconcileStatusLinked
		classification.ReasonCode = "service_link_and_runtime_adoption_verified"
	} else {
		classification.Status = legacyReconcileStatusUnlinked
		classification.ReasonCode = "single_authoritative_runtime_match_requires_operator_approval"
		if reuseService != nil {
			classification.ExistingServiceID = reuseService.ID
		}
		classification.Plan = buildLegacyReconcilePlan(soul, match, reuseService, placement)
	}
	classification.SoulContentHash = legacyClassificationContentHash(soul, classification.MatchedRuntimeIDs, classification.MatchedEvidence, placement[soul.AgentID], agents)
	return classification
}

func legacyServiceAdoptsRuntime(service LegacyReconcileService, runtime LegacyRunningAgent) bool {
	return runtime.AdoptedRuntime != nil &&
		service.RuntimeType == string(runtime.RuntimeType) &&
		service.RuntimeConfig != nil &&
		reflect.DeepEqual(service.RuntimeConfig.Adopted, runtime.AdoptedRuntime)
}

func legacyClassificationContentHash(soul LegacyReconcileSoul, runtimeIDs, evidence []string, placement LegacyReviewedPlacement, runtimeAgents []LegacyRunningAgent) string {
	payload := struct {
		AgentID           string                  `json:"agent_id"`
		ManagedPubkey     string                  `json:"managed_pubkey"`
		RuntimeBinding    string                  `json:"runtime_binding"`
		RuntimeState      string                  `json:"runtime_state"`
		RuntimeTarget     string                  `json:"runtime_target"`
		Workspace         string                  `json:"workspace"`
		PersonaRef        string                  `json:"persona_ref"`
		CustodyRef        string                  `json:"custody_ref"`
		AllowedKinds      []int                   `json:"allowed_kinds"`
		MatchedRuntimeIDs []string                `json:"matched_runtime_ids"`
		MatchedEvidence   []string                `json:"matched_evidence"`
		Placement         LegacyReviewedPlacement `json:"operator_reviewed_placement"`
		RuntimeEvidence   []LegacyRunningAgent    `json:"runtime_evidence"`
	}{
		AgentID: soul.AgentID, ManagedPubkey: soul.AgentPubkey, RuntimeBinding: soul.RuntimeBinding,
		RuntimeState: soul.RuntimeState, RuntimeTarget: soul.RuntimeTarget, Workspace: soul.Workspace,
		PersonaRef: soul.PersonaRef, CustodyRef: soul.CustodyRef, AllowedKinds: append([]int(nil), soul.AllowedKinds...),
		MatchedRuntimeIDs: sortedCopy(runtimeIDs), MatchedEvidence: sortedCopy(evidence), Placement: placement,
	}
	wanted := make(map[string]struct{}, len(runtimeIDs))
	for _, id := range runtimeIDs {
		wanted[id] = struct{}{}
	}
	for _, runtime := range runtimeAgents {
		if _, ok := wanted[runtime.InventoryID]; ok {
			payload.RuntimeEvidence = append(payload.RuntimeEvidence, runtime)
		}
	}
	sort.Slice(payload.RuntimeEvidence, func(i, j int) bool {
		return payload.RuntimeEvidence[i].InventoryID < payload.RuntimeEvidence[j].InventoryID
	})
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type legacyReconcileRuntimeMatch struct {
	agent  LegacyRunningAgent
	fields []string
}

func legacyReconcileRuntimeMatches(soul LegacyReconcileSoul, agents []LegacyRunningAgent) ([]legacyReconcileRuntimeMatch, []string) {
	identity := LegacyIdentityRecord{
		AgentID:        soul.AgentID,
		ManagedPubkey:  soul.AgentPubkey,
		RuntimeBinding: soul.RuntimeBinding,
		Workspace:      soul.Workspace,
		PersonaRef:     soul.PersonaRef,
		CustodyRef:     soul.CustodyRef,
		DisplayName:    soul.Name,
		SourceRefs:     []string{soul.SourceRef},
	}
	var matches []legacyReconcileRuntimeMatch
	var conflicts []string
	for _, agent := range agents {
		if !agent.Running {
			continue
		}
		fields, agentConflicts := compareLegacyIdentityEvidence(agent, identity)
		if len(agentConflicts) > 0 && isPlausibleLegacyConflict(fields, agentConflicts) {
			conflicts = append(conflicts, agent.InventoryID)
			continue
		}
		if len(agentConflicts) == 0 && isAuthoritativeLegacyMatch(fields) {
			matches = append(matches, legacyReconcileRuntimeMatch{agent: agent, fields: fields})
		}
	}
	return matches, conflicts
}

func buildLegacyReconcilePlan(soul LegacyReconcileSoul, match legacyReconcileRuntimeMatch, reuse *LegacyReconcileService, placement map[string]LegacyReviewedPlacement) *LegacyReconcilePlan {
	rollback := LegacyReconcileRollback{
		PreviousSoulEventID: soul.EventID,
		Steps: []string{
			"Republish the exact prior kind:31951 read model (event " + soul.EventID + ") as a superseding event that omits the service tag; this restores the unlinked read model.",
			"Do not stop, restart, redeploy, or re-key the running runtime; do not mutate grants, mounts, or custody.",
		},
	}
	if reuse != nil {
		rollback.Steps = append(rollback.Steps, "Bahia service "+reuse.ID+" was reused, not created; no service teardown is required.")
	} else {
		rollback.Steps = append(rollback.Steps, "Mark the newly created Bahia service "+soulServiceName(soul.AgentID)+" as unused for operator cleanup; do not delete it while environment state references it.")
	}

	plan := &LegacyReconcilePlan{
		Action:              LegacyAgentReconcileActionLink,
		OperatorApproval:    "Explicitly name this agent identity and this exact action; classification alone performs no mutation.",
		AgentID:             soul.AgentID,
		ServiceName:         soulServiceName(soul.AgentID),
		ServiceArtifactRepo: soulServiceArtifactRepo(soul.AgentID),
		PreservePubkey:      soul.AgentPubkey,
		PreserveRuntime:     soul.RuntimeBinding,
		PreserveWorkspace:   soul.Workspace,
		PreservePersonaRef:  soul.PersonaRef,
		CustodyEvidenceRef:  soul.CustodyRef,
		MatchedRuntimeIDs:   []string{match.agent.InventoryID},
		IdentitySourceRefs:  sortedCopy([]string{soul.SourceRef, match.agent.SourceRef}),
		ProhibitedActions: []string{
			"key_generation",
			"key_rotation",
			"key_revocation",
			"identity_replacement",
			"identity_rekey",
			"acl_or_grant_mutation",
			"custody_mutation",
			"mount_mutation",
			"runtime_restart_or_redeploy",
			"deployment_intent_publish",
		},
		Rollback: rollback,
	}
	if reuse != nil {
		plan.ReuseServiceID = reuse.ID
	}
	if placement != nil {
		plan.Placement = placement[soul.AgentID]
	}
	return plan
}

func findLegacyServiceByID(services []LegacyReconcileService, id string) *LegacyReconcileService {
	normalized := normalizeLegacyEvidence(id)
	for i := range services {
		if normalizeLegacyEvidence(services[i].ID) == normalized {
			return &services[i]
		}
	}
	return nil
}

func findLegacyServicesByName(services []LegacyReconcileService, agentID string) ([]LegacyReconcileService, []string) {
	name := soulServiceName(agentID)
	repo := soulServiceArtifactRepo(agentID)
	var matches []LegacyReconcileService
	var conflicts []string
	for _, svc := range services {
		if svc.Name != name {
			continue
		}
		if svc.ArtifactRepo != repo {
			conflicts = append(conflicts, svc.ID)
			continue
		}
		matches = append(matches, svc)
	}
	return matches, conflicts
}

// LegacyServiceRegistrar is satisfied by *BahiaIntegration.
type LegacyServiceRegistrar interface {
	RegisterSoulAsService(ctx context.Context, soul *domain.AgentSoul) (uuid.UUID, error)
}

// LegacyServiceStore is satisfied by *service.RegistryService.
type LegacyServiceStore interface {
	GetServiceByName(ctx context.Context, name string) (*domain.Service, error)
	ListServices(ctx context.Context) ([]domain.Service, error)
	UpdateService(ctx context.Context, service *domain.Service) error
}

// LegacyDeploymentUnitStore is satisfied by repository.DeploymentUnitRepository.
type LegacyDeploymentUnitStore interface {
	Create(ctx context.Context, unit *domain.DeploymentUnit) error
	GetByEnvironmentKey(ctx context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error)
}

// LegacySoulSource returns the current authoritative replaceable Soul event.
type LegacySoulSource interface {
	GetSoul(ctx context.Context, agentID string) (*domain.AgentSoul, error)
}

// LegacySoulLinkPublisher is satisfied by *Reactor.
type LegacySoulLinkPublisher interface {
	PublishSoul(ctx context.Context, soul *domain.AgentSoul) error
}

// LegacyAgentReconciliationRequest is the authenticated dry-run request.
type LegacyAgentReconciliationRequest struct {
	AgentID           string                  `json:"agent_id"`
	RuntimeAgents     []LegacyRunningAgent    `json:"running_agents"`
	ReviewedPlacement LegacyReviewedPlacement `json:"operator_reviewed_placement"`
}

// LegacyAgentReconcileApplyRequest is dry-run-first: apply must present the
// exact classification returned by Preview plus the same runtime evidence and
// reviewed placement.
type LegacyAgentReconcileApplyRequest struct {
	LegacyAgentReconciliationRequest
	Classification LegacyAgentReconciliationClassification `json:"classification"`
}

// LegacyAgentReconciler performs the authenticated write side of reconciliation.
// It records the already-running runtime but never starts, stops, or changes it.
type LegacyAgentReconciler struct {
	registrar LegacyServiceRegistrar
	services  LegacyServiceStore
	units     LegacyDeploymentUnitStore
	souls     LegacySoulSource
	publisher LegacySoulLinkPublisher
	logger    *slog.Logger
}

// NewLegacyAgentReconciler builds the production-capable reconciler. All
// dependencies are required so the supported surface fails closed.
func NewLegacyAgentReconciler(registrar LegacyServiceRegistrar, services LegacyServiceStore, units LegacyDeploymentUnitStore, souls LegacySoulSource, publisher LegacySoulLinkPublisher, logger *slog.Logger) (*LegacyAgentReconciler, error) {
	if registrar == nil || services == nil || units == nil || souls == nil || publisher == nil {
		return nil, fmt.Errorf("legacy agent reconciler requires registrar, service store, deployment units, authoritative soul source, and soul publisher")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LegacyAgentReconciler{registrar: registrar, services: services, units: units, souls: souls, publisher: publisher, logger: logger}, nil
}

// Preview reloads the authoritative Soul and Bahia services before classifying.
func (r *LegacyAgentReconciler) Preview(ctx context.Context, request LegacyAgentReconciliationRequest) (LegacyAgentReconciliationReport, error) {
	_, report, err := r.loadCurrentClassification(ctx, request)
	return report, err
}

func (r *LegacyAgentReconciler) loadCurrentClassification(ctx context.Context, request LegacyAgentReconciliationRequest) (*domain.AgentSoul, LegacyAgentReconciliationReport, error) {
	agentID := strings.TrimSpace(request.AgentID)
	if agentID == "" {
		return nil, LegacyAgentReconciliationReport{}, fmt.Errorf("%w: agent_id is required", ErrLegacyReconciliationRefused)
	}
	soul, err := r.souls.GetSoul(ctx, agentID)
	if err != nil {
		return nil, LegacyAgentReconciliationReport{}, fmt.Errorf("load current authoritative soul: %w", err)
	}
	if soul == nil || soul.AgentID != agentID {
		return nil, LegacyAgentReconciliationReport{}, fmt.Errorf("%w: current authoritative Soul not found", ErrLegacyReconciliationRefused)
	}
	services, err := r.services.ListServices(ctx)
	if err != nil {
		return nil, LegacyAgentReconciliationReport{}, fmt.Errorf("list Bahia services: %w", err)
	}
	input := LegacyAgentReconciliationInput{
		Schema:            LegacyAgentReconciliationInputSchemaV1,
		Souls:             []LegacyReconcileSoul{legacySoulProjection(soul)},
		RuntimeAgents:     append([]LegacyRunningAgent(nil), request.RuntimeAgents...),
		ReviewedPlacement: map[string]LegacyReviewedPlacement{agentID: request.ReviewedPlacement},
	}
	for i := range services {
		input.Services = append(input.Services, LegacyReconcileService{
			ID: services[i].ID.String(), Name: services[i].Name, ArtifactRepo: services[i].ArtifactRepo,
			RuntimeType: string(services[i].RuntimeType), RuntimeConfig: cloneServiceRuntimeConfig(services[i].RuntimeConfig),
			SourceRef: "bahia-service:" + services[i].ID.String(),
		})
	}
	report, err := ClassifyLegacyAgentReconciliation(input)
	return soul, report, err
}

func legacySoulProjection(soul *domain.AgentSoul) LegacyReconcileSoul {
	projection := LegacyReconcileSoul{
		EventID: soul.EventID, AgentID: soul.AgentID, Name: soul.Name, Status: string(soul.Status),
		AgentPubkey: soul.NostrPubkey, RuntimeBinding: soul.Runtime.RuntimeBinding, RuntimeState: soul.Runtime.State,
		RuntimeTarget: string(soul.Runtime.Target), Workspace: soul.WorkspaceRepoURL, PersonaRef: "",
		AllowedKinds: append([]int(nil), soul.AllowedKinds...), SourceRef: "soul-event:" + soul.EventID,
	}
	if soul.BahiaServiceID != nil {
		projection.BahiaServiceID = soul.BahiaServiceID.String()
	}
	return projection
}

// ReconcileApprovedLink reloads and reclassifies the current authoritative Soul,
// refuses stale/fabricated previews, records the exact runtime as adopted in one
// observe-only deployment unit, then publishes a Soul that only adds the link.
func (r *LegacyAgentReconciler) ReconcileApprovedLink(ctx context.Context, request LegacyAgentReconcileApplyRequest, approval LegacyReconcileApproval) (LegacyAgentReconcileReceipt, error) {
	receipt := LegacyAgentReconcileReceipt{Schema: legacyAgentReconciliationReceiptSchema, AgentID: request.AgentID, Action: LegacyAgentReconcileActionLink}
	soul, report, err := r.loadCurrentClassification(ctx, request.LegacyAgentReconciliationRequest)
	if err != nil && !errors.Is(err, ErrLegacyReconciliationAmbiguous) {
		return receipt, err
	}
	if len(report.Classifications) != 1 {
		return receipt, fmt.Errorf("%w: current classification unavailable", ErrLegacyReconciliationRefused)
	}
	current := report.Classifications[0]
	if request.Classification.SoulEventID != soul.EventID || request.Classification.SoulEventID != current.SoulEventID ||
		request.Classification.SoulContentHash == "" || request.Classification.SoulContentHash != current.SoulContentHash ||
		request.Classification.AgentID != current.AgentID || request.Classification.Status != current.Status ||
		!reflect.DeepEqual(request.Classification.MatchedRuntimeIDs, current.MatchedRuntimeIDs) ||
		!reflect.DeepEqual(request.Classification.MatchedEvidence, current.MatchedEvidence) {
		return receipt, fmt.Errorf("%w: classification is stale or does not describe the current Soul and runtime evidence", ErrLegacyReconciliationRefused)
	}
	if err := validateLegacyReconcileApproval(approval, current); err != nil {
		return receipt, err
	}
	if current.Status == legacyReconcileStatusAmbiguous || current.Status == legacyReconcileStatusOrphaned {
		return receipt, fmt.Errorf("%w: %s classification %q", ErrLegacyReconciliationRefused, current.Status, current.ReasonCode)
	}
	if soul.Status != domain.SoulStatusActive {
		return receipt, fmt.Errorf("%w: soul status %q is not active", ErrLegacyReconciliationRefused, soul.Status)
	}
	matched, err := matchedLegacyRuntime(request.RuntimeAgents, current)
	if err != nil {
		return receipt, err
	}

	name := soulServiceName(soul.AgentID)
	existing, err := r.services.GetServiceByName(ctx, name)
	if err != nil {
		return receipt, fmt.Errorf("look up existing service: %w", err)
	}
	serviceID, err := r.registrar.RegisterSoulAsService(ctx, soul)
	if err != nil {
		return receipt, fmt.Errorf("register soul as service: %w", err)
	}
	confirmed, err := r.services.GetServiceByName(ctx, name)
	if err != nil || confirmed == nil || confirmed.ID != serviceID {
		return receipt, fmt.Errorf("confirm service %q after registration: %w", name, err)
	}
	if confirmed.Name != name || confirmed.ArtifactRepo != soulServiceArtifactRepo(soul.AgentID) {
		return receipt, fmt.Errorf("%w: service identity does not match current Soul", ErrLegacyReconciliationRefused)
	}
	if err := r.ensureExactAdoptedService(ctx, confirmed, matched); err != nil {
		return receipt, err
	}
	unit, err := r.ensureExactAdoptedUnit(ctx, confirmed, matched, request.ReviewedPlacement)
	if err != nil {
		return receipt, err
	}

	if soul.BahiaServiceID != nil {
		if *soul.BahiaServiceID != serviceID || current.Status != legacyReconcileStatusLinked {
			return receipt, fmt.Errorf("%w: existing Soul link is not fully verified", ErrLegacyReconciliationRefused)
		}
		receipt.NoOp = true
		receipt.ServiceID = serviceID
		receipt.DeploymentUnitID = unit.ID
		receipt.RuntimeAdopted = true
		receipt.PreviousSoulEventID = soul.EventID
		receipt.PreservedRuntimeBinding = soul.Runtime.RuntimeBinding
		receipt.PreservedAgentPubkey = soul.NostrPubkey
		return receipt, nil
	}

	previousEventID := soul.EventID
	linkedSoul := cloneLegacySoul(soul)
	before := snapshotLegacySoulIdentity(linkedSoul)
	linkedSoul.BahiaServiceID = &serviceID
	if !reflect.DeepEqual(before, snapshotLegacySoulIdentity(linkedSoul)) {
		return receipt, fmt.Errorf("reconciliation mutated preserved soul identity fields")
	}
	latestSoul, err := r.souls.GetSoul(ctx, soul.AgentID)
	if err != nil {
		return receipt, fmt.Errorf("recheck authoritative Soul before publish: %w", err)
	}
	if latestSoul == nil || latestSoul.EventID != previousEventID || legacyClassificationContentHash(
		legacySoulProjection(latestSoul), current.MatchedRuntimeIDs, current.MatchedEvidence, request.ReviewedPlacement, request.RuntimeAgents,
	) != current.SoulContentHash {
		return receipt, fmt.Errorf("%w: Soul moved after classification and before publish", ErrLegacyReconciliationRefused)
	}
	if err := r.publisher.PublishSoul(ctx, linkedSoul); err != nil {
		return receipt, fmt.Errorf("publish superseding soul link: %w", err)
	}

	receipt.ServiceCreated = existing == nil
	receipt.ServiceID = serviceID
	receipt.DeploymentUnitID = unit.ID
	receipt.RuntimeAdopted = true
	receipt.PreviousSoulEventID = previousEventID
	receipt.SupersedingSoulEventID = linkedSoul.EventID
	receipt.PreservedRuntimeBinding = linkedSoul.Runtime.RuntimeBinding
	receipt.PreservedAgentPubkey = linkedSoul.NostrPubkey
	receipt.Rollback = legacyReconcileRollback(previousEventID, existing == nil, serviceID, soul.AgentID)
	r.logger.Info("reconciled legacy soul into Bahia without runtime mutation", "agent_id", soul.AgentID, "service_id", serviceID, "deployment_unit_id", unit.ID)
	return receipt, nil
}

func matchedLegacyRuntime(agents []LegacyRunningAgent, classification LegacyAgentReconciliationClassification) (LegacyRunningAgent, error) {
	if len(classification.MatchedRuntimeIDs) != 1 {
		return LegacyRunningAgent{}, fmt.Errorf("%w: exactly one matched runtime is required", ErrLegacyReconciliationRefused)
	}
	for _, agent := range agents {
		if agent.InventoryID == classification.MatchedRuntimeIDs[0] {
			if agent.AdoptedRuntime == nil || agent.RuntimeType == "" ||
				strings.TrimSpace(agent.AdoptedRuntime.TargetName) == "" ||
				strings.TrimSpace(agent.AdoptedRuntime.SourceRuntime) == "" ||
				strings.TrimSpace(agent.AdoptedRuntime.HostAlias) == "" {
				return LegacyRunningAgent{}, fmt.Errorf("%w: matched runtime lacks exact adoption evidence", ErrLegacyReconciliationRefused)
			}
			if agent.ContainerName != "" && agent.AdoptedRuntime.TargetName != agent.ContainerName {
				return LegacyRunningAgent{}, fmt.Errorf("%w: adopted target does not match authoritative runtime name", ErrLegacyReconciliationRefused)
			}
			if err := domain.ValidateRuntimeType(agent.RuntimeType); err != nil {
				return LegacyRunningAgent{}, fmt.Errorf("%w: invalid adopted runtime type: %v", ErrLegacyReconciliationRefused, err)
			}
			return agent, nil
		}
	}
	return LegacyRunningAgent{}, fmt.Errorf("%w: matched runtime evidence is unavailable", ErrLegacyReconciliationRefused)
}

func (r *LegacyAgentReconciler) ensureExactAdoptedService(ctx context.Context, service *domain.Service, runtime LegacyRunningAgent) error {
	desired := cloneAdoptedRuntime(runtime.AdoptedRuntime)
	if service.RuntimeConfig != nil && service.RuntimeConfig.Adopted != nil {
		if service.RuntimeType != runtime.RuntimeType || !reflect.DeepEqual(service.RuntimeConfig.Adopted, desired) {
			return fmt.Errorf("%w: existing service adoption does not match the exact runtime", ErrLegacyReconciliationRefused)
		}
		return nil
	}
	service.RuntimeType = runtime.RuntimeType
	service.RuntimeConfig = &domain.ServiceRuntimeConfig{Adopted: desired}
	if err := r.services.UpdateService(ctx, service); err != nil {
		return fmt.Errorf("record exact adopted runtime on service: %w", err)
	}
	return nil
}

func (r *LegacyAgentReconciler) ensureExactAdoptedUnit(ctx context.Context, service *domain.Service, runtime LegacyRunningAgent, placement LegacyReviewedPlacement) (*domain.DeploymentUnit, error) {
	environmentID, err := uuid.Parse(strings.TrimSpace(placement.EnvironmentID))
	if err != nil || strings.TrimSpace(placement.Ref) == "" {
		return nil, fmt.Errorf("%w: authenticated operator-reviewed placement requires ref and environment_id", ErrLegacyReconciliationRefused)
	}
	key := strings.TrimSpace(placement.DeploymentUnitKey)
	if key == "" {
		key = service.Name
	}
	runtimeConfig := map[string]any{
		"service_id": service.ID.String(), "inventory_id": runtime.InventoryID, "runtime_binding": runtime.RuntimeBinding,
		"managed_pubkey": runtime.ManagedPubkey, "source_ref": runtime.SourceRef,
		"target_name": runtime.AdoptedRuntime.TargetName, "container_id": runtime.AdoptedRuntime.ContainerID,
		"image_digest": runtime.AdoptedRuntime.ImageDigest, "placement_ref": placement.Ref,
	}
	existing, err := r.units.GetByEnvironmentKey(ctx, environmentID, key)
	if err != nil {
		return nil, fmt.Errorf("load adopted deployment unit: %w", err)
	}
	if existing != nil {
		if existing.RuntimeType != runtime.RuntimeType || existing.OwnershipMode != domain.OwnershipModeAdopted ||
			existing.ReconcileMode != domain.ReconcileModeObserveOnly || existing.EndpointRef != runtime.AdoptedRuntime.EndpointRef ||
			!reflect.DeepEqual(existing.RuntimeConfig, runtimeConfig) {
			return nil, fmt.Errorf("%w: existing deployment unit does not describe the exact adopted runtime", ErrLegacyReconciliationRefused)
		}
		return existing, nil
	}
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: environmentID, Key: key, DisplayName: service.Name,
		RuntimeType: runtime.RuntimeType, EndpointRef: runtime.AdoptedRuntime.EndpointRef,
		ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeAdopted,
		RuntimeConfig: runtimeConfig,
	}
	if err := r.units.Create(ctx, unit); err != nil {
		return nil, fmt.Errorf("create adopted deployment unit: %w", err)
	}
	return unit, nil
}

func cloneAdoptedRuntime(in *domain.AdoptedRuntimeConfig) *domain.AdoptedRuntimeConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.Environment = cloneStringMap(in.Environment)
	out.Labels = cloneStringMap(in.Labels)
	out.Ports = append([]string(nil), in.Ports...)
	out.Volumes = append([]string(nil), in.Volumes...)
	out.Command = append([]string(nil), in.Command...)
	out.Entrypoint = append([]string(nil), in.Entrypoint...)
	if in.Compose != nil {
		compose := *in.Compose
		compose.ConfigFiles = append([]string(nil), in.Compose.ConfigFiles...)
		out.Compose = &compose
	}
	return &out
}

func cloneServiceRuntimeConfig(in *domain.ServiceRuntimeConfig) *domain.ServiceRuntimeConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.Adopted = cloneAdoptedRuntime(in.Adopted)
	return &out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneLegacySoul(in *domain.AgentSoul) *domain.AgentSoul {
	out := *in
	out.AllowedKinds = append([]int(nil), in.AllowedKinds...)
	out.ToolGrants = append([]domain.ToolGrant(nil), in.ToolGrants...)
	return &out
}

type legacySoulIdentitySnapshot struct {
	AgentID                    string
	Name                       string
	Purpose                    string
	Tier                       domain.SoulTier
	Status                     domain.SoulStatus
	NostrPubkey                string
	NostrNpub                  string
	NIP05                      string
	SoulMD                     string
	AvatarURL                  string
	SoulBlobHash               string
	QdrantCollection           string
	WorkspaceRepoURL           string
	AllowedKinds               []int
	ToolGrants                 []domain.ToolGrant
	Runtime                    domain.SoulRuntimeSpec
	Workspace                  domain.SoulWorkspaceSpec
	AppliedFleetConfigRevision string
}

func snapshotLegacySoulIdentity(soul *domain.AgentSoul) legacySoulIdentitySnapshot {
	return legacySoulIdentitySnapshot{
		AgentID:                    soul.AgentID,
		Name:                       soul.Name,
		Purpose:                    soul.Purpose,
		Tier:                       soul.Tier,
		Status:                     soul.Status,
		NostrPubkey:                soul.NostrPubkey,
		NostrNpub:                  soul.NostrNpub,
		NIP05:                      soul.NIP05,
		SoulMD:                     soul.SoulMD,
		AvatarURL:                  soul.AvatarURL,
		SoulBlobHash:               soul.SoulBlobHash,
		QdrantCollection:           soul.QdrantCollection,
		WorkspaceRepoURL:           soul.WorkspaceRepoURL,
		AllowedKinds:               append([]int(nil), soul.AllowedKinds...),
		ToolGrants:                 append([]domain.ToolGrant(nil), soul.ToolGrants...),
		Runtime:                    soul.Runtime,
		Workspace:                  soul.Workspace,
		AppliedFleetConfigRevision: soul.AppliedFleetConfigRevision,
	}
}

func legacyReconcileRollback(previousEventID string, created bool, serviceID uuid.UUID, agentID string) LegacyReconcileRollback {
	rollback := LegacyReconcileRollback{PreviousSoulEventID: previousEventID}
	if created {
		rollback.CreatedServiceID = serviceID.String()
	}
	rollback.Steps = []string{
		"Republish the exact prior kind:31951 read model (event " + previousEventID + ") as a superseding event that omits the service tag.",
		"Do not stop, restart, redeploy, or re-key the running runtime; do not mutate grants, mounts, or custody.",
	}
	if created {
		rollback.Steps = append(rollback.Steps, "Mark Bahia service "+soulServiceName(agentID)+" ("+serviceID.String()+") as unused for operator cleanup; do not delete it while environment state references it.")
	} else {
		rollback.Steps = append(rollback.Steps, "Bahia service "+serviceID.String()+" was reused, not created; no service teardown is required.")
	}
	return rollback
}

func validateLegacyReconcileApproval(approval LegacyReconcileApproval, current LegacyAgentReconciliationClassification) error {
	if approval.Principal == nil || !approval.Principal.IsAuthenticated() || strings.TrimSpace(approval.Principal.Subject) == "" {
		return fmt.Errorf("%w: authenticated operator principal is required", ErrLegacyReconciliationRefused)
	}
	if approval.AgentID != current.AgentID || approval.Action != LegacyAgentReconcileActionLink {
		return fmt.Errorf("%w: approval is not bound to the exact agent_id and action", ErrLegacyReconciliationRefused)
	}
	if approval.SoulEventID != current.SoulEventID || approval.SoulContentHash != current.SoulContentHash {
		return fmt.Errorf("%w: approval is not bound to the current Soul event and content hash", ErrLegacyReconciliationRefused)
	}
	if strings.TrimSpace(approval.ApprovedBy) == "" || approval.ApprovedBy != approval.Principal.Subject {
		return fmt.Errorf("%w: approved_by does not match authenticated principal", ErrLegacyReconciliationRefused)
	}
	if strings.TrimSpace(approval.ApprovalRef) == "" {
		return fmt.Errorf("%w: approval approval_ref is required", ErrLegacyReconciliationRefused)
	}
	return nil
}

func validateLegacyReconciliationInput(input LegacyAgentReconciliationInput) error {
	if input.Schema != LegacyAgentReconciliationInputSchemaV1 {
		return fmt.Errorf("unsupported input schema %q", input.Schema)
	}
	seenSouls := make(map[string]struct{})
	for i, soul := range input.Souls {
		if strings.TrimSpace(soul.AgentID) == "" || strings.TrimSpace(soul.EventID) == "" || strings.TrimSpace(soul.SourceRef) == "" {
			return fmt.Errorf("souls[%d] requires agent_id, event_id, and source_ref", i)
		}
		key := normalizeLegacyEvidence(soul.AgentID)
		if _, exists := seenSouls[key]; exists {
			return fmt.Errorf("duplicate soul agent_id %q", soul.AgentID)
		}
		seenSouls[key] = struct{}{}
		if soul.AgentPubkey != "" && !validLegacyPubkey(soul.AgentPubkey) {
			return fmt.Errorf("souls[%d] agent_pubkey must be 64-character hex", i)
		}
		if soul.BahiaServiceID != "" {
			if _, err := uuid.Parse(strings.TrimSpace(soul.BahiaServiceID)); err != nil {
				return fmt.Errorf("souls[%d] bahia_service_id must be a UUID", i)
			}
		}
		if unsafeLegacyCustodyRef(soul.CustodyRef) {
			return fmt.Errorf("souls[%d] custody_ref must be a non-secret reference", i)
		}
	}
	seenRuntime := make(map[string]struct{})
	for i, agent := range input.RuntimeAgents {
		if strings.TrimSpace(agent.InventoryID) == "" || strings.TrimSpace(agent.SourceRef) == "" {
			return fmt.Errorf("running_agents[%d] requires inventory_id and source_ref", i)
		}
		key := normalizeLegacyEvidence(agent.InventoryID)
		if _, exists := seenRuntime[key]; exists {
			return fmt.Errorf("duplicate running agent inventory_id %q", agent.InventoryID)
		}
		seenRuntime[key] = struct{}{}
		if agent.ManagedPubkey != "" && !validLegacyPubkey(agent.ManagedPubkey) {
			return fmt.Errorf("running_agents[%d] managed_pubkey must be 64-character hex", i)
		}
		if unsafeLegacyCustodyRef(agent.CustodyRef) {
			return fmt.Errorf("running_agents[%d] custody_ref must be a non-secret reference", i)
		}
	}
	for i, svc := range input.Services {
		if strings.TrimSpace(svc.ID) == "" || strings.TrimSpace(svc.Name) == "" || strings.TrimSpace(svc.ArtifactRepo) == "" || strings.TrimSpace(svc.SourceRef) == "" {
			return fmt.Errorf("services[%d] requires id, name, artifact_repo, and source_ref", i)
		}
		if _, err := uuid.Parse(strings.TrimSpace(svc.ID)); err != nil {
			return fmt.Errorf("services[%d] id must be a UUID", i)
		}
	}
	for i, approval := range input.Approvals {
		if strings.TrimSpace(approval.AgentID) == "" {
			return fmt.Errorf("approvals[%d] requires agent_id", i)
		}
	}
	return nil
}

func isLegacyReconcileActive(status string) bool {
	return strings.TrimSpace(status) == string(domain.SoulStatusActive)
}
