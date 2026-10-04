package main

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func publishLastOpIntent(cmd *cobra.Command, domainName, op, coordinate, org, retryKey string, content map[string]any) (string, map[string]any, error) {
	org, err := requireIntentOrg(org)
	if err != nil {
		return "", nil, err
	}
	intentID, err := intentUUID(retryKey)
	if err != nil {
		return "", nil, err
	}
	content["intent_id"] = intentID
	var data map[string]any
	err = publishCLIIntentWithResult(cmd, client.PublishIntentRequest{
		Domain: domainName, Op: op, Coordinate: coordinate, OrgID: org, IntentID: intentID, Content: content,
	}, func(result *client.PublishIntentResult) error {
		data = result.Data
		return nil
	})
	return intentID, data, err
}

func lastOpServiceOrg(cmd *cobra.Command, serviceID string) (string, error) {
	svc, err := canonicalService(cmd, serviceID)
	if err != nil {
		return "", err
	}
	return svc.OrgID.String(), nil
}

func runArtifactRegisterIntent(cmd *cobra.Command, artifactID string, req client.RegisterArtifactNostrRequest) (*client.ArtifactCommandResult, error) {
	if err := requireContextVMUUID("build_id", req.BuildID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if req.ImageRepo == "" || req.ImageTag == "" || req.ImageDigest == "" {
		return nil, fmt.Errorf("image_repo, image_tag, and image_digest are required")
	}
	id, err := cliCreateEntityID(cmd, "artifact", artifactID)
	if err != nil {
		return nil, err
	}
	org, err := lastOpServiceOrg(cmd, req.ServiceID)
	if err != nil {
		return nil, err
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	delete(content, "idempotency_key")
	content["id"] = id
	intentID, _, err := publishLastOpIntent(cmd, "artifact", "register", "artifact:"+id, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	return &client.ArtifactCommandResult{Status: "accepted", ArtifactID: id, BuildID: req.BuildID, IntentID: intentID}, nil
}

func runArtifactImportObservedIntent(cmd *cobra.Command, req client.ImportObservedArtifactNostrRequest) (*client.ImportObservedArtifactResult, error) {
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("environment_id", req.EnvironmentID); err != nil {
		return nil, err
	}
	if req.ImageRepo == "" || req.ImageTag == "" || req.ImageDigest == "" {
		return nil, fmt.Errorf("image_repo, image_tag, and image_digest are required")
	}
	org, err := lastOpServiceOrg(cmd, req.ServiceID)
	if err != nil {
		return nil, err
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	delete(content, "idempotency_key")
	coordinate := "artifact-import:" + req.ServiceID + ":" + req.EnvironmentID + ":" + strings.ToLower(strings.TrimSpace(req.ImageDigest))
	_, data, err := publishLastOpIntent(cmd, "artifact", "import-observed", coordinate, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	result := &client.ImportObservedArtifactResult{Status: "accepted"}
	if status, ok := data["status"].(string); ok {
		result.Status = status
	}
	result.ArtifactID, _ = data["artifact_id"].(string)
	result.BuildID, _ = data["build_id"].(string)
	result.ObservationID, _ = data["observation_id"].(string)
	return result, nil
}

func runDNSDriftRemediateIntent(cmd *cobra.Command, req client.DNSDriftRemediateRequest) (*client.DNSCommandResult, error) {
	zone := strings.TrimRight(strings.ToLower(strings.TrimSpace(req.Zone)), ".")
	coordinate := "dns-remediate:all"
	if zone != "" {
		coordinate = "dns-remediate:" + zone
	}
	_, _, err := publishLastOpIntent(cmd, "dns", "drift-remediate", coordinate, operatorIntentOrg, req.IdempotencyKey, map[string]any{"zone": zone})
	if err != nil {
		return nil, err
	}
	return &client.DNSCommandResult{Action: "drift-remediate", Status: "accepted", Zone: zone}, nil
}

func runDeploymentPreviewIntent(cmd *cobra.Command, req client.DeploymentPreviewNostrRequest) (map[string]any, error) {
	for field, value := range map[string]string{"service_id": req.ServiceID, "environment_id": req.EnvironmentID, "artifact_id": req.ArtifactID} {
		if err := requireContextVMUUID(field, value); err != nil {
			return nil, err
		}
	}
	if len(req.ManagedRuntimeConfig) == 0 {
		return nil, fmt.Errorf("managed_runtime_config is required")
	}
	org, err := lastOpServiceOrg(cmd, req.ServiceID)
	if err != nil {
		return nil, err
	}
	content := map[string]any{"service_id": req.ServiceID, "environment_id": req.EnvironmentID, "artifact_id": req.ArtifactID, "managed_runtime_config": req.ManagedRuntimeConfig}
	if req.DeploymentUnitID != "" {
		content["deployment_unit_id"] = req.DeploymentUnitID
	}
	if req.Compact {
		content["compact"] = true
	}
	intentID, data, err := publishLastOpIntent(cmd, "deployment", "preview", "deployment-preview:"+req.ServiceID+":"+req.EnvironmentID, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("accepted deployment preview intent %s has no 30315 plan data", intentID)
	}
	return data, nil
}

func runRouteAttachIntent(cmd *cobra.Command, req client.RouteAttachRequest) (*client.DeploymentCommandResult, error) {
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("environment_id", req.EnvironmentID); err != nil {
		return nil, err
	}
	normalized, err := domain.NormalizePublicRouteRequest(req.PublicRoute)
	if err != nil {
		return nil, err
	}
	req.PublicRoute = normalized
	org, err := lastOpServiceOrg(cmd, req.ServiceID)
	if err != nil {
		return nil, err
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	delete(content, "idempotency_key")
	id, data, err := publishLastOpIntent(cmd, "deployment", "route-attach", "deployment-route:"+req.ServiceID+":"+req.EnvironmentID, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	result := &client.DeploymentCommandResult{Status: "accepted", IntentID: id, ServiceID: req.ServiceID, EnvironmentID: req.EnvironmentID, DeploymentUnitID: req.DeploymentUnitID}
	if attachedID, ok := data["intent_id"].(string); ok {
		result.IntentID = attachedID
	}
	return result, nil
}

func runAdoptionImportIntent(cmd *cobra.Command, req client.AdoptionImportRequest) (map[string]any, error) {
	targets, err := adoptionPayloadTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		return nil, fmt.Errorf("import requires --all or at least one selection")
	}
	org, err := requireIntentOrg(req.OrgID)
	if err != nil {
		return nil, err
	}
	content := map[string]any{"targets": targets, "selections": req.Selections, "org_id": org}
	if req.ImportAll {
		content["import_all"] = true
	}
	id, data, err := publishLastOpIntent(cmd, "adoption", "import", "adoption:"+org, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"intent_id": id, "status": "accepted"}
	if count, ok := data["candidate_count"]; ok {
		result["candidate_count"] = count
	}
	return result, nil
}
