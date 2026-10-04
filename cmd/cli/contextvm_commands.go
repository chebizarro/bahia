package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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

func newCLIOperatorLogger(stderr io.Writer) *zap.Logger {
	if stderr == nil {
		return zap.NewNop()
	}
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = ""
	return zap.New(zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(stderr), zap.WarnLevel))
}

func operatorStatusCallback(cmd *cobra.Command, label string) func(client.OperatorStatusEvent) {
	if outputFormat != "table" {
		return nil
	}
	return func(status client.OperatorStatusEvent) {
		message := strings.TrimSpace(status.Message)
		if message == "" {
			message = firstNonEmpty(status.Step, status.Status)
		}
		if message == "" {
			message = "status update"
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "→ %s: %s\n", label, message)
	}
}
