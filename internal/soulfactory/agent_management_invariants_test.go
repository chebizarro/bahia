package soulfactory

import (
	"errors"
	"testing"
	"time"
)

// healthyInvariantFixture builds a fully-consistent managed agent: active Soul
// linked to exactly one canonical service with unit, observation, desired
// state, and an up-to-date release binding.
func healthyInvariantFixture(agentID, serviceID string) AgentManagementInvariantInput {
	now := time.Unix(1_800_000_000, 0).UTC()
	return AgentManagementInvariantInput{
		Schema: AgentManagementInvariantInputSchemaV1,
		Souls: []ManagedSoulRecord{{
			EventID: "soul-event-" + agentID, AgentID: agentID, Name: agentID, Status: "active",
			AgentPubkey: "pubkey-" + agentID, RuntimeBinding: "binding-" + agentID,
			RuntimeTarget: "openclaw", BahiaServiceID: serviceID,
			Provenance: ProvenanceCanonical, ObservedAt: now, SourceRef: "relay:soul:" + agentID,
		}},
		Services: []ManagedServiceRecord{{
			ID: serviceID, Name: soulServiceName(agentID), ArtifactRepo: soulServiceArtifactRepo(agentID),
			RuntimeType: "container", AgentManaged: true,
			Provenance: ProvenanceCanonical, SourceRef: "db:service:" + serviceID,
		}},
		Units: []ManagedUnitRecord{{
			ID: "unit-" + agentID, ServiceID: serviceID, Key: soulServiceName(agentID),
			Provenance: ProvenanceCanonical, SourceRef: "db:unit:" + agentID,
		}},
		Observations: []ManagedObservationRecord{{
			ServiceID: serviceID, AgentPubkey: "pubkey-" + agentID, RuntimeBinding: "binding-" + agentID,
			Provenance: ProvenanceCanonical, ObservedAt: now, SourceRef: "db:obs:" + agentID,
		}},
		DesiredStates: []ManagedDesiredStateRecord{{
			ServiceID: serviceID, ArtifactRef: "sha256:artifact", RunRef: "run-1",
			Provenance: ProvenanceCanonical, SourceRef: "db:desired:" + agentID,
		}},
		Releases: []ManagedReleaseRecord{{
			ServiceID: serviceID, BoundReleaseID: "rel-1", LatestReleaseID: "rel-1",
			ReleaseChannel: "stable", Provenance: ProvenanceCanonical, SourceRef: "db:release:" + agentID,
		}},
	}
}

func soulResult(t *testing.T, report AgentManagementInvariantReport, agentID string) SoulInvariantResult {
	t.Helper()
	for _, s := range report.Souls {
		if s.AgentID == agentID {
			return s
		}
	}
	t.Fatalf("report does not inventory Soul %q; souls=%+v", agentID, report.Souls)
	return SoulInvariantResult{}
}

func hasViolation(result SoulInvariantResult, code string) bool {
	for _, v := range result.Violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

// TestAgentManagementInvariantsHealthyStateInventoriesWithoutViolation covers
// the healthy fixture and proves the report is read-only and inventories the
// Soul.
func TestAgentManagementInvariantsHealthyStateInventoriesWithoutViolation(t *testing.T) {
	report, err := CheckAgentManagementInvariants(healthyInvariantFixture("scout", "svc-scout"))
	if err != nil {
		t.Fatalf("healthy fixture error = %v", err)
	}
	if !report.ReadOnly || report.MutationAllowed {
		t.Fatalf("report must be read-only and disallow mutation: %+v", report)
	}
	if report.Summary.ActiveSouls != 1 || report.Summary.Healthy != 1 || report.Summary.Violated != 0 {
		t.Fatalf("summary = %+v, want 1 active/1 healthy/0 violated", report.Summary)
	}
	if got := soulResult(t, report, "scout"); got.Status != InvariantStatusHealthy || len(got.Violations) != 0 {
		t.Fatalf("scout result = %+v, want healthy with no violations", got)
	}
	if len(report.Alerts) != 0 {
		t.Fatalf("healthy state must not alert, got %+v", report.Alerts)
	}
}

// TestAgentManagementInvariantsDetectsMissingLinks covers a Soul with no
// bahia_service_id and one whose link does not resolve.
func TestAgentManagementInvariantsDetectsMissingLinks(t *testing.T) {
	t.Run("missing bahia_service_id", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Souls[0].BahiaServiceID = ""
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		got := soulResult(t, report, "scout")
		if !hasViolation(got, InvariantMissingServiceLink) {
			t.Fatalf("violations = %+v, want %s", got.Violations, InvariantMissingServiceLink)
		}
		if report.Summary.ActiveSouls != 1 {
			t.Fatalf("violating Soul must still be inventoried: %+v", report.Summary)
		}
	})

	t.Run("dangling bahia_service_id", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Souls[0].BahiaServiceID = "svc-does-not-exist"
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantServiceNotFound) {
			t.Fatalf("violations = %+v, want %s", got.Violations, InvariantServiceNotFound)
		}
	})
}

// TestAgentManagementInvariantsDetectsDuplicateServices covers two canonical
// services matching the same agent.
func TestAgentManagementInvariantsDetectsDuplicateServices(t *testing.T) {
	input := healthyInvariantFixture("scout", "svc-scout")
	input.Services = append(input.Services, ManagedServiceRecord{
		ID: "svc-scout-duplicate", Name: soulServiceName("scout"),
		ArtifactRepo: soulServiceArtifactRepo("scout"), AgentManaged: true,
		Provenance: ProvenanceCanonical, SourceRef: "db:service:duplicate",
	})
	report, err := CheckAgentManagementInvariants(input)
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation", err)
	}
	if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantDuplicateService) {
		t.Fatalf("violations = %+v, want %s", got.Violations, InvariantDuplicateService)
	}
}

// TestAgentManagementInvariantsDetectsMismatch covers linked-service identity
// drift and inconsistent pubkey/runtime binding.
func TestAgentManagementInvariantsDetectsMismatch(t *testing.T) {
	t.Run("linked service identity drift", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Services[0].Name = "agent-someone-else"
		input.Services[0].ArtifactRepo = "agents/someone-else"
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantServiceIdentityDrift) {
			t.Fatalf("violations = %+v, want %s", got.Violations, InvariantServiceIdentityDrift)
		}
	})

	t.Run("pubkey and runtime binding mismatch", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Observations[0].AgentPubkey = "pubkey-impostor"
		input.Observations[0].RuntimeBinding = "binding-elsewhere"
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		got := soulResult(t, report, "scout")
		if !hasViolation(got, InvariantPubkeyMismatch) || !hasViolation(got, InvariantRuntimeBindingDrift) {
			t.Fatalf("violations = %+v, want pubkey and runtime binding mismatch", got.Violations)
		}
	})
}

// TestAgentManagementInvariantsDetectsStaleProjection proves relay/dashboard
// hydration is never promoted to canonical truth: a hydration observation
// behind canonical, and one with no canonical backing at all, are both flagged.
func TestAgentManagementInvariantsDetectsStaleProjection(t *testing.T) {
	t.Run("hydration behind canonical", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Observations = append(input.Observations, ManagedObservationRecord{
			ServiceID: "svc-scout", AgentPubkey: "pubkey-scout", RuntimeBinding: "binding-scout",
			Provenance: ProvenanceHydration,
			ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			SourceRef:  "dashboard:obs:scout",
		})
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantStaleProjection) {
			t.Fatalf("violations = %+v, want %s", got.Violations, InvariantStaleProjection)
		}
		if report.Summary.HydrationOnly == 0 {
			t.Fatal("hydration records must be counted as ignored for canonical truth")
		}
	})

	t.Run("hydration without canonical backing is not promoted", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		// Remove canonical observation, leave only a dashboard projection.
		input.Observations = []ManagedObservationRecord{{
			ServiceID: "svc-scout", AgentPubkey: "pubkey-scout", RuntimeBinding: "binding-scout",
			Provenance: ProvenanceHydration, ObservedAt: time.Unix(1_800_000_000, 0).UTC(),
			SourceRef: "dashboard:obs:scout",
		}}
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		got := soulResult(t, report, "scout")
		if !hasViolation(got, InvariantMissingObservation) {
			t.Fatalf("hydration must not satisfy the canonical observation invariant: %+v", got.Violations)
		}
		if !hasViolation(got, InvariantStaleProjection) {
			t.Fatalf("violations = %+v, want %s", got.Violations, InvariantStaleProjection)
		}
	})

	t.Run("hydration service is never canonical", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.Services[0].Provenance = ProvenanceHydration
		report, err := CheckAgentManagementInvariants(input)
		if !errors.Is(err, ErrAgentManagementInvariantViolation) {
			t.Fatalf("error = %v, want violation", err)
		}
		if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantServiceNotFound) {
			t.Fatalf("a hydration-only service must not resolve a canonical link: %+v", got.Violations)
		}
	})
}

// TestAgentManagementInvariantsDetectsReverseOrphan covers an agent-managed
// Bahia service with no active Soul.
func TestAgentManagementInvariantsDetectsReverseOrphan(t *testing.T) {
	input := healthyInvariantFixture("scout", "svc-scout")
	input.Services = append(input.Services, ManagedServiceRecord{
		ID: "svc-ghost", Name: soulServiceName("ghost"), ArtifactRepo: soulServiceArtifactRepo("ghost"),
		AgentManaged: true, Provenance: ProvenanceCanonical, SourceRef: "db:service:ghost",
	})
	report, err := CheckAgentManagementInvariants(input)
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation", err)
	}
	if report.Summary.ReverseOrphans != 1 || len(report.Orphans) != 1 {
		t.Fatalf("orphans = %+v summary = %+v, want exactly one reverse orphan", report.Orphans, report.Summary)
	}
	if report.Orphans[0].ServiceID != "svc-ghost" || report.Orphans[0].Violations[0].Code != InvariantReverseOrphan {
		t.Fatalf("orphan = %+v, want svc-ghost reverse orphan", report.Orphans[0])
	}
	// The healthy Soul is still inventoried alongside the orphan.
	if got := soulResult(t, report, "scout"); got.Status != InvariantStatusHealthy {
		t.Fatalf("scout should remain healthy: %+v", got)
	}
}

// TestAgentManagementInvariantsDetectsReleaseLag covers a service bound behind
// the latest verified release on its channel.
func TestAgentManagementInvariantsDetectsReleaseLag(t *testing.T) {
	input := healthyInvariantFixture("scout", "svc-scout")
	input.Releases[0].LatestReleaseID = "rel-2"
	report, err := CheckAgentManagementInvariants(input)
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation", err)
	}
	if got := soulResult(t, report, "scout"); !hasViolation(got, InvariantReleaseLag) {
		t.Fatalf("violations = %+v, want %s", got.Violations, InvariantReleaseLag)
	}
}

// TestAgentManagementInvariantsAlertsOnlyAfterBoundedRetries proves a single
// transient failure does not page, and the alert fires exactly at threshold.
func TestAgentManagementInvariantsAlertsOnlyAfterBoundedRetries(t *testing.T) {
	base := healthyInvariantFixture("scout", "svc-scout")
	base.Souls[0].BahiaServiceID = ""
	base.AlertAfterConsecutive = 3

	t.Run("first failure does not alert", func(t *testing.T) {
		report, _ := CheckAgentManagementInvariants(base)
		if len(report.Alerts) != 0 {
			t.Fatalf("alerts = %+v, want none below threshold", report.Alerts)
		}
	})

	t.Run("below threshold does not alert", func(t *testing.T) {
		input := base
		input.ConsecutivePriorFailures = map[string]int{"scout": 1}
		report, _ := CheckAgentManagementInvariants(input)
		if len(report.Alerts) != 0 {
			t.Fatalf("alerts = %+v, want none at 2/3", report.Alerts)
		}
	})

	t.Run("at threshold alerts once", func(t *testing.T) {
		input := base
		input.ConsecutivePriorFailures = map[string]int{"scout": 2}
		report, _ := CheckAgentManagementInvariants(input)
		if len(report.Alerts) != 1 {
			t.Fatalf("alerts = %+v, want exactly one at threshold", report.Alerts)
		}
		alert := report.Alerts[0]
		if alert.Subject != "scout" || alert.ConsecutiveFailures != 3 || alert.Threshold != 3 {
			t.Fatalf("alert = %+v, want scout 3/3", alert)
		}
		if len(alert.Codes) == 0 || alert.Codes[0] != InvariantMissingServiceLink {
			t.Fatalf("alert codes = %+v, want %s", alert.Codes, InvariantMissingServiceLink)
		}
	})

	t.Run("healthy subject never alerts regardless of history", func(t *testing.T) {
		input := healthyInvariantFixture("scout", "svc-scout")
		input.AlertAfterConsecutive = 1
		input.ConsecutivePriorFailures = map[string]int{"scout": 99}
		report, err := CheckAgentManagementInvariants(input)
		if err != nil {
			t.Fatalf("healthy error = %v", err)
		}
		if len(report.Alerts) != 0 {
			t.Fatalf("alerts = %+v, want none for a healthy subject", report.Alerts)
		}
	})
}

// TestAgentManagementInvariantsInventoriesEveryActiveSoul proves the report is
// a complete inventory across mixed healthy/violating agents and that
// non-active Souls are excluded from the managed inventory.
func TestAgentManagementInvariantsInventoriesEveryActiveSoul(t *testing.T) {
	input := healthyInvariantFixture("scout", "svc-scout")
	// A second active Soul that is unlinked.
	input.Souls = append(input.Souls, ManagedSoulRecord{
		EventID: "soul-event-pilot", AgentID: "pilot", Status: "active",
		Provenance: ProvenanceCanonical, SourceRef: "relay:soul:pilot",
	})
	// A revoked Soul must not be counted as a managed active Soul.
	input.Souls = append(input.Souls, ManagedSoulRecord{
		EventID: "soul-event-retired", AgentID: "retired", Status: "revoked",
		Provenance: ProvenanceCanonical, SourceRef: "relay:soul:retired",
	})
	report, err := CheckAgentManagementInvariants(input)
	if !errors.Is(err, ErrAgentManagementInvariantViolation) {
		t.Fatalf("error = %v, want violation", err)
	}
	if report.Summary.ActiveSouls != 2 || len(report.Souls) != 2 {
		t.Fatalf("inventory = %d souls (summary %+v), want exactly the 2 active Souls", len(report.Souls), report.Summary)
	}
	if report.Summary.Healthy != 1 || report.Summary.Violated != 1 {
		t.Fatalf("summary = %+v, want 1 healthy / 1 violated", report.Summary)
	}
	for _, agentID := range []string{"scout", "pilot"} {
		_ = soulResult(t, report, agentID)
	}
}

// TestAgentManagementInvariantsRejectsUnknownSchema keeps the snapshot contract
// explicit.
func TestAgentManagementInvariantsRejectsUnknownSchema(t *testing.T) {
	input := healthyInvariantFixture("scout", "svc-scout")
	input.Schema = "something-else/v9"
	if _, err := CheckAgentManagementInvariants(input); err == nil {
		t.Fatal("expected unsupported schema to be rejected")
	}
}
