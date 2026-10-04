package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// fetchCLIRunLogs is the CLI's remaining ContextVM request path.
func fetchCLIRunLogs(cmd *cobra.Command, runID string, tail int, stream string, result any) (retErr error) {
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
	event, err := requester.Request(cmd.Context(), controlplane.ContextVMMethodDeploymentRunLogsGet, map[string]any{"run_id": runID, "tail": tail, "stream": stream}, nil, operatorStatusCallback(cmd, "logs run"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(event.Content), result); err != nil {
		return fmt.Errorf("decode logs run result: %w", err)
	}
	return nil
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
