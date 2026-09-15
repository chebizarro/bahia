package soulfactory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type legacyReconcileCapturePublisher struct {
	published []*domain.AgentSoul
	fail      error
}

func (p *legacyReconcileCapturePublisher) PublishSoul(_ context.Context, soul *domain.AgentSoul) error {
	if p.fail != nil {
		return p.fail
	}
	copied := *soul
	copied.EventID = "superseding-" + soul.AgentID
	p.published = append(p.published, &copied)
	soul.EventID = copied.EventID
	return nil
}

func legacyReconcilePubkey(c byte) string {
	return strings.Repeat(string(c), 64)
}

func TestClassifyLegacyAgentReconciliationClassifiesAllStates(t *testing.T) {
	linkedID := uuid.New()
	input := LegacyAgentReconciliationInput{
		Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls: []LegacyReconcileSoul{
			{EventID: strings.Repeat("1", 64), AgentID: "alpha", Status: string(domain.SoulStatusActive), BahiaServiceID: linkedID.String(), SourceRef: "soul:alpha"},
			{EventID: strings.Repeat("2", 64), AgentID: "bravo", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('b'), RuntimeBinding: "openclaw://agents/bravo", Workspace: "https://git/bravo", SourceRef: "soul:bravo"},
			{EventID: strings.Repeat("3", 64), AgentID: "charlie", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('c'), SourceRef: "soul:charlie"},
			{EventID: strings.Repeat("4", 64), AgentID: "drafty", Status: string(domain.SoulStatusDraft), SourceRef: "soul:drafty"},
		},
		RuntimeAgents: []LegacyRunningAgent{
			{InventoryID: "inv-alpha", Running: true, DocumentedAgentID: "alpha", ManagedPubkey: legacyReconcilePubkey('a'), SourceRef: "host:alpha"},
			{InventoryID: "inv-bravo", Running: true, DocumentedAgentID: "bravo", ManagedPubkey: legacyReconcilePubkey('b'), RuntimeBinding: "openclaw://agents/bravo", Workspace: "https://git/bravo", SourceRef: "host:bravo"},
		},
		Services: []LegacyReconcileService{
			{ID: linkedID.String(), Name: "agent-alpha", ArtifactRepo: "agents/alpha", SourceRef: "bahia:alpha"},
		},
	}

	report, err := ClassifyLegacyAgentReconciliation(input)
	if err != nil {
		t.Fatalf("ClassifyLegacyAgentReconciliation() error = %v", err)
	}
	if !report.ReadOnly || report.MutationAllowed {
		t.Fatalf("dry-run report must be read-only without mutation: %+v", report)
	}
	byAgent := map[string]LegacyAgentReconciliationClassification{}
	for _, c := range report.Classifications {
		byAgent[c.AgentID] = c
	}

	alpha := byAgent["alpha"]
	if alpha.Status != legacyReconcileStatusLinked || alpha.ReasonCode != "service_link_present" || alpha.ExistingServiceID != linkedID.String() {
		t.Fatalf("alpha classification = %+v, want linked to %s", alpha, linkedID)
	}

	bravo := byAgent["bravo"]
	if bravo.Status != legacyReconcileStatusUnlinked || bravo.ReasonCode != "single_authoritative_runtime_match_requires_operator_approval" {
		t.Fatalf("bravo classification = %+v, want unlinked", bravo)
	}
	if bravo.Plan == nil || bravo.Plan.Action != legacyAgentReconcileActionLink {
		t.Fatalf("bravo plan = %+v, want link plan", bravo.Plan)
	}
	if bravo.Plan.ReuseServiceID != "" {
		t.Fatalf("bravo plan reused service %q, want create", bravo.Plan.ReuseServiceID)
	}
	if bravo.Plan.ServiceName != "agent-bravo" || bravo.Plan.ServiceArtifactRepo != "agents/bravo" {
		t.Fatalf("bravo plan service mapping = %q/%q", bravo.Plan.ServiceName, bravo.Plan.ServiceArtifactRepo)
	}
	if len(bravo.MatchedRuntimeIDs) != 1 || bravo.MatchedRuntimeIDs[0] != "inv-bravo" {
		t.Fatalf("bravo matched runtimes = %v", bravo.MatchedRuntimeIDs)
	}
	if len(bravo.Plan.Rollback.Steps) == 0 || bravo.Plan.Rollback.PreviousSoulEventID != strings.Repeat("2", 64) {
		t.Fatalf("bravo rollback = %+v", bravo.Plan.Rollback)
	}
	if !slicesContains(bravo.Plan.ProhibitedActions, "deployment_intent_publish") {
		t.Fatalf("bravo prohibited actions = %v, want deployment_intent_publish", bravo.Plan.ProhibitedActions)
	}

	charlie := byAgent["charlie"]
	if charlie.Status != legacyReconcileStatusOrphaned || charlie.ReasonCode != "no_runtime_match" {
		t.Fatalf("charlie classification = %+v, want orphaned", charlie)
	}
	if _, ok := byAgent["drafty"]; ok {
		t.Fatalf("inactive soul drafty must not be classified")
	}

	if report.Summary.Active != 3 || report.Summary.Linked != 1 || report.Summary.Unlinked != 1 || report.Summary.Orphaned != 1 || report.Summary.Ambiguous != 0 {
		t.Fatalf("summary = %+v", report.Summary)
	}
}

func TestClassifyLegacyAgentReconciliationQuarantinesAmbiguity(t *testing.T) {
	foxtrotID := uuid.New()
	input := LegacyAgentReconciliationInput{
		Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls: []LegacyReconcileSoul{
			{EventID: strings.Repeat("a", 64), AgentID: "delta", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('d'), SourceRef: "soul:delta"},
			{EventID: strings.Repeat("b", 64), AgentID: "echo", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('f'), SourceRef: "soul:echo"},
			{EventID: strings.Repeat("c", 64), AgentID: "foxtrot", Status: string(domain.SoulStatusActive), AgentPubkey: legacyReconcilePubkey('7'), SourceRef: "soul:foxtrot"},
		},
		RuntimeAgents: []LegacyRunningAgent{
			{InventoryID: "inv-delta-1", Running: true, DocumentedAgentID: "delta", ManagedPubkey: legacyReconcilePubkey('d'), SourceRef: "host:d1"},
			{InventoryID: "inv-delta-2", Running: true, DocumentedAgentID: "delta", ManagedPubkey: legacyReconcilePubkey('d'), SourceRef: "host:d2"},
			{InventoryID: "inv-echo", Running: true, DocumentedAgentID: "echo", ManagedPubkey: legacyReconcilePubkey('e'), SourceRef: "host:e"},
		},
		Services: []LegacyReconcileService{
			{ID: foxtrotID.String(), Name: "agent-foxtrot", ArtifactRepo: "agents/someone-else", SourceRef: "bahia:foxtrot"},
		},
	}

	report, err := ClassifyLegacyAgentReconciliation(input)
	if !errors.Is(err, ErrLegacyReconciliationAmbiguous) {
		t.Fatalf("error = %v, want ErrLegacyReconciliationAmbiguous", err)
	}
	if report.Summary.Ambiguous != 3 {
		t.Fatalf("summary = %+v, want 3 ambiguous", report.Summary)
	}
	byAgent := map[string]LegacyAgentReconciliationClassification{}
	for _, c := range report.Classifications {
		byAgent[c.AgentID] = c
	}
	if got := byAgent["delta"]; got.Status != legacyReconcileStatusAmbiguous || got.ReasonCode != "multiple_authoritative_matches" || len(got.CandidateRuntimeIDs) != 2 {
		t.Fatalf("delta classification = %+v", got)
	}
	if got := byAgent["echo"]; got.Status != legacyReconcileStatusAmbiguous || got.ReasonCode != "identity_evidence_conflict" {
		t.Fatalf("echo classification = %+v", got)
	}
	if got := byAgent["foxtrot"]; got.Status != legacyReconcileStatusAmbiguous || got.ReasonCode != "service_identity_conflict" {
		t.Fatalf("foxtrot classification = %+v", got)
	}
	if byAgent["delta"].Plan != nil || byAgent["echo"].Plan != nil || byAgent["foxtrot"].Plan != nil {
		t.Fatalf("ambiguous classifications must not carry a mutation plan")
	}
}

func TestClassifyLegacyAgentReconciliationFlagsMissingServiceReference(t *testing.T) {
	input := LegacyAgentReconciliationInput{
		Schema: LegacyAgentReconciliationInputSchemaV1,
		Souls: []LegacyReconcileSoul{
			{EventID: strings.Repeat("9", 64), AgentID: "ghost", Status: string(domain.SoulStatusActive), BahiaServiceID: uuid.New().String(), SourceRef: "soul:ghost"},
		},
	}
	report, err := ClassifyLegacyAgentReconciliation(input)
	if err != nil {
		t.Fatalf("ClassifyLegacyAgentReconciliation() error = %v", err)
	}
	got := report.Classifications[0]
	if got.Status != legacyReconcileStatusOrphaned || got.ReasonCode != "missing_service_reference" {
		t.Fatalf("ghost classification = %+v, want orphaned missing_service_reference", got)
	}
}

func TestLegacyAgentReconciliationLinkIsIdempotentAndPreservesIdentity(t *testing.T) {
	registry, _, _, intents, observations, _ := newSoulFactoryRegistryHarness()
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}
	publisher := &legacyReconcileCapturePublisher{}
	reconciler, err := NewLegacyAgentReconciler(integration, registry, publisher, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewLegacyAgentReconciler() error = %v", err)
	}

	soul := &domain.AgentSoul{
		ID:               uuid.New(),
		AgentID:          "bravo",
		Name:             "Bravo",
		Purpose:          "reconciled agent",
		Tier:             domain.SoulTierStandard,
		Status:           domain.SoulStatusActive,
		NostrPubkey:      legacyReconcilePubkey('b'),
		NostrNpub:        "npub1bravo",
		SoulMD:           "soul read model",
		AllowedKinds:     []int{1, domain.KindAgentSoul},
		ToolGrants:       []domain.ToolGrant{{MCPServer: "memory", Scopes: []string{"read", "write"}}},
		Runtime:          domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimeBinding: "openclaw://agents/bravo", State: "running", RuntimePubkey: legacyReconcilePubkey('b')},
		WorkspaceRepoURL: "https://git/bravo",
		EventID:          strings.Repeat("a", 64),
		CreatedAt:        time.Now().UTC(),
	}
	classification := LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusUnlinked, ReasonCode: "single_authoritative_runtime_match_requires_operator_approval"}
	approval := LegacyReconcileApproval{AgentID: "bravo", Action: legacyAgentReconcileActionLink, ApprovedBy: "operator", ApprovalRef: "approval:1"}

	receipt, err := reconciler.ReconcileApprovedLink(t.Context(), soul, classification, approval)
	if err != nil {
		t.Fatalf("ReconcileApprovedLink() error = %v", err)
	}
	if receipt.NoOp || !receipt.ServiceCreated {
		t.Fatalf("receipt = %+v, want created non-noop", receipt)
	}
	if receipt.ServiceID == uuid.Nil || receipt.SupersedingSoulEventID == "" {
		t.Fatalf("receipt = %+v, want service and superseding event", receipt)
	}
	if receipt.PreviousSoulEventID != strings.Repeat("a", 64) {
		t.Fatalf("previous soul event = %q", receipt.PreviousSoulEventID)
	}
	if receipt.Rollback.CreatedServiceID != receipt.ServiceID.String() || len(receipt.Rollback.Steps) == 0 {
		t.Fatalf("rollback = %+v", receipt.Rollback)
	}

	services, err := registry.ListServices(t.Context())
	if err != nil || len(services) != 1 {
		t.Fatalf("ListServices() = %d, err=%v, want 1", len(services), err)
	}
	if services[0].Name != "agent-bravo" || services[0].ArtifactRepo != "agents/bravo" {
		t.Fatalf("service = %+v", services[0])
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published souls = %d, want 1", len(publisher.published))
	}

	captured := publisher.published[0]
	event := BuildAgentSoulEvent(captured)
	if findTag(event, tagParameterizedD) != "bravo" {
		t.Fatalf("superseding soul d-tag = %q", findTag(event, tagParameterizedD))
	}
	if findTag(event, tagService) != receipt.ServiceID.String() {
		t.Fatalf("superseding soul service tag = %q, want %s", findTag(event, tagService), receipt.ServiceID)
	}
	if findTag(event, tagRuntimeBinding) != "openclaw://agents/bravo" {
		t.Fatalf("runtime binding tag = %q", findTag(event, tagRuntimeBinding))
	}
	if findTag(event, tagPubkey) != legacyReconcilePubkey('b') {
		t.Fatalf("agent pubkey tag = %q", findTag(event, tagPubkey))
	}
	if !eventHasTagValue(event, tagAllowedKind, "31951") {
		t.Fatalf("allowed-kind tag missing from superseding soul")
	}
	if !eventHasTagValue(event, tagTool, "memory") {
		t.Fatalf("tool grant tag missing from superseding soul")
	}
	if captured.Runtime.RuntimeBinding != "openclaw://agents/bravo" || len(captured.ToolGrants) != 1 || len(captured.AllowedKinds) != 2 {
		t.Fatalf("identity fields mutated: %+v", captured)
	}
	if len(intents.intents) != 0 || len(observations.observations) != 0 {
		t.Fatalf("reconciliation fabricated deployables: intents=%d observations=%d", len(intents.intents), len(observations.observations))
	}

	receipt2, err := reconciler.ReconcileApprovedLink(t.Context(), soul, classification, approval)
	if err != nil {
		t.Fatalf("second ReconcileApprovedLink() error = %v", err)
	}
	if !receipt2.NoOp || receipt2.ServiceID != receipt.ServiceID {
		t.Fatalf("second receipt = %+v, want noop on %s", receipt2, receipt.ServiceID)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("idempotent re-run published %d souls, want 1", len(publisher.published))
	}
	services, _ = registry.ListServices(t.Context())
	if len(services) != 1 {
		t.Fatalf("idempotent re-run created %d services, want 1", len(services))
	}
}

func TestLegacyAgentReconciliationReusesExistingService(t *testing.T) {
	registry, _, _, _, _, _ := newSoulFactoryRegistryHarness()
	existingID := uuid.New()
	if err := registry.CreateService(t.Context(), &domain.Service{ID: existingID, Name: "agent-bravo", ArtifactRepo: "agents/bravo", RepoURL: "https://git/bravo"}); err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}
	integration, _ := NewBahiaIntegration(registry, BahiaIntegrationConfig{}, slogDefaultLogger())
	publisher := &legacyReconcileCapturePublisher{}
	reconciler, _ := NewLegacyAgentReconciler(integration, registry, publisher, slogDefaultLogger())

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "bravo", Name: "Bravo", Tier: domain.SoulTierStandard, Status: domain.SoulStatusActive, NostrPubkey: legacyReconcilePubkey('b'), WorkspaceRepoURL: "https://git/bravo", EventID: strings.Repeat("a", 64)}
	classification := LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusUnlinked}
	approval := LegacyReconcileApproval{AgentID: "bravo", Action: legacyAgentReconcileActionLink, ApprovedBy: "operator", ApprovalRef: "approval:1"}

	receipt, err := reconciler.ReconcileApprovedLink(t.Context(), soul, classification, approval)
	if err != nil {
		t.Fatalf("ReconcileApprovedLink() error = %v", err)
	}
	if receipt.ServiceCreated || receipt.ServiceID != existingID {
		t.Fatalf("receipt = %+v, want reuse of %s", receipt, existingID)
	}
	if receipt.Rollback.CreatedServiceID != "" {
		t.Fatalf("reuse rollback should not carry a created service id: %+v", receipt.Rollback)
	}
	services, _ := registry.ListServices(t.Context())
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1", len(services))
	}
}

func TestLegacyAgentReconciliationRefusesIneligibleWrites(t *testing.T) {
	registry, _, _, _, _, _ := newSoulFactoryRegistryHarness()
	integration, _ := NewBahiaIntegration(registry, BahiaIntegrationConfig{}, slogDefaultLogger())
	publisher := &legacyReconcileCapturePublisher{}
	reconciler, _ := NewLegacyAgentReconciler(integration, registry, publisher, slogDefaultLogger())

	activeSoul := &domain.AgentSoul{ID: uuid.New(), AgentID: "bravo", Tier: domain.SoulTierStandard, Status: domain.SoulStatusActive, NostrPubkey: legacyReconcilePubkey('b'), EventID: strings.Repeat("a", 64)}
	approval := LegacyReconcileApproval{AgentID: "bravo", Action: legacyAgentReconcileActionLink, ApprovedBy: "operator", ApprovalRef: "approval:1"}

	cases := []struct {
		name           string
		soul           *domain.AgentSoul
		classification LegacyAgentReconciliationClassification
		approval       LegacyReconcileApproval
	}{
		{"ambiguous", activeSoul, LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusAmbiguous, ReasonCode: "multiple_authoritative_matches"}, approval},
		{"orphaned", activeSoul, LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusOrphaned, ReasonCode: "no_runtime_match"}, approval},
		{"missing approval", activeSoul, LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusUnlinked}, LegacyReconcileApproval{AgentID: "bravo", Action: legacyAgentReconcileActionLink}},
		{"wrong action", activeSoul, LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusUnlinked}, LegacyReconcileApproval{AgentID: "bravo", Action: "reprovision", ApprovedBy: "operator", ApprovalRef: "approval:1"}},
		{"inactive soul", &domain.AgentSoul{ID: uuid.New(), AgentID: "bravo", Status: domain.SoulStatusSuspended, EventID: strings.Repeat("a", 64)}, LegacyAgentReconciliationClassification{AgentID: "bravo", Status: legacyReconcileStatusUnlinked}, approval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reconciler.ReconcileApprovedLink(t.Context(), tc.soul, tc.classification, tc.approval)
			if !errors.Is(err, ErrLegacyReconciliationRefused) {
				t.Fatalf("error = %v, want ErrLegacyReconciliationRefused", err)
			}
		})
	}
	if len(publisher.published) != 0 {
		t.Fatalf("refused writes published %d souls", len(publisher.published))
	}
}

func eventHasTagValue(event *nostr.Event, name, value string) bool {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
