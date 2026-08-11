package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/adapters/agentmemory"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/qdrant"
	"github.com/openagentsinc/bahia/internal/domain"
)

// FullProvisioner orchestrates the complete agent provisioning workflow.
// This is the Phase 2/3 implementation with all integrations including bahia.
type workspaceInitializer interface {
	InitWorkspace(context.Context, *domain.AgentSoul) (string, error)
}

type agentMemoryClient interface {
	Configured() bool
	RegisterAgent(context.Context, string, string, map[string]interface{}) error
	SeedMemory(context.Context, string, []agentmemory.MemoryEntry) error
}

type FullProvisioner struct {
	reactor                  *Reactor
	avatarGenerator          *llm.AvatarGenerator
	blossomClient            *blossom.Client
	qdrantClient             *qdrant.Client
	agentMemory              agentMemoryClient
	workspaceManager         workspaceInitializer
	nip05Manager             *NIP05Manager
	nip05Relays              []string
	bahiaIntegration         *BahiaIntegration
	nip29Membership          nip29MembershipAssigner
	nip29MembershipErr       error
	communikeysMembership    communikeysMembershipAssigner
	communikeysMembershipErr error
	concordMembership        concordMembershipAssigner
	concordMembershipErr     error
	runtimeAdapters          map[domain.RuntimeTarget]RuntimeAdapter
	lookupSoul               func(context.Context, string) (*domain.AgentSoul, error)
}

// FullProvisionerConfig holds all adapter configurations.
type FullProvisionerConfig struct {
	Blossom     blossom.Config
	Qdrant      qdrant.Config
	AgentMemory agentmemory.Config
	Avatar      llm.AvatarConfig
	Workspace   WorkspaceConfig
	NIP05       NIP05Config
	// NIP05Relays are advertised for provisioned identities. They must be explicitly configured when NIP-05 is enabled.
	NIP05Relays            []string
	NIP29Groups            []NIP29Group
	CommunikeysCommunities []CommunikeysCommunity
	ConcordCommunities     []ConcordCommunity
	Bahia                  BahiaIntegrationConfig
	RuntimeAdapters        map[domain.RuntimeTarget]RuntimeAdapter
}

// NewFullProvisioner creates a provisioner with all adapters.
// The bahiaIntegration parameter is optional - pass nil if bahia integration is not needed.
func NewFullProvisioner(reactor *Reactor, config FullProvisionerConfig, bahiaIntegration *BahiaIntegration) *FullProvisioner {
	logger := reactor.logger
	nip29Membership, nip29Err := newNIP29Membership(config.NIP29Groups, reactor.signer)
	if nip29Err != nil {
		logger.Error("NIP-29 membership configuration is invalid", "error", nip29Err)
	}
	communikeysMembership, communikeysErr := newCommunikeysMembership(config.CommunikeysCommunities, reactor.signer, reactor.relayBus)
	if communikeysErr != nil {
		logger.Error("Communikeys membership configuration is invalid", "error", communikeysErr)
	}
	concordMembership, concordErr := newConcordMembership(config.ConcordCommunities, reactor.signer, reactor.relayBus)
	if concordErr != nil {
		logger.Error("Concord membership configuration is invalid", "error", concordErr)
	}
	p := &FullProvisioner{
		reactor:                  reactor,
		qdrantClient:             qdrant.NewClient(config.Qdrant, logger),
		agentMemory:              agentmemory.NewClient(config.AgentMemory, logger),
		bahiaIntegration:         bahiaIntegration,
		nip29Membership:          nip29Membership,
		nip29MembershipErr:       nip29Err,
		communikeysMembership:    communikeysMembership,
		communikeysMembershipErr: communikeysErr,
		concordMembership:        concordMembership,
		concordMembershipErr:     concordErr,
		nip05Relays:              append([]string(nil), config.NIP05Relays...),
		runtimeAdapters:          cloneRuntimeAdapters(config.RuntimeAdapters),
		lookupSoul:               reactor.GetSoul,
	}
	if len(config.Blossom.Servers) > 0 {
		p.blossomClient = blossom.NewClient(config.Blossom, logger)
	}
	if config.Avatar.LemmyURL != "" {
		p.avatarGenerator = llm.NewAvatarGenerator(config.Avatar, logger)
	}
	if config.Workspace.GiteaURL != "" {
		p.workspaceManager = NewWorkspaceManager(config.Workspace, logger)
	}
	if config.NIP05.Domain != "" && config.NIP05.WellKnownDir != "" {
		p.nip05Manager = NewNIP05Manager(config.NIP05, logger)
	}
	// Lifecycle requests are orchestrated by lifecycle_handler.go, not the
	// provisioning engine. Installing the handler here preserves the Bahia
	// integration side effects previously wired through FullProvisioner. The
	// lifecycle handler receives the same runtime adapter registry as the
	// provisioner so dispatch never diverges between provisioning and
	// lifecycle/customization paths.
	reactor.lifecycleHandler = NewLifecycleHandler(reactor, bahiaIntegration, nil, logger)
	reactor.lifecycleHandler.SetRuntimeAdapters(config.RuntimeAdapters)
	return p
}

// Provision executes the configured full provisioning workflow.
func (p *FullProvisioner) Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	return p.ProvisionFull(ctx, req, run)
}

// ProvisionFull executes the complete provisioning workflow.
func (p *FullProvisioner) ProvisionFull(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	resolved, err := p.resolveProvisioningSpec(ctx, req)
	if err != nil {
		return nil, err
	}
	logger := p.reactor.logger.With("agent_id", resolved.AgentID, "run_id", run.ID)
	run.AgentID = resolved.AgentID
	run.DraftRef = resolved.DraftRef
	run.DraftEventID = resolved.DraftEventID
	run.SpecHash = resolved.SpecHash

	soul := &domain.AgentSoul{
		ID:        domain.NewUUID(),
		AgentID:   resolved.AgentID,
		Name:      resolved.Name,
		Tier:      resolved.Tier,
		Status:    domain.SoulStatusProvisioning,
		CreatedAt: time.Now(),
	}
	resolved.applyToSoul(soul)

	requestID, err := nostr.IDFromHex(strings.TrimSpace(run.RequestID))
	if err != nil {
		return nil, fmt.Errorf("parse provisioning request event id: %w", err)
	}
	requesterPubkey, err := nostr.PubKeyFromHex(strings.TrimSpace(run.RequesterPubkey))
	if err != nil {
		return nil, fmt.Errorf("parse provisioning requester pubkey: %w", err)
	}
	requestEvent := &nostr.Event{
		ID:     requestID,
		PubKey: requesterPubkey,
	}

	totalSteps := len(domain.ProvisioningSteps)

	// Step 1: Generate soul content
	logger.Info("step 1/8: generating soul content")
	run.CurrentStep = domain.StepGenerate
	if err := p.publishProgress(ctx, requestEvent, domain.StepGenerate, 1, totalSteps, "Generating soul content via LLM..."); err != nil {
		return nil, err
	}

	stepStart := time.Now()
	var output *domain.SoulGeneratorOutput
	if resolved.Draft != nil {
		// A signed draft is the authoritative, reproducible soul snapshot. Do
		// not send it back through an LLM: that can alter approved content and
		// makes deterministic provisioning depend on model availability.
		draft := resolved.Draft.Content.MigrateToLatest()
		avatarPrompt := ""
		if draft.Avatar.Generation != nil {
			avatarPrompt = draft.Avatar.Generation.Prompt
		}
		output = &domain.SoulGeneratorOutput{
			SoulMD:       draft.SoulMD,
			IdentityMD:   draft.IdentityMD,
			AllowedKinds: append([]int{}, draft.Permissions.AllowedKinds...),
			ToolGrants:   append([]domain.ToolGrant{}, draft.Permissions.ToolGrants...),
			AvatarPrompt: avatarPrompt,
		}
	} else {
		output, err = p.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{
			Template: resolved.Template,
			AgentID:  resolved.AgentID,
			Name:     resolved.Name,
			Brief:    resolved.Brief,
			Tier:     resolved.Tier,
		})
		if err != nil {
			p.recordStep(run, domain.StepGenerate, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("generate soul: %w", err)
		}
	}

	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	if resolved.Draft != nil || len(resolved.Permissions.AllowedKinds) > 0 {
		soul.AllowedKinds = append([]int{}, resolved.Permissions.AllowedKinds...)
	} else {
		soul.AllowedKinds = output.AllowedKinds
	}
	if resolved.Draft != nil || len(resolved.Permissions.ToolGrants) > 0 {
		soul.ToolGrants = append([]domain.ToolGrant{}, resolved.Permissions.ToolGrants...)
	} else {
		soul.ToolGrants = output.ToolGrants
	}
	if soul.Name == "" {
		soul.Name = resolved.AgentID
	}
	soul.Purpose = resolved.Brief

	p.recordStep(run, domain.StepGenerate, domain.StepStatusComplete, map[string]interface{}{
		"allowed_kinds": len(output.AllowedKinds),
		"tool_count":    len(output.ToolGrants),
	}, nil, time.Since(stepStart))

	// Step 2: Register with Signet
	logger.Info("step 2/8: registering with Signet")
	run.CurrentStep = domain.StepSignet
	if err := p.publishProgress(ctx, requestEvent, domain.StepSignet, 2, totalSteps, "Registering keypair with Signet..."); err != nil {
		return nil, err
	}

	stepStart = time.Now()
	pubkey, npub, bunkerURI, err := p.reactor.signer.ProvisionAgent(ctx, resolved.AgentID, soul.AllowedKinds)
	if err != nil {
		p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("signet provision: %w", err)
	}

	soul.NostrPubkey = pubkey
	soul.NostrNpub = npub
	soul.BunkerURI = bunkerURI

	var assignedGroups []string
	var assignedCommunities []string
	var assignedConcordCommunities []string
	if p.nip29MembershipErr != nil {
		p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, p.nip29MembershipErr, time.Since(stepStart))
		return nil, fmt.Errorf("configure NIP-29 groups: %w", p.nip29MembershipErr)
	}
	if p.nip29Membership != nil {
		assignedGroups, err = p.nip29Membership.Assign(ctx, soul.NostrPubkey)
		if err != nil {
			p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("assign NIP-29 groups: %w", err)
		}
	}
	if p.communikeysMembershipErr != nil {
		p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, p.communikeysMembershipErr, time.Since(stepStart))
		return nil, fmt.Errorf("configure Communikeys communities: %w", p.communikeysMembershipErr)
	}
	if p.communikeysMembership != nil {
		assignedCommunities, err = p.communikeysMembership.Assign(ctx, soul.NostrPubkey)
		if err != nil {
			p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("assign Communikeys communities: %w", err)
		}
	}
	if p.concordMembershipErr != nil {
		p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, p.concordMembershipErr, time.Since(stepStart))
		return nil, fmt.Errorf("configure Concord communities: %w", p.concordMembershipErr)
	}
	if p.concordMembership != nil {
		assignedConcordCommunities, err = p.concordMembership.Assign(ctx, soul.NostrPubkey)
		if err != nil {
			p.recordStep(run, domain.StepSignet, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("assign Concord communities: %w", err)
		}
	}
	p.recordStep(run, domain.StepSignet, domain.StepStatusComplete, map[string]interface{}{
		"npub":                    npub,
		"nip29_groups":            assignedGroups,
		"communikeys_communities": assignedCommunities,
		"concord_communities":     assignedConcordCommunities,
	}, nil, time.Since(stepStart))

	// Step 3: Generate and upload avatar
	logger.Info("step 3/8: generating avatar")
	run.CurrentStep = domain.StepAvatar
	if err := p.publishProgress(ctx, requestEvent, domain.StepAvatar, 3, totalSteps, "Generating avatar via FLUX..."); err != nil {
		return nil, err
	}

	stepStart = time.Now()
	if p.avatarGenerator == nil {
		p.recordStep(run, domain.StepAvatar, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "avatar generator not configured",
		}, nil, 0)
	} else {
		avatarResult, err := p.avatarGenerator.Generate(ctx, output.AvatarPrompt, resolved.AgentID)
		if err != nil {
			p.recordStep(run, domain.StepAvatar, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("generate avatar: %w", err)
		}
		if p.blossomClient == nil {
			err := fmt.Errorf("avatar storage is not configured")
			p.recordStep(run, domain.StepAvatar, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, err
		}
		stored, err := p.blossomClient.StoreAvatar(ctx, avatarResult.ImageData, avatarResult.ContentType, avatarResult.SourceURL)
		if err != nil {
			p.recordStep(run, domain.StepAvatar, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("store avatar: %w", err)
		}
		soul.AvatarBlobHash = stored.Hash
		soul.AvatarURL = stored.URL
		soul.Assets.AvatarRef = stored.Ref
		p.recordStep(run, domain.StepAvatar, domain.StepStatusComplete, map[string]interface{}{
			"avatar_ref": stored.Ref,
			"avatar_url": stored.URL,
			"fallback":   stored.Fallback,
		}, nil, time.Since(stepStart))
	}

	// Step 4: Publish Nostr profile
	logger.Info("step 4/8: publishing Nostr profile")
	run.CurrentStep = domain.StepProfile
	if err := p.publishProgress(ctx, requestEvent, domain.StepProfile, 4, totalSteps, "Publishing Nostr profile (kind:0)..."); err != nil {
		return nil, err
	}

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
	if err := p.publishProgress(ctx, requestEvent, domain.StepQdrant, 5, totalSteps, "Creating vector memory collection..."); err != nil {
		return nil, err
	}

	stepStart = time.Now()
	if p.qdrantClient == nil || !p.qdrantClient.Configured() {
		p.recordStep(run, domain.StepQdrant, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "qdrant url not configured",
		}, nil, 0)
	} else {
		soul.QdrantCollection = resolved.AgentID
		if err := p.qdrantClient.CreateCollection(ctx, soul.QdrantCollection, qdrant.DefaultCollectionConfig()); err != nil {
			p.recordStep(run, domain.StepQdrant, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("create Qdrant collection: %w", err)
		} else {
			p.recordStep(run, domain.StepQdrant, domain.StepStatusComplete, map[string]interface{}{
				"collection": soul.QdrantCollection,
			}, nil, time.Since(stepStart))
		}
	}

	// Step 6: Seed agent-memory
	logger.Info("step 6/8: seeding agent memory")
	run.CurrentStep = domain.StepMemory
	if err := p.publishProgress(ctx, requestEvent, domain.StepMemory, 6, totalSteps, "Seeding agent memory..."); err != nil {
		return nil, err
	}

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
			p.recordStep(run, domain.StepMemory, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("register agent memory: %w", err)
		} else {
			// Seed initial memories
			entries := agentmemory.CreateInitialMemory(soul.AgentID, soul.Name, soul.Purpose, soul.SoulMD)
			if err := p.agentMemory.SeedMemory(ctx, soul.AgentID, entries); err != nil {
				p.recordStep(run, domain.StepMemory, domain.StepStatusFailed, nil, err, time.Since(stepStart))
				return nil, fmt.Errorf("seed agent memory: %w", err)
			}
			p.recordStep(run, domain.StepMemory, domain.StepStatusComplete, map[string]interface{}{
				"entries": len(entries),
			}, nil, time.Since(stepStart))
		}
	}

	// Step 7: Initialize workspace
	logger.Info("step 7/8: initializing workspace")
	run.CurrentStep = domain.StepWorkspace
	if err := p.publishProgress(ctx, requestEvent, domain.StepWorkspace, 7, totalSteps, "Initializing workspace repository..."); err != nil {
		return nil, err
	}

	stepStart = time.Now()
	if p.workspaceManager == nil {
		p.recordStep(run, domain.StepWorkspace, domain.StepStatusSkipped, map[string]interface{}{
			"reason": "workspace remote not configured",
		}, nil, 0)
	} else {
		repoURL, err := p.workspaceManager.InitWorkspace(ctx, soul)
		if err != nil {
			p.recordStep(run, domain.StepWorkspace, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("initialize workspace: %w", err)
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
	if err := p.publishProgress(ctx, requestEvent, domain.StepDeploy, 8, totalSteps, "Registering NIP-05 and finalizing..."); err != nil {
		return nil, err
	}

	stepStart = time.Now()

	// Register NIP-05
	if p.nip05Manager != nil {
		relays, err := explicitNIP05Relays(p.nip05Relays)
		if err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, err
		}
		if err := p.nip05Manager.Register(ctx, soul.AgentID, soul.NostrPubkey, relays); err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("NIP-05 registration: %w", err)
		}
	}

	// Upload soul snapshot to Blossom (birth certificate)
	soulJSON, _ := json.Marshal(map[string]interface{}{
		"id":            soul.AgentID,
		"npub":          soul.NostrNpub,
		"pubkey":        soul.NostrPubkey,
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
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("upload soul snapshot: %w", err)
		}
		soul.SoulBlobHash = bd.SHA256
	}

	var runtimeResult *RuntimeControlResultEnvelope
	if resolved.Runtime.Target != "" {
		logger.Info("binding runtime through SoulFactory runtime adapter", "runtime", resolved.Runtime.Target)
		var err error
		runtimeResult, err = p.executeRuntimeProvision(ctx, soul, resolved, run)
		if err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, err
		}
	}

	// Register with bahia if integration is configured
	if p.bahiaIntegration != nil {
		logger.Info("registering with bahia deployment registry")
		serviceID, err := p.bahiaIntegration.RegisterSoulAsService(ctx, soul)
		if err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("register Bahia service: %w", err)
		}
		soul.BahiaServiceID = &serviceID

		if _, err := p.bahiaIntegration.CreateInitialDeployment(ctx, soul, serviceID, runtimeResult); err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("create initial Bahia deployment: %w", err)
		}

		deployStatus, err := p.bahiaIntegration.SyncSoulStatus(ctx, soul)
		if err != nil {
			p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
			return nil, fmt.Errorf("sync Bahia deployment status: %w", err)
		}
		soul.DeployStatus = deployStatus
	}

	// Only an entirely successful configured workflow may activate the soul.
	soul.Status = domain.SoulStatusActive
	now := time.Now()
	soul.ProvisionedAt = &now

	// Publish the final authoritative read model only after immediately-known
	// runtime and Bahia fields have been populated.
	logger.Info("publishing final soul event (kind:31951)")
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		p.recordStep(run, domain.StepDeploy, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("publish soul: %w", err)
	}

	p.recordStep(run, domain.StepDeploy, domain.StepStatusComplete, map[string]interface{}{
		"nip05":           soul.NIP05,
		"soul_blob":       soul.SoulBlobHash,
		"bahia_service":   soul.BahiaServiceID,
		"deploy_status":   soul.DeployStatus,
		"runtime":         soul.Runtime.Target,
		"runtime_binding": soul.Runtime.RuntimeBinding,
		"runtime_state":   soul.Runtime.State,
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

func (p *FullProvisioner) SuspendSoul(ctx context.Context, soulRef, reason string) error {
	soul, err := p.lookupSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}
	if p.bahiaIntegration != nil {
		if err := p.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionSuspend); err != nil {
			return err
		}
	}
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.SuspendAgent(ctx, soul.NostrPubkey); err != nil {
			return fmt.Errorf("suspend signer access: %w", err)
		}
	}
	soul.Status = domain.SoulStatusSuspended
	soul.DeployStatus = "stopped"
	now := time.Now().UTC()
	soul.SuspendedAt = &now
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish suspended soul: %w", err)
	}
	return nil
}

func (p *FullProvisioner) ResumeSoul(ctx context.Context, soulRef string) error {
	soul, err := p.lookupSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}
	if soul.Status != domain.SoulStatusSuspended {
		return fmt.Errorf("cannot resume soul in status %s", soul.Status)
	}
	if p.bahiaIntegration != nil {
		if err := p.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionResume); err != nil {
			return err
		}
	}
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.ResumeAgent(ctx, soul.NostrPubkey); err != nil {
			return fmt.Errorf("resume signer access: %w", err)
		}
	}
	soul.Status = domain.SoulStatusActive
	soul.DeployStatus = "deploying"
	soul.SuspendedAt = nil
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish resumed soul: %w", err)
	}
	return nil
}

func (p *FullProvisioner) RevokeSoul(ctx context.Context, soulRef, reason string) error {
	soul, err := p.lookupSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}
	if p.bahiaIntegration != nil {
		if err := p.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRevoke); err != nil {
			return err
		}
	}
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.RevokeAgent(ctx, soul.NostrPubkey); err != nil {
			return fmt.Errorf("revoke signer access: %w", err)
		}
	}
	soul.Status = domain.SoulStatusRevoked
	soul.DeployStatus = "stopped"
	now := time.Now().UTC()
	soul.RevokedAt = &now
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish revoked soul: %w", err)
	}
	return nil
}

func (p *FullProvisioner) RegenerateSoul(ctx context.Context, soulRef, newBrief string) error {
	soul, err := p.lookupSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}
	if newBrief == "" {
		return fmt.Errorf("regenerate requires a new brief")
	}
	if soul.Status == domain.SoulStatusRevoked {
		return fmt.Errorf("cannot regenerate revoked soul")
	}
	output, err := p.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{
		AgentID: soul.AgentID,
		Name:    soul.Name,
		Brief:   newBrief,
		Tier:    soul.Tier,
	})
	if err != nil {
		return fmt.Errorf("regenerate soul: %w", err)
	}
	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	soul.OriginalBrief = newBrief
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish regenerated soul: %w", err)
	}
	return nil
}

func (p *FullProvisioner) RedeploySoul(ctx context.Context, soulRef string) error {
	soul, err := p.lookupSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}
	if soul.Status != domain.SoulStatusActive {
		return fmt.Errorf("cannot redeploy soul in status %s", soul.Status)
	}
	if p.bahiaIntegration != nil {
		if err := p.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRedeploy); err != nil {
			return err
		}
	}
	soul.DeployStatus = "deploying"
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish redeployed soul: %w", err)
	}
	return nil
}

func (p *FullProvisioner) publishProgress(ctx context.Context, requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) error {
	if err := p.reactor.PublishStatus(ctx, requestEvent, step, current, total, message); err != nil {
		p.reactor.logger.Error("failed to publish progress", "step", step, "error", err)
		return fmt.Errorf("publish progress %s: %w", step, err)
	}
	return nil
}

func explicitNIP05Relays(configured []string) ([]string, error) {
	relays := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, configuredRelay := range configured {
		relay := strings.TrimSpace(configuredRelay)
		if relay == "" {
			continue
		}
		if _, ok := seen[relay]; ok {
			continue
		}
		seen[relay] = struct{}{}
		relays = append(relays, relay)
	}
	if len(relays) == 0 {
		return nil, fmt.Errorf("NIP-05 relays are not configured; set soul_factory.nip05_relays explicitly")
	}
	return relays, nil
}

func cloneRuntimeAdapters(adapters map[domain.RuntimeTarget]RuntimeAdapter) map[domain.RuntimeTarget]RuntimeAdapter {
	if len(adapters) == 0 {
		return nil
	}
	out := make(map[domain.RuntimeTarget]RuntimeAdapter, len(adapters))
	for target, adapter := range adapters {
		if adapter != nil {
			out[target] = adapter
		}
	}
	return out
}

func (p *FullProvisioner) executeRuntimeProvision(ctx context.Context, soul *domain.AgentSoul, resolved *resolvedProvisioningSpec, run *domain.ProvisioningRun) (*RuntimeControlResultEnvelope, error) {
	adapter := p.runtimeAdapters[resolved.Runtime.Target]
	if adapter == nil {
		return nil, fmt.Errorf("no runtime adapter configured for %s", resolved.Runtime.Target)
	}
	result, err := adapter.Execute(ctx, RuntimeAdapterRequest{
		Method: RuntimeMethodProvision,
		Operator: RuntimeOperatorRef{
			Pubkey:       run.RequesterPubkey,
			RequestEvent: run.RequestID,
		},
		Soul: RuntimeSoulRef{
			ID:       soul.AgentID,
			Draft:    firstNonEmpty(resolved.DraftEventID, resolved.DraftRef),
			SpecHash: resolved.SpecHash,
		},
		Target: RuntimeTargetRef{
			Runtime:       resolved.Runtime.Target,
			RuntimePubkey: resolved.Runtime.RuntimePubkey,
			AgentID:       soul.AgentID,
		},
		Params:      resolved.provisionRuntimeParams(soul),
		DraftPolicy: resolved.RelayPolicy,
		RequestKind: domain.KindProvisioningRequest,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime provision %s: %w", resolved.Runtime.Target, err)
	}
	applyRuntimeProvisionResult(soul, resolved, result)
	return result, nil
}

func applyRuntimeProvisionResult(soul *domain.AgentSoul, resolved *resolvedProvisioningSpec, result *RuntimeControlResultEnvelope) {
	if soul == nil || resolved == nil || result == nil {
		return
	}
	soul.Runtime.Target = resolved.Runtime.Target
	soul.Runtime.RuntimePubkey = firstNonEmpty(stringResult(result.Result, "runtime_pubkey"), resolved.Runtime.RuntimePubkey)
	if soul.Runtime.RuntimePubkey == "" && result.Event != nil {
		soul.Runtime.RuntimePubkey = result.Event.PubKey.Hex()
	}
	soul.Runtime.RuntimeBinding = firstNonEmpty(stringResult(result.Result, "runtime_binding"), soul.Runtime.RuntimeBinding)
	soul.Runtime.State = firstNonEmpty(stringResult(result.Result, "state"), soul.Runtime.State, "running")
	soul.Runtime.CapabilityRef = firstNonEmpty(stringResult(result.Result, "capability_ref"), soul.Runtime.CapabilityRef, resolved.Runtime.CapabilityRef)
	soul.CapabilityRef = firstNonEmpty(soul.Runtime.CapabilityRef, soul.CapabilityRef)
	if specHash := stringResult(result.Result, "spec_hash"); specHash != "" {
		soul.SpecHash = specHash
	}
}

func stringResult(result map[string]interface{}, key string) string {
	if result == nil {
		return ""
	}
	value, ok := result[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
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
