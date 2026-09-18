package soulfactory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

const wizardTestDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

var wizardTestServiceID = uuid.MustParse("11111111-2222-3333-4444-555555555555")

func validWizardCanaryInput() WizardCanaryInput {
	return WizardCanaryInput{
		Schema: WizardCanaryInputSchemaV1,
		Soul: LegacyReconcileSoul{
			EventID: "wizard-soul-event", AgentID: WizardCanaryAgentID, Name: "Wizard", Status: "active",
			AgentPubkey: WizardCanaryPubkey, RuntimeBinding: "max/wizard-dock",
			CustodyRef: "custody:wizard", SourceRef: "relay:soul:wizard",
		},
		Runtime: LegacyRunningAgent{
			InventoryID: "max/wizard-dock", Running: true,
			DocumentedAgentID: WizardCanaryAgentID, ManagedPubkey: WizardCanaryPubkey,
			RuntimeBinding: "max/wizard-dock", ContainerName: "wizard-dock",
			CustodyRef: "custody:wizard", SourceRef: "inventory:max",
			AdoptedRuntime: &domain.AdoptedRuntimeConfig{
				TargetName: WizardCanaryRuntimeSource, SourceRuntime: WizardCanaryRuntimeSource,
				HostAlias: "max", ImageDigest: wizardTestDigest,
				Volumes: []string{"/srv/wizard/state:/state"},
			},
		},
		Placement: LegacyReviewedPlacement{
			Ref: "operator-review:wizard-canary", EnvironmentID: uuid.NewString(),
			DeploymentUnitKey: WizardCanaryDeploymentUnitKey(),
		},
		TargetServiceID: wizardTestServiceID.String(),
		SecretRefs: []domain.SecretRef{{
			ID: uuid.New(), ServiceID: wizardTestServiceID, Name: "WIZARD_RELAY_TOKEN",
		}},
		RollbackBaseline: WizardRollbackBaseline{
			SoulEventID: "wizard-soul-event", RuntimeImageDigest: wizardTestDigest,
			CapturedAt: time.Unix(1_800_000_000, 0).UTC(), Ref: "baseline:wizard:1",
		},
	}
}

// TestPlanWizardCanaryHappyPathPreservesIdentityAndDefersLiveExecution proves a
// valid canary emits an operator plan that preserves identity and state and
// never claims to have performed the live rollout.
func TestPlanWizardCanaryHappyPathPreservesIdentityAndDefersLiveExecution(t *testing.T) {
	plan, err := PlanWizardCanary(validWizardCanaryInput())
	if err != nil {
		t.Fatalf("PlanWizardCanary() error = %v", err)
	}
	if !plan.ReadOnly || !plan.RequiresOperatorExecution {
		t.Fatalf("plan must be read-only and defer execution to an operator: %+v", plan)
	}
	if plan.PreservedAgentPubkey != WizardCanaryPubkey {
		t.Fatalf("preserved pubkey = %q, want the pinned Wizard identity", plan.PreservedAgentPubkey)
	}
	if plan.AdoptedRuntimeSource != WizardCanaryRuntimeSource || plan.AdoptedImageDigest != wizardTestDigest {
		t.Fatalf("plan must adopt the exact wizard-dock runtime by digest: %+v", plan)
	}
	if len(plan.PreservedVolumes) != 1 || plan.PreservedVolumes[0] != "/srv/wizard/state:/state" {
		t.Fatalf("persistent state volumes must be preserved: %+v", plan.PreservedVolumes)
	}
	if plan.PreservedRuntimeBinding != "max/wizard-dock" || plan.PreservedCustodyRef != "custody:wizard" {
		t.Fatalf("binding/custody must be carried unchanged: %+v", plan)
	}
	// The plan feeds the supported reconciliation surface verbatim.
	if plan.ReconciliationRequest.AgentID != WizardCanaryAgentID ||
		len(plan.ReconciliationRequest.RuntimeAgents) != 1 ||
		plan.ReconciliationRequest.ReviewedPlacement.DeploymentUnitKey != WizardCanaryDeploymentUnitKey() {
		t.Fatalf("reconciliation request malformed: %+v", plan.ReconciliationRequest)
	}
	if len(plan.ProhibitedActions) == 0 || len(plan.OperatorSteps) == 0 || len(plan.LiveAcceptanceChecks) == 0 {
		t.Fatalf("plan must enumerate prohibited actions, operator steps, and live acceptance checks: %+v", plan)
	}
	// Live acceptance evidence is explicitly deferred, never asserted here.
	joined := strings.Join(plan.LiveAcceptanceChecks, "|")
	for _, want := range []string{"EOSE", "fleet_tasks", "readiness", "rollback"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live acceptance checks missing %q: %+v", want, plan.LiveAcceptanceChecks)
		}
	}
	// Secret references must be safe: scoped, and carrying no secret value.
	if len(plan.ServiceScopedSecretRefs) != 1 || plan.ServiceScopedSecretRefs[0].ServiceID != wizardTestServiceID.String() {
		t.Fatalf("secret refs must be service-scoped: %+v", plan.ServiceScopedSecretRefs)
	}
}

// TestPlanWizardCanaryPinsAuthoritativeIdentity proves the canary refuses any
// identity deviation so a mis-targeted adoption never reaches an operator.
func TestPlanWizardCanaryPinsAuthoritativeIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WizardCanaryInput)
	}{
		{"soul pubkey mismatch", func(in *WizardCanaryInput) {
			in.Soul.AgentPubkey = "deadbeef" + strings.Repeat("0", 56)
		}},
		{"runtime managed pubkey mismatch", func(in *WizardCanaryInput) {
			in.Runtime.ManagedPubkey = "deadbeef" + strings.Repeat("0", 56)
		}},
		{"wrong agent id", func(in *WizardCanaryInput) { in.Soul.AgentID = "not-wizard" }},
		{"soul not active", func(in *WizardCanaryInput) { in.Soul.Status = "suspended" }},
		{"missing soul event id", func(in *WizardCanaryInput) { in.Soul.EventID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validWizardCanaryInput()
			tc.mutate(&input)
			if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
				t.Fatalf("error = %v, want refusal", err)
			}
		})
	}
}

// TestPlanWizardCanaryRequiresExactWizardDockRuntime proves the canary adopts
// only the real wizard-dock runtime and never infers identity from a container
// name alone.
func TestPlanWizardCanaryRequiresExactWizardDockRuntime(t *testing.T) {
	t.Run("runtime not running", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.Running = false
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("missing adopted runtime", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime = nil
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("different runtime", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.ContainerName = "something-else"
		input.Runtime.AdoptedRuntime.SourceRuntime = "other-dock"
		input.Runtime.AdoptedRuntime.TargetName = "other-dock"
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("look-alike container name is not sufficient identity", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime.SourceRuntime = "impostor"
		input.Runtime.AdoptedRuntime.TargetName = "impostor"
		input.Runtime.ContainerName = WizardCanaryRuntimeSource
		input.Runtime.DocumentedAgentID = "" // no documented corroboration
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("container name alone must not establish identity; error = %v", err)
		}
	})
	t.Run("non-immutable image digest", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime.ImageDigest = "latest"
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
}

// TestPlanWizardCanaryRequiresPreservedStateAndBindings proves persistent state
// and identity-adjacent bindings cannot be dropped or changed.
func TestPlanWizardCanaryRequiresPreservedStateAndBindings(t *testing.T) {
	t.Run("no persistent volumes", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime.Volumes = nil
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("runtime binding disagreement", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.RuntimeBinding = "lemmy/wizard-dock"
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("binding change must be refused; error = %v", err)
		}
	})
}

// TestPlanWizardCanaryRequiresDedicatedReviewedPlacement proves the canary will
// not share a default unit and needs operator-reviewed placement.
func TestPlanWizardCanaryRequiresDedicatedReviewedPlacement(t *testing.T) {
	t.Run("missing reviewed placement", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Placement = LegacyReviewedPlacement{}
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("missing dedicated unit key", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Placement.DeploymentUnitKey = ""
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	// Reviewer adversarial case: wizard-dock running on the wrong host.
	t.Run("wizard-dock on edge-01 is refused; planner must require max", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime.HostAlias = "edge-01"
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("accepted wizard-dock on edge-01; error = %v", err)
		}
	})
	// Reviewer adversarial case: the shared/default deployment unit.
	t.Run("shared/default deployment unit is refused", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Placement.DeploymentUnitKey = domain.DefaultDeploymentUnitKey
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("accepted shared/default deployment unit; error = %v", err)
		}
	})
	t.Run("another agent's dedicated unit is refused", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Placement.DeploymentUnitKey = soulServiceName("someone-else")
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("accepted a foreign dedicated unit; error = %v", err)
		}
	})
	t.Run("host comparison is exact but case-insensitive", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.Runtime.AdoptedRuntime.HostAlias = "MAX"
		if _, err := PlanWizardCanary(input); err != nil {
			t.Fatalf("case-insensitive max host should be accepted: %v", err)
		}
	})
}

// TestPlanWizardCanaryRequiresServiceScopedSecrets proves global or
// foreign-scoped secrets are refused.
func TestPlanWizardCanaryRequiresServiceScopedSecrets(t *testing.T) {
	t.Run("unscoped secret", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.SecretRefs = []domain.SecretRef{{ID: uuid.New(), Name: "GLOBAL_TOKEN"}}
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
	t.Run("secret scoped to a different service", func(t *testing.T) {
		input := validWizardCanaryInput()
		input.SecretRefs = []domain.SecretRef{{ID: uuid.New(), ServiceID: uuid.New(), Name: "OTHER_TOKEN"}}
		if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
			t.Fatalf("error = %v, want refusal", err)
		}
	})
}

// TestPlanWizardCanaryRequiresImmutableRollbackBaseline proves a rollout is
// never planned without a durable, immutable baseline to return to.
func TestPlanWizardCanaryRequiresImmutableRollbackBaseline(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WizardCanaryInput)
	}{
		{"missing baseline", func(in *WizardCanaryInput) { in.RollbackBaseline = WizardRollbackBaseline{} }},
		{"baseline soul event is not the current soul", func(in *WizardCanaryInput) {
			in.RollbackBaseline.SoulEventID = "some-older-event"
		}},
		{"mutable runtime digest", func(in *WizardCanaryInput) {
			in.RollbackBaseline.RuntimeImageDigest = "latest"
		}},
		{"missing capture metadata", func(in *WizardCanaryInput) {
			in.RollbackBaseline.CapturedAt = time.Time{}
			in.RollbackBaseline.Ref = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validWizardCanaryInput()
			tc.mutate(&input)
			if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
				t.Fatalf("error = %v, want refusal", err)
			}
		})
	}
}

// TestPlanWizardCanaryRejectsUnknownSchema keeps the snapshot contract explicit.
func TestPlanWizardCanaryRejectsUnknownSchema(t *testing.T) {
	input := validWizardCanaryInput()
	input.Schema = "other/v1"
	if _, err := PlanWizardCanary(input); !errors.Is(err, ErrWizardCanaryRefused) {
		t.Fatalf("error = %v, want refusal", err)
	}
}

// TestWizardCanaryProhibitedActionsCoverForbiddenChanges pins the boundary the
// task forbids so it is inherited by the executing operator.
func TestWizardCanaryProhibitedActionsCoverForbiddenChanges(t *testing.T) {
	plan, err := PlanWizardCanary(validWizardCanaryInput())
	if err != nil {
		t.Fatalf("PlanWizardCanary() error = %v", err)
	}
	joined := strings.ToLower(strings.Join(plan.ProhibitedActions, "|"))
	for _, want := range []string{"key", "acl", "grant", "binding", "custody"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("prohibited actions must forbid %q changes: %+v", want, plan.ProhibitedActions)
		}
	}
}
