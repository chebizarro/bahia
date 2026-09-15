package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"

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
	legacyAgentReconcileActionLink = "link_existing_soul_to_bahia_service"

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
	Schema            string                    `json:"schema"`
	Souls             []LegacyReconcileSoul     `json:"souls"`
	RuntimeAgents     []LegacyRunningAgent      `json:"running_agents"`
	Services          []LegacyReconcileService  `json:"services"`
	Approvals         []LegacyReconcileApproval `json:"approvals,omitempty"`
	ReviewedPlacement map[string]string         `json:"operator_reviewed_placement,omitempty"`
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	ArtifactRepo string `json:"artifact_repo"`
	RuntimeType  string `json:"runtime_type,omitempty"`
	SourceRef    string `json:"source_ref"`
}

// LegacyReconcileApproval is the explicit operator authorization required
// before any write. It must name the exact agent identity and action.
type LegacyReconcileApproval struct {
	AgentID      string `json:"agent_id"`
	Action       string `json:"action"`
	ApprovedBy   string `json:"approved_by"`
	ApprovalRef  string `json:"approval_ref"`
	PlacementRef string `json:"placement_ref,omitempty"`
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
	PlacementRef        string                  `json:"operator_reviewed_placement,omitempty"`
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

func classifyLegacyReconcileSoul(soul LegacyReconcileSoul, agents []LegacyRunningAgent, services []LegacyReconcileService, placement map[string]string) LegacyAgentReconciliationClassification {
	classification := LegacyAgentReconciliationClassification{AgentID: soul.AgentID, SoulEventID: soul.EventID}

	if strings.TrimSpace(soul.BahiaServiceID) != "" {
		svc := findLegacyServiceByID(services, soul.BahiaServiceID)
		if svc == nil {
			classification.Status = legacyReconcileStatusOrphaned
			classification.ReasonCode = "missing_service_reference"
			return classification
		}
		classification.Status = legacyReconcileStatusLinked
		classification.ReasonCode = "service_link_present"
		classification.ExistingServiceID = svc.ID
		return classification
	}

	nameMatches, conflicts := findLegacyServicesByName(services, soul.AgentID)
	if len(conflicts) > 0 {
		classification.Status = legacyReconcileStatusAmbiguous
		classification.ReasonCode = "service_identity_conflict"
		classification.CandidateServiceIDs = conflicts
		return classification
	}
	var reuseService *LegacyReconcileService
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
		if reuseService != nil {
			classification.ExistingServiceID = reuseService.ID
		}
		return classification
	}

	match := matches[0]
	classification.Status = legacyReconcileStatusUnlinked
	classification.ReasonCode = "single_authoritative_runtime_match_requires_operator_approval"
	classification.MatchedRuntimeIDs = []string{match.agent.InventoryID}
	classification.MatchedEvidence = match.fields
	if reuseService != nil {
		classification.ExistingServiceID = reuseService.ID
	}
	classification.Plan = buildLegacyReconcilePlan(soul, match, reuseService, placement)
	return classification
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

func buildLegacyReconcilePlan(soul LegacyReconcileSoul, match legacyReconcileRuntimeMatch, reuse *LegacyReconcileService, placement map[string]string) *LegacyReconcilePlan {
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
		Action:              legacyAgentReconcileActionLink,
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
		plan.PlacementRef = strings.TrimSpace(placement[soul.AgentID])
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

// LegacyServiceLookup is satisfied by *service.RegistryService.
type LegacyServiceLookup interface {
	GetServiceByName(ctx context.Context, name string) (*domain.Service, error)
}

// LegacySoulLinkPublisher is satisfied by *Reactor.
type LegacySoulLinkPublisher interface {
	PublishSoul(ctx context.Context, soul *domain.AgentSoul) error
}

// LegacyAgentReconciler performs the approval-gated write side of
// reconciliation. It never reprovisions, restarts, re-keys, or mutates grants.
type LegacyAgentReconciler struct {
	registrar LegacyServiceRegistrar
	lookup    LegacyServiceLookup
	publisher LegacySoulLinkPublisher
	logger    *slog.Logger
}

// NewLegacyAgentReconciler builds a reconciler. All dependencies are required.
func NewLegacyAgentReconciler(registrar LegacyServiceRegistrar, lookup LegacyServiceLookup, publisher LegacySoulLinkPublisher, logger *slog.Logger) (*LegacyAgentReconciler, error) {
	if registrar == nil || lookup == nil || publisher == nil {
		return nil, fmt.Errorf("legacy agent reconciler requires registrar, service lookup, and soul publisher")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LegacyAgentReconciler{registrar: registrar, lookup: lookup, publisher: publisher, logger: logger}, nil
}

// ReconcileApprovedLink links exactly one already-existing Soul to exactly one
// Bahia service and publishes a superseding kind:31951 read model that adds
// bahia_service_id. It reuses an existing service by name, so re-runs create no
// duplicate service. Ambiguous and orphaned classifications are refused.
func (r *LegacyAgentReconciler) ReconcileApprovedLink(ctx context.Context, soul *domain.AgentSoul, classification LegacyAgentReconciliationClassification, approval LegacyReconcileApproval) (LegacyAgentReconcileReceipt, error) {
	receipt := LegacyAgentReconcileReceipt{
		Schema:  legacyAgentReconciliationReceiptSchema,
		AgentID: approval.AgentID,
		Action:  legacyAgentReconcileActionLink,
	}
	if soul == nil {
		return receipt, fmt.Errorf("%w: soul is required", ErrLegacyReconciliationRefused)
	}
	if err := validateLegacyReconcileApproval(approval); err != nil {
		return receipt, err
	}
	if strings.TrimSpace(soul.AgentID) == "" || strings.TrimSpace(approval.AgentID) != soul.AgentID {
		return receipt, fmt.Errorf("%w: approval agent_id does not match soul", ErrLegacyReconciliationRefused)
	}
	if classification.AgentID != "" && classification.AgentID != soul.AgentID {
		return receipt, fmt.Errorf("%w: classification agent_id does not match soul", ErrLegacyReconciliationRefused)
	}
	switch classification.Status {
	case legacyReconcileStatusLinked, legacyReconcileStatusUnlinked:
	case legacyReconcileStatusAmbiguous:
		return receipt, fmt.Errorf("%w: ambiguous classification %q", ErrLegacyReconciliationRefused, classification.ReasonCode)
	case legacyReconcileStatusOrphaned:
		return receipt, fmt.Errorf("%w: orphaned classification %q", ErrLegacyReconciliationRefused, classification.ReasonCode)
	default:
		return receipt, fmt.Errorf("%w: unknown classification status %q", ErrLegacyReconciliationRefused, classification.Status)
	}
	if soul.Status != domain.SoulStatusActive {
		return receipt, fmt.Errorf("%w: soul status %q is not active", ErrLegacyReconciliationRefused, soul.Status)
	}

	name := soulServiceName(soul.AgentID)
	existing, err := r.lookup.GetServiceByName(ctx, name)
	if err != nil {
		return receipt, fmt.Errorf("look up existing service: %w", err)
	}

	// Idempotency: an already-linked Soul is a no-op even if the caller passed
	// a stale classification. Re-runs never create a duplicate service.
	if soul.BahiaServiceID != nil {
		if existing == nil || existing.ID != *soul.BahiaServiceID {
			return receipt, fmt.Errorf("%w: soul references service %s that is not present", ErrLegacyReconciliationRefused, soul.BahiaServiceID)
		}
		receipt.NoOp = true
		receipt.ServiceID = *soul.BahiaServiceID
		receipt.PreviousSoulEventID = soul.EventID
		receipt.PreservedRuntimeBinding = soul.Runtime.RuntimeBinding
		receipt.PreservedAgentPubkey = soul.NostrPubkey
		return receipt, nil
	}

	before := snapshotLegacySoulIdentity(soul)
	serviceID, err := r.registrar.RegisterSoulAsService(ctx, soul)
	if err != nil {
		return receipt, fmt.Errorf("register soul as service: %w", err)
	}
	if serviceID == uuid.Nil {
		return receipt, fmt.Errorf("register soul as service returned an empty service id")
	}

	confirmed, err := r.lookup.GetServiceByName(ctx, name)
	if err != nil {
		return receipt, fmt.Errorf("confirm service: %w", err)
	}
	if confirmed == nil || confirmed.ID != serviceID {
		return receipt, fmt.Errorf("service confirmation mismatch for %q", name)
	}
	if confirmed.ArtifactRepo != soulServiceArtifactRepo(soul.AgentID) {
		return receipt, fmt.Errorf("service %q artifact repository %q does not match agent", name, confirmed.ArtifactRepo)
	}

	previousEventID := soul.EventID
	soul.BahiaServiceID = &serviceID
	if !reflect.DeepEqual(before, snapshotLegacySoulIdentity(soul)) {
		return receipt, fmt.Errorf("reconciliation mutated preserved soul identity fields")
	}
	if err := r.publisher.PublishSoul(ctx, soul); err != nil {
		return receipt, fmt.Errorf("publish superseding soul link: %w", err)
	}

	receipt.ServiceCreated = existing == nil
	receipt.ServiceID = serviceID
	receipt.PreviousSoulEventID = previousEventID
	receipt.SupersedingSoulEventID = soul.EventID
	receipt.PreservedRuntimeBinding = soul.Runtime.RuntimeBinding
	receipt.PreservedAgentPubkey = soul.NostrPubkey
	receipt.Rollback = legacyReconcileRollback(previousEventID, existing == nil, serviceID, soul.AgentID)

	r.logger.Info("reconciled legacy soul into bahia service",
		"agent_id", soul.AgentID,
		"service_id", serviceID,
		"service_created", receipt.ServiceCreated,
		"superseding_soul_event", receipt.SupersedingSoulEventID,
	)
	return receipt, nil
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

func validateLegacyReconcileApproval(approval LegacyReconcileApproval) error {
	if strings.TrimSpace(approval.AgentID) == "" {
		return fmt.Errorf("%w: approval agent_id is required", ErrLegacyReconciliationRefused)
	}
	if approval.Action != legacyAgentReconcileActionLink {
		return fmt.Errorf("%w: approval action %q is not %q", ErrLegacyReconciliationRefused, approval.Action, legacyAgentReconcileActionLink)
	}
	if strings.TrimSpace(approval.ApprovedBy) == "" {
		return fmt.Errorf("%w: approval approved_by is required", ErrLegacyReconciliationRefused)
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
