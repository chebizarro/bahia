package handlers

import (
	"iter"

	"fiatjaf.com/nostr"
	mcpserver "github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type emptyHandlerMCPStore struct{}

func (emptyHandlerMCPStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

func newHandlerTestMCPServer(registry *service.RegistryService, logger *zap.Logger) *mcpserver.Server {
	return newHandlerTestMCPServerWithOptions(registry, logger, mcpserver.ServerDeps{})
}
func newHandlerTestMCPServerWithOptions(registry *service.RegistryService, logger *zap.Logger, deps mcpserver.ServerDeps) *mcpserver.Server {
	deps.StateStore = emptyHandlerMCPStore{}
	deps.ServicePubkey = nostr.Generate().Public().Hex()
	server, err := mcpserver.NewServerWithOptionsChecked(registry, logger, deps)
	if err != nil {
		panic(err)
	}
	return server
}
