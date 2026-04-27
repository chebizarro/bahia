<script>
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import TemplateSelector from '$lib/components/TemplateSelector.svelte';
  import ProvisioningProgress from '$lib/components/ProvisioningProgress.svelte';
  import { nostr, KINDS } from '$lib/nostr/client.js';
  import { trackProvisioningRun, provisioningRuns } from '$lib/stores/souls.js';
  
  // Form state
  let step = 1; // 1: template, 2: configure, 3: provisioning
  let selectedTemplate = null;
  let agentId = '';
  let agentName = '';
  let brief = '';
  let tier = 'standard';
  
  // Provisioning state
  let requestEventId = null;
  let currentRun = null;
  
  // Error state
  let error = null;
  let submitting = false;
  
  // Subscribe to run updates
  $: if (requestEventId && $provisioningRuns.has(requestEventId)) {
    currentRun = $provisioningRuns.get(requestEventId);
  }
  
  function handleTemplateSelect(e) {
    selectedTemplate = e.detail;
    if (selectedTemplate) {
      // Pre-fill tier from template
      tier = selectedTemplate.tier || 'standard';
    }
  }
  
  function nextStep() {
    if (step === 1) {
      step = 2;
    }
  }
  
  function prevStep() {
    if (step === 2) {
      step = 1;
    }
  }
  
  function generateAgentId() {
    // Generate a slug from name or random
    if (agentName) {
      agentId = agentName
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-|-$/g, '')
        .slice(0, 32);
    } else {
      agentId = 'agent-' + Math.random().toString(36).slice(2, 10);
    }
  }
  
  async function submitProvisioning() {
    error = null;
    submitting = true;
    
    try {
      // Validate
      if (!agentId) {
        generateAgentId();
      }
      if (!brief && !selectedTemplate) {
        throw new Error('Please provide a brief or select a template');
      }
      
      // Build the provisioning request event
      const tags = [
        ['agent-id', agentId],
        ['name', agentName || agentId],
        ['tier', tier],
        ['output', 'application/json']
      ];
      
      if (selectedTemplate) {
        // Reference: "31950:<pubkey>:<identifier>"
        tags.push(['template', `31950:${selectedTemplate.pubkey}:${selectedTemplate.identifier}`]);
      }
      
      const content = JSON.stringify({
        brief: brief || selectedTemplate?.basePrompt || ''
      });
      
      // Create unsigned event
      const event = {
        kind: KINDS.PROVISIONING_REQUEST,
        created_at: Math.floor(Date.now() / 1000),
        tags,
        content
      };
      
      // Note: In production, this would be signed by the user's keypair
      // For now, we'll just show what would be published
      console.log('[designer] Would publish:', event);
      
      // TODO: Sign with NIP-07 or bunker
      // For demo, simulate the request ID
      requestEventId = 'demo_' + Math.random().toString(36).slice(2);
      
      // Move to step 3
      step = 3;
      
      // Track the provisioning run
      trackProvisioningRun(requestEventId, {
        onProgress: (p) => console.log('[designer] Progress:', p),
        onComplete: (data) => console.log('[designer] Complete:', data),
        onError: (err) => console.log('[designer] Error:', err)
      });
      
      // Simulate progress for demo
      simulateProvisioning();
      
    } catch (err) {
      error = err.message;
    } finally {
      submitting = false;
    }
  }
  
  // Demo: simulate provisioning progress
  function simulateProvisioning() {
    const steps = ['generate', 'signet', 'avatar', 'profile', 'qdrant', 'memory', 'workspace', 'deploy'];
    let idx = 0;
    
    const interval = setInterval(() => {
      if (idx >= steps.length) {
        // Complete
        provisioningRuns.update(runs => {
          const r = runs.get(requestEventId);
          if (r) {
            r.status = 'completed';
            r.result = {
              success: true,
              data: {
                soul_id: agentId,
                npub: 'npub1' + Math.random().toString(36).slice(2, 22)
              }
            };
          }
          return new Map(runs);
        });
        clearInterval(interval);
        return;
      }
      
      provisioningRuns.update(runs => {
        const r = runs.get(requestEventId);
        if (r) {
          r.status = 'running';
          r.step = steps[idx];
          r.progress = { current: idx + 1, total: steps.length };
          r.message = `Running ${steps[idx]}...`;
        }
        return new Map(runs);
      });
      
      idx++;
    }, 1500);
  }
  
  function viewSoul() {
    goto(`/souls/${agentId}`);
  }
  
  onMount(async () => {
    await nostr.connect();
  });
</script>

<svelte:head>
  <title>Create Soul | Bahia</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <a href="/souls" class="back-link">← Back to Gallery</a>
    <h1>Create New Soul</h1>
    <p class="subtitle">Design and provision a new agent identity</p>
  </header>
  
  <!-- Progress indicator -->
  <div class="wizard-progress">
    <div class="progress-step" class:active={step >= 1} class:complete={step > 1}>
      <span class="step-num">1</span>
      <span class="step-label">Template</span>
    </div>
    <div class="progress-line" class:active={step > 1}></div>
    <div class="progress-step" class:active={step >= 2} class:complete={step > 2}>
      <span class="step-num">2</span>
      <span class="step-label">Configure</span>
    </div>
    <div class="progress-line" class:active={step > 2}></div>
    <div class="progress-step" class:active={step >= 3}>
      <span class="step-num">3</span>
      <span class="step-label">Provision</span>
    </div>
  </div>
  
  <!-- Error -->
  {#if error}
    <div class="error-banner">
      <span class="icon">⚠️</span>
      {error}
    </div>
  {/if}
  
  <!-- Step 1: Template Selection -->
  {#if step === 1}
    <div class="wizard-content">
      <TemplateSelector 
        selected={selectedTemplate} 
        on:select={handleTemplateSelect}
      />
      
      <div class="wizard-actions">
        <div></div>
        <button class="btn-primary" on:click={nextStep}>
          Continue →
        </button>
      </div>
    </div>
  {/if}
  
  <!-- Step 2: Configure -->
  {#if step === 2}
    <div class="wizard-content">
      <div class="config-form">
        <div class="form-section">
          <h3>Agent Identity</h3>
          
          <div class="form-group">
            <label for="agentName">Name</label>
            <input 
              id="agentName"
              type="text" 
              bind:value={agentName}
              placeholder="e.g., Scout, CodeBot, ResearchHelper"
              on:blur={generateAgentId}
            />
            <span class="hint">A friendly name for your agent</span>
          </div>
          
          <div class="form-group">
            <label for="agentId">Agent ID</label>
            <input 
              id="agentId"
              type="text" 
              bind:value={agentId}
              placeholder="auto-generated from name"
            />
            <span class="hint">Unique identifier (lowercase, no spaces)</span>
          </div>
        </div>
        
        <div class="form-section">
          <h3>Configuration</h3>
          
          <div class="form-group">
            <label for="tier">Resource Tier</label>
            <select id="tier" bind:value={tier}>
              <option value="lightweight">⚡ Lightweight - Fast, minimal resources</option>
              <option value="standard">🤖 Standard - Balanced capabilities</option>
              <option value="heavy">🦾 Heavy - Maximum resources</option>
            </select>
          </div>
          
          <div class="form-group">
            <label for="brief">
              Brief
              {#if selectedTemplate}
                <span class="badge">Extends template</span>
              {/if}
            </label>
            <textarea 
              id="brief"
              bind:value={brief}
              rows="6"
              placeholder={selectedTemplate 
                ? 'Add specific instructions or customizations...'
                : 'Describe what this agent should do, its personality, capabilities...'}
            ></textarea>
            <span class="hint">
              {#if selectedTemplate}
                Optional additions to the template's base prompt
              {:else}
                Required for custom souls - describe the agent's purpose and behavior
              {/if}
            </span>
          </div>
        </div>
        
        {#if selectedTemplate}
          <div class="template-preview">
            <h4>Selected Template: {selectedTemplate.name}</h4>
            <p class="template-desc">{selectedTemplate.description}</p>
            <details>
              <summary>View base prompt</summary>
              <pre>{selectedTemplate.basePrompt}</pre>
            </details>
          </div>
        {/if}
      </div>
      
      <div class="wizard-actions">
        <button class="btn-secondary" on:click={prevStep}>
          ← Back
        </button>
        <button 
          class="btn-primary" 
          on:click={submitProvisioning}
          disabled={submitting}
        >
          {#if submitting}
            <span class="spinner"></span>
            Submitting...
          {:else}
            Provision Soul ✨
          {/if}
        </button>
      </div>
    </div>
  {/if}
  
  <!-- Step 3: Provisioning -->
  {#if step === 3}
    <div class="wizard-content">
      <ProvisioningProgress 
        run={currentRun} 
        onComplete={viewSoul}
      />
    </div>
  {/if}
</div>

<style>
  .page {
    max-width: 800px;
    margin: 0 auto;
  }
  
  .page-header {
    margin-bottom: 2rem;
  }
  
  .back-link {
    font-size: 0.875rem;
    color: var(--text-muted);
    text-decoration: none;
    display: inline-block;
    margin-bottom: 0.5rem;
  }
  
  .back-link:hover {
    color: var(--primary);
  }
  
  .page-header h1 {
    font-size: 1.75rem;
    margin: 0 0 0.25rem 0;
  }
  
  .subtitle {
    color: var(--text-muted);
    margin: 0;
  }
  
  .wizard-progress {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    margin-bottom: 2rem;
    padding: 1.5rem;
    background: var(--card-bg);
    border-radius: 12px;
    border: 1px solid var(--border-color);
  }
  
  .progress-step {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-muted);
  }
  
  .progress-step.active {
    color: var(--primary);
  }
  
  .progress-step.complete {
    color: var(--success);
  }
  
  .step-num {
    width: 28px;
    height: 28px;
    border-radius: 50%;
    border: 2px solid currentColor;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.8rem;
    font-weight: 600;
  }
  
  .progress-step.active .step-num,
  .progress-step.complete .step-num {
    background: currentColor;
    color: white;
  }
  
  .step-label {
    font-size: 0.875rem;
    font-weight: 500;
  }
  
  .progress-line {
    width: 60px;
    height: 2px;
    background: var(--border-color);
  }
  
  .progress-line.active {
    background: var(--primary);
  }
  
  .error-banner {
    background: rgba(239, 68, 68, 0.1);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: #ef4444;
    padding: 1rem;
    border-radius: 8px;
    margin-bottom: 1.5rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  
  .wizard-content {
    background: var(--card-bg);
    border-radius: 12px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }
  
  .wizard-actions {
    display: flex;
    justify-content: space-between;
    margin-top: 2rem;
    padding-top: 1.5rem;
    border-top: 1px solid var(--border-color);
  }
  
  .btn-primary, .btn-secondary {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1.5rem;
    border-radius: 8px;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s;
    border: none;
  }
  
  .btn-primary {
    background: var(--primary);
    color: white;
  }
  
  .btn-primary:hover:not(:disabled) {
    background: #5558e3;
  }
  
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  
  .btn-secondary {
    background: transparent;
    color: var(--text-muted);
    border: 1px solid var(--border-color);
  }
  
  .btn-secondary:hover {
    color: var(--text-primary);
    border-color: var(--text-muted);
  }
  
  .config-form {
    display: flex;
    flex-direction: column;
    gap: 2rem;
  }
  
  .form-section h3 {
    font-size: 1rem;
    margin: 0 0 1rem 0;
    padding-bottom: 0.5rem;
    border-bottom: 1px solid var(--border-color);
  }
  
  .form-group {
    margin-bottom: 1.25rem;
  }
  
  .form-group label {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
    font-weight: 500;
    margin-bottom: 0.5rem;
    color: var(--text-primary);
  }
  
  .form-group .badge {
    font-size: 0.65rem;
    padding: 0.15rem 0.4rem;
    background: rgba(99, 102, 241, 0.15);
    color: var(--primary);
    border-radius: 4px;
    font-weight: normal;
  }
  
  .form-group input,
  .form-group select,
  .form-group textarea {
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 0.75rem 1rem;
    color: var(--text-primary);
    font-size: 0.9rem;
  }
  
  .form-group textarea {
    resize: vertical;
    min-height: 120px;
    font-family: inherit;
  }
  
  .form-group input:focus,
  .form-group select:focus,
  .form-group textarea:focus {
    outline: none;
    border-color: var(--primary);
  }
  
  .form-group .hint {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-top: 0.35rem;
  }
  
  .template-preview {
    background: rgba(99, 102, 241, 0.05);
    border: 1px solid rgba(99, 102, 241, 0.2);
    border-radius: 8px;
    padding: 1rem;
  }
  
  .template-preview h4 {
    margin: 0 0 0.25rem 0;
    font-size: 0.9rem;
    color: var(--primary);
  }
  
  .template-preview .template-desc {
    margin: 0 0 0.75rem 0;
    font-size: 0.85rem;
    color: var(--text-muted);
  }
  
  .template-preview details {
    font-size: 0.8rem;
  }
  
  .template-preview summary {
    cursor: pointer;
    color: var(--text-muted);
  }
  
  .template-preview pre {
    background: var(--bg);
    padding: 0.75rem;
    border-radius: 6px;
    margin-top: 0.5rem;
    font-size: 0.75rem;
    overflow-x: auto;
    white-space: pre-wrap;
    max-height: 200px;
    overflow-y: auto;
  }
  
  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid rgba(255,255,255,0.3);
    border-top-color: white;
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
