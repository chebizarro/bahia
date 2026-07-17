package mcp

import (
	"context"

	"github.com/openagentsinc/bahia/internal/auth"
)

func authorizedMCPContext() context.Context {
	return auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("mcp-test"))
}
