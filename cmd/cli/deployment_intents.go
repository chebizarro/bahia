package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func deploymentIntentUUID(field, value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("%s must be a non-nil UUID", field)
	}
	return id.String(), nil
}

func publishDeploymentCLIIntent(cmd *cobra.Command, domain, op, org, coordinate, retryKey string, content map[string]interface{}) (string, error) {
	org, err := requireIntentOrg(org)
	if err != nil {
		return "", err
	}
	intentID, err := intentUUID(retryKey)
	if err != nil {
		return "", err
	}
	content["intent_id"] = intentID
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{
		Domain: domain, Op: op, Coordinate: coordinate, OrgID: org, IntentID: intentID, Content: content,
	}); err != nil {
		return "", err
	}
	return intentID, nil
}

func runDeploymentCreateIntent(cmd *cobra.Command, org string, req client.DeploymentIntentNostrRequest) (*client.DeploymentCommandResult, error) {
	serviceID, err := deploymentIntentUUID("service_id", req.ServiceID)
	if err != nil {
		return nil, err
	}
	environmentID, err := deploymentIntentUUID("environment_id", req.EnvironmentID)
	if err != nil {
		return nil, err
	}
	artifactID, err := deploymentIntentUUID("artifact_id", req.ArtifactID)
	if err != nil {
		return nil, err
	}
	content := map[string]interface{}{"service_id": serviceID, "environment_id": environmentID, "artifact_id": artifactID}
	if req.DeploymentUnitID != "" {
		content["deployment_unit_id"], err = deploymentIntentUUID("deployment_unit_id", req.DeploymentUnitID)
		if err != nil {
			return nil, err
		}
	}
	if req.ExpectedDesiredStateHash != "" {
		content["expected_desired_state_hash"] = req.ExpectedDesiredStateHash
	}
	intentID, err := publishDeploymentCLIIntent(cmd, "deployment", "create", org, serviceID+":"+environmentID, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	return &client.DeploymentCommandResult{Status: "accepted", IntentID: intentID, ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: req.DeploymentUnitID, ArtifactID: artifactID}, nil
}

func runDeploymentRollbackIntent(cmd *cobra.Command, org string, req client.RollbackDeploymentNostrRequest) (*client.DeploymentCommandResult, error) {
	serviceID, err := deploymentIntentUUID("service_id", req.ServiceID)
	if err != nil {
		return nil, err
	}
	environmentID, err := deploymentIntentUUID("environment_id", req.EnvironmentID)
	if err != nil {
		return nil, err
	}
	artifactID, err := deploymentIntentUUID("target_artifact_id", req.TargetArtifactID)
	if err != nil {
		return nil, err
	}
	supersedesID, err := deploymentIntentUUID("supersedes_intent_id", req.SupersedesIntentID)
	if err != nil {
		return nil, err
	}
	content := map[string]interface{}{"service_id": serviceID, "environment_id": environmentID, "target_artifact_id": artifactID, "supersedes_intent_id": supersedesID}
	if req.DeploymentUnitID != "" {
		content["deployment_unit_id"], err = deploymentIntentUUID("deployment_unit_id", req.DeploymentUnitID)
		if err != nil {
			return nil, err
		}
	}
	intentID, err := publishDeploymentCLIIntent(cmd, "deployment", "rollback", org, serviceID+":"+environmentID, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	return &client.DeploymentCommandResult{Status: "accepted", IntentID: intentID, ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: req.DeploymentUnitID, ArtifactID: artifactID}, nil
}

func runDeploymentDecisionIntent(cmd *cobra.Command, org string, req client.DeploymentApprovalNostrRequest, revision string) (*client.DeploymentCommandResult, error) {
	deploymentID, err := deploymentIntentUUID("deployment_intent_id", req.IntentID)
	if err != nil {
		return nil, err
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return nil, fmt.Errorf("decision must be approve or reject")
	}
	content := map[string]interface{}{"deployment_intent_id": deploymentID}
	if revision != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, revision)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid expected_updated_at: %w", parseErr)
		}
		content["expected_updated_at"] = parsed.Format(time.RFC3339Nano)
	}
	intentID, err := publishDeploymentCLIIntent(cmd, "deployment", req.Decision, org, deploymentID, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	return &client.DeploymentCommandResult{Status: "accepted", IntentID: intentID}, nil
}

func runRuntimeIntent(cmd *cobra.Command, org, action, service, environment, artifact, retryKey string) (*client.RuntimeActionResult, error) {
	if action != "deploy" && action != "restart" && action != "stop" {
		return nil, fmt.Errorf("unsupported runtime action %q", action)
	}
	serviceID, err := deploymentIntentUUID("service_id", service)
	if err != nil {
		return nil, err
	}
	environmentID, err := deploymentIntentUUID("environment_id", environment)
	if err != nil {
		return nil, err
	}
	content := map[string]interface{}{"service_id": serviceID, "environment_id": environmentID}
	if artifact != "" {
		if action != "deploy" {
			return nil, fmt.Errorf("artifact_id is only valid for deploy")
		}
		content["artifact_id"], err = deploymentIntentUUID("artifact_id", artifact)
		if err != nil {
			return nil, err
		}
	}
	_, err = publishDeploymentCLIIntent(cmd, "runtime", action, org, serviceID+":"+environmentID, retryKey, content)
	if err != nil {
		return nil, err
	}
	return &client.RuntimeActionResult{Action: action, ServiceID: serviceID, EnvironmentID: environmentID}, nil
}
