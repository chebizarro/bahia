package soulfactory

import (
	"errors"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

var (
	testLegacyPubkeyA = strings.Repeat("a", 64)
	testLegacyPubkeyB = strings.Repeat("b", 64)
)

func TestClassifyLegacyAgentsNoMatch(t *testing.T) {
	report, err := ClassifyLegacyAgents(testLegacyInput(
		[]LegacyRunningAgent{{InventoryID: "runtime-1", Running: true, ManagedPubkey: testLegacyPubkeyA, SourceRef: "inventory.json#runtime-1"}},
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyClassification(t, report, "no_match", "no_authoritative_match")
}

func TestClassifyLegacyAgentsSingleStrongMatch(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, DocumentedAgentID: "donny", ManagedPubkey: testLegacyPubkeyA,
		RuntimeBinding: "openclaw:ai-02:main", ContainerName: "unrelated-container", SourceRef: "runtime.json#donny",
	}
	identity := LegacyIdentityRecord{
		AgentID: "donny", ManagedPubkey: testLegacyPubkeyA, RuntimeBinding: "openclaw:ai-02:main",
		SourceRefs: []string{"docs/agents/donny.md"},
	}
	report, err := ClassifyLegacyAgents(testLegacyInput([]LegacyRunningAgent{agent}, []LegacyIdentityRecord{identity}))
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyClassification(t, report, "operator_review_required", "single_authoritative_match_requires_operator_approval")
	classification := report.Classifications[0]
	if classification.Plan == nil || classification.Plan.AgentID != "donny" || classification.Plan.PreservePubkey != testLegacyPubkeyA {
		t.Fatalf("unexpected adoption plan: %#v", classification.Plan)
	}
	if report.MutationAllowed {
		t.Fatal("dry-run report unexpectedly permits mutation")
	}
}

func TestClassifyLegacyAgentsMultiCandidateAmbiguity(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, ManagedPubkey: testLegacyPubkeyA,
		RuntimeBinding: "openclaw:shared", SourceRef: "runtime.json#shared",
	}
	identities := []LegacyIdentityRecord{
		{AgentID: "agent-a", ManagedPubkey: testLegacyPubkeyA, RuntimeBinding: "openclaw:shared", SourceRefs: []string{"identity-a"}},
		{AgentID: "agent-b", ManagedPubkey: testLegacyPubkeyA, RuntimeBinding: "openclaw:shared", SourceRefs: []string{"identity-b"}},
	}
	report, err := ClassifyLegacyAgents(testLegacyInput([]LegacyRunningAgent{agent}, identities))
	if !errors.Is(err, ErrLegacyAdoptionAmbiguous) {
		t.Fatalf("error = %v, want ErrLegacyAdoptionAmbiguous", err)
	}
	assertLegacyClassification(t, report, "ambiguous", "multiple_authoritative_matches")
	if report.Summary.Ambiguous != 1 || report.Classifications[0].Plan != nil {
		t.Fatalf("ambiguous report must refuse a plan: %#v", report)
	}
}

func TestClassifyLegacyAgentsIgnoresNameOnlyFalsePositive(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, DisplayName: "Stew", ContainerName: "stew", SourceRef: "runtime.json#stew",
	}
	identity := LegacyIdentityRecord{AgentID: "stew", DisplayName: "Stew", SourceRefs: []string{"docs/agents/stew.md"}}
	report, err := ClassifyLegacyAgents(testLegacyInput([]LegacyRunningAgent{agent}, []LegacyIdentityRecord{identity}))
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyClassification(t, report, "no_match", "no_authoritative_match")
}

func TestClassifyLegacyAgentsContainerNameMismatchDoesNotOverrideIdentity(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, DocumentedAgentID: "roost",
		Workspace: "https://git.example/fleet/roost", ContainerName: "gateway-production-17", SourceRef: "runtime.json#roost",
	}
	identity := LegacyIdentityRecord{
		AgentID: "roost", Workspace: "https://git.example/fleet/roost", DisplayName: "Roost",
		SourceRefs: []string{"docs/agents/roost.md"},
	}
	report, err := ClassifyLegacyAgents(testLegacyInput([]LegacyRunningAgent{agent}, []LegacyIdentityRecord{identity}))
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyClassification(t, report, "operator_review_required", "single_authoritative_match_requires_operator_approval")
}

func TestClassifyLegacyAgentsConflictingIdentityEvidenceIsAmbiguous(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, DocumentedAgentID: "donny",
		ManagedPubkey: testLegacyPubkeyA, SourceRef: "runtime.json#donny",
	}
	identity := LegacyIdentityRecord{
		AgentID: "donny", ManagedPubkey: testLegacyPubkeyB, SourceRefs: []string{"docs/agents/donny.md"},
	}
	report, err := ClassifyLegacyAgents(testLegacyInput([]LegacyRunningAgent{agent}, []LegacyIdentityRecord{identity}))
	if !errors.Is(err, ErrLegacyAdoptionAmbiguous) {
		t.Fatalf("error = %v, want ErrLegacyAdoptionAmbiguous", err)
	}
	assertLegacyClassification(t, report, "ambiguous", "identity_evidence_conflict")
}

func TestClassifyLegacyAgentsTrustedSoulPreventsReadoption(t *testing.T) {
	agent := LegacyRunningAgent{
		InventoryID: "runtime-1", Running: true, DocumentedAgentID: "donny",
		ManagedPubkey: testLegacyPubkeyA, SourceRef: "runtime.json#donny",
	}
	identity := LegacyIdentityRecord{
		AgentID: "donny", ManagedPubkey: testLegacyPubkeyA, SourceRefs: []string{"docs/agents/donny.md"},
	}
	input := testLegacyInput([]LegacyRunningAgent{agent}, []LegacyIdentityRecord{identity})
	input.TrustedSouls = []LegacyTrustedSoul{{
		Kind: domain.KindAgentSoul, EventID: strings.Repeat("c", 64), AgentID: "donny",
		AgentPubkey: testLegacyPubkeyA, Trusted: true, SourceRef: "relay-capture.json#donny",
	}}
	report, err := ClassifyLegacyAgents(input)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyClassification(t, report, "already_trusted", "trusted_soul_exists")
	if report.Classifications[0].Plan != nil {
		t.Fatal("already trusted identity must not receive a re-adoption plan")
	}
}

func testLegacyInput(agents []LegacyRunningAgent, identities []LegacyIdentityRecord) LegacyAdoptionInput {
	return LegacyAdoptionInput{Schema: LegacyAdoptionInputSchemaV1, RunningAgents: agents, IdentityRecords: identities}
}

func assertLegacyClassification(t *testing.T, report LegacyAdoptionReport, status, reason string) {
	t.Helper()
	if len(report.Classifications) != 1 {
		t.Fatalf("classifications = %d, want 1", len(report.Classifications))
	}
	got := report.Classifications[0]
	if got.Status != status || got.ReasonCode != reason {
		t.Fatalf("classification = %s/%s, want %s/%s", got.Status, got.ReasonCode, status, reason)
	}
}
