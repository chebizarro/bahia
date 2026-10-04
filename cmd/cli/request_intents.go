package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func requireCLIUUID(label, raw string) error {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("%s must be a non-nil UUID", label)
	}
	return nil
}

func publishCLIRequestIntent(cmd *cobra.Command, domain, op, coordinate, org, retryKey string, content map[string]any) (string, map[string]any, error) {
	org, err := requireIntentOrg(org)
	if err != nil {
		return "", nil, err
	}
	intentID, err := intentUUID(retryKey)
	if err != nil {
		return "", nil, err
	}
	var data map[string]any
	err = publishCLIIntentWithResult(cmd, client.PublishIntentRequest{
		Domain: domain, Op: op, Coordinate: coordinate, OrgID: org, IntentID: intentID, Content: content,
	}, func(result *client.PublishIntentResult) error {
		data = result.Data
		return nil
	})
	return intentID, data, err
}

func runBuildRequestIntent(cmd *cobra.Command, req client.BuildRequestNostrRequest) (*client.BuildCommandResult, error) {
	if err := requireCLIUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireCLIUUID("repository_credential_ref", req.RepositoryCredentialRef); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GitRef) == "" || strings.TrimSpace(req.ArtifactRepo) == "" {
		return nil, fmt.Errorf("git_ref and artifact_repo are required")
	}
	org, err := lastOpServiceOrg(cmd, req.ServiceID)
	if err != nil {
		return nil, err
	}
	if operatorIntentOrg != "" {
		configuredOrg, err := requireIntentOrg(operatorIntentOrg)
		if err != nil {
			return nil, err
		}
		if configuredOrg != org {
			return nil, fmt.Errorf("--org does not match the canonical service organization")
		}
	}
	content, err := jsonObject(req)
	if err != nil {
		return nil, err
	}
	if req.BuildArgs == nil {
		content["build_args"] = map[string]string{}
	}
	intentID, data, err := publishCLIRequestIntent(cmd, "build", "request", "build-request:"+req.ServiceID, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("accepted build request intent %s has no 30315 result data", intentID)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode build request status data: %w", err)
	}
	var result client.BuildCommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode build request status data: %w", err)
	}
	if result.BuildID == "" {
		return nil, fmt.Errorf("accepted build request intent %s has no build_id in 30315 result data", intentID)
	}
	result.IntentID = intentID
	return &result, nil
}

func adoptionPayloadTargets(targets []client.AdoptionTarget) ([]map[string]string, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one target is required")
	}
	result := make([]map[string]string, 0, len(targets))
	for _, target := range targets {
		if target.DockerHost != "" {
			return nil, fmt.Errorf("raw Docker hosts are not supported; use endpoint_ref")
		}
		if target.Name == "" || target.EndpointRef == "" {
			return nil, fmt.Errorf("adoption target requires name and endpoint_ref")
		}
		entry := map[string]string{"name": target.Name, "endpoint_ref": target.EndpointRef}
		if target.EnvironmentName != "" {
			entry["environment_name"] = target.EnvironmentName
		}
		result = append(result, entry)
	}
	return result, nil
}

type adoptionScanFinding struct {
	TargetName                  string `json:"target_name"`
	EndpointRef                 string `json:"endpoint_ref,omitempty"`
	ContainerID                 string `json:"container_id,omitempty"`
	ContainerName               string `json:"container_name,omitempty"`
	ImageRef                    string `json:"image_ref,omitempty"`
	ProposedServiceName         string `json:"proposed_service_name,omitempty"`
	ExistingServiceID           string `json:"existing_service_id,omitempty"`
	WillUpdate                  bool   `json:"will_update,omitempty"`
	Adoptable                   bool   `json:"adoptable"`
	WarningsCount               int    `json:"warnings_count,omitempty"`
	RedactedEnvironmentKeyCount int    `json:"redacted_environment_key_count,omitempty"`
	RedactedLabelKeyCount       int    `json:"redacted_label_key_count,omitempty"`
	ScanFailed                  bool   `json:"scan_failed,omitempty"`
	ItemTruncated               bool   `json:"item_truncated,omitempty"`
}

type adoptionScanStatus struct {
	IntentID      string                `json:"intent_id"`
	Findings      []adoptionScanFinding `json:"findings"`
	Offset        int                   `json:"offset"`
	Limit         int                   `json:"limit"`
	NextOffset    int                   `json:"next_offset"`
	TotalFindings int                   `json:"total_findings"`
	Truncated     bool                  `json:"truncated"`
}

func runAdoptionScanIntent(cmd *cobra.Command, req client.AdoptionScanRequest) (*adoptionScanStatus, error) {
	targets, err := adoptionPayloadTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	if req.Offset < 0 || req.Limit < 1 || req.Limit > 100 {
		return nil, fmt.Errorf("adoption scan offset must be non-negative and limit must be 1..100")
	}
	org, err := requireIntentOrg(operatorIntentOrg)
	if err != nil {
		return nil, err
	}
	content := map[string]any{"targets": targets, "offset": req.Offset, "limit": req.Limit}
	intentID, data, err := publishCLIRequestIntent(cmd, "adoption", "scan", "adoption:"+org, org, req.IdempotencyKey, content)
	if err != nil {
		return nil, err
	}
	if data == nil || data["findings"] == nil {
		return nil, fmt.Errorf("accepted adoption scan intent %s has no 30315 findings data", intentID)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode adoption scan status data: %w", err)
	}
	var result adoptionScanStatus
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode adoption scan status data: %w", err)
	}
	result.IntentID = intentID
	return &result, nil
}
