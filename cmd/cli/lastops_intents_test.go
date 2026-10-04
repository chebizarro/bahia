package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
)

type lastOpsArtifactRegistry struct {
	*cliIntentRegistry
	registered []*domain.Artifact
	imports    []service.ImportObservedArtifactInput
}

func (r *lastOpsArtifactRegistry) RegisterArtifact(_ context.Context, artifact *domain.Artifact) error {
	r.registered = append(r.registered, artifact)
	return nil
}

func (r *lastOpsArtifactRegistry) ImportObservedArtifact(_ context.Context, input service.ImportObservedArtifactInput) (*service.ImportObservedArtifactResult, error) {
	r.imports = append(r.imports, input)
	return &service.ImportObservedArtifactResult{Status: "imported", ObservationID: uuid.New()}, nil
}

type lastOpsAdoption struct {
	imported []service.AdoptionImportRequest
}

func (*lastOpsAdoption) Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	return nil, nil
}
func (a *lastOpsAdoption) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	a.imported = append(a.imported, req)
	return []service.AdoptionImportResult{{TargetName: "production", Status: "imported"}}, nil
}

func TestCLILastOpsThroughD76Handlers(t *testing.T) {
	registry, transport, org, _, processor, actor := setupCLIIntentPipelineWithProcessor(t)
	serviceID, environmentID, buildID, artifactID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	registry.services[serviceID] = &domain.Service{ID: serviceID, OrgID: uuid.MustParse(org)}
	registry.environments[environmentID] = &domain.Environment{ID: environmentID, OrgID: uuid.MustParse(org)}
	if err := registry.record(kinds.ServiceRegistry, serviceID.String(), registry.services[serviceID]); err != nil {
		t.Fatal(err)
	}
	artifacts := &lastOpsArtifactRegistry{cliIntentRegistry: registry}
	processor.RegisterHandler("artifact", controlplane.NewArtifactIntentHandler(artifacts, registry))
	adoption := &lastOpsAdoption{}
	processor.RegisterHandler("adoption", controlplane.NewAdoptionIntentHandler(adoption, []string{actor}))
	processor.RegisterHandler("dns", controlplane.NewDNSIntentHandler(cliDNSOperator{}, nil))

	base := []string{"--org", org}
	register := append(append([]string{}, base...), "artifacts", "register", "--id", artifactID.String(), "--build", buildID.String(), "--service", serviceID.String(), "--image-repo", "registry.example/api", "--image-tag", "v1", "--image-digest", "sha256:"+strings.Repeat("a", 64))
	if err := executeIntentCommand(t, register...); err != nil {
		t.Fatal(err)
	}
	if len(artifacts.registered) != 1 || artifacts.registered[0].ID != artifactID {
		t.Fatalf("artifact registration not handled: %#v", artifacts.registered)
	}
	observed := append(append([]string{}, base...), "artifacts", "import-observed", "--service", serviceID.String(), "--environment", environmentID.String(), "--image-repo", "registry.example/api", "--image-tag", "v1", "--image-digest", "sha256:"+strings.Repeat("a", 64))
	if err := executeIntentCommand(t, observed...); err != nil {
		t.Fatal(err)
	}
	if len(artifacts.imports) != 1 || artifacts.imports[0].EnvironmentID != environmentID {
		t.Fatalf("observed import not handled: %#v", artifacts.imports)
	}
	adopt := append(append([]string{}, base...), "adopt", "import", "--target", "production=docker-prod", "--select", "production/container-123=production")
	if err := executeIntentCommand(t, adopt...); err != nil {
		t.Fatal(err)
	}
	if len(adoption.imported) != 1 || len(adoption.imported[0].Selections) != 1 {
		t.Fatalf("adoption import not handled: %#v", adoption.imported)
	}
	dns := append(append([]string{}, base...), "dns", "drift-remediate", "--zone", "example.com")
	if err := executeIntentCommand(t, dns...); err != nil {
		t.Fatal(err)
	}
	if len(transport.published) != 4 || len(transport.statuses) != 4 {
		t.Fatalf("published=%d statuses=%d", len(transport.published), len(transport.statuses))
	}
	for _, status := range transport.statuses {
		if !hasIntentStatusTag(status.Tags, "status", "accepted") {
			t.Fatalf("nonaccepted D76 status: %#v", status.Tags)
		}
	}
	for _, command := range buildsCommands().Commands() {
		if command.Name() == "register-result" {
			t.Fatal("daemon-only build registration command is still available")
		}
	}

}

func TestCLILastOpsD76FixtureContent(t *testing.T) {
	registry, transport, _, _, _, _ := setupCLIIntentPipelineWithProcessor(t)
	transport.process = nil // inspect the signed envelope without a daemon status
	bytes, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "d76-intent-content.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Intents []struct {
			Domain, Op, Coordinate string
			Content                map[string]any
		}
	}
	if err := json.Unmarshal(bytes, &fixture); err != nil {
		t.Fatal(err)
	}
	const org = "018f6a60-0000-7000-8000-000000000001"
	const svc = "018f6a60-0000-7000-8000-000000000002"
	const env = "018f6a60-0000-7000-8000-000000000003"
	const build = "018f6a60-0000-7000-8000-000000000004"
	const artifact = "018f6a60-0000-7000-8000-000000000005"
	registry.services[uuid.MustParse(svc)] = &domain.Service{ID: uuid.MustParse(svc), OrgID: uuid.MustParse(org)}
	if err := registry.record(kinds.ServiceRegistry, svc, registry.services[uuid.MustParse(svc)]); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(runtimeFile, []byte(`{"replicas":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	commands := [][]string{
		{"artifacts", "register", "--id", artifact, "--build", build, "--service", svc, "--image-repo", "registry.example/api", "--image-tag", "v1", "--image-digest", digest},
		{"artifacts", "import-observed", "--service", svc, "--environment", env, "--image-repo", "registry.example/api", "--image-tag", "v1", "--image-digest", digest},
		{"adopt", "import", "--target", "production=docker-prod", "--select", "production/container-123"},
		{"dns", "drift-remediate", "--zone", "example.com"},
		{"deployments", "preview", "--service", svc, "--environment", env, "--artifact", artifact, "--managed-runtime-config-file", runtimeFile, "--compact"},
		{"deployments", "route-attach", "--service", svc, "--environment", env, "--deployment-unit", uuid.NewString(), "--hostname", "api.example.com", "--upstream-port", "8080", "--health-path", "/healthz", "--expected-updated-at", "2026-10-03T12:00:00Z"},
	}
	for i, row := range fixture.Intents {
		args := append([]string{"--org", org}, commands[i]...)
		args = append(args, "--idempotency-key", row.Content["intent_id"].(string))
		err := executeIntentCommandWithTimeout(t, "1ms", args...)
		var exit *IntentExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("%s/%s: %v, want status timeout", row.Domain, row.Op, err)
		}
		event := transport.published[len(transport.published)-1]
		if !event.CheckID() || !event.VerifySignature() {
			t.Fatalf("%s/%s: invalid signed event", row.Domain, row.Op)
		}
		intent, err := controlplane.ParseIntent(&event)
		if err != nil {
			t.Fatal(err)
		}
		if intent.Domain != row.Domain || intent.Op != row.Op || intent.Coordinate != row.Coordinate {
			t.Fatalf("got %s/%s %s, want %#v", intent.Domain, intent.Op, intent.Coordinate, row)
		}
		var actual map[string]any
		if err := json.Unmarshal([]byte(event.Content), &actual); err != nil {
			t.Fatal(err)
		}
		for key, value := range row.Content {
			if row.Op == "route-attach" && key == "public_route" {
				route, ok := actual[key].(map[string]any)
				if !ok || route["hostname"] != value.(map[string]any)["hostname"] {
					t.Errorf("route hostname=%#v, want %#v", actual[key], value)
				}
				continue
			}
			if !reflect.DeepEqual(actual[key], value) {
				t.Errorf("%s/%s content[%s]=%#v, want %#v", row.Domain, row.Op, key, actual[key], value)
			}
		}
		if row.Op != "route-attach" && !reflect.DeepEqual(actual, row.Content) {
			t.Errorf("%s/%s content=%#v, want exact fixture %#v", row.Domain, row.Op, actual, row.Content)
		}
	}
}
