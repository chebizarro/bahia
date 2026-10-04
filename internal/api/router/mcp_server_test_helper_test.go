package router_test

import (
	"iter"

	"fiatjaf.com/nostr"
	mcpserver "github.com/openagentsinc/bahia/internal/mcp"
	"go.uber.org/zap"
)

type emptyRouterMCPStore struct{}

func (emptyRouterMCPStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}
func newRouterTestMCPServer(logger *zap.Logger) *mcpserver.Server {
	server, err := mcpserver.NewServerWithOptionsChecked(nil, logger, mcpserver.ServerDeps{StateStore: emptyRouterMCPStore{}, ServicePubkey: nostr.Generate().Public().Hex()})
	if err != nil {
		panic(err)
	}
	return server
}
