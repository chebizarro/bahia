package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type ToolProvisioningCoordinator struct {
	repo            repository.ToolProvisioningRepository
	serviceRepo     repository.ServiceRepository
	envRepo         repository.EnvironmentRepository
	securityService *ToolSecurityService
	builder         *build.DockerBuilder
	runtime         runtime.Runtime
	responder       ToolProvisioningResponder
	dispatcher      ToolProvisioningDispatcher
	logger          *zap.Logger
	config          ToolProvisioningConfig
}

type ToolProvisioningConfig struct {
	BaseImageRef     string
	TargetRegistry   string
	TargetRepo       string
	InstallerVersion string
}

type ToolProvisioningResponder interface {
	PublishStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, step string, message string) error
	PublishResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, success bool, errMsg string) error
}

type ToolProvisioningDispatcher interface {
	Dispatch(ctx context.Context, eventType string, payload map[string]any)
}

func NewToolProvisioningCoordinator(repo repository.ToolProvisioningRepository, serviceRepo repository.ServiceRepository, envRepo repository.EnvironmentRepository, securityService *ToolSecurityService, builder *build.DockerBuilder, rt runtime.Runtime, responder ToolProvisioningResponder, dispatcher ToolProvisioningDispatcher, logger *zap.Logger, cfg ToolProvisioningConfig) *ToolProvisioningCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(cfg.InstallerVersion) == "" {
		cfg.InstallerVersion = "v1"
	}
	return &ToolProvisioningCoordinator{repo: repo, serviceRepo: serviceRepo, envRepo: envRepo, securityService: securityService, builder: builder, runtime: rt, responder: responder, dispatcher: dispatcher, logger: logger.Named("tool-provisioning-coordinator"), config: cfg}
}

func (c *ToolProvisioningCoordinator) ProcessIntent(ctx context.Context, intentID uuid.UUID) error {
	intent, err := c.repo.GetIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("intent %s not found", intentID)
	}
	if intent.Status == domain.ToolProvisionStatusApproved {
		return c.processBuildAndDeploy(ctx, intent)
	}
	if intent.Status != domain.ToolProvisionStatusPending {
		return nil
	}
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusValidating, "validating", "validating tool request"); err != nil {
		return err
	}
	resolved, scan, flags, err := c.securityService.ValidateTools(ctx, intent.RequestedTools)
	if err != nil {
		return c.fail(ctx, intent, "validation failed", err)
	}
	intent.ResolvedTools = resolved
	intent.SecurityScanResults = scan
	intent.ApprovalFlags = flags
	intent.ApprovalRequired = len(flags) > 0
	if intent.ApprovalRequired {
		if err := c.transition(ctx, intent, domain.ToolProvisionStatusAwaitingApproval, "awaiting_approval", "tool request awaiting human approval"); err != nil {
			return err
		}
		if c.dispatcher != nil {
			c.dispatcher.Dispatch(ctx, string(events.EventToolProvisionApprovalRequired), map[string]any{"intent_id": intent.ID.String(), "service_id": intent.ServiceID.String(), "environment_id": intent.EnvironmentID.String(), "approval_flags": intent.ApprovalFlags})
		}
		return nil
	}
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusApproved, "approved", "tool request auto-approved"); err != nil {
		return err
	}
	return c.processBuildAndDeploy(ctx, intent)
}

func (c *ToolProvisioningCoordinator) ProcessApprovedIntent(ctx context.Context, intentID uuid.UUID) error {
	intent, err := c.repo.GetIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("intent %s not found", intentID)
	}
	if intent.Status != domain.ToolProvisionStatusApproved {
		return nil
	}
	return c.processBuildAndDeploy(ctx, intent)
}

func (c *ToolProvisioningCoordinator) ProcessPendingIntents(ctx context.Context) error {
	intents, err := c.repo.ListIntentsByStatus(ctx, domain.ToolProvisionStatusPending, domain.ToolProvisionStatusApproved)
	if err != nil {
		return err
	}
	for i := range intents {
		if err := c.ProcessIntent(ctx, intents[i].ID); err != nil {
			c.logger.Warn("tool intent processing failed", zap.String("intent_id", intents[i].ID.String()), zap.Error(err))
		}
	}
	return nil
}

func (c *ToolProvisioningCoordinator) Rollback(ctx context.Context, serviceID, envID uuid.UUID) error {
	state, err := c.repo.GetProfileState(ctx, serviceID, envID)
	if err != nil {
		return err
	}
	if state == nil || strings.TrimSpace(state.PreviousImageDigest) == "" {
		return fmt.Errorf("no previous image available for rollback")
	}
	svc, err := c.serviceRepo.GetByID(ctx, serviceID)
	if err != nil || svc == nil {
		return fmt.Errorf("service not found: %w", err)
	}
	return c.runtime.Deploy(ctx, svc.RuntimeTargetName(), state.PreviousImageDigest, runtime.DeployOptions{})
}

func (c *ToolProvisioningCoordinator) processBuildAndDeploy(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusBuilding, "building", "building or reusing tool image"); err != nil {
		return err
	}
	base := strings.TrimSpace(c.config.BaseImageRef)
	intent.ToolsetHash = build.ComputeToolsetHash(base, intent.ResolvedTools, c.config.InstallerVersion)
	imageRef := c.targetRef(intent.ToolsetHash)
	if c.builder != nil {
		imageID, hit, err := c.builder.CheckImageExists(ctx, intent.ToolsetHash)
		if err != nil {
			return c.fail(ctx, intent, "image cache check failed", err)
		}
		if !hit {
			result, err := c.builder.BuildImage(ctx, build.BuildRequest{BaseImage: base, Tools: intent.ResolvedTools, ToolsetHash: intent.ToolsetHash, SourceEventID: intent.NostrEventID, TargetRegistry: c.config.TargetRegistry, TargetRepo: strings.TrimPrefix(imageRef, strings.TrimSpace(c.config.TargetRegistry)+"/")})
			if err != nil {
				return c.fail(ctx, intent, "image build failed", err)
			}
			imageID = result.ImageID
			if err := c.builder.PushImage(ctx, imageID, imageRef); err != nil {
				return c.fail(ctx, intent, "image push failed", err)
			}
		}
	}
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusDeploying, "deploying", "deploying provisioned image"); err != nil {
		return err
	}
	svc, err := c.serviceRepo.GetByID(ctx, intent.ServiceID)
	if err != nil || svc == nil {
		return c.fail(ctx, intent, "service lookup failed", err)
	}
	env, err := c.envRepo.GetByID(ctx, intent.EnvironmentID)
	if err != nil || env == nil {
		return c.fail(ctx, intent, "environment lookup failed", err)
	}
	if c.runtime != nil {
		if err := c.runtime.Deploy(ctx, svc.RuntimeTargetName(), imageRef, runtime.DeployOptions{}); err != nil {
			return c.fail(ctx, intent, "runtime deploy failed", err)
		}
	}
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusObserving, "observing", "observing runtime health"); err != nil {
		return err
	}
	if c.runtime != nil {
		obs, err := c.runtime.Observe(ctx, svc.ID, env.ID, svc.RuntimeTargetName())
		if err != nil {
			return c.fail(ctx, intent, "runtime observation failed", err)
		}
		if obs != nil && obs.HealthStatus == domain.HealthStatusUnhealthy {
			return c.fail(ctx, intent, "runtime unhealthy after deployment", fmt.Errorf("health status unhealthy"))
		}
	}
	state, _ := c.repo.GetProfileState(ctx, intent.ServiceID, intent.EnvironmentID)
	newState := &domain.ToolProfileState{ServiceID: intent.ServiceID, EnvironmentID: intent.EnvironmentID, CurrentToolsetHash: intent.ToolsetHash, CurrentImageDigest: imageRef, InstalledTools: intent.ResolvedTools}
	if state != nil {
		newState.PreviousImageDigest = state.CurrentImageDigest
	}
	if err := c.repo.UpsertProfileState(ctx, newState); err != nil {
		return c.fail(ctx, intent, "state update failed", err)
	}
	if err := c.transition(ctx, intent, domain.ToolProvisionStatusCompleted, "completed", "tool provisioning completed"); err != nil {
		return err
	}
	if c.dispatcher != nil {
		c.dispatcher.Dispatch(ctx, string(events.EventToolProvisionCompleted), map[string]any{"intent_id": intent.ID.String(), "service_id": intent.ServiceID.String(), "environment_id": intent.EnvironmentID.String(), "toolset_hash": intent.ToolsetHash})
	}
	c.publishResult(ctx, intent, true, "")
	return nil
}

func (c *ToolProvisioningCoordinator) transition(ctx context.Context, intent *domain.ToolProvisionIntent, status domain.ToolProvisionStatus, step, message string) error {
	intent.Status = status
	if err := c.repo.UpdateIntent(ctx, intent); err != nil {
		return err
	}
	c.logger.Info("tool intent transition", zap.String("intent_id", intent.ID.String()), zap.String("status", string(status)), zap.String("step", step), zap.String("message", message))
	c.publishStatus(ctx, intent, step, message)
	return nil
}

func (c *ToolProvisioningCoordinator) fail(ctx context.Context, intent *domain.ToolProvisionIntent, message string, cause error) error {
	intent.Status = domain.ToolProvisionStatusFailed
	_ = c.repo.UpdateIntent(ctx, intent)
	c.logger.Error("tool provisioning failed", zap.String("intent_id", intent.ID.String()), zap.String("message", message), zap.Error(cause))
	c.publishStatus(ctx, intent, "failed", message)
	c.publishResult(ctx, intent, false, message+": "+cause.Error())
	if c.dispatcher != nil {
		c.dispatcher.Dispatch(ctx, string(events.EventToolProvisionFailed), map[string]any{"intent_id": intent.ID.String(), "service_id": intent.ServiceID.String(), "environment_id": intent.EnvironmentID.String(), "error": cause.Error()})
	}
	return cause
}

func (c *ToolProvisioningCoordinator) publishStatus(ctx context.Context, intent *domain.ToolProvisionIntent, step, message string) {
	if c.responder == nil || intent == nil {
		return
	}
	_ = c.responder.PublishStatus(ctx, &nostr.Event{ID: intent.NostrEventID, PubKey: intent.RequesterPubkey}, intent, step, message)
}

func (c *ToolProvisioningCoordinator) publishResult(ctx context.Context, intent *domain.ToolProvisionIntent, success bool, errMsg string) {
	if c.responder == nil || intent == nil {
		return
	}
	_ = c.responder.PublishResult(ctx, &nostr.Event{ID: intent.NostrEventID, PubKey: intent.RequesterPubkey}, intent, success, errMsg)
}

func (c *ToolProvisioningCoordinator) targetRef(hash string) string {
	tag := "toolset"
	if hash != "" {
		tag = strings.TrimPrefix(hash, "sha256:")
		if len(tag) > 12 {
			tag = tag[:12]
		}
	}
	repo := strings.Trim(c.config.TargetRepo, "/")
	reg := strings.TrimSuffix(strings.TrimSpace(c.config.TargetRegistry), "/")
	if reg != "" {
		return reg + "/" + repo + ":" + tag
	}
	return repo + ":" + tag
}
