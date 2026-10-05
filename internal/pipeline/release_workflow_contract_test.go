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
	// The image carries no baked trust roots: the entrypoint must write the
	// validated runtime seed, or fail without touching the existing seed file.
	script, err := filepath.Abs("../../web/docker-entrypoint.d/40-bahia-bootstrap-env.sh")
	if err != nil {
		t.Fatal(err)
	}
	const untouched = "// seed from the image build\n"
	key := strings.Repeat("c", 64)
	widget := strings.Repeat("d", 64)
	for _, tc := range []struct {
		name, relays, keys, widgets string
		want                        string // empty: startup must fail and leave the seed untouched
	}{
		{name: "no runtime env fails closed"},
		{name: "missing explicit identity", relays: "wss://relay.example"},
		{name: "missing relays", keys: key},
		{name: "invalid relay URL", relays: "wss://relay.example/path|3", keys: key},
		{name: "invalid service pubkey", relays: "wss://relay.example", keys: "not-a-pubkey"},
		{name: "invalid widget pubkey", relays: "wss://relay.example", keys: key, widgets: "not-a-pubkey"},
		{name: "runtime seed", relays: "wss://relay.example/path?a=1&b=2,ws://localhost:3334/relay", keys: key,
			want: `window.__BAHIA_BOOTSTRAP__ = {schema:"bahia.bootstrap.v1",relay_urls:["wss://relay.example/path?a=1&b=2","ws://localhost:3334/relay"],service_pubkeys:["` + key + `"],widget_pubkeys:[]};` + "\n"},
		{name: "runtime seed with widget publishers", relays: "wss://relay.example", keys: key, widgets: widget,
			want: `window.__BAHIA_BOOTSTRAP__ = {schema:"bahia.bootstrap.v1",relay_urls:["wss://relay.example"],service_pubkeys:["` + key + `"],widget_pubkeys:["` + widget + `"]};` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bahia-bootstrap.js")
			if err := os.WriteFile(path, []byte(untouched), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", script)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "BAHIA_BOOTSTRAP_SCRIPT_PATH=" + path}
			for name, value := range map[string]string{
				"PUBLIC_BAHIA_BOOTSTRAP_RELAYS":     tc.relays,
				"PUBLIC_BAHIA_SERVICE_PUBKEYS":      tc.keys,
				"PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS": tc.widgets,
			} {
				if value != "" {
					cmd.Env = append(cmd.Env, name+"="+value)
				}
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != (tc.want != "") {
				t.Fatalf("error=%v output=%s", err, output)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = untouched
			}
			if string(got) != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}
