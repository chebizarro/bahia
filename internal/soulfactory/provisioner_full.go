package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/adapters/agentmemory"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/qdrant"
	"github.com/openagentsinc/bahia/internal/domain"
)

// FullProvisioner orchestrates the complete agent provisioning workflow.
// This is the Phase 2/3 implementation with all integrations including bahia.
type FullProvisioner struct {
	reactor          *Reactor
	avatarGenerator  *llm.AvatarGenerator
	blossomClient    *blossom.Client
	qdrantClient     *qdrant.Client
	agentMemory      *agentmemory.Client
	workspaceManager *WorkspaceManager
	nip05Manager     *NIP05Manager
	bahiaIntegration *BahiaIntegration
}

// FullProvisionerConfig holds all adapter configurations.
type FullProvisionerConfig struct {
	Blossom     blossom.Config
	Qdrant      qdrant.Config
	AgentMemory agentmemory.Config
	Avatar      llm.AvatarConfig
	Workspace   WorkspaceConfig
	NIP05       NIP05Config
	Bahia       BahiaIntegrationConfig
}

// NewFullProvisioner creates a provisioner with all adapters.
// The bahiaIntegration parameter is optional - pass nil if bahia integration is not needed.
func NewFullProvisioner(reactor *Reactor, config FullProvisionerConfig, bahiaIntegration *BahiaIntegration) *FullProvisioner {
	logger := reactor.logger
	p := &FullProvisioner{
		reactor:          reactor,
		qdrantClient:     qdrant.NewClient(config.Qdrant, logger),
		agentMemory:      agentmemory.NewClient(config.AgentMemory, logger),
		bahiaIntegration: bahiaIntegration,
	}
	if len(config.Blossom.Servers) > 0 {
		p.blossomClient = blossom.NewClient(config.Blossom, logger)
	}
	if config.Avatar.LemmyURL != "" && p.blossomClient != nil {
		p.avatarGenerator = llm.NewAvatarGenerator(config.Avatar, logger)
	}
	if config.Workspace.GiteaURL != "" {
		p.workspaceManager = NewWorkspaceManager(config.Workspace, logger)
	}
	if config.NIP05.Domain != "" && config.NIP05.WellKnownDir != "" {
		p.nip05Manager = NewNIP05Manager(config.NIP05, logger)
	}
	return p
}

// Provision executes the configured full provisioning workflow.
func (p *FullProvisioner) Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	return p.ProvisionFull(ctx, req, run)
}

// ProvisionFull executes the complete provisioning workflow.
func (p *FullProvisioner) ProvisionFull(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	logger := p.reactor.logger.With("agent_id", req.AgentID, "run_id", run.ID)

	soul := &domain.AgentSoul{
		ID:            domain.NewUUID(),
		AgentID:       req.AgentID,
		Name:          req.Name,
		Tier:          req.Tier,
		Status:        domain.SoulStatusProvisioning,
		TemplateRef:   req.TemplateRef,
		OriginalBrief: req.Brief,
		CreatedAt:     time.Now(),
	}

	requestEvent := &nostr.Event{
		ID:     run.RequestID,
		PubKey: run.RequesterPubkey,
	}

	totalSteps := len(domain.ProvisioningSteps)

	// Step 1: Generate soul content
	logger.Info("step 1/8: generating soul content")
	run.CurrentStep = domain.StepGenerate
	p.publishProgress(ctx, requestEvent, domain.StepGenerate, 1, totalSteps, "Generating soul content via LLM...")

	stepStart := time.Now()
	output, err := p.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{
		AgentID: req.AgentID,
		Name:    req.Name,
		Brief:   req.Brief,
		Tier:    req.Tier,
	})
	if err != nil {
		p.recordStep(run, domain.StepGenerate, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("generate soul: %w", err)
	}

	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	if soul.Name == "" {
		soul.Name = req.AgentID
	}
	soul.Purpose = req.Brief

	p.recordStep(run, domain.StepGenerate, domain.StepStatusComplete, map[string]interface{}{
		"allowed_kinds": len(output.AllowedKinds),
		"tool_count":    len(output.ToolGrants),
	}, nil, time.Since(stepStart))

	// Step 2: Register with Signet
	logger.Info("step 2/8: registering with Signet")
	run.CurrentStep = domain.StepSignet
	p.publishProgress(ctx, requestEvent, domain.StepSignet, 2, totalSteps, "Registering keypair with Signet...")

	stepStart = time.Now()
	pubkey, npub, bunkerURI, err := p.reactor.signer.ProvisionAgent(ctx, req.AgentID, soul.AllowedKinds)
	if err != nil {
		p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("signet provision: %w", err)
	}

	soul.NostrPubkey = pubkey
	soul.NostrNpub = npub
	soul.BunkerURI = bunkerURI

	p.recordStep(run, domain.StepSignet, domain.StepStatusComplete, map[string]interface{}{
		"npub": npub,
	}, nil, time.Since(stepStart))

	// Step 3: Generate and upload avatar
	logger.Info("step 3/8: generating avatar")
	run.CurrentStep = domain.StepAvatar
	p.publishProgress(ctx, requestEvent, domain.StepAvatar, 3, totalSteps, "Generating avatar via FLUX...")

	stepStart = time.Now()
	if p.avatarGenerator == nil || p.blossomClient == nil {
		p.recordStep(run, domain.StepAvatar, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "avatar generator or blossom storage not configured",
		}, nil, 0)
	} else {
		avatarResult, err := p.avatarGenerator.Generate(ctx, output.AvatarPrompt, req.AgentID)
		if err != nil {
			logger.Warn("avatar generation failed; continuing without avatar", "error", err)
			p.recordStep(run, domain.StepAvatar, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			// Continue without avatar - not fatal
		} else {
			// Upload avatar to Blossom
			bd, err := p.blossomClient.Upload(ctx, avatarResult.ImageData, avatarResult.ContentType)
			if err != nil {
				logger.Warn("avatar upload failed", "error", err)
			} else {
				soul.AvatarBlobHash = bd.SHA256
				soul.AvatarURL = bd.URL
				p.recordStep(run, domain.StepAvatar, domain.StepStatusComplete, map[string]interface{}{
					"avatar_url": bd.URL,
				}, nil, time.Since(stepStart))
			}
		}
	}

	// Step 4: Publish Nostr profile
	logger.Info("step 4/8: publishing Nostr profile")
	run.CurrentStep = domain.StepProfile
	p.publishProgress(ctx, requestEvent, domain.StepProfile, 4, totalSteps, "Publishing Nostr profile (kind:0)...")

	stepStart = time.Now()
	if p.nip05Manager != nil {
		soul.NIP05 = p.nip05Manager.GetNIP05(soul.AgentID)
	}
	if err := p.publishProfile(ctx, soul); err != nil {
		p.recordStep(run, domain.StepProfile, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("publish profile: %w", err)
	}

	p.recordStep(run, domain.StepProfile, domain.StepStatusComplete, map[string]interface{}{
		"nip05": soul.NIP05,
	}, nil, time.Since(stepStart))

	// Step 5: Create Qdrant collection
	logger.Info("step 5/8: creating Qdrant collection")
	run.CurrentStep = domain.StepQdrant
	p.publishProgress(ctx, requestEvent, domain.StepQdrant, 5, totalSteps, "Creating vector memory collection...")

	stepStart = time.Now()
	if p.qdrantClient == nil || !p.qdrantClient.Configured() {
		p.recordStep(run, domain.StepQdrant, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "qdrant url not configured",
		}, nil, 0)
	} else {
		soul.QdrantCollection = req.AgentID
		if err := p.qdrantClient.CreateCollection(ctx, soul.QdrantCollection, qdrant.DefaultCollectionConfig()); err != nil {
			logger.Warn("Qdrant collection creation failed", "error", err)
			p.recordStep(run, domain.StepQdrant, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			// Continue - not fatal for basic operation
		} else {
			p.recordStep(run, domain.StepQdrant, domain.StepStatusComplete, map[string]interface{}{
				"collection": soul.QdrantCollection,
			}, nil, time.Since(stepStart))
		}
	}

	// Step 6: Seed agent-memory
	logger.Info("step 6/8: seeding agent memory")
	run.CurrentStep = domain.StepMemory
	p.publishProgress(ctx, requestEvent, domain.StepMemory, 6, totalSteps, "Seeding agent memory...")

	stepStart = time.Now()
	if p.agentMemory == nil || !p.agentMemory.Configured() {
		p.recordStep(run, domain.StepMemory, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "agent-memory url not configured",
		}, nil, 0)
	} else {
		if err := p.agentMemory.RegisterAgent(ctx, soul.AgentID, soul.NostrNpub, map[string]interface{}{
			"tier":   soul.Tier,
			"status": "provisioning",
		}); err != nil {
			logger.Warn("agent memory registration failed", "error", err)
			p.recordStep(run, domain.StepMemory, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			// Continue - not fatal
		} else {
			// Seed initial memories
			entries := agentmemory.CreateInitialMemory(soul.AgentID, soul.Name, soul.Purpose, soul.SoulMD)
			if err := p.agentMemory.SeedMemory(ctx, soul.AgentID, entries); err != nil {
				logger.Warn("memory seeding failed", "error", err)
			}
			p.recordStep(run, domain.StepMemory, domain.StepStatusComplete, map[string]interface{}{
				"entries": len(entries),
			}, nil, time.Since(stepStart))
		}
	}

	// Step 7: Initialize workspace
	logger.Info("step 7/8: initializing workspace")
	run.CurrentStep = domain.StepWorkspace
	p.publishProgress(ctx, requestEvent, domain.StepWorkspace, 7, totalSteps, "Initializing workspace repository...")

	stepStart = time.Now()
	if p.workspaceManager == nil {
		p.recordStep(run, domain.StepWorkspace, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "workspace remote not configured",
		}, nil, 0)
	} else {
		repoURL, err := p.workspaceManager.InitWorkspace(ctx, soul)
		if err != nil {
			logger.Warn("workspace initialization failed", "error", err)
			p.recordStep(run, domain.StepWorkspace, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			// Continue - not fatal
		} else {
			soul.WorkspaceRepoURL = repoURL
			p.recordStep(run, domain.StepWorkspace, domain.StepStatusComplete, map[string]interface{}{
				"repo_url": repoURL,
			}, nil, time.Since(stepStart))
		}
	}

	// Step 8: Register NIP-05 and finalize
	logger.Info("step 8/8: registering NIP-05 and finalizing")
	run.CurrentStep = domain.StepDeploy
	p.publishProgress(ctx, requestEvent, domain.StepDeploy, 8, totalSteps, "Registering NIP-05 and finalizing...")

	stepStart = time.Now()

	// Register NIP-05
	if p.nip05Manager != nil {
		if err := p.nip05Manager.Register(ctx, soul.AgentID, soul.NostrPubkey, []string{
			"wss://relay.sharegap.net",
			"wss://armada.sharegap.net",
		}); err != nil {
			logger.Warn("NIP-05 registration failed", "error", err)
		}
	}

	// Upload soul snapshot to Blossom (birth certificate)
	soulJSON, _ := json.Marshal(map[string]interface{}{
		"id":            soul.AgentID,
		"npub":          soul.NostrNpub,
		"pubkey":        soul.NostrPubkey,
		"bunker_uri":    soul.BunkerURI,
		"avatar_url":    soul.AvatarURL,
		"qdrant":        soul.QdrantCollection,
		"workspace":     soul.WorkspaceRepoURL,
		"created":       time.Now().UTC().Format(time.RFC3339),
		"template":      soul.TemplateRef,
		"tier":          soul.Tier,
		"allowed_kinds": soul.AllowedKinds,
	})

	if p.blossomClient != nil {
		bd, err := p.blossomClient.Upload(ctx, soulJSON, "application/json")
		if err != nil {
			logger.Warn("soul snapshot upload failed", "error", err)
		} else {
			soul.SoulBlobHash = bd.SHA256
		}
	}

	// Mark soul as active
	soul.Status = domain.SoulStatusActive
	now := time.Now()
	soul.ProvisionedAt = &now

	// Publish the soul event
	logger.Info("publishing soul event (kind:31951)")
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return nil, fmt.Errorf("publish soul: %w", err)
	}

	// Register with bahia if integration is configured
	if p.bahiaIntegration != nil {
		logger.Info("registering with bahia deployment registry")
		serviceID, err := p.bahiaIntegration.RegisterSoulAsService(ctx, soul)
		if err != nil {
			logger.Warn("bahia service registration failed", "error", err)
		} else {
			soul.BahiaServiceID = &serviceID

			// Create initial deployment intent
			if _, err := p.bahiaIntegration.CreateInitialDeployment(ctx, soul, serviceID); err != nil {
				if errors.Is(err, ErrDeployableArtifactRequired) {
					logger.Info("bahia initial deployment skipped", "reason", err)
				} else {
					logger.Warn("bahia initial deployment failed", "error", err)
				}
			}

			// Sync status
			deployStatus, err := p.bahiaIntegration.SyncSoulStatus(ctx, soul)
			if err != nil {
				logger.Warn("bahia status sync failed", "error", err)
			} else {
				soul.DeployStatus = deployStatus
			}
		}
	}

	p.recordStep(run, domain.StepDeploy, domain.StepStatusComplete, map[string]interface{}{
		"nip05":         soul.NIP05,
		"soul_blob":     soul.SoulBlobHash,
		"bahia_service": soul.BahiaServiceID,
		"deploy_status": soul.DeployStatus,
	}, nil, time.Since(stepStart))

	run.SoulID = &soul.ID
	logger.Info("provisioning complete",
		"soul_id", soul.ID,
		"npub", soul.NostrNpub,
		"avatar", soul.AvatarURL,
		"workspace", soul.WorkspaceRepoURL,
		"bahia_service", soul.BahiaServiceID,
	)

	return soul, nil
}

func (p *FullProvisioner) SuspendSoul(context.Context, string, string) error {
	return ErrLifecycleUnsupported
}

func (p *FullProvisioner) ResumeSoul(context.Context, string) error {
	return ErrLifecycleUnsupported
}

func (p *FullProvisioner) RevokeSoul(context.Context, string, string) error {
	return ErrLifecycleUnsupported
}

func (p *FullProvisioner) RegenerateSoul(context.Context, string, string) error {
	return ErrLifecycleUnsupported
}

func (p *FullProvisioner) RedeploySoul(context.Context, string) error {
	return ErrLifecycleUnsupported
}

func (p *FullProvisioner) publishProgress(ctx context.Context, requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) {
	if err := p.reactor.PublishStatus(ctx, requestEvent, step, current, total, message); err != nil {
		p.reactor.logger.Warn("failed to publish progress", "step", step, "error", err)
	}
}

func (p *FullProvisioner) recordStep(run *domain.ProvisioningRun, step domain.ProvisioningStep, status domain.StepStatus, output map[string]interface{}, err error, duration time.Duration) {
	result := domain.ProvisioningStepResult{
		Name:     step,
		Status:   status,
		Output:   output,
		Duration: duration,
	}
	if err != nil {
		result.Error = err.Error()
	}
	run.Steps = append(run.Steps, result)
}

func (p *FullProvisioner) publishProfile(ctx context.Context, soul *domain.AgentSoul) error {
	profile := map[string]string{
		"name":  soul.Name,
		"about": soul.Purpose,
		"nip05": soul.NIP05,
	}
	if soul.AvatarURL != "" {
		profile["picture"] = soul.AvatarURL
	}

	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}

	event := &nostr.Event{
		Kind:      0,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   string(profileJSON),
	}

	if err := p.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign profile: %w", err)
	}

	return p.reactor.publish(ctx, event, p.reactor.config.Relays)
}
