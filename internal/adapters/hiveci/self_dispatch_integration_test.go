package hiveci

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/google/uuid"
	giteaadapter "github.com/openagentsinc/bahia/internal/adapters/gitea"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type selfDispatchMirror struct{}

func (selfDispatchMirror) GetRepo(context.Context, string, string) (*giteaadapter.RepoInfo, error) {
	return &giteaadapter.RepoInfo{
		Private: true, Mirror: true,
		OriginalURL: "https://github.com/team/repository.git",
	}, nil
}
func (selfDispatchMirror) MigrateMirror(context.Context, giteaadapter.MigrateMirrorRequest) error {
	return nil
}
func (selfDispatchMirror) SyncMirror(context.Context, string, string) error { return nil }
func (selfDispatchMirror) ResolveRef(context.Context, string, string, string) (string, error) {
	return strings.Repeat("1", 40), nil
}

type selfDispatchSecretResolver struct{}

func (selfDispatchSecretResolver) ResolveSecretWithAudit(context.Context, string, domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	return "resolved-only-in-memory", domain.SecretAccessManifest{}, nil
}

type selfDispatchPublisher struct{ events []nostr.Event }

func (p *selfDispatchPublisher) Publish(_ context.Context, event nostr.Event) (int, error) {
	p.events = append(p.events, event)
	return 1, nil
}

func TestBahiaSelfDispatchRoundTripsThroughSubscriberAndLineageReference(t *testing.T) {
	ctx := context.Background()
	signer, err := controlplane.NewPrivateKeySigner(strings.Repeat("8b", 32))
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	pubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		t.Fatalf("get signer pubkey: %v", err)
	}
	publisher := &selfDispatchPublisher{}
	store := giteaadapter.NewMemoryInitiationStore()
	initiator := giteaadapter.NewInitiator(
		selfDispatchMirror{}, selfDispatchSecretResolver{}, publisher, signer, store,
		giteaadapter.InitiatorConfig{
			MirrorOwner:          "fleet",
			WorkflowPath:         ".hive/workflows/build.yml",
			RepoAnnouncementAddr: "30617:" + pubkey.Hex() + ":repository",
			TrustedCIPubkeys:     []string{pubkey.Hex()},
		},
		zap.NewNop(),
	)
	request := controlplane.HiveCIBuildStartRequest{
		BuildID: uuid.New(), ServiceID: uuid.New(),
		RepositoryCoordinate: "team/repository", GitRef: "main", CredentialRef: uuid.New(),
		RequesterPubkey: strings.Repeat("a", 64), SourceEventID: strings.Repeat("b", 64),
	}

	started, err := initiator.StartHiveCIBuild(ctx, request)
	if err != nil {
		t.Fatalf("StartHiveCIBuild: %v", err)
	}
	replayed, err := initiator.StartHiveCIBuild(ctx, request)
	if err != nil {
		t.Fatalf("replay StartHiveCIBuild: %v", err)
	}
	if replayed.CIRunID != started.CIRunID || len(publisher.events) != 2 {
		t.Fatalf("initiation replay duplicated publication: started=%+v replayed=%+v events=%d", started, replayed, len(publisher.events))
	}

	run := publisher.events[0]
	t.Run("durable kind and run ID correlation", func(t *testing.T) {
		if int(run.Kind) != 5401 {
			t.Errorf("published run kind = %d, want wire kind 5401", run.Kind)
		}
		if int(run.Kind) != kinds.HiveCIWorkflowRun {
			t.Errorf("published run kind = %d, want Bahia constant %d", run.Kind, kinds.HiveCIWorkflowRun)
		}
		contextVMKind := cascadia.ContextVMMethods["ci/workflow-run"].Kind
		if contextVMKind != 25910 {
			t.Errorf("ci/workflow-run ContextVM binding kind = %d, want 25910", contextVMKind)
		}
		if int(run.Kind) == contextVMKind {
			t.Errorf("published run used ephemeral ContextVM kind %d", run.Kind)
		}
		if run.ID.Hex() != started.CIRunID {
			t.Errorf("CIRunID = %s, published run ID = %s", started.CIRunID, run.ID.Hex())
		}
	})

	t.Run("subscriber round trip and replay", func(t *testing.T) {
		repo := newTestHiveRepo()
		dispatches := 0
		subscriber := NewSubscriber(nil, repo, []string{pubkey.Hex()}, zap.NewNop(), nil)
		filters := subscriber.subscriptionFilters()
		if len(filters) < 1 || len(filters[0].Authors) != 1 || filters[0].Authors[0].Hex() != pubkey.Hex() {
			t.Fatalf("self-issued run is not reachable through trusted-author filter: %+v", filters)
		}
		subscriber.SetRunConsumer(func(context.Context, WorkflowRunDispatch) { dispatches++ })
		subscriber.handleEvent(ctx, &run)
		subscriber.handleEvent(ctx, &run)

		stored, ok := repo.runs[started.CIRunID]
		if !ok {
			t.Fatalf("Bahia subscriber did not persist self-dispatched run %s", started.CIRunID)
		}
		if len(repo.runs) != 1 || dispatches != 1 {
			t.Fatalf("subscriber replay duplicated run lineage: runs=%d dispatches=%d", len(repo.runs), dispatches)
		}
		if stored.RunEventID != started.CIRunID || stored.RepoCoordinate != "30617:"+pubkey.Hex()+":repository" ||
			stored.CommitSHA != strings.Repeat("1", 40) || stored.Branch != "main" ||
			stored.WorkflowPath != ".hive/workflows/build.yml" || stored.PublisherPubkey != pubkey.Hex() {
			t.Fatalf("parsed self-dispatch does not match published lineage: %+v", stored)
		}
	})

	t.Run("release lineage reference", func(t *testing.T) {
		if err := validateWorkflowRunReference(&run, started.CIRunID); err != nil {
			t.Fatalf("release lineage reference rejected Bahia 5401: %v", err)
		}
	})
}
