package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Provisioner orchestrates the agent provisioning workflow.
type Provisioner struct {
	reactor *Reactor
}

// NewProvisioner creates a new provisioner.
func NewProvisioner(reactor *Reactor) *Provisioner {
	return &Provisioner{reactor: reactor}
}

// ProgressCallback is called with provisioning progress updates.
type ProgressCallback func(step domain.ProvisioningStep, current, total int, message string)

// Provision executes the full provisioning workflow.
func (p *Provisioner) Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	if run != nil {
		run.CurrentStep = domain.StepGenerate
		p.recordStep(run, domain.StepGenerate, domain.StepStatusFailed, nil, ErrSoulFactoryUnavailable, 0)
	}
	return nil, ErrSoulFactoryUnavailable

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

	// Create a mock request event for publishing status
	requestEvent := &nostr.Event{
		ID:     run.RequestID,
		PubKey: run.RequesterPubkey,
	}

	totalSteps := len(domain.ProvisioningSteps)

	// Step 1: Generate soul content
	logger.Info("step 1: generating soul content")
	run.CurrentStep = domain.StepGenerate
	p.publishProgress(ctx, requestEvent, domain.StepGenerate, 1, totalSteps, "Generating soul content via LLM...")

	stepStart := time.Now()
	output, err := p.generateSoul(ctx, req, soul)
	if err != nil {
		p.recordStep(run, domain.StepGenerate, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("generate soul: %w", err)
	}

	// Apply generated content
	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	if soul.Name == "" {
		soul.Name = req.AgentID // fallback
	}
	if soul.Purpose == "" {
		soul.Purpose = req.Brief // fallback
	}

	p.recordStep(run, domain.StepGenerate, domain.StepStatusComplete, map[string]interface{}{
		"allowed_kinds": output.AllowedKinds,
		"tool_count":    len(output.ToolGrants),
	}, nil, time.Since(stepStart))

	// Step 2: Register with Signet
	logger.Info("step 2: registering with Signet")
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
		"pubkey": pubkey[:16] + "...",
		"npub":   npub,
	}, nil, time.Since(stepStart))

	// Step 3: Generate avatar (Phase 2 - skip for now)
	logger.Info("step 3: avatar generation (skipped - Phase 2)")
	run.CurrentStep = domain.StepAvatar
	p.publishProgress(ctx, requestEvent, domain.StepAvatar, 3, totalSteps, "Avatar generation (Phase 2 - skipped)...")
	p.recordStep(run, domain.StepAvatar, domain.StepStatusSkipped, nil, nil, 0)

	// Step 4: Publish Nostr profile
	logger.Info("step 4: publishing Nostr profile")
	run.CurrentStep = domain.StepProfile
	p.publishProgress(ctx, requestEvent, domain.StepProfile, 4, totalSteps, "Publishing Nostr profile (kind:0)...")

	stepStart = time.Now()
	if err := p.publishProfile(ctx, soul); err != nil {
		p.recordStep(run, domain.StepProfile, domain.StepStatusFailed, nil, err, time.Since(stepStart))
		return nil, fmt.Errorf("publish profile: %w", err)
	}

	p.recordStep(run, domain.StepProfile, domain.StepStatusComplete, map[string]interface{}{
		"nip05": soul.NIP05,
	}, nil, time.Since(stepStart))

	// Step 5: Create Qdrant collection (Phase 2 - skip for now)
	logger.Info("step 5: Qdrant collection (skipped - Phase 2)")
	run.CurrentStep = domain.StepQdrant
	p.publishProgress(ctx, requestEvent, domain.StepQdrant, 5, totalSteps, "Qdrant collection (Phase 2 - skipped)...")
	p.recordStep(run, domain.StepQdrant, domain.StepStatusSkipped, nil, nil, 0)
	soul.QdrantCollection = req.AgentID // Set collection name for later

	// Step 6: Seed agent-memory (Phase 2 - skip for now)
	logger.Info("step 6: agent-memory seeding (skipped - Phase 2)")
	run.CurrentStep = domain.StepMemory
	p.publishProgress(ctx, requestEvent, domain.StepMemory, 6, totalSteps, "Agent memory seeding (Phase 2 - skipped)...")
	p.recordStep(run, domain.StepMemory, domain.StepStatusSkipped, nil, nil, 0)

	// Step 7: Initialize workspace (Phase 2 - skip for now)
	logger.Info("step 7: workspace initialization (skipped - Phase 2)")
	run.CurrentStep = domain.StepWorkspace
	p.publishProgress(ctx, requestEvent, domain.StepWorkspace, 7, totalSteps, "Workspace initialization (Phase 2 - skipped)...")
	p.recordStep(run, domain.StepWorkspace, domain.StepStatusSkipped, nil, nil, 0)

	// Step 8: Register with bahia (Phase 3 - skip for now)
	logger.Info("step 8: bahia registration (skipped - Phase 3)")
	run.CurrentStep = domain.StepDeploy
	p.publishProgress(ctx, requestEvent, domain.StepDeploy, 8, totalSteps, "Bahia registration (Phase 3 - skipped)...")
	p.recordStep(run, domain.StepDeploy, domain.StepStatusSkipped, nil, nil, 0)

	// Mark soul as active
	soul.Status = domain.SoulStatusActive
	now := time.Now()
	soul.ProvisionedAt = &now

	// Publish the soul event
	logger.Info("publishing soul event")
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return nil, fmt.Errorf("publish soul: %w", err)
	}

	run.SoulID = &soul.ID
	logger.Info("provisioning complete", "soul_id", soul.ID, "npub", soul.NostrNpub)

	return soul, nil
}

// generateSoul calls the LLM to generate soul content.
func (p *Provisioner) generateSoul(ctx context.Context, req *domain.ProvisioningRequest, soul *domain.AgentSoul) (*domain.SoulGeneratorOutput, error) {
	input := domain.SoulGeneratorInput{
		AgentID: req.AgentID,
		Name:    req.Name,
		Brief:   req.Brief,
		Tier:    req.Tier,
	}

	// TODO: Load template from relay if TemplateRef is set
	// For now, use a default prompt

	output, err := p.reactor.generator.Generate(ctx, input)
	if err != nil {
		return nil, err
	}

	// Extract purpose from soul_md if not set
	if soul.Purpose == "" && output.SoulMD != "" {
		// Simple extraction - first paragraph after the title
		soul.Purpose = req.Brief
	}

	return output, nil
}

// publishProfile publishes a kind:0 profile for the agent.
func (p *Provisioner) publishProfile(ctx context.Context, soul *domain.AgentSoul) error {
	// Set NIP-05
	soul.NIP05 = fmt.Sprintf("%s@sharegap.net", soul.AgentID)

	// Build profile JSON
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

	// Sign with agent's key via Signet
	// For now, sign with Soul Factory's key (the agent bunker isn't set up yet for this event)
	if err := p.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign profile: %w", err)
	}

	// Publish to public relays
	return p.reactor.publish(ctx, event, p.reactor.config.Relays)
}

// publishProgress publishes a status update.
func (p *Provisioner) publishProgress(ctx context.Context, requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) {
	if err := p.reactor.PublishStatus(ctx, requestEvent, step, current, total, message); err != nil {
		p.reactor.logger.Warn("failed to publish progress", "step", step, "error", err)
	}
}

// recordStep records the result of a provisioning step.
func (p *Provisioner) recordStep(run *domain.ProvisioningRun, step domain.ProvisioningStep, status domain.StepStatus, output map[string]interface{}, err error, duration time.Duration) {
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

// --- Lifecycle Actions ---

// SuspendSoul temporarily disables an agent.
func (p *Provisioner) SuspendSoul(ctx context.Context, soulRef, reason string) error {
	return ErrLifecycleUnsupported

	logger := p.reactor.logger.With("soul", soulRef, "action", "suspend")
	logger.Info("suspending soul", "reason", reason)

	// 1. Load existing soul from relay
	soul, err := p.reactor.GetSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}

	// 2. Suspend agent in Signet (blocks signing)
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.SuspendAgent(ctx, soul.NostrPubkey); err != nil {
			logger.Warn("failed to suspend agent in Signet", "error", err)
			// Continue anyway - we still want to update the soul status
		}
	}

	// 3. Update soul status
	soul.Status = domain.SoulStatusSuspended
	now := time.Now()
	soul.SuspendedAt = &now

	// 4. Publish updated kind:31951 event
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish suspended soul: %w", err)
	}

	logger.Info("soul suspended")
	return nil
}

// ResumeSoul re-enables a suspended agent.
func (p *Provisioner) ResumeSoul(ctx context.Context, soulRef string) error {
	return ErrLifecycleUnsupported

	logger := p.reactor.logger.With("soul", soulRef, "action", "resume")
	logger.Info("resuming soul")

	// 1. Load existing soul from relay
	soul, err := p.reactor.GetSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}

	// Verify soul is suspended
	if soul.Status != domain.SoulStatusSuspended {
		return fmt.Errorf("soul is not suspended (current status: %s)", soul.Status)
	}

	// 2. Resume agent in Signet (allows signing)
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.ResumeAgent(ctx, soul.NostrPubkey); err != nil {
			return fmt.Errorf("resume agent in Signet: %w", err)
		}
	}

	// 3. Update soul status
	soul.Status = domain.SoulStatusActive
	soul.SuspendedAt = nil

	// 4. Publish updated kind:31951 event
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish resumed soul: %w", err)
	}

	logger.Info("soul resumed")
	return nil
}

// RevokeSoul permanently disables an agent.
func (p *Provisioner) RevokeSoul(ctx context.Context, soulRef, reason string) error {
	return ErrLifecycleUnsupported

	logger := p.reactor.logger.With("soul", soulRef, "action", "revoke")
	logger.Info("revoking soul", "reason", reason)

	// 1. Load existing soul from relay
	soul, err := p.reactor.GetSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}

	// 2. Revoke agent keypair in Signet (permanent)
	if soul.NostrPubkey != "" {
		if err := p.reactor.signer.RevokeAgent(ctx, soul.NostrPubkey); err != nil {
			logger.Warn("failed to revoke agent in Signet", "error", err)
			// Continue anyway - we still want to update the soul status
		}
	}

	// 3. Update soul status
	soul.Status = domain.SoulStatusRevoked
	now := time.Now()
	soul.RevokedAt = &now

	// 4. Publish updated kind:31951 event
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish revoked soul: %w", err)
	}

	logger.Info("soul revoked")
	return nil
}

// RegenerateSoul re-runs LLM generation with a new brief.
func (p *Provisioner) RegenerateSoul(ctx context.Context, soulRef, newBrief string) error {
	return ErrLifecycleUnsupported

	logger := p.reactor.logger.With("soul", soulRef, "action", "regenerate")
	logger.Info("regenerating soul")

	// 1. Load existing soul from relay
	soul, err := p.reactor.GetSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}

	// Verify soul is active or suspended (not revoked)
	if soul.Status == domain.SoulStatusRevoked {
		return fmt.Errorf("cannot regenerate revoked soul")
	}

	// 2. Run LLM generation with new brief
	input := domain.SoulGeneratorInput{
		AgentID: soul.AgentID,
		Name:    soul.Name,
		Brief:   newBrief,
		Tier:    soul.Tier,
	}

	output, err := p.reactor.generator.Generate(ctx, input)
	if err != nil {
		return fmt.Errorf("regenerate soul content: %w", err)
	}

	// 3. Update soul with new content
	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	soul.OriginalBrief = newBrief

	// 4. Publish updated kind:31951 event
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish regenerated soul: %w", err)
	}

	logger.Info("soul regenerated")
	return nil
}

// RedeploySoul triggers a bahia redeployment.
func (p *Provisioner) RedeploySoul(ctx context.Context, soulRef string) error {
	return ErrLifecycleUnsupported

	logger := p.reactor.logger.With("soul", soulRef, "action", "redeploy")
	logger.Info("redeploying soul")

	// 1. Load existing soul from relay
	soul, err := p.reactor.GetSoul(ctx, soulRef)
	if err != nil {
		return fmt.Errorf("load soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", soulRef)
	}

	// Verify soul has a bahia service ID
	if soul.BahiaServiceID == nil {
		return fmt.Errorf("soul has no bahia service ID")
	}

	// Verify soul is active
	if soul.Status != domain.SoulStatusActive {
		return fmt.Errorf("cannot redeploy soul with status: %s", soul.Status)
	}

	// 2. Update deploy status
	soul.DeployStatus = "redeploying"
	if err := p.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish redeploy status: %w", err)
	}

	// Note: Actual deployment intent creation would require access to the
	// bahia registry service. For now, we just update the soul's deploy status.
	// In a full implementation, this would:
	// - Call registry.CreateDeploymentIntent(serviceID, envID, artifactID, requestedBy)
	// - Or publish a deployment request event that bahia subscribes to

	logger.Info("soul redeploy initiated", "service_id", soul.BahiaServiceID)
	return nil
}
