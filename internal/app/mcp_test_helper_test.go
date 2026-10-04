package app

import (
	"iter"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type emptyAppMCPStateStore struct{}

func (emptyAppMCPStateStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

func newAppTestMCPServer(registry *service.RegistryService, logger *zap.Logger, deps mcp.ServerDeps) *mcp.Server {
	if deps.StateStore == nil {
		deps.StateStore = emptyAppMCPStateStore{}
		deps.ServicePubkey = nostr.Generate().Public().Hex()
	}
	server, err := mcp.NewServerWithOptionsChecked(registry, logger, deps)
	if err != nil {
		panic(err)
	}
	return server
}
