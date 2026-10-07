package docs_test

import (
	"fmt"
	"iter"
	"os"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/mcp"
	"go.uber.org/zap"
)

type zzStore struct{}

func (zzStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

func TestZZDumpTools(t *testing.T) {
	if os.Getenv("DUMP_MCP_TOOLS") == "" {
		t.Skip("set DUMP_MCP_TOOLS")
	}
	server, err := mcp.NewServerWithOptionsChecked(nil, zap.NewNop(), mcp.ServerDeps{
		StateStore:    zzStore{},
		ServicePubkey: nostr.Generate().Public().Hex(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range server.GetTools() {
		fmt.Println(tool.Name)
	}
}
