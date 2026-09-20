package telemetry

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var updateCatalog = flag.Bool("update", false, "rewrite deploy/observability/metrics-catalog.txt from the code")

const catalogRelPath = "../../../deploy/observability/metrics-catalog.txt"
const alertsRelPath = "../../../deploy/observability/bahia-alerts.yml"

var (
	helpDeclaration = regexp.MustCompile(`#\s*HELP\s+(bahia_[a-z0-9_]+)`)
	metricReference = regexp.MustCompile(`\bbahia_[a-z0-9_]+`)
)

// declaredMetrics returns every bahia_ metric family this package declares a
// HELP line for. HELP is the authoritative declaration: a metric emitted
// without one is invisible to this catalog and to anyone reading /metrics.
func declaredMetrics(t *testing.T) []string {
	t.Helper()
	// Scoped to this package on purpose: the catalog's bahia_ section documents
	// the main /metrics exposition. internal/soulfactory/saga serves its own
	// handler and is covered by the alert guard instead.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	set := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range helpDeclaration.FindAllStringSubmatch(string(data), -1) {
			set[match[1]] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func catalogMetrics(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(catalogRelPath))
	if err != nil {
		t.Fatalf("read metrics catalog: %v", err)
	}
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "bahia_") {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

// The catalog is the documented contract for /metrics. It drifted badly once
// already - it listed 15 names while the code exposed 73 - so it is generated
// and asserted rather than maintained by hand.
func TestMetricsCatalogIsCurrent(t *testing.T) {
	declared := declaredMetrics(t)

	if *updateCatalog {
		rewriteCatalog(t, declared)
		return
	}

	cataloged := catalogMetrics(t)
	missing := difference(declared, cataloged)
	extra := difference(cataloged, declared)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("metrics catalog is stale; run: go test ./internal/adapters/telemetry/ -run TestMetricsCatalogIsCurrent -update\n"+
			"  declared in code but absent from catalog: %v\n"+
			"  in catalog but no longer declared:        %v", missing, extra)
	}
}

// An alert naming a metric that no longer exists never fires. That is a worse
// failure than a noisy alert, because it looks like health.
func TestAlertRulesOnlyReferenceKnownMetrics(t *testing.T) {
	data, err := os.ReadFile(filepath.Clean(alertsRelPath))
	if err != nil {
		t.Skipf("alert rules not present: %v", err)
	}

	// Ground truth for "does this metric exist" is any bahia_ metric-name
	// literal in non-test Go. A plain `# HELP` scan is not enough:
	// internal/soulfactory/saga/observability.go builds its HELP lines from a
	// table of {name, help} tuples, so its names never appear after "# HELP".
	declared := metricNameLiterals(t)

	unknown := map[string]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, ref := range metricReference.FindAllString(line, -1) {
			// Prometheus summaries and histograms expose derived series; the
			// catalog records the family, so compare against the family name.
			base := strings.TrimSuffix(strings.TrimSuffix(ref, "_count"), "_sum")
			if _, ok := declared[ref]; ok {
				continue
			}
			if _, ok := declared[base]; ok {
				continue
			}
			unknown[ref] = struct{}{}
		}
	}
	if len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for name := range unknown {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("bahia-alerts.yml references %d metric(s) this code no longer exposes, so those alerts can never fire: %v", len(names), names)
	}
}

// metricNameLiterals collects every bahia_ metric name appearing as a string
// literal in non-test Go across the module, regardless of how the exposition
// is constructed.
func metricNameLiterals(t *testing.T) map[string]struct{} {
	t.Helper()
	set := map[string]struct{}{}
	err := filepath.Walk("../../..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", "prompt-exports":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range metricReference.FindAllString(string(data), -1) {
			set[match] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan module for metric names: %v", err)
	}
	return set
}

func difference(a, b []string) []string {
	inB := map[string]struct{}{}
	for _, v := range b {
		inB[v] = struct{}{}
	}
	out := []string{}
	for _, v := range a {
		if _, ok := inB[v]; !ok {
			out = append(out, v)
		}
	}
	return out
}

func rewriteCatalog(t *testing.T, declared []string) {
	t.Helper()
	existing, err := os.ReadFile(filepath.Clean(catalogRelPath))
	if err != nil {
		t.Fatalf("read metrics catalog: %v", err)
	}
	var header, tail []string
	for _, line := range strings.Split(string(existing), "\n") {
		switch {
		case strings.HasPrefix(line, "bahia_"):
			continue
		case strings.HasPrefix(strings.TrimSpace(line), "fleet_routstr") ||
			strings.Contains(line, "fleet-routstr-gateway"):
			tail = append(tail, line)
		case len(tail) > 0:
			tail = append(tail, line)
		default:
			header = append(header, line)
		}
	}
	var body strings.Builder
	body.WriteString(strings.Join(header, "\n"))
	body.WriteString("\n")
	body.WriteString(strings.Join(declared, "\n"))
	body.WriteString("\n\n")
	body.WriteString(strings.Join(tail, "\n"))
	if err := os.WriteFile(filepath.Clean(catalogRelPath), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write metrics catalog: %v", err)
	}
	t.Logf("rewrote catalog with %d metrics", len(declared))
}
