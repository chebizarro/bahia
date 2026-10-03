package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// deploymentRequestKey gives each ContextVM invocation a replayable key. The
// caller can supply the same key on a retry after an interrupted response.
func deploymentRequestKey(cmd *cobra.Command, supplied string) (string, error) {
	if key := strings.TrimSpace(supplied); key != "" {
		return key, nil
	}
	key, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate deployment idempotency key: %w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "ContextVM idempotency key: %s (reuse with --idempotency-key when retrying)\n", key)
	return key.String(), nil
}
