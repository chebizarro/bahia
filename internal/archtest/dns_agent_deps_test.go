package archtest

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDNSAgentMIPSDependenciesExcludeSQLite keeps the resolver binary free of
// the daemon's SQLite driver packages on 32-bit MIPS.
func TestDNSAgentMIPSDependenciesExcludeSQLite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/bahia-dns-agent")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=mips", "GOMIPS=softfloat")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list linux/mips DNS dependencies: %v\n%s", err, output)
	}
	for _, dep := range strings.Fields(string(output)) {
		if strings.Contains(strings.ToLower(dep), "sqlite") || strings.HasPrefix(dep, "modernc.org/libc") {
			t.Fatalf("linux/mips DNS agent imports %s", dep)
		}
	}
}
