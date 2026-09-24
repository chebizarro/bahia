package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

const packageApprovePlanMethod = "package/approve-plan"
const packageApprovalMaxAge = 10 * time.Minute

type packageContextVMHandlers struct {
	r     *Reactor
	gate  *FleetOperatorGate
	store repository.PackageAuthorizationStore
}

// RegisterPackageContextVMHandlers executes package mutations rather than
// republishing commands. Only the transport sends JSON-RPC terminal responses.
func (r *Reactor) RegisterPackageContextVMHandlers(transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
	if r == nil || transport == nil {
		return
	}
	store, _ := r.packageProjection.(repository.PackageAuthorizationStore)
	h := packageContextVMHandlers{r: r, gate: gate, store: store}
	for method, handler := range map[string]func(context.Context, *packagePlan) (map[string]any, error){
		"package/publish":             r.handlePackagePublishIntent,
		ContextVMMethodPackagePromote: r.handlePackagePromotionRequest,
		"package/yank":                r.handlePackageYankRequest,
		"package/drift-detect":        r.handlePackageDriftDetect,
	} {
		transport.RegisterContextVMHandler(method, gate.wrap(h.run(handler)))
	}
	transport.RegisterContextVMHandler(packageApprovePlanMethod, gate.wrap(h.approvePlan))
}

type packagePlan struct {
	request                  ContextVMRequest
	op                       domain.PackageOperation
	cmd                      any
	repo, targetRepo         *domain.PackageRepository
	artifact, targetArtifact *domain.PackageArtifact
	approvalID               uuid.UUID
	approvalRequired         bool
	approvedBy               string
}

func (h packageContextVMHandlers) configured() error {
	if h.r.packageService == nil || h.r.packageProjection == nil || h.store == nil {
		return fmt.Errorf("package service, projection and durable authorization store are required")
	}
	return nil
}

func (h packageContextVMHandlers) prepare(ctx context.Context, request ContextVMRequest) (*packagePlan, error) {
	p := &packagePlan{request: request}
	var repoID uuid.UUID
	var repoName, namespace, name, version, filename string
	switch request.RPC.Method {
	case "package/publish":
		var cmd PackagePublishCommand
		if err := decodeContextVMParams(request.RPC.Params, &cmd); err != nil {
			return nil, err
		}
		if cmd.ApprovedBy != "" {
			return nil, fmt.Errorf("approved_by is not approval provenance; use approval_id")
		}
		p.op, p.approvalID = domain.PackageOperationArtifactPublish, cmd.ApprovalID
		cmd.ApprovalID = uuid.Nil
		p.cmd = cmd
		repoID, repoName, namespace, name, version, filename = cmd.RepositoryID, cmd.RepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename
	case ContextVMMethodPackagePromote:
		var cmd PackagePromotionCommand
		if err := decodeContextVMParams(request.RPC.Params, &cmd); err != nil {
			return nil, err
		}
		if cmd.ApprovedBy != "" {
			return nil, fmt.Errorf("approved_by is not approval provenance; use approval_id")
		}
		p.op, p.approvalID = domain.PackageOperationPromote, cmd.ApprovalID
		cmd.ApprovalID = uuid.Nil
		p.cmd = cmd
		repoID, repoName, namespace, name, version, filename = cmd.SourceRepositoryID, cmd.SourceRepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename
	case "package/yank":
		var cmd PackageYankCommand
		if err := decodeContextVMParams(request.RPC.Params, &cmd); err != nil {
			return nil, err
		}
		p.op, p.cmd = domain.PackageOperationYank, cmd
		if cmd.Deprecated {
			p.op = domain.PackageOperationDeprecate
		}
		repoID, repoName, namespace, name, version, filename = cmd.RepositoryID, cmd.RepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename
	case "package/drift-detect":
		var cmd PackageDriftDetectCommand
		if err := decodeContextVMParams(request.RPC.Params, &cmd); err != nil {
			return nil, err
		}
		p.op, p.cmd = domain.PackageOperationDriftDetect, cmd
		repoID, repoName = cmd.RepositoryID, cmd.RepositoryName
	default:
		return nil, fmt.Errorf("unsupported package method")
	}
	var err error
	p.repo, err = h.r.lookupPackageRepository(ctx, repoID, repoName)
	if err != nil {
		return nil, err
	}
	if p.op == domain.PackageOperationDriftDetect {
		return p, nil
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" || strings.TrimSpace(filename) == "" {
		return nil, fmt.Errorf("package_name, version and filename are required")
	}
	p.artifact, err = h.lookupArtifact(ctx, p.repo.ID, strings.Trim(namespace, "/"), name, version, filename)
	if err != nil {
		return nil, err
	}
	if p.op == domain.PackageOperationArtifactPublish {
		p.approvalRequired = p.repo.Policy.PublishRequiresApproval
	}
	if cmd, ok := p.cmd.(PackagePromotionCommand); ok {
		if p.artifact == nil {
			return nil, fmt.Errorf("source package artifact not found")
		}
		p.targetRepo, err = h.r.lookupPackageRepository(ctx, cmd.TargetRepositoryID, cmd.TargetRepositoryName)
		if err != nil {
			return nil, err
		}
		p.targetArtifact, err = h.lookupArtifact(ctx, p.targetRepo.ID, p.artifact.Namespace, p.artifact.PackageName, p.artifact.Version, p.artifact.Filename)
		if err != nil {
			return nil, err
		}
		p.approvalRequired = p.targetRepo.Policy.PromotionRequiresApproval
	}
	return p, nil
}

func (h packageContextVMHandlers) lookupArtifact(ctx context.Context, id uuid.UUID, namespace, name, version, filename string) (*domain.PackageArtifact, error) {
	a, err := h.r.packageProjection.GetArtifact(ctx, id, namespace, name, version, filename)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	return a, err
}

// Like VM ApprovePlan, compute the binding from the decoded action and current
// server-side resources, never a caller's hash. Full snapshots include policy,
// digest, identity and last-event revision (including absence of a target).
func (p *packagePlan) hash() (string, error) {
	body, err := json.Marshal([]any{p.request.RPC.Method, p.cmd, p.repo, p.targetRepo, p.artifact, p.targetArtifact})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func (h packageContextVMHandlers) approvePlan(ctx context.Context, request ContextVMRequest) (any, error) {
	if err := h.configured(); err != nil {
		return nil, err
	}
	var cmd struct {
		Requester string          `json:"requester_pubkey"`
		Method    string          `json:"method"`
		Params    json.RawMessage `json:"params"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &cmd); err != nil {
		return nil, err
	}
	approver := request.Event.PubKey.Hex()
	if !isHexNostrPubKey(cmd.Requester) || cmd.Requester == approver || !slices.Contains(h.gate.authorizedPubkeys, cmd.Requester) {
		return nil, fmt.Errorf("approval requires a distinct authorized requester and approver")
	}
	if cmd.Method != "package/publish" && cmd.Method != ContextVMMethodPackagePromote {
		return nil, fmt.Errorf("only publish and promote plans can be approved")
	}
	planRequest := request
	planRequest.RPC.Method, planRequest.RPC.Params = cmd.Method, cmd.Params
	plan, err := h.prepare(ctx, planRequest)
	if err != nil {
		return nil, err
	}
	if plan.approvalID != uuid.Nil {
		return nil, fmt.Errorf("an approval plan cannot contain approval_id")
	}
	hash, err := plan.hash()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	approval := repository.PackageApproval{ID: uuid.New(), Requester: cmd.Requester, Approver: approver, Method: cmd.Method, PlanHash: hash, EventID: request.Event.ID.Hex(), CreatedAt: now, ExpiresAt: now.Add(packageApprovalMaxAge)}
	if err := h.store.CreatePackageApproval(ctx, approval); err != nil {
		return nil, fmt.Errorf("record package approval: %w", err)
	}
	return map[string]any{"approval_id": approval.ID, "expires_at": approval.ExpiresAt, "request_hash": hash}, nil
}

func (h packageContextVMHandlers) run(handler func(context.Context, *packagePlan) (map[string]any, error)) ContextVMHandler {
	return func(ctx context.Context, request ContextVMRequest) (any, error) {
		if err := h.configured(); err != nil {
			return nil, err
		}
		if request.ProgressToken == "" {
			return nil, fmt.Errorf("idempotency_key or _meta.progressToken is required")
		}
		fingerprint, err := contextVMRequestFingerprint(request.RPC.Params)
		if err != nil {
			return nil, err
		}
		claim, fresh, err := h.store.ClaimPackageRequest(ctx, repository.PackageRequestClaim{Requester: request.Event.PubKey.Hex(), Method: request.RPC.Method, Token: request.ProgressToken, EventID: request.Event.ID.Hex(), Fingerprint: fingerprint})
		if err != nil {
			return nil, err
		}
		if !fresh {
			intent, err := h.r.packageProjection.GetIntentByRequestEventID(ctx, claim.EventID)
			if err != nil {
				return nil, err
			}
			if intent == nil || !intent.Status.Terminal() {
				return nil, fmt.Errorf("package request already admitted; outcome is not terminal; mutation will not be repeated")
			}
			if intent.Status != domain.PackageIntentStatusSucceeded {
				return nil, fmt.Errorf("package request failed: %s", intent.ErrorMessage)
			}
			if !claim.Completed {
				return nil, fmt.Errorf("package completion publication was not confirmed; mutation will not be repeated")
			}
			return intent.ResultPayload, nil
		}
		plan, err := h.prepare(ctx, request)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		intent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: claim.EventID, RequesterPubkey: claim.Requester, RepositoryID: &plan.repo.ID, RepositoryName: plan.repo.Name, Operation: plan.op, Status: domain.PackageIntentStatusExecuting, CreatedAt: now, UpdatedAt: now}
		if err := json.Unmarshal(request.RPC.Params, &intent.RequestPayload); err != nil {
			return nil, err
		}
		if err := h.r.packageProjection.UpsertIntent(ctx, intent); err != nil {
			return nil, err
		}
		var result map[string]any
		if plan.approvalRequired || plan.approvalID != uuid.Nil {
			hash, hashErr := plan.hash()
			err = hashErr
			if err == nil {
				plan.approvedBy, err = h.store.ConsumePackageApproval(ctx, plan.approvalID, claim.Requester, claim.Method, hash, h.gate.authorizedPubkeys)
			}
			if err != nil {
				err = fmt.Errorf("%w: %v", service.ErrPackageApprovalRequired, err)
			}
		}
		if err == nil {
			result, err = handler(ctx, plan)
		}
		out, err := h.finish(ctx, request, intent, result, err)
		if err != nil {
			return nil, err
		}
		if err := h.store.CompletePackageRequest(ctx, claim.EventID); err != nil {
			return nil, fmt.Errorf("persist package publication confirmation: %w", err)
		}
		return out, nil
	}
}

func (h packageContextVMHandlers) finish(ctx context.Context, request ContextVMRequest, intent *domain.PackageIntent, result map[string]any, operationErr error) (any, error) {
	if result == nil {
		result = map[string]any{}
	}
	result["operation"], result["intent_id"], result["request_event_id"] = intent.Operation, intent.ID, intent.RequestEventID
	now := time.Now().UTC()
	intent.UpdatedAt, intent.CompletedAt = now, &now
	intent.Status, result["status"] = domain.PackageIntentStatusSucceeded, "succeeded"
	if operationErr != nil {
		intent.Status, intent.ErrorMessage = domain.PackageIntentStatusFailed, operationErr.Error()
		result["status"], result["error"] = "failed", operationErr.Error()
	}
	intent.ResultPayload = result
	if err := h.r.packageProjection.UpsertIntent(ctx, intent); err != nil {
		return nil, errors.Join(operationErr, fmt.Errorf("persist package completion: %w", err))
	}
	// Publish a durable observable, not another JSON-RPC result. Persistence
	// precedes the terminal event and the transport's one correlated response.
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "package:intent:" + intent.ID.String()}, {"domain", "package"}, {"schema", "bahia.result.package.v1"}, {"e", request.Event.ID.Hex()}, {"p", request.Event.PubKey.Hex()}, {"status", string(intent.Status)}}, Content: mustJSON(result)}
	if err := h.r.publishPackageState(ctx, event); err != nil {
		operationErr = errors.Join(operationErr, fmt.Errorf("publish package completion: %w", err))
		intent.Status, intent.ErrorMessage = domain.PackageIntentStatusFailed, operationErr.Error()
		result["status"], result["error"] = "failed", operationErr.Error()
		operationErr = errors.Join(operationErr, h.r.packageProjection.UpsertIntent(ctx, intent))
	}
	if operationErr != nil {
		return nil, operationErr
	}
	return result, nil
}
