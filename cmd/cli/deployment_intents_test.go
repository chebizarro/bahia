package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

func TestCLIDeploymentRuntimeIntentsMatchD69FixturesAndStayInOutboxWithoutStatus(t *testing.T) {
	_, transport, _, _ := setupCLIIntentPipeline(t)
	transport.process = nil // relay accepts the event, but daemon emits no status
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "deployment-intents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []nostr.Event
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		domain, op := deploymentFixtureTag(fixture.Tags, "domain"), deploymentFixtureTag(fixture.Tags, "op")
		if domain != "deployment" && domain != "runtime" {
			continue
		}
		name := domain + "/" + op
		t.Run(name, func(t *testing.T) {
			var want map[string]any
			if err := json.Unmarshal([]byte(fixture.Content), &want); err != nil {
				t.Fatal(err)
			}
			org := deploymentFixtureTag(fixture.Tags, "org")
			id := deploymentFixtureTag(fixture.Tags, "intent_id")
			args := []string{"--http-fallback"}
			if domain == "deployment" {
				if op == "approve" || op == "reject" {
					args = append(args, "deployments")
				}
				if op == "create" {
					args = append(args, "deploy", "--service", want["service_id"].(string), "--environment", want["environment_id"].(string), "--artifact", want["artifact_id"].(string))
				}
				if op == "rollback" {
					args = append(args, "rollback", "--service", want["service_id"].(string), "--environment", want["environment_id"].(string), "--target-artifact", want["target_artifact_id"].(string), "--supersedes-intent", want["supersedes_intent_id"].(string))
				}
				if op == "approve" || op == "reject" {
					args = append(args, op, "--intent", want["deployment_intent_id"].(string), "--expected-updated-at", want["expected_updated_at"].(string))
				}
			} else {
				args = append(args, "services", op, "--service", want["service_id"].(string), "--environment", want["environment_id"].(string))
				if op == "deploy" {
					args = append(args, "--artifact", want["artifact_id"].(string))
				}
			}
			args = append(args, "--org", org, "--idempotency-key", id)
			err := executeIntentCommandWithTimeout(t, "1ms", args...)
			var exit *IntentExitError
			if !errors.As(err, &exit) || exit.Code != 2 {
				t.Fatalf("missing status exit = %v, want 2", err)
			}
			got := transport.published[len(transport.published)-1]
			if got.Kind != 30900 || !got.CheckID() || !got.VerifySignature() {
				t.Fatalf("not a signed 30900 intent: %#v", got)
			}
			for _, key := range []string{"d", "domain", "schema", "op", "org", "intent_id"} {
				if deploymentFixtureTag(got.Tags, key) != deploymentFixtureTag(fixture.Tags, key) {
					t.Errorf("%s=%q, want %q", key, deploymentFixtureTag(got.Tags, key), deploymentFixtureTag(fixture.Tags, key))
				}
			}
			var actual map[string]any
			if err := json.Unmarshal([]byte(got.Content), &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Errorf("content = %#v, want %#v", actual, want)
			}
		})
	}
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	entries, err := outbox.ListEntries([]string{localstore.OutboxPending}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("pending outbox entries = %d, want 7", len(entries))
	}
}

func deploymentFixtureTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

type cliRuntimeServiceRepo struct {
	repository.ServiceRepository
	service *domain.Service
}

func (r cliRuntimeServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if r.service != nil && r.service.ID == id {
		return r.service, nil
	}
	return nil, nil
}

type cliRuntimeEnvironmentRepo struct {
	repository.EnvironmentRepository
	environment *domain.Environment
}

func (r cliRuntimeEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if r.environment != nil && r.environment.ID == id {
		return r.environment, nil
	}
	return nil, nil
}

type cliRuntimeLifecycle struct{ restarts int }

func (*cliRuntimeLifecycle) DeployWithStatus(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{}, nil
}
func (r *cliRuntimeLifecycle) Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	r.restarts++
	return &domain.RuntimeObservation{}, nil
}
func (*cliRuntimeLifecycle) Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{}, nil
}

func TestCLIRuntimeIntentThroughD69ProcessorAcceptsAndRejects(t *testing.T) {
	registry, transport, org, _ := setupCLIIntentPipeline(t)
	operatorSecret, err := nostr.SecretKeyFromHex(os.Getenv("BAHIA_NOSTR_PRIVATE_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	serviceID, environmentID := uuid.New(), uuid.New()
	serviceRecord := &domain.Service{ID: serviceID, OrgID: uuid.MustParse(org)}
	environmentRecord := &domain.Environment{ID: environmentID, OrgID: uuid.MustParse(org)}
	resourceRegistry := service.NewRegistryService(
		cliRuntimeServiceRepo{service: serviceRecord}, cliRuntimeEnvironmentRepo{environment: environmentRecord},
		nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop(),
	)
	lifecycle := &cliRuntimeLifecycle{}
	statuses := controlplane.NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		transport.statuses = append(transport.statuses, ev)
		transport.events <- &ev
		return nil
	}, registry.signer, zap.NewNop())
	processor := controlplane.NewIntentProcessor(
		controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{org: operatorSecret.Public().Hex()})),
		registry.eventStore, statuses, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"runtime": true}}, zap.NewNop(),
	)
	processor.RegisterHandler("runtime", controlplane.NewDeploymentIntentHandler(controlplane.EncryptedServiceHandlersConfig{Registry: resourceRegistry}, lifecycle))
	transport.process = func(ev nostr.Event) {
		intent, parseErr := controlplane.ParseIntent(&ev)
		if parseErr != nil {
			t.Errorf("parse signed CLI intent: %v", parseErr)
			return
		}
		intent.Actor = ev.PubKey.Hex()
		_ = processor.ProcessInProcess(context.Background(), intent)
	}
	args := []string{"services", "actions", "restart", "--org", org, "--service", serviceID.String(), "--environment", environmentID.String()}
	if err := executeIntentCommand(t, args...); err != nil {
		t.Fatalf("accepted runtime CLI exit: %v", err)
	}
	if lifecycle.restarts != 1 || !hasIntentStatusTag(transport.statuses[0].Tags, "status", "accepted") {
		t.Fatalf("runtime accepted without lifecycle/status: restarts=%d statuses=%#v", lifecycle.restarts, transport.statuses)
	}
	serviceRecord.OrgID = uuid.New()
	err = executeIntentCommand(t, args...)
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("rejected runtime CLI exit = %v, want 1", err)
	}
	if lifecycle.restarts != 1 || !hasIntentStatusTag(transport.statuses[1].Tags, "status", "rejected") {
		t.Fatalf("rejected runtime changed lifecycle: restarts=%d statuses=%#v", lifecycle.restarts, transport.statuses)
	}
}

func TestDeploymentAndRuntimeDirectCommandAliases(t *testing.T) {
	root := newRootCommand()
	for _, path := range [][]string{
		{"deploy"}, {"rollback"},
		{"services", "deploy"}, {"services", "restart"}, {"services", "stop"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] || cmd.RunE == nil {
			t.Fatalf("command %v not executable: cmd=%v err=%v", path, cmd, err)
		}
		if cmd.Flags().Lookup("org") == nil || cmd.Flags().Lookup("idempotency-key") == nil {
			t.Fatalf("command %v missing intent flags", path)
		}
	}
}

type cliDeploymentIntentRepo struct {
	repository.DeploymentIntentRepository
	intents map[uuid.UUID]*domain.DeploymentIntent
}

func (r *cliDeploymentIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.intents[id], nil
}
func (r *cliDeploymentIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, approval domain.ApprovalStatus) error {
	r.intents[id].ApprovalStatus = approval
	return nil
}
func (r *cliDeploymentIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	r.intents[id].Status = status
	return nil
}

type cliDeploymentStateRepo struct {
	repository.EnvironmentServiceStateRepository
}

func (cliDeploymentStateRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return nil, nil
}

type cliDeploymentCanonicalPublisher struct {
	service.BuildDeployStatePublisher
	store  *localstore.Store
	signer nostr.Signer
	count  int
}

func (p *cliDeploymentCanonicalPublisher) PublishDeploymentIntentRegistry(ctx context.Context, intent *domain.DeploymentIntent, deleted bool) error {
	kind, tags := nostrpool.ControlStateEnvelope(kinds.DeploymentIntentRegistry, intent.ID.String(), deleted)
	content, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	event := nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := p.signer.SignEvent(ctx, &event); err != nil {
		return err
	}
	if _, err := p.store.SaveEvent(event); err != nil {
		return err
	}
	p.count++
	return nil
}

func TestCLIDeploymentRejectThroughD69ProcessorPublishesCanonicalState(t *testing.T) {
	pipeline, transport, org, _ := setupCLIIntentPipeline(t)
	operatorSecret, err := nostr.SecretKeyFromHex(os.Getenv("BAHIA_NOSTR_PRIVATE_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	serviceID, environmentID, targetID := uuid.New(), uuid.New(), uuid.New()
	revision := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	serviceRecord := &domain.Service{ID: serviceID, OrgID: uuid.MustParse(org)}
	environmentRecord := &domain.Environment{ID: environmentID, OrgID: uuid.MustParse(org)}
	intents := &cliDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{targetID: {
		ID: targetID, ServiceID: serviceID, EnvironmentID: environmentID,
		ApprovalStatus: domain.ApprovalStatusPending, Status: domain.IntentStatusPending, UpdatedAt: revision,
	}}}
	canonical := &cliDeploymentCanonicalPublisher{store: pipeline.eventStore, signer: pipeline.signer}
	resourceRegistry := service.NewRegistryService(
		cliRuntimeServiceRepo{service: serviceRecord}, cliRuntimeEnvironmentRepo{environment: environmentRecord},
		nil, nil, intents, nil, nil, cliDeploymentStateRepo{}, nil, &events.NoopPublisher{}, zap.NewNop(),
	)
	resourceRegistry.SetCPStatePublisher(canonical)
	statuses := controlplane.NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		transport.statuses = append(transport.statuses, ev)
		transport.events <- &ev
		return nil
	}, pipeline.signer, zap.NewNop())
	processor := controlplane.NewIntentProcessor(
		controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{org: operatorSecret.Public().Hex()})),
		pipeline.eventStore, statuses, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"deployment": true}}, zap.NewNop(),
	)
	processor.RegisterHandler("deployment", controlplane.NewDeploymentIntentHandler(controlplane.EncryptedServiceHandlersConfig{Registry: resourceRegistry}, nil))
	transport.process = func(ev nostr.Event) {
		intent, parseErr := controlplane.ParseIntent(&ev)
		if parseErr != nil {
			t.Errorf("parse signed CLI intent: %v", parseErr)
			return
		}
		intent.Actor = ev.PubKey.Hex()
		_ = processor.ProcessInProcess(context.Background(), intent)
	}
	args := []string{"deployments", "reject", "--org", org, "--intent", targetID.String(), "--expected-updated-at", revision.Format(time.RFC3339)}
	if err := executeIntentCommand(t, args...); err != nil {
		t.Fatalf("accepted deployment rejection CLI exit: %v", err)
	}
	if intents.intents[targetID].Status != domain.IntentStatusRejected || canonical.count != 1 || !hasIntentStatusTag(transport.statuses[0].Tags, "status", "accepted") {
		t.Fatalf("deployment rejection did not persist and publish canonical state: intent=%#v canonical=%d statuses=%#v", intents.intents[targetID], canonical.count, transport.statuses)
	}
	var persisted bool
	for ev := range pipeline.eventStore.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30900}}) {
		decoded, decodeErr := client.DecodeControlStateEvent(ev)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if decoded.LegacyKind == kinds.DeploymentIntentRegistry && decoded.DTag == targetID.String() && ev.VerifySignature() {
			persisted = true
		}
	}
	if !persisted {
		t.Fatal("accepted deployment decision lacks canonical signed 30900")
	}
	serviceRecord.OrgID = uuid.New()
	err = executeIntentCommand(t, args...)
	var exit *IntentExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("unauthorized decision exit = %v, want 1", err)
	}
	if canonical.count != 1 || !hasIntentStatusTag(transport.statuses[1].Tags, "status", "rejected") {
		t.Fatalf("rejected decision changed canonical state")
	}
}
