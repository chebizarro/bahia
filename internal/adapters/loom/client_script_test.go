package loom

import (
	"strings"
	"testing"
)

func TestBuildDeployScriptPullsRemoteImages(t *testing.T) {
	job := JobRequest{
		Image:       "ghcr.io/openagentsinc/bahia",
		Digest:      "sha256:abc123",
		Environment: "prod",
		Service:     "bahia",
	}

	script := buildDeployScript(job)

	if !strings.Contains(script, "docker pull ghcr.io/openagentsinc/bahia@sha256:abc123") {
		t.Fatalf("expected remote image deploy script to pull digest ref, got: %s", script)
	}
}

func TestBuildDeployScriptUsesLocalCacheForLocalImages(t *testing.T) {
	job := JobRequest{
		Image:       "local/gitea:migration",
		Digest:      "sha256:def456",
		Environment: "prod",
		Service:     "gitea",
	}

	script := buildDeployScript(job)

	if strings.Contains(script, "docker pull local/gitea:migration@sha256:def456") {
		t.Fatalf("expected local image deploy script to skip docker pull, got: %s", script)
	}
	if !strings.Contains(script, "docker image inspect 'local/gitea:migration' >/dev/null") {
		t.Fatalf("expected local image deploy script to verify local cache, got: %s", script)
	}
	if !strings.Contains(script, "docker run -d --name gitea local/gitea:migration@sha256:def456") {
		t.Fatalf("expected deploy to keep digest-qualified runtime ref, got: %s", script)
	}
}
