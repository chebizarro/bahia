package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// DeploymentIntentHandler routes operator-signed deployment and runtime desires
// through the same services used by the legacy ContextVM and direct-action paths.
// Those services are the sole publishers of canonical deployment state.
type DeploymentIntentResourceReader interface {
	GetService(context.Context, uuid.UUID) (*domain.Service, error)
	GetEnvironment(context.Context, uuid.UUID) (*domain.Environment, error)
}

type RuntimeIntentLifecycle interface {
	DeployWithStatus(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, service.DeployStatusCallback) (*domain.RuntimeObservation, error)
	Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
	Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
}

type DeploymentIntentHandler struct {
	service   *encryptedServiceHandlers
	resources DeploymentIntentResourceReader
	runtime   RuntimeIntentLifecycle
}

func NewDeploymentIntentHandler(cfg EncryptedServiceHandlersConfig, runtime RuntimeIntentLifecycle) *DeploymentIntentHandler {
	return &DeploymentIntentHandler{service: newEncryptedServiceHandlers(cfg), resources: cfg.Registry, runtime: runtime}
}

func (h *DeploymentIntentHandler) PermissionFor(op string) domain.Permission {
	if op == "approve" || op == "reject" {
		return domain.PermApproveDeployments
	}
	return domain.PermWriteDeployments
}

func (h *DeploymentIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent == nil {
		return fmt.Errorf("deployment intent is nil")
	}
	switch intent.Domain {
	case "runtime":
		return h.handleRuntime(ctx, intent)
	case "deployment":
		return h.handleDeployment(ctx, intent)
	default:
		return fmt.Errorf("unsupported deployment domain %q", intent.Domain)
	}
}

func (h *DeploymentIntentHandler) handleDeployment(ctx context.Context, intent *Intent) error {
	if h.service == nil || h.service.registry == nil {
		return fmt.Errorf("deployment registry is not configured")
	}
	content := make(map[string]any, len(intent.Content)+1)
	for k, v := range intent.Content {
		content[k] = v
	}
	var method func(context.Context, ContextVMRequest) (any, error)
	switch intent.Op {
	case "preview", "route-attach":
		serviceID, err := uuid.Parse(firstIntentString(content, "service_id"))
		if err != nil || serviceID == uuid.Nil {
			return fmt.Errorf("service_id must be a UUID")
		}
		environmentID, err := uuid.Parse(firstIntentString(content, "environment_id"))
		if err != nil || environmentID == uuid.Nil {
			return fmt.Errorf("environment_id must be a UUID")
		}
		prefix := "deployment-preview:"
		if intent.Op == "route-attach" {
			prefix = "deployment-route:"
		}
		if intent.Coordinate != prefix+serviceID.String()+":"+environmentID.String() {
			return fmt.Errorf("deployment %s coordinate does not match content", intent.Op)
		}
		if intent.Op == "route-attach" {
			if intent.ExpectedUpdatedAt != nil {
				var unitID *uuid.UUID
				if raw := firstIntentString(content, "deployment_unit_id"); raw != "" {
					parsed, err := uuid.Parse(raw)
					if err != nil || parsed == uuid.Nil {
						return fmt.Errorf("deployment_unit_id must be a UUID")
					}
					unitID = &parsed
				}
				current, err := h.service.latestDeployedIntent(ctx, serviceID, environmentID, unitID)
				if err != nil {
					return err
				}
				if current == nil || !intent.RevisionMatches(current.UpdatedAt) {
					return &revisionConflictError{entityID: serviceID, expected: *intent.ExpectedUpdatedAt}
				}
			}
			method = h.service.routeAttachLegacy
		} else {
			if intent.ExpectedUpdatedAt != nil {
				return fmt.Errorf("expected_updated_at is not supported for deployment preview")
			}
			method = h.service.previewDeployLegacy
		}
		delete(content, "intent_id")
		delete(content, "expected_updated_at")
	case "create":
		delete(content, "intent_id")
		delete(content, "expected_updated_at")
		method = h.service.deployLegacy
	case "rollback":
		if intent.ExpectedUpdatedAt != nil {
			supersedesID, parseErr := uuid.Parse(firstIntentString(content, "supersedes_intent_id"))
			if parseErr != nil || supersedesID == uuid.Nil {
				return fmt.Errorf("supersedes_intent_id must be a UUID")
			}
			superseded, getErr := h.service.registry.GetDeploymentIntent(ctx, supersedesID)
			if getErr != nil {
				return getErr
			}
			if superseded == nil {
				return fmt.Errorf("superseded deployment intent %s not found", supersedesID)
			}
			expected := *intent.ExpectedUpdatedAt
			if !intent.RevisionMatches(superseded.UpdatedAt) {
				return &revisionConflictError{entityID: supersedesID, expected: expected, actual: superseded.UpdatedAt}
			}
		}
		if firstIntentString(content, "target_artifact_id") == "" {
			runID, parseErr := uuid.Parse(firstIntentString(content, "target_run_id"))
			if parseErr != nil || runID == uuid.Nil {
				return fmt.Errorf("target_artifact_id or target_run_id must be a UUID")
			}
			run, getErr := h.service.registry.GetDeploymentRun(ctx, runID)
			if getErr != nil {
				return getErr
			}
			if run == nil {
				return fmt.Errorf("target deployment run %s not found", runID)
			}
			if run.Status != domain.RunStatusSucceeded {
				return fmt.Errorf("target deployment run %s is not successful", runID)
			}
			prior, getErr := h.service.registry.GetDeploymentIntent(ctx, run.DeploymentIntentID)
			if getErr != nil {
				return getErr
			}
			if prior == nil {
				return fmt.Errorf("target run deployment intent %s not found", run.DeploymentIntentID)
			}
			serviceID, serviceErr := uuid.Parse(firstIntentString(content, "service_id"))
			environmentID, environmentErr := uuid.Parse(firstIntentString(content, "environment_id"))
			if serviceErr != nil || environmentErr != nil || prior.ServiceID != serviceID || prior.EnvironmentID != environmentID {
				return fmt.Errorf("target deployment run must belong to the requested service and environment")
			}
			content["target_artifact_id"] = prior.ArtifactID.String()
		}
		delete(content, "target_run_id")
		delete(content, "intent_id")
		delete(content, "expected_updated_at")
		method = h.service.rollbackLegacy
	case "approve", "reject":
		id, err := uuid.Parse(firstIntentString(content, "deployment_intent_id", "target_intent_id", "intent_id"))
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("deployment_intent_id must be a UUID")
		}
		current, err := h.service.registry.GetDeploymentIntent(ctx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("deployment intent %s not found", id)
		}
		if intent.ExpectedUpdatedAt != nil {
			expected := *intent.ExpectedUpdatedAt
			if !intent.RevisionMatches(current.UpdatedAt) {
				return &revisionConflictError{entityID: id, expected: expected, actual: current.UpdatedAt}
			}
		}
		delete(content, "deployment_intent_id")
		delete(content, "target_intent_id")
		delete(content, "expected_updated_at")
		content["intent_id"] = id.String()
		content["decision"] = intent.Op
		method = func(ctx context.Context, request ContextVMRequest) (any, error) {
			return h.service.decideLegacy(ctx, request, intent.Op)
		}
	default:
		return fmt.Errorf("unknown deployment operation %q", intent.Op)
	}
	// The legacy methods retain validation, policy checks, promotion audit and
	// canonical publication. This request is never signed or emitted as an intent.
	params, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("encode deployment content: %w", err)
	}
	event := intent.Event
	if event == nil {
		pubkey, err := nostr.PubKeyFromHex(intent.Actor)
		if err != nil {
			return fmt.Errorf("invalid deployment actor pubkey: %w", err)
		}
		event = &nostr.Event{PubKey: pubkey}
	}
	req := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: params}, ProgressToken: intent.IntentID}
	ctx = context.WithValue(ctx, authorizedDeploymentIntentOrgKey{}, intent.OrgID)
	result, err := method(ctx, req)
	if err == nil && (intent.Op == "preview" || intent.Op == "route-attach") {
		if data, ok := result.(map[string]any); ok {
			intent.Result = data
			if intent.Op == "preview" {
				intent.StatusData = deploymentPreviewStatusData(data)
			} else {
				intent.StatusData = map[string]any{
					"status": data["status"], "intent_id": data["intent_id"],
					"service_id": data["service_id"], "environment_id": data["environment_id"],
					"desired_state_hash": data["desired_state_hash"],
				}
			}
		}
	}
	return err
}

// The preview response can include environment values and arbitrarily long
// commands. The public 30315 status deliberately carries only fixed-size,
// non-secret plan structure and the authoritative review hash.
func deploymentPreviewStatusData(data map[string]any) map[string]any {
	status := map[string]any{
		"service_id": data["service_id"], "environment_id": data["environment_id"],
		"artifact_id": data["artifact_id"], "desired_state_hash": data["desired_state_hash"],
		"route_approval_required": data["route_approval_required"], "plan_truncated": true,
	}
	var summary *desiredStateSummary
	if value, ok := data["desired_state_summary"].(*desiredStateSummary); ok {
		summary = value
	} else if desired, ok := data["desired_state"].(*domain.DesiredServiceSpec); ok {
		summary = buildDesiredStateSummary(desired)
	}
	if summary != nil {
		plan := map[string]any{
			"image_ref":     boundedIntentStatusText(summary.ImageRef, 256),
			"env_key_count": summary.EnvKeyCount, "labels_count": summary.LabelsCount,
			"ports_count": len(summary.Ports), "volumes_count": len(summary.Volumes),
			"command_count": len(summary.Command), "secret_ref_count": len(summary.SecretRefKeys),
		}
		if summary.PublicRoute != nil {
			plan["public_route_hostname"] = boundedIntentStatusText(summary.PublicRoute.Hostname, 256)
		}
		status["desired_state_summary"] = plan
	}
	if policy, ok := data["policy"].(*domain.PolicyEvaluation); ok {
		status["policy"] = map[string]any{
			"allowed": policy.Allowed, "blockers": policy.Blockers, "warnings": policy.Warnings,
			"requires_approval": policy.RequiresApproval,
		}
	}
	return status
}

func boundedIntentStatusText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func (h *DeploymentIntentHandler) handleRuntime(ctx context.Context, intent *Intent) error {
	if h.runtime == nil {
		return fmt.Errorf("runtime lifecycle is not configured")
	}
	serviceID, err := uuid.Parse(firstIntentString(intent.Content, "service_id"))
	if err != nil || serviceID == uuid.Nil {
		return fmt.Errorf("service_id must be a UUID")
	}
	environmentID, err := uuid.Parse(firstIntentString(intent.Content, "environment_id"))
	if err != nil || environmentID == uuid.Nil {
		return fmt.Errorf("environment_id must be a UUID")
	}
	if h.resources == nil {
		return fmt.Errorf("runtime registry is not configured")
	}
	svc, err := h.resources.GetService(ctx, serviceID)
	if err != nil {
		return err
	}
	env, err := h.resources.GetEnvironment(ctx, environmentID)
	if err != nil {
		return err
	}
	if svc == nil || env == nil || svc.OrgID == uuid.Nil || svc.OrgID != intent.OrgID || env.OrgID != intent.OrgID {
		return fmt.Errorf("runtime service and environment must belong to the authorized organization")
	}
	if intent.ExpectedUpdatedAt != nil {
		expected := *intent.ExpectedUpdatedAt
		if !intent.RevisionMatches(svc.UpdatedAt) {
			return &revisionConflictError{entityID: serviceID, expected: expected, actual: svc.UpdatedAt}
		}
	}
	switch intent.Op {
	case "deploy":
		var artifactID *uuid.UUID
		if raw := firstIntentString(intent.Content, "artifact_id"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil || id == uuid.Nil {
				return fmt.Errorf("artifact_id must be a UUID")
			}
			artifactID = &id
		}
		_, err = h.runtime.DeployWithStatus(ctx, serviceID, environmentID, artifactID, nil)
	case "restart":
		if firstIntentString(intent.Content, "artifact_id") != "" {
			return fmt.Errorf("artifact_id is only valid for deploy")
		}
		_, err = h.runtime.Restart(ctx, serviceID, environmentID)
	case "stop":
		if firstIntentString(intent.Content, "artifact_id") != "" {
			return fmt.Errorf("artifact_id is only valid for deploy")
		}
		_, err = h.runtime.Stop(ctx, serviceID, environmentID)
	default:
		return fmt.Errorf("unknown runtime operation %q", intent.Op)
	}
	return err
}

func firstIntentString(content map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := content[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
