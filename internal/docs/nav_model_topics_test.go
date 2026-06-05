package docs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestWebNavDocTopicsExistInCentralCatalog(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	catalog, err := New(filepath.Join(repoRoot, DefaultBasePath)).Catalog(context.Background())
	if err != nil {
		t.Fatalf("load central docs catalog: %v", err)
	}
	catalogTopics := map[string]bool{}
	for _, item := range catalog {
		catalogTopics[item.Topic] = true
	}

	navModelPath := filepath.Join(repoRoot, "web", "src", "lib", "components", "nav-model.js")
	navModel, err := os.ReadFile(navModelPath)
	if err != nil {
		t.Fatalf("read nav model: %v", err)
	}

	matches := regexp.MustCompile(`docTopic:\s*'([^']+)'`).FindAllStringSubmatch(string(navModel), -1)
	if len(matches) == 0 {
		t.Fatal("nav model does not define any docTopic metadata")
	}

	required := map[string]bool{
		"features-services":     false,
		"features-deployments":  false,
		"features-fleet-health": false,
		"features-llm-routes":   false,
		"features-ml-models":    false,
	}

	seen := map[string]bool{}
	for _, match := range matches {
		topic := match[1]
		if seen[topic] {
			continue
		}
		seen[topic] = true
		if !catalogTopics[topic] {
			t.Fatalf("nav model docTopic %q is absent from central docs catalog", topic)
		}
		if _, ok := required[topic]; ok {
			required[topic] = true
		}
	}

	for topic, found := range required {
		if !found {
			t.Fatalf("required route docTopic %q was not found in nav model", topic)
		}
	}
}
