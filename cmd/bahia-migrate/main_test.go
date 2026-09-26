package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDownRequiresConfirmationInEitherArgumentOrder(t *testing.T) {
	for _, args := range [][]string{{"down"}, {"down", "--force"}, {"--force", "down"}} {
		var output, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.Contains(t, errors.String(), "down requires --confirm")
	}
}
