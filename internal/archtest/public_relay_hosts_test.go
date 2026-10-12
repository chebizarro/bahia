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

// relayURLPattern finds websocket relay URLs in source literals and config.
var relayURLPattern = regexp.MustCompile("wss?://[^\\s\"'`,)\\]}]+")

// shippedConfigGlobs are the configuration files Bahia ships or deploys from.
var shippedConfigGlobs = []string{
	"config*.yaml",
	"docker-compose*.yml",
	".github/workflows/*.yml",
}

// TestNoPublicRelayHostsInProductionSources bans well-known public relay URLs
// (relayguard.IsWellKnownPublic) from production Go string literals and from
// shipped configuration (repository config files, compose files, deploy/ and
// CI workflows). Bahia's relays are configuration, never code, and the
// shipped defaults never point at a public relay. Zero violations are allowed.
func TestNoPublicRelayHostsInProductionSources(t *testing.T) {
	found := newViolations()
	collectPublicRelayLiterals(loadModule(t), found)
	collectPublicRelaysInShippedConfig(t, repoRoot(t), found)
	ratchet(t, "public_relay_hosts", found)
}

func collectPublicRelayLiterals(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, relay := range relayURLPattern.FindAllString(value, -1) {
				if relayguard.IsWellKnownPublic(relay) {
					found.add(path+"|"+relayguard.Normalize(relay), pkg.Fset.Position(lit.Pos()))
				}
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
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, relay := range relayURLPattern.FindAllString(line, -1) {
				if relayguard.IsWellKnownPublic(relay) {
					found.add(relPath(path)+"|"+relayguard.Normalize(relay), token.Position{Filename: path, Line: i + 1})
				}
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
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found := newViolations()
	collectPublicRelaysInShippedConfig(t, root, found)
	if len(found.counts) != 2 {
		t.Fatalf("violations = %v, want relay.damus.io in config.yaml and nos.lol in deploy/", found.counts)
	}
}
