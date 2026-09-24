package pipeline

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func workflowRunStep(t *testing.T, path, name string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Name == name {
				return step.Run
			}
		}
	}
	t.Fatalf("step %q missing from %s", name, path)
	return ""
}

func TestReleaseWorkflowEmitsOnlyVerifiedArtifact(t *testing.T) {
	script := workflowRunStep(t, "../../.gitea/workflows/release.yml", "Build and publish immutable backend artifact")
	sha := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, mode, ref, secret string
		success                 bool
	}{
		{name: "repo digest", success: true},
		{name: "registry fallback", mode: "fallback", success: true},
		{name: "no runtime", mode: "no-runtime"},
		{name: "push rejected", mode: "push-failed"},
		{name: "invalid digest", mode: "bad-digest"},
		{name: "wrong ref", ref: "refs/heads/feature"},
		{name: "wrong checkout", mode: "wrong-checkout"},
		{name: "clone secret", secret: "HIVE_CI_GIT_PASSWORD"},
		{name: "result signer secret", secret: "HIVE_CI_NSEC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, contents := range map[string]string{
				"git": "#!/bin/sh\nif [ \"$MODE\" = wrong-checkout ] && [ \"$2\" != HEAD ]; then echo wrong; else echo \"$SHA\"; fi\n",
				"docker": `#!/bin/sh
case "$1 $2" in
  "version ") [ "$MODE" != no-runtime ] ;;
  "push "*) [ "$MODE" != push-failed ] ;;
  "image inspect")
    case "$MODE" in
      fallback) exit 1 ;;
      bad-digest) echo repo@sha256:invalid ;;
      *) echo "repo@$DIGEST" ;;
    esac ;;
  "buildx imagetools") echo "$DIGEST" ;;
esac
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ref := tc.ref
			if ref == "" {
				ref = "refs/heads/master"
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "SHA=" + sha, "DIGEST=" + digest, "MODE=" + tc.mode, "IMAGE_REPO=registry.example/bahia", "LOOM_CI_REF=" + ref}
			if tc.secret != "" {
				cmd.Env = append(cmd.Env, tc.secret+"=must-not-leak")
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("success=%t error=%v output=%s", tc.success, err, output)
			}
			if strings.Contains(string(output), "must-not-leak") {
				t.Fatal("workflow leaked a credential")
			}
			if !tc.success {
				if strings.Contains(string(output), "BAHIA_ARTIFACT=") {
					t.Fatalf("failed build advertised artifact: %s", output)
				}
				return
			}
			assertArtifactMarker(t, string(output), "registry.example/bahia", "master-aaaaaaa", digest)
		})
	}
}

func assertArtifactMarker(t *testing.T, output, repo, tag, digest string) {
	t.Helper()
	var markers []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "BAHIA_ARTIFACT=") {
			markers = append(markers, strings.TrimPrefix(line, "BAHIA_ARTIFACT="))
		}
	}
	if len(markers) != 1 {
		t.Fatalf("expected exactly one marker: %s", output)
	}
	var artifact map[string]string
	if err := json.Unmarshal([]byte(markers[0]), &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact["image_repo"] != repo || artifact["image_tag"] != tag || artifact["image_digest"] != digest {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
}

func TestHiveCIBuildRetainsResultFileAndEmitsLoomMarker(t *testing.T) {
	script := workflowRunStep(t, "../../.github/workflows/hive-ci-build.yml", "Write .hiveci-result.json")
	digest := "sha256:" + strings.Repeat("b", 64)
	script = strings.NewReplacer("${{ steps.meta.outputs.tag }}", "master-aaaaaaa", "${{ steps.meta.outputs.sha }}", strings.Repeat("a", 40), "${{ steps.digest.outputs.digest }}", digest).Replace(script)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "IMAGE_REPO=registry.example/bahia"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("result step failed: %v: %s", err, output)
	}
	assertArtifactMarker(t, string(output), "registry.example/bahia", "master-aaaaaaa", digest)
	if _, err := os.Stat(filepath.Join(cmd.Dir, ".hiveci-result.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWebBootstrapEntrypoint(t *testing.T) {
	body, err := os.ReadFile("../../web/docker-entrypoint.d/40-bahia-bootstrap-env.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, html, relays, keys string
		success                  bool
	}{
		{name: "baked values need no runtime env", html: "<html>baked config</html>", success: true},
		{name: "missing explicit identity", html: "__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__ __PUBLIC_BAHIA_SERVICE_PUBKEYS__", relays: "wss://relay.example"},
		{name: "runtime substitution", html: "__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__ __PUBLIC_BAHIA_SERVICE_PUBKEYS__", relays: "wss://relay.example/path?a=1&b=2|3", keys: strings.Repeat("c", 64), success: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.html")
			if err := os.WriteFile(path, []byte(tc.html), 0o600); err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(body), "INDEX_HTML=/usr/share/nginx/html/index.html", "INDEX_HTML="+path, 1)
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "PUBLIC_BAHIA_BOOTSTRAP_RELAYS=" + tc.relays, "PUBLIC_BAHIA_SERVICE_PUBKEYS=" + tc.keys}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("error=%v output=%s", err, output)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.html
			if tc.success && tc.keys != "" {
				want = tc.relays + " " + tc.keys
			}
			if string(got) != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}
