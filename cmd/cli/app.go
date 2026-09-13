package main

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

type OnboardStep string

const (
	OnboardStepService     OnboardStep = "service"
	OnboardStepEnvironment OnboardStep = "environment"
	OnboardStepPolicy      OnboardStep = "policy"
)

var onboardStepOrder = []OnboardStep{
	OnboardStepService,
	OnboardStepEnvironment,
	OnboardStepPolicy,
}

type OnboardReport struct {
	ServiceID      string       `json:"service_id,omitempty"`
	EnvironmentID  string       `json:"environment_id,omitempty"`
	PolicyID       string       `json:"policy_id,omitempty"`
	CompletedSteps []OnboardStep `json:"completed_steps"`
	Message        string       `json:"message,omitempty"`
	Error          string       `json:"error,omitempty"`
}

func appCommands() *cobra.Command {
	onboardCmd := &cobra.Command{
		Use:   "onboard",
		Short: "Create and register a new service with its environment, policy, and monitoring",
		Long: `Creates or adopts the full set of Bahia entities for a deployable app:
- Service (with Git source and artifact repository)
- Environment with a default deployment unit
- Pipeline policy for deployment gating

Partial failure reports which steps completed. Re-running with the same
idempotency key is safe and will not duplicate entities.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			orgID, _ := cmd.Flags().GetString("org")
			artifactRepo, _ := cmd.Flags().GetString("artifact-repo")
			repoURL, _ := cmd.Flags().GetString("repo-url")
			repoSource, _ := cmd.Flags().GetString("repo-source")
			repoCoordinate, _ := cmd.Flags().GetString("repo-coordinate")
			cloneURL, _ := cmd.Flags().GetString("clone-url")
			webURL, _ := cmd.Flags().GetString("web-url")
			ciProvider, _ := cmd.Flags().GetString("ci-provider")
			ciWorkflow, _ := cmd.Flags().GetString("ci-workflow")
			defaultBranch, _ := cmd.Flags().GetString("default-branch")
			runtimeType, _ := cmd.Flags().GetString("runtime-type")
			envName, _ := cmd.Flags().GetString("environment")
			strategy, _ := cmd.Flags().GetString("strategy")
			policyName, _ := cmd.Flags().GetString("policy")
			idempotencyKey, _ := cmd.Flags().GetString("idempotency-key")

			if name == "" {
				return fmt.Errorf("service name is required")
			}
			if artifactRepo == "" {
				return fmt.Errorf("artifact repository is required")
			}
			if envName == "" {
				return fmt.Errorf("environment name is required")
			}

			report := OnboardReport{CompletedSteps: []OnboardStep{}}

			// Step 1: Create service with Git source (repo boundary + service).
			var repository *client.RepositoryRefRequest
			if repoSource != "" || repoCoordinate != "" || cloneURL != "" || webURL != "" || ciProvider != "" || ciWorkflow != "" {
				repository = &client.RepositoryRefRequest{
					Source:         repoSource,
					RepoCoordinate: repoCoordinate,
					CloneURL:       firstNonEmpty(cloneURL, repoURL),
					WebURL:         webURL,
				}
				if ciProvider != "" || ciWorkflow != "" {
					repository.CI = &client.ServiceCIConfigRequest{Provider: ciProvider, WorkflowPath: ciWorkflow}
				}
			}
			stepKey := idempotencyKey + ":service"
			svcResult, err := runServiceCreateNostr(cmd, client.CreateServiceNostrRequest{
				OrgID:          orgID,
				Name:           name,
				RepoURL:        repoURL,
				Repository:     repository,
				ArtifactRepo:   artifactRepo,
				DefaultBranch:  defaultBranch,
				RuntimeType:    runtimeType,
				IdempotencyKey: stepKey,
			})
			if err != nil {
				return fmt.Errorf("onboard service step failed: %w (completed steps: %v)", err, report.CompletedSteps)
			}
			serviceID := svcResult.ServiceID
			if svcResult.Service != nil {
				serviceID = svcResult.Service.ID.String()
			}
			if serviceID == "" {
				return fmt.Errorf("onboard service step returned empty service id (completed steps: %v)", report.CompletedSteps)
			}
			report.ServiceID = serviceID
			report.CompletedSteps = append(report.CompletedSteps, OnboardStepService)

			// Step 2: Create environment with default deployment unit.
			envResult, err := runEnvironmentCreateNostr(cmd, client.CreateEnvironmentNostrRequest{
				OrgID:          orgID,
				Name:           envName,
				DeployStrategy: strings.TrimSpace(strategy),
			})
			if err != nil {
				return fmt.Errorf("onboard environment step failed: %w (completed steps: %v)", err, report.CompletedSteps)
			}
			envID := envResult.EnvironmentID
			if envResult.Environment != nil {
				envID = envResult.Environment.ID.String()
			}
			if envID == "" {
				return fmt.Errorf("onboard environment step returned empty environment id (completed steps: %v)", report.CompletedSteps)
			}
			report.EnvironmentID = envID
			report.CompletedSteps = append(report.CompletedSteps, OnboardStepEnvironment)

			// Step 3: Create pipeline policy.
			if policyName != "" {
				stepKey = idempotencyKey + ":policy"
				enabled := true
				policyResult, err := runPolicyCreateNostrFirst(cmd, controlplane.PolicyMutationCommand{
					Name:           policyName,
					EnvironmentID:  nil,
					Rules:          []domain.PolicyRule{},
					Enforcement:    "warn",
					Enabled:        &enabled,
					IdempotencyKey: stepKey,
				})
				if err != nil {
					return fmt.Errorf("onboard policy step failed: %w (completed steps: %v)", err, report.CompletedSteps)
				}
				report.PolicyID = policyResult.RequestEventID
				report.CompletedSteps = append(report.CompletedSteps, OnboardStepPolicy)
			}

			report.Message = fmt.Sprintf("onboard complete for %s in %s", name, envName)
			return outputSingle(report)
		},
	}

	onboardCmd.Flags().String("name", "", "Service name")
	onboardCmd.Flags().String("org", "", "Organization UUID")
	onboardCmd.Flags().String("artifact-repo", "", "Artifact repository")
	onboardCmd.Flags().String("repo-url", "", "Repository clone URL")
	onboardCmd.Flags().String("repo-source", "", "Repository source")
	onboardCmd.Flags().String("repo-coordinate", "", "Repository coordinate (owner/repo)")
	onboardCmd.Flags().String("clone-url", "", "Repository clone URL")
	onboardCmd.Flags().String("web-url", "", "Repository web URL")
	onboardCmd.Flags().String("ci-provider", "", "CI provider")
	onboardCmd.Flags().String("ci-workflow", "", "CI workflow path")
	onboardCmd.Flags().String("default-branch", "main", "Default branch")
	onboardCmd.Flags().String("runtime-type", string(domain.RuntimeTypeCompose), "Runtime type")
	onboardCmd.Flags().String("environment", "", "Environment name")
	onboardCmd.Flags().String("strategy", string(domain.DeployStrategyReplace), "Deploy strategy")
	onboardCmd.Flags().String("policy", "", "Pipeline policy name (optional)")
	onboardCmd.Flags().String("idempotency-key", "", "Global idempotency key for resumable workflow")

	_ = onboardCmd.MarkFlagRequired("name")
	_ = onboardCmd.MarkFlagRequired("artifact-repo")
	_ = onboardCmd.MarkFlagRequired("environment")

	appCmd := &cobra.Command{Use: "app", Short: "Manage applications"}
	appCmd.AddCommand(onboardCmd)
	return appCmd
}
