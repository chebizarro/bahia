package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"go.uber.org/zap"
)

const (
	testTag     = "github-1a2b3c4"
	testDir     = releaseRoot + "/" + testTag
	testBackend = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testWeb     = "example/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeSDK struct {
	project *types.Project
	loads   [][]string
	applied []*types.Project
	upErr   error
}

func (f *fakeSDK) LoadProjectFiles(_ context.Context, paths ...string) (*types.Project, error) {
	f.loads = append(f.loads, paths)
	project := cloneProject(f.project)
	project.WorkingDir = filepath.Dir(paths[0])
	project.ComposeFiles = append([]string(nil), paths...)
	project.Environment = types.Mapping{"DOCKER_DEFAULT_PLATFORM": "linux/amd64"}
	return project, nil
}
func (f *fakeSDK) ApplyProject(_ context.Context, p *types.Project, services ...string) error {
	if !reflect.DeepEqual(services, []string{"bahia", "web"}) {
		panic("wrong service scope")
	}
	f.applied = append(f.applied, cloneProject(p))
	return f.upErr
}
func (f *fakeSDK) Close() error { return nil }

func cloneProject(p *types.Project) *types.Project {
	q := *p
	q.Services = types.Services{}
	for name, service := range p.Services {
		service.Volumes = append([]types.ServiceVolumeConfig(nil), service.Volumes...)
		service.Environment = types.MappingWithEquals{}
		for key, value := range p.Services[name].Environment {
			if value == nil {
				service.Environment[key] = nil
				continue
			}
			copy := *value
			service.Environment[key] = &copy
		}
		q.Services[name] = service
	}
	return &q
}

func fixture() *types.Project {
	return &types.Project{Name: "edge", Services: types.Services{
		"bahia": {Name: "bahia", Image: "local/bahia:old", CustomLabels: types.Labels{"com.docker.compose.project": "edge"}, Volumes: []types.ServiceVolumeConfig{
			{Type: "bind", Source: releaseRoot + "/github-0000000/docs", Target: "/docs", ReadOnly: true},
			{Type: "bind", Source: "/config.yaml", Target: "/config/config.yaml", ReadOnly: true},
		}},
		"web":      {Name: "web", Image: "local/web:old"},
		"postgres": {Name: "postgres", Image: "postgres:16"},
	}}
}

func args(base, receipt string) []string {
	return []string{"apply", "--base-file", base, "--receipt", receipt, "--tag", testTag, "--release-dir", testDir, "--backend-image", testBackend, "--web-image", testWeb}
}

func TestTypedProjectPinsSeedAndPreservesOtherServices(t *testing.T) {
	t.Setenv("PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "wss://relay.example/relay?a=1&b=2|literal")
	t.Setenv("PUBLIC_BAHIA_SERVICE_PUBKEYS", "abc|&/:")
	t.Setenv("PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS", "")
	base := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(base, []byte("# unchanged | & / :\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "applied.json")
	f := &fakeSDK{project: fixture()}
	if err := run(context.Background(), args(base, receiptPath), io.Discard, f); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 {
		t.Fatalf("apply count %d", len(f.applied))
	}
	p := f.applied[0]
	if p.Services["bahia"].Image != testBackend || p.Services["web"].Image != testWeb || p.Services["postgres"].Image != "postgres:16" || p.Services["bahia"].Volumes[0].Source != testDir+"/docs" {
		t.Fatalf("unexpected applied project: %#v", p.Services)
	}
	if got := *p.Services["web"].Environment["PUBLIC_BAHIA_BOOTSTRAP_RELAYS"]; got != "wss://relay.example/relay?a=1&b=2|literal" {
		t.Fatal(got)
	}
	if got, _ := os.ReadFile(base); string(got) != "# unchanged | & / :\n" {
		t.Fatal("base changed")
	}
	roundTrip, recordedBase, err := readReceipt(receiptPath)
	if err != nil || recordedBase != base {
		t.Fatalf("receipt: %s: %v", recordedBase, err)
	}
	if !reflect.DeepEqual(roundTrip.Services["bahia"].Volumes, p.Services["bahia"].Volumes) || roundTrip.Services["web"].Image != testWeb || roundTrip.Services["web"].Name != "web" || roundTrip.Services["bahia"].CustomLabels["com.docker.compose.project"] != "edge" || roundTrip.Environment["DOCKER_DEFAULT_PLATFORM"] != "linux/amd64" {
		t.Fatal("receipt lost applied fields")
	}
	stat, err := os.Stat(receiptPath)
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode: %v %v", stat, err)
	}
	before, _ := os.ReadFile(receiptPath)
	if err := run(context.Background(), args(base, receiptPath), io.Discard, &fakeSDK{project: fixture()}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(receiptPath)
	if string(before) != string(after) {
		t.Fatal("re-apply receipt is not deterministic")
	}
}

func TestExistingEnvironmentPreserved(t *testing.T) {
	for _, value := range []string{"wss://relay.example/a?x=1&y=2|literal", "${literal-old-delimiter|value}"} {
		t.Run(value, func(t *testing.T) {
			project := fixture()
			web := project.Services["web"]
			web.Environment = types.MappingWithEquals{"PUBLIC_BAHIA_BOOTSTRAP_RELAYS": &value}
			project.Services["web"] = web
			if err := seedWeb(project, func(key string) string {
				if key == "PUBLIC_BAHIA_SERVICE_PUBKEYS" {
					return "signer"
				}
				return ""
			}); err != nil {
				t.Fatal(err)
			}
			if got := *project.Services["web"].Environment["PUBLIC_BAHIA_BOOTSTRAP_RELAYS"]; got != value {
				t.Fatal(got)
			}
		})
	}
}

func TestMissingAndUnsafeInputsFailWithoutApplying(t *testing.T) {
	t.Setenv("PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "wss://relay.example")
	t.Setenv("PUBLIC_BAHIA_SERVICE_PUBKEYS", "signer")
	base := filepath.Join(t.TempDir(), "base.yml")
	receiptPath := filepath.Join(t.TempDir(), "applied.json")
	cases := []struct {
		name       string
		mutate     func(*types.Project)
		changeArgs func([]string) []string
		want       string
	}{
		{"missing-bahia", func(p *types.Project) { delete(p.Services, "bahia") }, nil, "missing expected services: bahia"},
		{"missing-web", func(p *types.Project) { delete(p.Services, "web") }, nil, "missing expected services: web"},
		{"missing-image", func(p *types.Project) { s := p.Services["web"]; s.Image = ""; p.Services["web"] = s }, nil, "missing image"},
		{"missing-docs", func(p *types.Project) { s := p.Services["bahia"]; s.Volumes = nil; p.Services["bahia"] = s }, nil, "docs mount"},
		{"read-write-docs", func(p *types.Project) {
			s := p.Services["bahia"]
			s.Volumes[0].ReadOnly = false
			p.Services["bahia"] = s
		}, nil, "read-only release docs"},
		{"duplicate-docs", func(p *types.Project) {
			s := p.Services["bahia"]
			s.Volumes = append(s.Volumes, s.Volumes[0])
			p.Services["bahia"] = s
		}, nil, "exactly one release docs"},
		{"bad-tag", nil, func(a []string) []string { a[6] = "github-1a2b3c4;docker"; return a }, "tag must match"},
		{"bad-dir", nil, func(a []string) []string { a[8] = testDir + "|evil"; return a }, "release directory"},
		{"bad-backend", nil, func(a []string) []string { a[10] = testBackend + "|evil"; return a }, "backend image"},
		{"bad-web", nil, func(a []string) []string { a[12] = "latest"; return a }, "web image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture()
			if tc.mutate != nil {
				tc.mutate(p)
			}
			a := args(base, receiptPath)
			if tc.changeArgs != nil {
				a = tc.changeArgs(a)
			}
			f := &fakeSDK{project: p}
			err := run(context.Background(), a, io.Discard, f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if len(f.applied) != 0 {
				t.Fatal("applied unsafe project")
			}
		})
	}
	t.Setenv("PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "")
	if err := run(context.Background(), args(base, receiptPath), io.Discard, &fakeSDK{project: fixture()}); err == nil {
		t.Fatal("accepted missing required seed")
	}
}

func TestSnapshotRollbackAndFailedApply(t *testing.T) {
	t.Setenv("PUBLIC_BAHIA_BOOTSTRAP_RELAYS", "wss://relay.example")
	t.Setenv("PUBLIC_BAHIA_SERVICE_PUBKEYS", "signer")
	base := filepath.Join(t.TempDir(), "base.yml")
	dir := t.TempDir()
	legacy := filepath.Join(dir, "old-override.yml")
	backup := filepath.Join(dir, "backup.json")
	safe := filepath.Join(dir, "safe.json")
	current := filepath.Join(dir, "current.json")
	f := &fakeSDK{project: fixture()}
	if err := run(context.Background(), []string{"snapshot", "--base-file", base, "--legacy-override", legacy, "--receipt", backup}, io.Discard, f); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.loads[0], []string{base, legacy}) {
		t.Fatal(f.loads)
	}
	if err := run(context.Background(), []string{"snapshot", "--source-receipt", backup, "--receipt", safe, "--safe-backend-image", testBackend}, io.Discard, f); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"rollback", "--receipt", safe, "--current-receipt", current}, io.Discard, f); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 || f.applied[0].Services["bahia"].Image != testBackend {
		t.Fatal("rollback did not apply safe project")
	}
	got, _, err := readReceipt(current)
	if err != nil || got.Services["bahia"].Image != testBackend {
		t.Fatalf("current receipt: %v", err)
	}
	f.upErr = errors.New("engine failure")
	if err := run(context.Background(), args(base, current), io.Discard, f); err == nil {
		t.Fatal("ignored SDK failure")
	}
	got, _, err = readReceipt(current)
	if err != nil || got.Services["bahia"].Image != testBackend {
		t.Fatal("failed apply overwrote current receipt")
	}
}

func TestReceiptPreservesSDKLoadedProjectMetadata(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "docker-compose.yml")
	compose := "name: edge\nservices:\n  bahia:\n    image: bahia:old\n    volumes:\n      - /srv/data/bahia-controlplane/releases/github-0000000/docs:/docs:ro\n  web:\n    image: web:old\n    environment:\n      - PUBLIC_BAHIA_BOOTSTRAP_RELAYS=wss://relay.example/a?x=1&y=2|literal\n      - PUBLIC_BAHIA_SERVICE_PUBKEYS=signer\n"
	if err := os.WriteFile(base, []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	sdk := runtime.NewSDKComposeExecutor(runtime.NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	defer func() { _ = sdk.Close() }()
	project, err := sdk.LoadProjectFiles(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedWeb(project, os.Getenv); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "receipt.json")
	if err := writeReceipt(path, base, project); err != nil {
		t.Fatal(err)
	}
	restored, _, err := readReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bahia", "web"} {
		if !reflect.DeepEqual(project.Services[name].CustomLabels, restored.Services[name].CustomLabels) {
			t.Fatalf("%s SDK labels changed across receipt", name)
		}
	}
	if restored.WorkingDir != project.WorkingDir || !reflect.DeepEqual(restored.ComposeFiles, project.ComposeFiles) {
		t.Fatal("SDK project paths changed across receipt")
	}
	if got := *restored.Services["web"].Environment["PUBLIC_BAHIA_BOOTSTRAP_RELAYS"]; got != "wss://relay.example/a?x=1&y=2|literal" {
		t.Fatal(got)
	}
}
