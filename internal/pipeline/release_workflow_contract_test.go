package pipeline

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// artifactMarkerPrintf matches the single stdout line Loom's ci/workflow-run
// profile parses (loom-worker src/ci/artifact-marker.ts) and copies into the
// signed 5402 consumed by the pipeline bridge.
var artifactMarkerPrintf = regexp.MustCompile(`printf 'BAHIA_ARTIFACT=\{"image_repo":"%s","image_tag":"%s","image_digest":"%s"\}\\n'`)

func TestHiveCIWorkflowsEmitBahiaArtifactMarker(t *testing.T) {
	for _, tc := range []struct {
		path      string
		imageRepo string
	}{
		// Canonical workflow_path mapped by hiveci pipeline policies and the
		// operator seed script for chebizarro/bahia.
		{path: "../../.gitea/workflows/release.yml", imageRepo: "harbor.sharegap.net/cascadia/bahia"},
		// Legacy hive-ci-runner workflow: keeps .hiveci-result.json and must
		// also satisfy the Loom marker contract.
		{path: "../../.github/workflows/hive-ci-build.yml", imageRepo: "harbor.sharegap.net/cascadia/bahia"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			raw, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read workflow: %v", err)
			}
			workflow := string(raw)
			if got := len(artifactMarkerPrintf.FindAllString(workflow, -1)); got != 1 {
				t.Fatalf("BAHIA_ARTIFACT printf occurrences=%d, want exactly 1", got)
			}
			if !strings.Contains(workflow, "IMAGE_REPO: "+tc.imageRepo) {
				t.Fatalf("workflow IMAGE_REPO is not %q", tc.imageRepo)
			}
			if !strings.Contains(workflow, "sha256:[0-9a-f]{64}") {
				t.Fatal("workflow must validate an immutable sha256 digest before emitting the marker")
			}
		})
	}
}

func TestHiveCISeedScriptWorkflowPathExists(t *testing.T) {
	script, err := os.ReadFile("../../scripts/seed_hiveci_pipeline_policy.sql")
	if err != nil {
		t.Fatalf("read seed script: %v", err)
	}
	paths := regexp.MustCompile(`'(\.gitea/workflows/[^']+|\.github/workflows/[^']+)'`).FindAllStringSubmatch(string(script), -1)
	if len(paths) == 0 {
		t.Fatal("seed script references no workflow_path")
	}
	for _, match := range paths {
		if _, err := os.Stat("../../" + match[1]); err != nil {
			t.Fatalf("seed script workflow_path %q does not exist in the repository: %v", match[1], err)
		}
	}
}
