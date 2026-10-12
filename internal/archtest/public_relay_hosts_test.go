package archtest

import (
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/relayguard"
	"golang.org/x/tools/go/packages"
)

// relayHostPattern finds host names, with or without a ws:// or wss://
// scheme, in source literals and config.
var relayHostPattern = regexp.MustCompile(`[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}`)

// publicRelayHostsIn returns the well-known public relay hosts text names,
// as a URL or a bare host name.
func publicRelayHostsIn(text string) []string {
	var hosts []string
	for _, host := range relayHostPattern.FindAllString(text, -1) {
		if relayguard.IsWellKnownPublic("wss://" + host) {
			hosts = append(hosts, strings.ToLower(host))
		}
	}
	return hosts
}

// publicRelayGateExempt is the denylist itself.
const publicRelayGateExempt = "internal/relayguard/"

// shippedConfigGlobs are the configuration files Bahia ships or deploys from.
var shippedConfigGlobs = []string{
	"config*.yaml",
	"docker-compose*.yml",
	".github/workflows/*.yml",
}

// TestNoPublicRelayHostsInProductionSources bans well-known public relay hosts
// (relayguard.IsWellKnownPublic), as URLs or bare host names, from production
// Go string literals (outside the denylist in internal/relayguard) and from
// shipped configuration (repository config files, compose files, deploy/, CI
// workflows and env files anywhere in the repository). Bahia's relays are
// configuration, never code, and shipped defaults never name a public relay.
// Zero violations are allowed.
func TestNoPublicRelayHostsInProductionSources(t *testing.T) {
	found := newViolations()
	collectPublicRelayLiterals(loadModule(t), found)
	collectPublicRelaysInShippedConfig(t, repoRoot(t), found)
	ratchet(t, "public_relay_hosts", found)
}

func collectPublicRelayLiterals(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		if strings.HasPrefix(path, publicRelayGateExempt) {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, host := range publicRelayHostsIn(value) {
				found.add(path+"|"+host, pkg.Fset.Position(lit.Pos()))
			}
			return true
		})
	})
}

func collectPublicRelaysInShippedConfig(t *testing.T, root string, found *violations) {
	t.Helper()
	var files []string
	for _, glob := range shippedConfigGlobs {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatalf("glob %s: %v", glob, err)
		}
		files = append(files, matches...)
	}
	err := filepath.WalkDir(filepath.Join(root, "deploy"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files = append(files, path)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk deploy: %v", err)
	}
	files = append(files, envFiles(t, root)...)
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, host := range publicRelayHostsIn(line) {
				found.add(relPath(path)+"|"+host, token.Position{Filename: path, Line: i + 1})
			}
		}
	}
}

func TestPublicRelayGateFindsShippedConfigRelays(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "edge"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.yaml":                 "nostr:\n  relays: [\"wss://relay.sharegap.net\", \"wss://relay.damus.io\"]\n",
		"deploy/edge/bahia.env":       "BAHIA_NOSTR_RELAYS=wss://nos.lol,wss://bahia.sharegap.net/relay\n",
		"docker-compose.override.yml": "x: wss://relay.example\n",
		".env.production":             "LOOM_RELAYS=relay.primal.net\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found := newViolations()
	collectPublicRelaysInShippedConfig(t, root, found)
	if len(found.counts) != 3 {
		t.Fatalf("violations = %v, want relay.damus.io in config.yaml, nos.lol in deploy/ and bare relay.primal.net in .env.production", found.counts)
	}
}

// envFiles are the repository's env files (.env, .env.*, *.env) outside
// dependency, vendored and VCS trees; deploy/ is walked separately.
func envFiles(t *testing.T, root string) []string {
	t.Helper()
	skip := map[string]bool{".git": true, "node_modules": true, "third_party": true, "deploy": true, ".beads": true}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && skip[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".env") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk env files: %v", err)
	}
	return files
}
