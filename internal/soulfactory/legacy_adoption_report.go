package soulfactory

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const LegacyAdoptionInputSchemaV1 = "soulfactory-legacy-adoption-input/v1"

var ErrLegacyAdoptionAmbiguous = errors.New("legacy agent adoption classification is ambiguous")

// LegacyAdoptionInput is a secret-free snapshot of independently collected,
// read-only fleet evidence. Collection and mutation are deliberately outside
// this classifier.
type LegacyAdoptionInput struct {
	Schema          string                 `json:"schema"`
	RunningAgents   []LegacyRunningAgent   `json:"running_agents"`
	IdentityRecords []LegacyIdentityRecord `json:"identity_records"`
	TrustedSouls    []LegacyTrustedSoul    `json:"trusted_souls"`
}

type LegacyRunningAgent struct {
	InventoryID       string                       `json:"inventory_id"`
	Running           bool                         `json:"running"`
	DocumentedAgentID string                       `json:"documented_agent_id,omitempty"`
	ManagedPubkey     string                       `json:"managed_pubkey,omitempty"`
	RuntimeBinding    string                       `json:"runtime_binding,omitempty"`
	Workspace         string                       `json:"workspace,omitempty"`
	PersonaRef        string                       `json:"persona_ref,omitempty"`
	CustodyRef        string                       `json:"custody_ref,omitempty"`
	DisplayName       string                       `json:"display_name,omitempty"`
	ContainerName     string                       `json:"container_name,omitempty"`
	RuntimeType       domain.RuntimeType           `json:"runtime_type,omitempty"`
	AdoptedRuntime    *domain.AdoptedRuntimeConfig `json:"adopted_runtime,omitempty"`
	SourceRef         string                       `json:"source_ref"`
}

type LegacyIdentityRecord struct {
	AgentID        string   `json:"agent_id"`
	ManagedPubkey  string   `json:"managed_pubkey,omitempty"`
	RuntimeBinding string   `json:"runtime_binding,omitempty"`
	Workspace      string   `json:"workspace,omitempty"`
	PersonaRef     string   `json:"persona_ref,omitempty"`
	CustodyRef     string   `json:"custody_ref,omitempty"`
	DisplayName    string   `json:"display_name,omitempty"`
	SourceRefs     []string `json:"source_refs"`
}

type LegacyTrustedSoul struct {
	Kind        int    `json:"kind"`
	EventID     string `json:"event_id"`
	AgentID     string `json:"agent_id"`
	AgentPubkey string `json:"agent_pubkey"`
	Trusted     bool   `json:"trusted"`
	SourceRef   string `json:"source_ref"`
}

type LegacyAdoptionReport struct {
	Schema          string                         `json:"schema"`
	ReadOnly        bool                           `json:"read_only"`
	MutationAllowed bool                           `json:"mutation_allowed"`
	Classifications []LegacyAdoptionClassification `json:"classifications"`
	Summary         LegacyAdoptionSummary          `json:"summary"`
}

type LegacyAdoptionSummary struct {
	Running                int `json:"running"`
	OperatorReviewRequired int `json:"operator_review_required"`
	AlreadyTrusted         int `json:"already_trusted"`
	NoMatch                int `json:"no_match"`
	Ambiguous              int `json:"ambiguous"`
}

type LegacyAdoptionClassification struct {
	InventoryID      string              `json:"inventory_id"`
	SourceRef        string              `json:"source_ref"`
	Status           string              `json:"status"`
	ReasonCode       string              `json:"reason_code"`
	CandidateAgentID string              `json:"candidate_agent_id,omitempty"`
	MatchedEvidence  []string            `json:"matched_evidence,omitempty"`
	CandidateIDs     []string            `json:"candidate_ids,omitempty"`
	TrustedSoulID    string              `json:"trusted_soul_event_id,omitempty"`
	Plan             *LegacyAdoptionPlan `json:"adoption_plan,omitempty"`
}

type LegacyAdoptionPlan struct {
	Action             string   `json:"action"`
	OperatorApproval   string   `json:"operator_approval_required"`
	AgentID            string   `json:"agent_id"`
	PreservePubkey     string   `json:"preserve_managed_pubkey,omitempty"`
	PreserveRuntime    string   `json:"preserve_runtime_binding,omitempty"`
	PreserveWorkspace  string   `json:"preserve_workspace,omitempty"`
	PreservePersonaRef string   `json:"preserve_persona_ref,omitempty"`
	CustodyEvidenceRef string   `json:"custody_evidence_ref,omitempty"`
	IdentitySourceRefs []string `json:"identity_source_refs"`
	ProhibitedActions  []string `json:"prohibited_actions"`
}

type legacyEvidenceMatch struct {
	candidate LegacyIdentityRecord
	fields    []string
}

// ClassifyLegacyAgents creates a deterministic, read-only adoption report. If
// any running agent is ambiguous, it returns the complete report together with
// ErrLegacyAdoptionAmbiguous so callers can refuse the adoption workflow.
func ClassifyLegacyAgents(input LegacyAdoptionInput) (LegacyAdoptionReport, error) {
	if err := validateLegacyAdoptionInput(input); err != nil {
		return LegacyAdoptionReport{}, err
	}

	agents := append([]LegacyRunningAgent(nil), input.RunningAgents...)
	sort.Slice(agents, func(i, j int) bool { return agents[i].InventoryID < agents[j].InventoryID })
	identities := append([]LegacyIdentityRecord(nil), input.IdentityRecords...)
	sort.Slice(identities, func(i, j int) bool { return identities[i].AgentID < identities[j].AgentID })
	souls := append([]LegacyTrustedSoul(nil), input.TrustedSouls...)
	sort.Slice(souls, func(i, j int) bool { return souls[i].EventID < souls[j].EventID })

	report := LegacyAdoptionReport{
		Schema:          "soulfactory-legacy-adoption-report/v1",
		ReadOnly:        true,
		MutationAllowed: false,
	}
	ambiguous := false
	for _, agent := range agents {
		if !agent.Running {
			continue
		}
		report.Summary.Running++
		classification := classifyLegacyAgent(agent, identities, souls)
		report.Classifications = append(report.Classifications, classification)
		switch classification.Status {
		case "operator_review_required":
			report.Summary.OperatorReviewRequired++
		case "already_trusted":
			report.Summary.AlreadyTrusted++
		case "no_match":
			report.Summary.NoMatch++
		case "ambiguous":
			report.Summary.Ambiguous++
			ambiguous = true
		}
	}
	if ambiguous {
		return report, ErrLegacyAdoptionAmbiguous
	}
	return report, nil
}

func classifyLegacyAgent(agent LegacyRunningAgent, identities []LegacyIdentityRecord, souls []LegacyTrustedSoul) LegacyAdoptionClassification {
	classification := LegacyAdoptionClassification{InventoryID: agent.InventoryID, SourceRef: agent.SourceRef}
	matches := make([]legacyEvidenceMatch, 0)
	conflictingCandidates := make([]string, 0)
	for _, identity := range identities {
		fields, conflicts := compareLegacyIdentityEvidence(agent, identity)
		if len(conflicts) > 0 && isPlausibleLegacyConflict(fields, conflicts) {
			conflictingCandidates = append(conflictingCandidates, identity.AgentID)
			continue
		}
		if len(conflicts) == 0 && isAuthoritativeLegacyMatch(fields) {
			matches = append(matches, legacyEvidenceMatch{candidate: identity, fields: fields})
		}
	}
	if len(conflictingCandidates) > 0 {
		classification.Status = "ambiguous"
		classification.ReasonCode = "identity_evidence_conflict"
		classification.CandidateIDs = conflictingCandidates
		return classification
	}
	if len(matches) == 0 {
		classification.Status = "no_match"
		classification.ReasonCode = "no_authoritative_match"
		return classification
	}
	if len(matches) > 1 {
		classification.Status = "ambiguous"
		classification.ReasonCode = "multiple_authoritative_matches"
		for _, match := range matches {
			classification.CandidateIDs = append(classification.CandidateIDs, match.candidate.AgentID)
		}
		return classification
	}

	match := matches[0]
	classification.CandidateAgentID = match.candidate.AgentID
	classification.MatchedEvidence = match.fields
	matchingSouls, soulConflict := findLegacyTrustedSouls(match.candidate, souls)
	if soulConflict || len(matchingSouls) > 1 {
		classification.Status = "ambiguous"
		classification.ReasonCode = "trusted_soul_identity_conflict"
		for _, soul := range matchingSouls {
			classification.CandidateIDs = append(classification.CandidateIDs, soul.EventID)
		}
		return classification
	}
	if len(matchingSouls) == 1 {
		classification.Status = "already_trusted"
		classification.ReasonCode = "trusted_soul_exists"
		classification.TrustedSoulID = matchingSouls[0].EventID
		return classification
	}

	classification.Status = "operator_review_required"
	classification.ReasonCode = "single_authoritative_match_requires_operator_approval"
	classification.Plan = &LegacyAdoptionPlan{
		Action:             "authorize_create_one_canonical_kind_31951_soul",
		OperatorApproval:   "Explicitly name this agent identity and authorize this exact action; this report performs no mutation.",
		AgentID:            match.candidate.AgentID,
		PreservePubkey:     match.candidate.ManagedPubkey,
		PreserveRuntime:    match.candidate.RuntimeBinding,
		PreserveWorkspace:  match.candidate.Workspace,
		PreservePersonaRef: match.candidate.PersonaRef,
		CustodyEvidenceRef: match.candidate.CustodyRef,
		IdentitySourceRefs: sortedCopy(match.candidate.SourceRefs),
		ProhibitedActions: []string{
			"key_generation",
			"key_rotation",
			"key_revocation",
			"identity_replacement",
			"re_adoption",
			"acl_or_grant_mutation",
			"custody_mutation",
			"bahia_service_mutation",
		},
	}
	return classification
}

func compareLegacyIdentityEvidence(agent LegacyRunningAgent, identity LegacyIdentityRecord) ([]string, []string) {
	pairs := []struct {
		name         string
		runtimeFact  string
		identityFact string
	}{
		{"documented_agent_id", agent.DocumentedAgentID, identity.AgentID},
		{"managed_pubkey", agent.ManagedPubkey, identity.ManagedPubkey},
		{"runtime_binding", agent.RuntimeBinding, identity.RuntimeBinding},
		{"workspace", agent.Workspace, identity.Workspace},
		{"persona_ref", agent.PersonaRef, identity.PersonaRef},
		{"custody_ref", agent.CustodyRef, identity.CustodyRef},
	}
	var matched, conflicts []string
	for _, pair := range pairs {
		left, right := normalizeLegacyEvidence(pair.runtimeFact), normalizeLegacyEvidence(pair.identityFact)
		if left == "" || right == "" {
			continue
		}
		if left != right {
			conflicts = append(conflicts, pair.name)
			continue
		}
		matched = append(matched, pair.name)
	}
	return matched, conflicts
}

func isAuthoritativeLegacyMatch(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	for _, field := range fields {
		if field == "documented_agent_id" || field == "managed_pubkey" || field == "custody_ref" {
			return true
		}
	}
	return false
}

func isPlausibleLegacyConflict(matches, conflicts []string) bool {
	return hasLegacyIdentityAnchor(matches) || (len(matches) > 0 && hasLegacyIdentityAnchor(conflicts))
}

func hasLegacyIdentityAnchor(fields []string) bool {
	for _, field := range fields {
		if field == "documented_agent_id" || field == "managed_pubkey" || field == "custody_ref" {
			return true
		}
	}
	return false
}

func findLegacyTrustedSouls(identity LegacyIdentityRecord, souls []LegacyTrustedSoul) ([]LegacyTrustedSoul, bool) {
	var matches []LegacyTrustedSoul
	conflict := false
	for _, soul := range souls {
		if !soul.Trusted {
			continue
		}
		idMatch := normalizeLegacyEvidence(soul.AgentID) == normalizeLegacyEvidence(identity.AgentID)
		pubkeyMatch := identity.ManagedPubkey != "" && normalizeLegacyEvidence(soul.AgentPubkey) == normalizeLegacyEvidence(identity.ManagedPubkey)
		if idMatch || pubkeyMatch {
			matches = append(matches, soul)
			if idMatch && identity.ManagedPubkey != "" && soul.AgentPubkey != "" && !pubkeyMatch {
				conflict = true
			}
			if pubkeyMatch && soul.AgentID != "" && !idMatch {
				conflict = true
			}
		}
	}
	return matches, conflict
}

func validateLegacyAdoptionInput(input LegacyAdoptionInput) error {
	if input.Schema != LegacyAdoptionInputSchemaV1 {
		return fmt.Errorf("unsupported input schema %q", input.Schema)
	}
	seenAgents := make(map[string]struct{})
	for i, agent := range input.RunningAgents {
		if strings.TrimSpace(agent.InventoryID) == "" || strings.TrimSpace(agent.SourceRef) == "" {
			return fmt.Errorf("running_agents[%d] requires inventory_id and source_ref", i)
		}
		key := normalizeLegacyEvidence(agent.InventoryID)
		if _, exists := seenAgents[key]; exists {
			return fmt.Errorf("duplicate running agent inventory_id %q", agent.InventoryID)
		}
		seenAgents[key] = struct{}{}
		if unsafeLegacyCustodyRef(agent.CustodyRef) {
			return fmt.Errorf("running_agents[%d] custody_ref must be a non-secret reference", i)
		}
		if agent.ManagedPubkey != "" && !validLegacyPubkey(agent.ManagedPubkey) {
			return fmt.Errorf("running_agents[%d] managed_pubkey must be 64-character hex", i)
		}
	}
	seenIdentities := make(map[string]struct{})
	for i, identity := range input.IdentityRecords {
		if strings.TrimSpace(identity.AgentID) == "" || len(identity.SourceRefs) == 0 {
			return fmt.Errorf("identity_records[%d] requires agent_id and source_refs", i)
		}
		key := normalizeLegacyEvidence(identity.AgentID)
		if _, exists := seenIdentities[key]; exists {
			return fmt.Errorf("duplicate identity agent_id %q", identity.AgentID)
		}
		seenIdentities[key] = struct{}{}
		if unsafeLegacyCustodyRef(identity.CustodyRef) {
			return fmt.Errorf("identity_records[%d] custody_ref must be a non-secret reference", i)
		}
		if identity.ManagedPubkey != "" && !validLegacyPubkey(identity.ManagedPubkey) {
			return fmt.Errorf("identity_records[%d] managed_pubkey must be 64-character hex", i)
		}
	}
	for i, soul := range input.TrustedSouls {
		if soul.Trusted && (soul.Kind != domain.KindAgentSoul || !validLegacyPubkey(soul.EventID) || strings.TrimSpace(soul.AgentID) == "" || strings.TrimSpace(soul.AgentPubkey) == "" || strings.TrimSpace(soul.SourceRef) == "") {
			return fmt.Errorf("trusted_souls[%d] requires kind 31951, a 64-character hex event_id, agent_id, agent_pubkey, and source_ref", i)
		}
		if soul.AgentPubkey != "" && !validLegacyPubkey(soul.AgentPubkey) {
			return fmt.Errorf("trusted_souls[%d] agent_pubkey must be 64-character hex", i)
		}
	}
	return nil
}

func validLegacyPubkey(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func unsafeLegacyCustodyRef(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(value, "nsec") || strings.Contains(value, "bunker://") || strings.Contains(value, "secret")
}

func normalizeLegacyEvidence(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
