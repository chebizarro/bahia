package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

// Keep the constructor's production caller list explicit while the DNS-agent
// transport has its own retirement track.
func TestContextVMRequestClientCallerAllowlist(t *testing.T) {
	root := filepath.Join("..", "..")
	want := map[string]bool{
		"cmd/cli/contextvm_commands.go":          true,
		"internal/adapters/dns/dnsmasq_agent.go": true,
	}
	seen := map[string]bool{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "NewContextVMRequestClient(") {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				seen[filepath.ToSlash(rel)] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("ContextVM constructor callers = %#v, want %#v", seen, want)
	}
	for path := range want {
		if !seen[path] {
			t.Errorf("ContextVM constructor caller %s missing", path)
		}
	}
	cli, err := os.ReadFile(filepath.Join(root, "cmd", "cli", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(cli), "fetchCLIRunLogs(") != 1 || !strings.Contains(string(cli), "fetchCLIRunLogs(cmd, args[0], tail, stream, result)") {
		t.Fatal("CLI ContextVM call must remain only the run-log fetch")
	}
}

func TestContextVMRequestClientRejectsMigratedMethods(t *testing.T) {
	transport := newFakeOperatorTransport()
	requester := newTestOperatorClient(t, nostr.Generate().Hex(), transport)
	for _, method := range []string{
		"build/request", "adoption/scan", "security/scan-run", "sbom/generate", "sbom/import",
		"artifact/signature-verify", "artifact/register-build-result", "relay/policy-set",
		"notification/channel-test", "environment/worker-policy-apply", "ml/pin",
	} {
		if _, err := requester.Request(context.Background(), method, map[string]any{}, nil, nil); err == nil || !strings.Contains(err.Error(), "not a run-log fetch or DNS-agent RPC") {
			t.Errorf("Request(%q) error = %v", method, err)
		}
	}
	if len(transport.published) != 0 {
		t.Fatalf("unsupported methods published %d events", len(transport.published))
	}
}
