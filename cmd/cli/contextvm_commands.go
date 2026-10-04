package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

// requestCLIContextVM is only for operations with no registered daemon intent
// handler. ContextVMRequestClient always carries a d-tag idempotency key, using
// the explicit key when provided and a deterministic method/payload key otherwise.
func requestCLIContextVM(cmd *cobra.Command, method string, payload any, tags nostr.Tags, explicitKey, label string, result any) (retErr error) {
	signer, closeSigner, err := newCLIReadSigner(cmd)
	if err != nil {
		return err
	}
	if closeSigner != nil {
		defer func() { retErr = errors.Join(retErr, closeSigner()) }()
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return err
	}
	service := resolveOperatorServicePubkey(cmd)
	if service == "" {
		return fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for ContextVM")
	}
	pubkey, err := signer.GetPublicKey(cmd.Context())
	if err != nil {
		return err
	}
	cfg := client.ContextVMRequestConfig{Relays: relays, Signer: signer, SenderPubkey: pubkey.Hex(), RecipientPubkey: service, Encrypted: operatorEncrypted, ResultTimeout: operatorResultTimeout, ResultRetries: &operatorResultRetries}
	requester, err := client.NewContextVMRequestClient(cfg, client.WithContextVMRequestLogger(newCLIOperatorLogger(cmd.ErrOrStderr())))
	if err != nil {
		return err
	}
	defer requester.Close()
	if explicitKey != "" {
		tags = append(nostr.Tags{{"d", explicitKey}}, tags...)
	}
	if operatorHTTPFallback {
		fmt.Fprintln(cmd.ErrOrStderr(), "--http-fallback has no effect for this command; using keyed ContextVM")
	}
	event, err := requester.Request(cmd.Context(), method, payload, tags, operatorStatusCallback(cmd, label))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(event.Content), result); err != nil {
		return fmt.Errorf("decode %s result: %w", label, err)
	}
	return nil
}

func requireContextVMUUID(label, raw string) error {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("%s must be a non-nil UUID", label)
	}
	return nil
}

func runBuildRequestContextVM(cmd *cobra.Command, req client.BuildRequestNostrRequest) (*client.BuildCommandResult, error) {
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("repository_credential_ref", req.RepositoryCredentialRef); err != nil {
		return nil, err
	}
	if req.GitRef == "" || req.ArtifactRepo == "" {
		return nil, fmt.Errorf("git_ref and artifact_repo are required")
	}
	result := &client.BuildCommandResult{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodBuildRequest, req, nostr.Tags{{"service", req.ServiceID}, {"git-ref", req.GitRef}}, req.IdempotencyKey, "builds request", result)
	return result, err
}

func runBuildRegisterResultContextVM(cmd *cobra.Command, buildID string) (*client.ArtifactCommandResult, error) {
	if err := requireContextVMUUID("build_id", buildID); err != nil {
		return nil, err
	}
	result := &client.ArtifactCommandResult{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodArtifactRegisterBuildResult, map[string]string{"build_id": buildID}, nostr.Tags{{"build", buildID}}, "", "builds register-result", result)
	return result, err
}

func runArtifactRegisterContextVM(cmd *cobra.Command, req client.RegisterArtifactNostrRequest) (*client.ArtifactCommandResult, error) {
	if err := requireContextVMUUID("build_id", req.BuildID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if req.ImageRepo == "" || req.ImageTag == "" || req.ImageDigest == "" {
		return nil, fmt.Errorf("image_repo, image_tag, and image_digest are required")
	}
	result := &client.ArtifactCommandResult{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodArtifactRegister, req, nostr.Tags{{"service", req.ServiceID}, {"build", req.BuildID}, {"digest", req.ImageDigest}}, req.IdempotencyKey, "artifact register", result)
	return result, err
}

func runArtifactImportObservedContextVM(cmd *cobra.Command, req client.ImportObservedArtifactNostrRequest) (*client.ImportObservedArtifactResult, error) {
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("environment_id", req.EnvironmentID); err != nil {
		return nil, err
	}
	if req.ImageRepo == "" || req.ImageTag == "" || req.ImageDigest == "" {
		return nil, fmt.Errorf("image_repo, image_tag, and image_digest are required")
	}
	result := &client.ImportObservedArtifactResult{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodArtifactImportObserved, req, nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"digest", req.ImageDigest}}, req.IdempotencyKey, "artifact import-observed", result)
	return result, err
}

func runDNSDriftRemediateContextVM(cmd *cobra.Command, req client.DNSDriftRemediateRequest) (*client.DNSCommandResult, error) {
	tags := nostr.Tags{}
	if req.Zone != "" {
		tags = append(tags, nostr.Tag{"zone", req.Zone})
	}
	result := &client.DNSCommandResult{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodDNSDriftRemediate, req, tags, "", "dns drift-remediate", result)
	return result, err
}

func runDeploymentPreviewContextVM(cmd *cobra.Command, req client.DeploymentPreviewNostrRequest) (map[string]any, error) {
	if err := requireContextVMUUID("service_id", req.ServiceID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("environment_id", req.EnvironmentID); err != nil {
		return nil, err
	}
	if err := requireContextVMUUID("artifact_id", req.ArtifactID); err != nil {
		return nil, err
	}
	if len(req.ManagedRuntimeConfig) == 0 {
		return nil, fmt.Errorf("managed_runtime_config is required")
	}
	payload := map[string]any{"service_id": req.ServiceID, "environment_id": req.EnvironmentID, "artifact_id": req.ArtifactID, "managed_runtime_config": req.ManagedRuntimeConfig}
	if req.DeploymentUnitID != "" {
		payload["deployment_unit_id"] = req.DeploymentUnitID
	}
	if req.Compact {
		payload["compact"] = true
	}
	if req.IdempotencyKey != "" {
		payload["idempotency_key"] = req.IdempotencyKey
	}
	tags := nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"artifact", req.ArtifactID}}
	result := map[string]any{}
	err := requestCLIContextVM(cmd, controlplane.ContextVMMethodServiceDeployPreview, payload, tags, req.IdempotencyKey, "deploy preview", &result)
	return result, err
}

func runRouteAttachContextVM(cmd *cobra.Command, req client.RouteAttachRequest) (*client.DeploymentCommandResult, error) {
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
	result := &client.DeploymentCommandResult{}
	err = requestCLIContextVM(cmd, controlplane.ContextVMMethodServiceRouteAttach, req, nostr.Tags{{"service", req.ServiceID}, {"environment", req.EnvironmentID}, {"hostname", req.PublicRoute.Hostname}}, req.IdempotencyKey, "route attach", result)
	return result, err
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

func runAdoptionScanContextVM(cmd *cobra.Command, req client.AdoptionScanRequest) ([]client.AdoptionPreview, error) {
	targets, err := adoptionPayloadTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	result := []client.AdoptionPreview{}
	err = requestCLIContextVM(cmd, "adoption/scan", map[string]any{"targets": targets}, nil, "", "adoption scan", &result)
	return result, err
}

func runAdoptionImportContextVM(cmd *cobra.Command, req client.AdoptionImportRequest) ([]client.AdoptionImportResult, error) {
	targets, err := adoptionPayloadTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	if !req.ImportAll && len(req.Selections) == 0 {
		return nil, fmt.Errorf("import requires --all or at least one selection")
	}
	result := []client.AdoptionImportResult{}
	payload := map[string]any{"targets": targets, "selections": req.Selections, "import_all": req.ImportAll, "org_id": req.OrgID}
	err = requestCLIContextVM(cmd, "adoption/import", payload, nil, "", "adoption import", &result)
	return result, err
}
