package docs_test

import (
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/mcp"
	"go.uber.org/zap"
)

// TestMCPToolsReferenceMatchesRegistry keeps docs/user-guide/mcp-tools.md in
// lockstep with the MCP tool registry: every registered tool must be named in
// the reference, and every backticked tool-like name in the reference must be
// a registered tool.
func TestMCPToolsReferenceMatchesRegistry(t *testing.T) {
	server, err := mcp.NewServerWithOptionsChecked(nil, zap.NewNop(), mcp.ServerDeps{
		StateStore:    referenceStateStore{},
		ServicePubkey: nostr.Generate().Public().Hex(),
	})
	if err != nil {
		t.Fatalf("construct MCP server: %v", err)
	}
	registered := map[string]bool{}
	for _, tool := range server.GetTools() {
		registered[tool.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("MCP registry returned no tools")
	}

	path := filepath.Join("..", "..", "docs", "user-guide", "mcp-tools.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	documented := map[string]bool{}
	for _, match := range regexp.MustCompile("`([a-z][a-z0-9_]*)`").FindAllStringSubmatch(string(content), -1) {
		name := match[1]
		if (strings.HasPrefix(name, "bahia_") && len(name) > len("bahia_")) || strings.Contains(name, "_backup_") {
			documented[name] = true
		}
	}

	var missing, stale []string
	for name := range registered {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	for name := range documented {
		if !registered[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("registered MCP tools absent from mcp-tools.md: %s", strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("mcp-tools.md names tools that are not registered: %s", strings.Join(stale, ", "))
	}
}

type referenceStateStore struct{}

func (referenceStateStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}
