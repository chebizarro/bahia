package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGrafanaFleetHealthDashboardReferencesCataloguedMetrics(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	dashboardPath := filepath.Join(root, "deploy", "observability", "grafana", "dashboards", "bahia-fleet-health.json")
	body, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID != "bahia-fleet-health-v1" {
		t.Fatalf("dashboard UID = %q", dashboard.UID)
	}
	catalogBody, err := os.ReadFile(filepath.Join(root, "deploy", "observability", "metrics-catalog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]bool{}
	for _, line := range strings.Split(string(catalogBody), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			catalog[line] = true
		}
	}
	metricPattern := regexp.MustCompile(`(?:bahia|fleet_routstr)_[a-zA-Z0-9_]+`)
	seen := map[string]bool{}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			for _, metric := range metricPattern.FindAllString(target.Expr, -1) {
				seen[metric] = true
				if !catalog[metric] {
					t.Errorf("dashboard query references uncatalogued metric %q", metric)
				}
			}
		}
	}
	if len(seen) != len(catalog) {
		t.Fatalf("dashboard uses %d catalogued metrics, catalog contains %d", len(seen), len(catalog))
	}
}
