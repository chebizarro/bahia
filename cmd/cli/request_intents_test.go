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
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
)

func TestCLIRequestIntentsMatchD79Fixtures(t *testing.T) {
	registry, transport, _, _, _, _ := setupCLIIntentPipelineWithProcessor(t)
	transport.process = nil
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "d79-intent-content.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Intents []struct {
			Domain         string         `json:"domain"`
			Op             string         `json:"op"`
			Coordinate     string         `json:"coordinate"`
			IdempotencyKey string         `json:"idempotency_key"`
			Content        map[string]any `json:"content"`
		} `json:"intents"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	const org = "018f6a60-0000-7000-8000-000000000001"
	const svc = "018f6a60-0000-7000-8000-000000000002"
	const credential = "018f6a60-0000-7000-8000-000000000007"
	registry.services[uuid.MustParse(svc)] = &domain.Service{ID: uuid.MustParse(svc), OrgID: uuid.MustParse(org)}
	if err := registry.record(kinds.ServiceRegistry, svc, registry.services[uuid.MustParse(svc)]); err != nil {
		t.Fatal(err)
	}
	commands := map[string][]string{
		"build/request": {"builds", "request", "--service", svc, "--git-ref", "refs/heads/main", "--credential-ref", credential, "--artifact-repo", "registry.example/api"},
		"adoption/scan": {"adopt", "scan", "--target", "production=docker-prod"},
	}
	for _, item := range fixture.Intents {
		command, ok := commands[item.Domain+"/"+item.Op]
		if !ok {
			continue
		}
		args := append([]string{"--org", org}, command...)
		args = append(args, "--idempotency-key", item.IdempotencyKey)
		err := executeIntentCommandWithTimeout(t, "1ms", args...)
		var exit *IntentExitError
		if !errors.As(err, &exit) || exit.Code != client.ExitCodeTimeout {
			t.Fatalf("%s/%s: %v, want status timeout", item.Domain, item.Op, err)
		}
		event := transport.published[len(transport.published)-1]
		if event.Kind != 30900 || !event.CheckID() || !event.VerifySignature() {
			t.Fatalf("%s/%s: not a signed intent", item.Domain, item.Op)
		}
		intent, err := controlplane.ParseIntent(&event)
		if err != nil {
			t.Fatal(err)
		}
		if intent.Domain != item.Domain || intent.Op != item.Op || intent.Coordinate != item.Coordinate || intent.IntentID != item.IdempotencyKey {
			t.Fatalf("got %s/%s %s %s, want %#v", intent.Domain, intent.Op, intent.Coordinate, intent.IntentID, item)
		}
		var actual map[string]any
		if err := json.Unmarshal([]byte(event.Content), &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, item.Content) {
			t.Errorf("%s/%s content=%#v, want exact fixture %#v", item.Domain, item.Op, actual, item.Content)
		}
	}
	if len(transport.published) != len(commands) {
		t.Fatalf("published %d request intents, want %d", len(transport.published), len(commands))
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	entries, err := outbox.ListEntries([]string{localstore.OutboxPending}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(commands) {
		t.Fatalf("pending request intents = %d, want %d", len(entries), len(commands))
	}
}

type cliBuildStarter struct {
	requests []controlplane.HiveCIBuildStartRequest
}

func (s *cliBuildStarter) StartHiveCIBuild(_ context.Context, req controlplane.HiveCIBuildStartRequest) (*controlplane.HiveCIBuildStartResult, error) {
	s.requests = append(s.requests, req)
	return &controlplane.HiveCIBuildStartResult{BuildID: req.BuildID, GitSHA: "0123456789abcdef0123456789abcdef01234567", GitRef: req.GitRef, CIRunID: "hive-run-1"}, nil
}

type cliBuildRegistry struct{ build *domain.Build }

func (r *cliBuildRegistry) RegisterBuild(_ context.Context, build *domain.Build) error {
	r.build = build
	return nil
}
func (r *cliBuildRegistry) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	if r.build != nil && r.build.ID == id {
		return r.build, nil
	}
	return nil, nil
}
func (r *cliBuildRegistry) ListBuilds(context.Context, uuid.UUID, int, int) ([]domain.Build, error) {
	return nil, nil
}

type cliBuildCredentials struct{ secret *domain.ServiceSecret }

func (c cliBuildCredentials) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	return c.secret, nil
}

type cliBuildMembers struct{}

func (cliBuildMembers) GetMember(_ context.Context, org uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	return &domain.OrgMember{OrgID: org, Pubkey: pubkey, Role: domain.RoleOwner}, nil
}
func (cliBuildMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type cliScanAdoption struct {
	controlplane.AdoptionOperatorService
	calls int
	fail  error
}

func (a *cliScanAdoption) Scan(_ context.Context, req service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	a.calls++
	if a.fail != nil {
		return nil, a.fail
	}
	return []service.AdoptionPreview{{Target: req.Targets[0], Containers: []service.AdoptionPreviewContainer{{
		Discovered:          runtime.DiscoveredContainer{ContainerID: "container-1", ContainerName: "api", ImageRef: "registry.example/api:v1", Environment: map[string]string{"SECRET": "hidden"}},
		ProposedServiceName: "api", Adoptable: true, Warnings: []string{"review"},
	}}}}, nil
}

func TestCLIRequestIntentsThroughD79Handlers(t *testing.T) {
	registry, transport, org, _, processor, actor := setupCLIIntentPipelineWithProcessor(t)
	serviceID, credentialID := uuid.New(), uuid.New()
	serviceRecord := &domain.Service{ID: serviceID, OrgID: uuid.MustParse(org), ArtifactRepo: "registry.example/api", Repository: &domain.RepositoryRef{RepoCoordinate: "team/api"}}
	registry.services[serviceID] = serviceRecord
	if err := registry.record(kinds.ServiceRegistry, serviceID.String(), serviceRecord); err != nil {
		t.Fatal(err)
	}
	starter, builds := &cliBuildStarter{}, &cliBuildRegistry{}
	buildHandler := controlplane.NewEncryptedBuildHandlers(controlplane.EncryptedBuildHandlersConfig{
		Starter: starter, Registry: builds, Builds: builds, Services: registry,
		Secrets: cliBuildCredentials{secret: &domain.ServiceSecret{ID: credentialID, ServiceID: serviceID}},
		RBAC:    auth.NewRBAC(cliBuildMembers{}),
	})
	processor.RegisterHandler("build", controlplane.NewBuildIntentHandler(buildHandler))
	adoption := &cliScanAdoption{}
	processor.RegisterHandler("adoption", controlplane.NewAdoptionIntentHandler(adoption, []string{actor}))

	root := newRootCommand()
	root.SetContext(context.Background())
	if err := root.PersistentFlags().Set("relay", "wss://test.relay"); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("org", org); err != nil {
		t.Fatal(err)
	}
	buildKey := uuid.Must(uuid.NewV7()).String()
	buildReq := client.BuildRequestNostrRequest{ServiceID: serviceID.String(), GitRef: "refs/heads/main", RepositoryCredentialRef: credentialID.String(), ArtifactRepo: "registry.example/api", IdempotencyKey: buildKey}
	result, err := runBuildRequestIntent(root, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "queued" || result.BuildID == "" || result.IntentID != buildKey || len(starter.requests) != 1 || builds.build == nil {
		t.Fatalf("build status=%#v starter=%d registry=%#v", result, len(starter.requests), builds.build)
	}
	if _, err := runBuildRequestIntent(root, buildReq); err != nil {
		t.Fatal(err)
	}
	if len(starter.requests) != 1 {
		t.Fatalf("build replay started %d runs", len(starter.requests))
	}
	if err := root.PersistentFlags().Set("org", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := runBuildRequestIntent(root, buildReq); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched --org error = %v", err)
	}
	if err := root.PersistentFlags().Set("org", org); err != nil {
		t.Fatal(err)
	}
	rejectedBuild := buildReq
	rejectedBuild.IdempotencyKey = uuid.Must(uuid.NewV7()).String()
	rejectedBuild.ArtifactRepo = "registry.example/wrong"
	_, err = runBuildRequestIntent(root, rejectedBuild)
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != client.ExitCodeRejected || len(starter.requests) != 1 {
		t.Fatalf("rejected build error=%v starter calls=%d", err, len(starter.requests))
	}
	scanKey := uuid.Must(uuid.NewV7()).String()
	scanReq := client.AdoptionScanRequest{Targets: []client.AdoptionTarget{{Name: "production", EndpointRef: "docker-prod"}}, Limit: 20, IdempotencyKey: scanKey}
	scan, err := runAdoptionScanIntent(root, scanReq)
	if err != nil {
		t.Fatal(err)
	}
	if scan.IntentID != scanKey || scan.TotalFindings != 1 || len(scan.Findings) != 1 || scan.Findings[0].ContainerName != "api" || scan.Findings[0].WarningsCount != 1 {
		t.Fatalf("adoption status data = %#v", scan)
	}
	if len(flattenAdoptionPreviewRows(scan)) != 1 || flattenAdoptionPreviewRows(scan)[0].Container != "api" {
		t.Fatalf("adoption rows = %#v", flattenAdoptionPreviewRows(scan))
	}
	adoption.fail = errors.New("scan unavailable")
	scanReq.IdempotencyKey = uuid.Must(uuid.NewV7()).String()
	_, err = runAdoptionScanIntent(root, scanReq)
	if !errors.As(err, &exit) || exit.Code != client.ExitCodeRejected {
		t.Fatalf("rejected scan error=%v", err)
	}
	if len(transport.statuses) != 5 || adoption.calls != 2 {
		t.Fatalf("statuses=%d adoption scans=%d", len(transport.statuses), adoption.calls)
	}
	for i, status := range transport.statuses {
		if i == 2 || i == 4 {
			if !hasIntentStatusTag(status.Tags, "status", "rejected") {
				t.Fatalf("unexpected rejection status: %#v", status.Tags)
			}
			continue
		}
		if !hasIntentStatusTag(status.Tags, "status", "accepted") {
			t.Fatalf("unexpected intent status: %#v", status.Tags)
		}
		if status.Kind != 30315 {
			t.Fatalf("status kind=%d", status.Kind)
		}
	}
	if stringData := transport.statuses[3].Content; strings.Contains(stringData, "hidden") {
		t.Fatalf("adoption status exposed runtime secret: %s", stringData)
	}
}
