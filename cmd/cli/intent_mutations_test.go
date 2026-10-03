package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type cliIntentRegistry struct {
	controlplane.RegistryMutationBackend
	services     map[uuid.UUID]*domain.Service
	environments map[uuid.UUID]*domain.Environment
	units        map[uuid.UUID][]domain.DeploymentUnit
	canonical    []nostr.Event
	eventStore   *localstore.Store
	signer       nostr.Signer
	createdAt    map[string]nostr.Timestamp
	reject       bool
}

func (r *cliIntentRegistry) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if svc := r.services[id]; svc != nil {
		copy := *svc
		return &copy, nil
	}
	return nil, nil
}
func (r *cliIntentRegistry) GetEnvironment(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if env := r.environments[id]; env != nil {
		copy := *env
		return &copy, nil
	}
	return nil, nil
}
func (r *cliIntentRegistry) record(kind int, id string, value any) error {
	wireKind, tags := nostrpool.ControlStateEnvelope(kind, id, false)
	if kind == kinds.EnvironmentRegistry {
		content, err := jsonObject(value)
		if err != nil {
			return err
		}
		content["deployment_units"] = r.units[uuid.MustParse(id)]
		value = content
	}
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if r.createdAt == nil {
		r.createdAt = make(map[string]nostr.Timestamp)
	}
	coordinate := fmt.Sprintf("%d:%s", wireKind, id)
	createdAt := nostr.Now()
	if createdAt <= r.createdAt[coordinate] {
		createdAt = r.createdAt[coordinate] + 1
	}
	r.createdAt[coordinate] = createdAt
	ev := nostr.Event{Kind: nostr.Kind(wireKind), CreatedAt: createdAt, Tags: tags, Content: string(content)}
	if err := r.signer.SignEvent(context.Background(), &ev); err != nil {
		return err
	}
	r.canonical = append(r.canonical, ev)
	if _, err := r.eventStore.SaveEvent(ev); err != nil {
		return err
	}
	return nil
}
func (r *cliIntentRegistry) CreateService(_ context.Context, svc *domain.Service) error {
	if r.reject {
		return errors.New("service creation rejected")
	}
	svc.CreatedAt = domain.NormalizeRevisionTime(time.Now())
	svc.UpdatedAt = svc.CreatedAt
	r.services[svc.ID] = svc
	return r.record(kinds.ServiceRegistry, svc.ID.String(), svc)
}
func (r *cliIntentRegistry) UpdateService(_ context.Context, svc *domain.Service) error {
	svc.UpdatedAt = domain.NormalizeRevisionTime(time.Now().Add(time.Second))
	r.services[svc.ID] = svc
	return r.record(kinds.ServiceRegistry, svc.ID.String(), svc)
}
func (r *cliIntentRegistry) UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expected time.Time) error {
	current, _ := r.GetByID(ctx, svc.ID)
	if current == nil || !domain.SameRevision(current.UpdatedAt, expected) {
		return fmt.Errorf("revision conflict")
	}
	return r.UpdateService(ctx, svc)
}
func (r *cliIntentRegistry) CreateEnvironment(_ context.Context, env *domain.Environment) error {
	env.CreatedAt = domain.NormalizeRevisionTime(time.Now())
	env.UpdatedAt = env.CreatedAt
	r.environments[env.ID] = env
	return r.record(kinds.EnvironmentRegistry, env.ID.String(), env)
}

func (r *cliIntentRegistry) CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	r.units[env.ID] = copyIntentTestUnits(units)
	return r.CreateEnvironment(ctx, env)
}
func (r *cliIntentRegistry) UpdateEnvironment(_ context.Context, env *domain.Environment) error {
	env.UpdatedAt = domain.NormalizeRevisionTime(time.Now().Add(time.Second))
	r.environments[env.ID] = env
	return r.record(kinds.EnvironmentRegistry, env.ID.String(), env)
}
func (r *cliIntentRegistry) UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expected time.Time) error {
	current, _ := r.GetEnvironment(ctx, env.ID)
	if current == nil || !domain.SameRevision(current.UpdatedAt, expected) {
		return fmt.Errorf("revision conflict")
	}
	r.units[env.ID] = copyIntentTestUnits(units)
	return r.UpdateEnvironment(ctx, env)
}

func copyIntentTestUnits(units []*domain.DeploymentUnit) []domain.DeploymentUnit {
	result := make([]domain.DeploymentUnit, 0, len(units))
	for _, unit := range units {
		result = append(result, *unit)
	}
	return result
}

var _ service.EnvironmentIntentRegistry = (*cliIntentRegistry)(nil)

type cliIntentTransport struct {
	process        func(nostr.Event)
	events         chan *nostr.Event
	published      []nostr.Event
	statuses       []nostr.Event
	noRelay        bool
	networkFailure bool
}

func (t *cliIntentTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	results, err := t.PublishWithResults(ctx, ev)
	if err != nil {
		return 0, err
	}
	if results[0].Accepted {
		return 1, nil
	}
	return 0, nil
}
func (t *cliIntentTransport) PublishWithResults(_ context.Context, ev nostr.Event) ([]client.ContextVMPublishResult, error) {
	t.published = append(t.published, ev)
	if t.noRelay {
		return []client.ContextVMPublishResult{{RelayURL: "wss://test.relay", Accepted: false, Reason: "blocked"}}, nil
	}
	if t.networkFailure {
		return []client.ContextVMPublishResult{{RelayURL: "wss://test.relay", Error: errors.New("relay unavailable")}}, errors.New("relay unavailable")
	}
	if t.process != nil {
		t.process(ev)
	}
	return []client.ContextVMPublishResult{{RelayURL: "wss://test.relay", Accepted: true}}, nil
}
func (t *cliIntentTransport) SubscribeOperator(_ context.Context, filters []nostr.Filter) (*client.ContextVMSubscription, error) {
	if len(filters) != 1 || len(filters[0].Kinds) != 1 || filters[0].Kinds[0] != 30315 {
		return nil, fmt.Errorf("unscoped status filter: %#v", filters)
	}
	return client.NewContextVMSubscription(t.events, make(chan struct{}), nil, nil, []string{"wss://test.relay"}, func() {}), nil
}
func (t *cliIntentTransport) Close() {}

func setupCLIIntentPipeline(t *testing.T) (*cliIntentRegistry, *cliIntentTransport, string, string) {
	t.Helper()
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	operatorKey, daemonKey := nostr.Generate(), nostr.Generate()
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", operatorKey.Hex())
	t.Setenv("BAHIA_NOSTR_SERVICE_PUBKEY", daemonKey.Public().Hex())
	orgID := uuid.NewString()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "processor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	signer := keyer.NewPlainKeySigner(daemonKey)
	registry := &cliIntentRegistry{services: map[uuid.UUID]*domain.Service{}, environments: map[uuid.UUID]*domain.Environment{}, units: map[uuid.UUID][]domain.DeploymentUnit{}, signer: signer, eventStore: store}
	transport := &cliIntentTransport{events: make(chan *nostr.Event, 8)}
	status := controlplane.NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		transport.statuses = append(transport.statuses, ev)
		transport.events <- &ev
		return nil
	}, signer, zap.NewNop())
	trust := controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID: operatorKey.Public().Hex()}))
	processor := controlplane.NewIntentProcessor(trust, store, status, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true, "environment": true}}, zap.NewNop())
	processor.RegisterHandler("service", controlplane.NewServiceIntentHandler(controlplane.ServiceIntentHandlerConfig{Registry: registry, Reader: registry, Logger: zap.NewNop()}))
	processor.RegisterHandler("environment", controlplane.NewEnvironmentIntentHandler(registry, nil, zap.NewNop()))
	transport.process = func(ev nostr.Event) {
		if !ev.CheckID() || !ev.VerifySignature() {
			t.Errorf("CLI intent is not signed correctly")
		}
		intent, err := controlplane.ParseIntent(&ev)
		if err != nil {
			t.Errorf("ParseIntent: %v", err)
			return
		}
		intent.Actor = ev.PubKey.Hex()
		_ = processor.ProcessInProcess(context.Background(), intent)
	}
	oldPublisher := newCLIIntentPublisher
	newCLIIntentPublisher = func(cfg client.IntentPublisherConfig) (*client.IntentPublisher, error) {
		cfg.Transport = transport
		return client.NewIntentPublisher(cfg)
	}
	t.Cleanup(func() { newCLIIntentPublisher = oldPublisher })
	oldReader := readIntentCanonicalEvents
	readIntentCanonicalEvents = func(_ *cobra.Command, _ string, kind int) ([]nostr.Event, error) {
		var out []nostr.Event
		for ev := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30900}, Authors: []nostr.PubKey{daemonKey.Public()}}) {
			decoded, err := client.DecodeControlStateEvent(ev)
			if err != nil {
				return nil, err
			}
			if decoded.LegacyKind == kind {
				out = append(out, ev)
			}
		}
		return out, nil
	}
	t.Cleanup(func() { readIntentCanonicalEvents = oldReader })
	return registry, transport, orgID, daemonKey.Public().Hex()
}

func executeIntentCommand(t *testing.T, args ...string) error {
	return executeIntentCommandWithTimeout(t, "2s", args...)
}

func executeIntentCommandWithTimeout(t *testing.T, timeout string, args ...string) error {
	t.Helper()
	root := newRootCommand()
	root.SilenceUsage = true
	root.SilenceErrors = true
	root.SetArgs(append([]string{"--relay", "wss://test.relay", "--result-timeout", timeout}, args...))
	return root.ExecuteContext(context.Background())
}

func TestCLIServiceEnvironmentIntentPipeline(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	serviceID, environmentID := uuid.NewString(), uuid.NewString()
	if err := executeIntentCommand(t, "--http-fallback", "services", "create", "--id", serviceID, "--org", org, "--name", "api", "--artifact-repo", "registry/api"); err != nil {
		t.Fatal(err)
	}
	if len(registry.canonical) != 1 || registry.canonical[0].Kind != 30900 || len(transport.published) != 1 || transport.published[0].Kind != 30900 {
		t.Fatalf("service intent/canonical not produced")
	}
	if registry.services[uuid.MustParse(serviceID)].Name != "api" {
		t.Fatal("service handler did not apply CLI payload")
	}
	serviceRevision := registry.services[uuid.MustParse(serviceID)].UpdatedAt
	if err := executeIntentCommand(t, "environments", "create", "--id", environmentID, "--org", org, "--name", "prod"); err != nil {
		t.Fatal(err)
	}
	if len(registry.canonical) != 2 || registry.environments[uuid.MustParse(environmentID)].Name != "prod" {
		t.Fatal("environment handler did not publish canonical state")
	}
	environmentRevision := registry.environments[uuid.MustParse(environmentID)].UpdatedAt
	if err := executeIntentCommand(t, "services", "update", "--service", serviceID, "--name", "api-v2"); err != nil {
		t.Fatal(err)
	}
	if registry.services[uuid.MustParse(serviceID)].Name != "api-v2" {
		t.Fatal("service update not applied")
	}
	if err := executeIntentCommand(t, "environments", "update", environmentID, "--name", "prod-v2"); err != nil {
		t.Fatal(err)
	}
	if registry.environments[uuid.MustParse(environmentID)].Name != "prod-v2" {
		t.Fatal("environment update not applied")
	}
	for index, revision := range map[int]time.Time{2: serviceRevision, 3: environmentRevision} {
		var content map[string]json.RawMessage
		if err := json.Unmarshal([]byte(transport.published[index].Content), &content); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := json.Unmarshal(content["expected_updated_at"], &got); err != nil {
			t.Fatal(err)
		}
		if got != revision.Format(time.RFC3339Nano) {
			t.Fatalf("intent %d revision = %s, want %s", index, got, revision.Format(time.RFC3339Nano))
		}
	}
	for _, ev := range transport.published {
		if ev.Kind != 30900 || !ev.VerifySignature() {
			t.Fatalf("published event is not a signed intent: %#v", ev)
		}
	}
	if len(transport.statuses) != 4 {
		t.Fatalf("30315 status events = %d, want 4", len(transport.statuses))
	}
	for _, ev := range transport.statuses {
		if ev.Kind != 30315 || !ev.VerifySignature() || !hasIntentStatusTag(ev.Tags, "status", "accepted") {
			t.Fatalf("invalid accepted status: %#v", ev)
		}
	}
	serviceState, err := canonicalService(&cobra.Command{}, serviceID)
	if err != nil || serviceState.Name != "api-v2" {
		t.Fatalf("latest service state = %#v, %v", serviceState, err)
	}
	environmentState, err := canonicalEnvironment(&cobra.Command{}, environmentID)
	if err != nil || environmentState.Name != "prod-v2" {
		t.Fatalf("latest environment state = %#v, %v", environmentState, err)
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	entries, err := outbox.ListEntries([]string{localstore.OutboxPublished}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("published outbox entries = %d, want 4", len(entries))
	}
}

func hasIntentStatusTag(tags nostr.Tags, key, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func TestCLIIntentRejectionAndTimeoutRemainInspectable(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	registry.reject = true
	err := executeIntentCommand(t, "services", "create", "--id", uuid.NewString(), "--org", org, "--name", "bad", "--artifact-repo", "registry/bad")
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Message, "service creation rejected") {
		t.Fatalf("rejection = %v", err)
	}
	transport.process = nil
	id := uuid.NewString()
	err = executeIntentCommandWithTimeout(t, "15ms", "services", "create", "--id", id, "--org", org, "--name", "pending", "--artifact-repo", "registry/pending")
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(exit.Message, "intent_id=") || !strings.Contains(exit.Message, "event_id=") {
		t.Fatalf("timeout = %v", err)
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	pending, err := outbox.ListEntries([]string{localstore.OutboxPending}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].EntityID != id {
		t.Fatalf("pending CLI outbox = %#v", pending)
	}
	var listing bytes.Buffer
	cmd := outboxListCommand()
	cmd.SetOut(&listing)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listing.String(), pending[0].Event.ID.Hex()) {
		t.Fatalf("bahia outbox list did not show pending intent: %s", listing.String())
	}
}

func TestCLIIntentNoRelayExit3(t *testing.T) {
	_, transport, org, _ := setupCLIIntentPipeline(t)
	transport.noRelay = true
	err := executeIntentCommand(t, "environments", "create", "--id", uuid.NewString(), "--org", org, "--name", "prod")
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("no relay = %v", err)
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	failed, err := outbox.ListEntries([]string{localstore.OutboxFailed}, 10)
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed outbox entries = %#v, %v", failed, err)
	}
}

func TestCLIIntentRelayTransportFailureStaysPending(t *testing.T) {
	_, transport, org, _ := setupCLIIntentPipeline(t)
	transport.networkFailure = true
	err := executeIntentCommand(t, "environments", "create", "--id", uuid.NewString(), "--org", org, "--name", "prod")
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("network failure = %v", err)
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	pending, err := outbox.ListEntries([]string{localstore.OutboxPending}, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending outbox entries = %#v, %v", pending, err)
	}
}

func TestCLIServiceRevisionConflictReturnsOne30315(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	id := uuid.NewString()
	if err := executeIntentCommand(t, "services", "create", "--id", id, "--org", org, "--name", "api", "--artifact-repo", "registry/api"); err != nil {
		t.Fatal(err)
	}
	process := transport.process
	transport.process = func(ev nostr.Event) {
		registry.services[uuid.MustParse(id)].UpdatedAt = registry.services[uuid.MustParse(id)].UpdatedAt.Add(time.Second)
		process(ev)
	}
	err := executeIntentCommand(t, "services", "update", "--service", id, "--name", "api-new")
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Message, "conflict") {
		t.Fatalf("conflict = %v", err)
	}
	if len(transport.statuses) != 2 || !hasIntentStatusTag(transport.statuses[1].Tags, "status", "conflict") {
		t.Fatalf("conflict statuses = %#v", transport.statuses)
	}
	if registry.services[uuid.MustParse(id)].Name != "api" {
		t.Fatal("conflicted service update mutated canonical state")
	}
}

func TestCLIIntentPreservesFlagAndFileDesiredState(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	serviceID, envID := uuid.NewString(), uuid.NewString()
	managedPath := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(managedPath, []byte(`{"schema_version":"1","service_name":"web"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeIntentCommand(t, "services", "create", "--id", serviceID, "--org", org,
		"--name", "web", "--artifact-repo", "registry/web", "--repo-source", "gitea", "--repo-coordinate", "acme/web",
		"--clone-url", "https://git.example/acme/web.git", "--ci-provider", "hiveci", "--managed-runtime-config-file", managedPath); err != nil {
		t.Fatal(err)
	}
	svc := registry.services[uuid.MustParse(serviceID)]
	if svc.RuntimeConfig == nil || svc.RuntimeConfig.Managed == nil || svc.RuntimeConfig.Managed.ServiceName != "web" || svc.Repository == nil || svc.Repository.RepoCoordinate != "acme/web" {
		t.Fatalf("service desired state = %#v", svc)
	}
	if err := executeIntentCommand(t, "services", "update", "--service", serviceID, "--ci-workflow", ".hiveci/build.yaml"); err != nil {
		t.Fatal(err)
	}
	svc = registry.services[uuid.MustParse(serviceID)]
	if svc.Repository.Source != "gitea" || svc.Repository.RepoCoordinate != "acme/web" || svc.Repository.CI.Provider != "hiveci" || svc.Repository.CI.WorkflowPath != ".hiveci/build.yaml" || svc.RuntimeConfig.Managed.ServiceName != "web" {
		t.Fatalf("merged service desired state = %#v", svc)
	}
	unitsPath := filepath.Join(t.TempDir(), "units.json")
	if err := os.WriteFile(unitsPath, []byte(`[{"key":"web","runtime_type":"compose","endpoint_ref":"target","compose_dir":"/srv/web"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeIntentCommand(t, "environments", "create", "--id", envID, "--org", org, "--name", "prod", "--default-unit-key", "web", "--units-file", unitsPath); err != nil {
		t.Fatal(err)
	}
	units := registry.units[uuid.MustParse(envID)]
	if len(units) != 1 || units[0].Key != "web" || units[0].ComposeDir != "/srv/web" || registry.environments[uuid.MustParse(envID)].Targeting.DefaultUnitKey != "web" {
		t.Fatalf("environment desired units = %#v", units)
	}
	if err := executeIntentCommand(t, "environments", "update", envID, "--protected"); err != nil {
		t.Fatal(err)
	}
	if !registry.environments[uuid.MustParse(envID)].Protected || len(registry.units[uuid.MustParse(envID)]) != 1 {
		t.Fatal("environment update lost unchanged unit set")
	}
	if len(transport.published) != 4 {
		t.Fatalf("intent count = %d", len(transport.published))
	}
}

func TestCLIIntentMintsEntityAndIntentUUIDv7(t *testing.T) {
	_, transport, org, _ := setupCLIIntentPipeline(t)
	if err := executeIntentCommand(t, "services", "create", "--org", org, "--name", "api", "--artifact-repo", "registry/api"); err != nil {
		t.Fatal(err)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published %d intents", len(transport.published))
	}
	for _, key := range []string{"d", "intent_id"} {
		var value string
		for _, tag := range transport.published[0].Tags {
			if len(tag) >= 2 && tag[0] == key {
				value = tag[1]
			}
		}
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.Version() != 7 {
			t.Fatalf("%s=%q is not UUIDv7: %v", key, value, err)
		}
	}
}

func TestCLIEnvironmentUnitMutationUsesIntentPipeline(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	envID := uuid.NewString()
	if err := executeIntentCommand(t, "environments", "create", "--id", envID, "--org", org, "--name", "prod"); err != nil {
		t.Fatal(err)
	}
	unitFile := filepath.Join(t.TempDir(), "unit.json")
	if err := os.WriteFile(unitFile, []byte(`{"key":"web","runtime_type":"compose","endpoint_ref":"target","compose_dir":"/srv/web"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeIntentCommand(t, "environments", "units", "create", envID, "--file", unitFile, "--default-unit-key", "web"); err != nil {
		t.Fatal(err)
	}
	if len(registry.units[uuid.MustParse(envID)]) != 1 || len(transport.published) != 2 || transport.published[1].Kind != 30900 {
		t.Fatalf("unit mutation did not use complete-set intent: units=%#v", registry.units[uuid.MustParse(envID)])
	}
}

func TestCLIIntentPublisherUsesNIP46SignerWithoutLocalIdentityKey(t *testing.T) {
	resetOperatorGlobals(t)
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "bunker://operator?relay=wss://relay.example&secret=connect")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_SERVICE_PUBKEY", nostr.Generate().Public().Hex())
	operatorKey := nostr.Generate()
	closed := false
	previous := newCLINIP46Signer
	newCLINIP46Signer = func(_ context.Context, bunkerURI, clientKey string) (nostr.Signer, string, func() error, error) {
		if !strings.HasPrefix(bunkerURI, "bunker://") || clientKey == "" {
			t.Fatalf("invalid NIP-46 inputs: %q", bunkerURI)
		}
		return keyer.NewPlainKeySigner(operatorKey), operatorKey.Public().Hex(), func() error { closed = true; return nil }, nil
	}
	t.Cleanup(func() { newCLINIP46Signer = previous })
	cmd := newRootCommand()
	cmd.SetContext(context.Background())
	if err := cmd.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatal(err)
	}
	publisher, _, err := buildCLIIntentPublisher(cmd)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := publisher.PrepareIntent(context.Background(), client.PublishIntentRequest{Domain: "service", Op: "create", Coordinate: uuid.NewString(), OrgID: uuid.NewString(), IntentID: uuid.NewString(), Content: map[string]interface{}{"name": "api"}})
	if err != nil || !prepared.Event.VerifySignature() || prepared.Event.PubKey.Hex() != operatorKey.Public().Hex() {
		t.Fatalf("NIP-46-signed intent = %#v, %v", prepared, err)
	}
	publisher.Close()
	if !closed {
		t.Fatal("NIP-46 signer was not closed")
	}
}
