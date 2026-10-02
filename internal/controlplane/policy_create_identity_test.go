package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// bahia-irsry.42: policy/create carries a client-minted id. A retry with the
// same id and content replays (no second write, no second publication); the
// same id with different content is domain.ErrEntityIDConflict (JSON-RPC
// -32010 at the transport).
func TestPolicyCreateIsIdempotentByClientID(t *testing.T) {
	ctx := context.Background()
	repo := &identityPolicyRepo{rows: map[uuid.UUID]domain.DeploymentPolicy{}}
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	reactor := NewReactor(Config{}, nil, nil, responder.signer, zap.NewNop(),
		WithControlPlanePublisher(publisher),
		WithPolicyService(service.NewPolicyService(repo, &testSignatureRepo{}, nil, zap.NewNop())),
		WithPolicyStatePublisher(testPolicyPublisher(publisher, responder.signer)))

	id := domain.NewEntityID()
	create := func(params map[string]any) (any, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return reactor.handlePolicyCreate(ctx, ContextVMRequest{Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Method: ContextVMMethodPolicyCreate, Params: raw}, ProgressToken: "policy-create:" + uuid.NewString()})
	}
	params := map[string]any{"id": id.String(), "name": "require-signature", "rules": []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, "enforcement": "block", "enabled": true}

	result, err := create(params)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if got := result.(map[string]any)["policy_id"]; got != id.String() {
		t.Fatalf("policy_id = %v, want the client id %s", got, id)
	}
	stored := repo.rows[id]
	published := len(publisher.events)
	if repo.creates != 1 || published == 0 {
		t.Fatalf("first create: %d writes, %d publications", repo.creates, published)
	}

	// Same id, same content: idempotent replay.
	result, err = create(params)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := result.(map[string]any)["policy_id"]; got != id.String() {
		t.Fatalf("retry policy_id = %v", got)
	}
	if repo.creates != 1 || len(publisher.events) != published || !repo.rows[id].UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("retry wrote or published again: writes=%d publications=%d->%d", repo.creates, published, len(publisher.events))
	}

	// Same id, different content: conflict, nothing written or published.
	params["enforcement"] = "warn"
	if _, err := create(params); !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("different content under the same id: err = %v, want ErrEntityIDConflict", err)
	}
	if repo.creates != 1 || len(publisher.events) != published || repo.rows[id].Enforcement != domain.PolicyEnforcementBlock {
		t.Fatal("a conflicting create changed the stored policy or published")
	}

	// A name-derived (v5) or non-canonical id is rejected before any write;
	// without an id the daemon mints a UUIDv7.
	for _, bad := range []string{"886313e1-3b8a-5372-9b90-0c9aee199e5d", "{" + id.String() + "}"} {
		params["id"], params["name"] = bad, "other"
		if _, err := create(params); !errors.Is(err, domain.ErrInvalidEntityID) {
			t.Fatalf("id %q: err = %v, want ErrInvalidEntityID", bad, err)
		}
	}
	delete(params, "id")
	params["name"] = "minted"
	result, err = create(params)
	if err != nil {
		t.Fatalf("create without id: %v", err)
	}
	minted, err := uuid.Parse(result.(map[string]any)["policy_id"].(string))
	if err != nil || minted.Version() != 7 {
		t.Fatalf("minted policy id %v is not a UUIDv7", result)
	}
}

// A create that loses the insert race on its id (the repository reports
// ErrAlreadyExists) is re-resolved by content.
func TestPolicyCreateResolvesAnInsertRaceByContent(t *testing.T) {
	ctx := context.Background()
	id := domain.NewEntityID()
	winner := domain.DeploymentPolicy{ID: id, Name: "sig", Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	repo := &identityPolicyRepo{rows: map[uuid.UUID]domain.DeploymentPolicy{}, raceWinner: &winner}
	policies := service.NewPolicyService(repo, &testSignatureRepo{}, nil, zap.NewNop())

	same := winner
	same.CreatedAt, same.UpdatedAt = time.Time{}, time.Time{}
	replayed, err := policies.CreatePolicy(ctx, &same)
	if err != nil || !replayed || !same.CreatedAt.Equal(winner.CreatedAt) {
		t.Fatalf("lost race with identical content: replayed=%v err=%v", replayed, err)
	}

	repo.rows = map[uuid.UUID]domain.DeploymentPolicy{}
	different := winner
	different.Enabled = false
	if _, err := policies.CreatePolicy(ctx, &different); !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("lost race with different content: err = %v", err)
	}
}

// identityPolicyRepo stores policies under their supplied ids, as
// PgDeploymentPolicyRepository does. raceWinner simulates a concurrent create
// that inserts the same id between the replay check and the insert.
type identityPolicyRepo struct {
	testPolicyRepo
	mu         sync.Mutex
	rows       map[uuid.UUID]domain.DeploymentPolicy
	creates    int
	raceWinner *domain.DeploymentPolicy
}

func (r *identityPolicyRepo) Create(_ context.Context, p *domain.DeploymentPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.raceWinner != nil && r.raceWinner.ID == p.ID {
		r.rows[p.ID] = *r.raceWinner
		return repository.ErrAlreadyExists
	}
	if _, ok := r.rows[p.ID]; ok {
		return repository.ErrAlreadyExists
	}
	r.creates++
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	r.rows[p.ID] = *p
	return nil
}

func (r *identityPolicyRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &p, nil
}

// PublishEnvironmentCreateRequest sends exactly the params environment/create
// decodes (strictly), including the client-minted id.
func TestEnvironmentCreateCommandParamsDecodeStrictly(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	commands := NewServiceCommandPublisher(publisher, newResponder(t, publisher).signer)
	id, orgID := domain.NewEntityID(), uuid.New()
	receipt, err := commands.PublishEnvironmentCreateRequest(context.Background(), EnvironmentCreateCommand{
		ID: id, OrgID: orgID, Name: "prod", LoomWorkerSelector: map[string]any{"region": "eu"},
		ReconcileMode: "observe_only", DeployStrategy: "replace", Protected: true, IdempotencyKey: "environment-create:prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EnvironmentID != id.String() || receipt.IdempotencyKey != "environment-create:prod" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published %d events", len(publisher.events))
	}
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(publisher.events[0].Content), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Method != ContextVMMethodEnvironmentCreate {
		t.Fatalf("method = %q", rpc.Method)
	}
	var payload encryptedEnvironmentCreatePayload
	if err := decodeStrictContextVMParams(rpc.Params, &payload); err != nil {
		t.Fatalf("environment/create rejects the command's params: %v (%s)", err, rpc.Params)
	}
	if payload.ID != id.String() || payload.OrgID != orgID || payload.Name != "prod" || !payload.Protected || payload.ReconcileMode != "observe_only" {
		t.Fatalf("decoded payload = %+v", payload)
	}
}
