<script>
  import { goto } from '$app/navigation';
  import { onMount, onDestroy } from 'svelte';
  import { get } from 'svelte/store';
  import TemplateSelector from '$lib/components/TemplateSelector.svelte';
  import ProvisioningProgress from '$lib/components/ProvisioningProgress.svelte';
  import { nostr, KINDS } from '$lib/nostr/client.js';
  import { trackProvisioningRun, provisioningRuns } from '$lib/stores/souls.js';
  import { authState, initializeAuth, login, signWithAuth } from '$lib/stores/auth.js';
  
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
  let provisioningCleanup = null;
  
  // Publishing state
  let publishing = false;
  let publishError = null;
  let publishResults = [];
  
  // Error state
  let error = null;
  let submitting = false;
  
  // Subscribe to run updates
  $: if (requestEventId && $provisioningRuns.has(requestEventId)) {
    currentRun = $provisioningRuns.get(requestEventId);
  }
  
  // Derived auth status for UI
  $: isAuthenticated = $authState.status === 'authenticated';
  $: hasExtension = $authState.extensionAvailable;
  $: authError = $authState.error;
  $: userPubkey = $authState.pubkey;
  
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
  
  async function handleLogin() {
    try {
      error = null;
      await login();
    } catch (err) {
      error = `Login failed: ${err.message}`;
    }
  }
  
  async function submitProvisioning() {
    error = null;
    publishError = null;
    submitting = true;
    publishing = true;
    
    try {
      // Validate
      if (!agentId) {
        generateAgentId();
      }
      if (!brief && !selectedTemplate) {
        throw new Error('Please provide a brief or select a template');
      }
      
      // Ensure authenticated
      const auth = get(authState);
      if (!isAuthenticated) {
        // Try to login
        await login();
        
        // Re-check auth state
        const authAfterLogin = get(authState);
        if (authAfterLogin.status !== 'authenticated') {
          throw new Error('Authentication required to provision a soul');
        }
      }
      
      // Get current auth state
      const currentAuth = get(authState);
      
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
      const unsignedEvent = {
        kind: KINDS.PROVISIONING_REQUEST,
        created_at: Math.floor(Date.now() / 1000),
        pubkey: currentAuth.pubkey,
        tags,
        content
      };
      
      // Sign the event with NIP-07
      const signedEvent = await signWithAuth(unsignedEvent);
      
      // Set request ID from signed event
      requestEventId = signedEvent.id;
      
      // Start tracking before publish
      provisioningCleanup = trackProvisioningRun(requestEventId, {
        onProgress: (p) => console.log('[provisioning] Progress:', p),
        onComplete: (data) => console.log('[provisioning] Complete:', data),
        onError: (err) => console.error('[provisioning] Error:', err)
      });
      
      // Publish to relays
      publishResults = await nostr.publish(signedEvent);
      
      // Check if any relay accepted the event
      const successfulPublish = publishResults.some(result => result.sent === true);
      
      if (!successfulPublish) {
        // Clean up tracking if publish failed
        if (provisioningCleanup) {
          provisioningCleanup();
          provisioningCleanup = null;
        }
        throw new Error('No connected relays available for publishing');
      }
      
      // Move to step 3
      step = 3;
      
    } catch (err) {
      publishError = err.message;
      error = err.message;
      
      // Clean up tracking on error
      if (provisioningCleanup) {
        provisioningCleanup();
        provisioningCleanup = null;
      }
    } finally {
      submitting = false;
      publishing = false;
    }
  }
  
  function viewSoul() {
    goto(`/souls/${agentId}`);
  }
  
  onMount(async () => {
    // Initialize auth system
    await initializeAuth();
    
    // Connect to Nostr relays
    const auth = get(authState);
    const writeRelays = auth.relays
      ? Object.entries(auth.relays)
          .filter(([_, perms]) => perms.write !== false)
          .map(([url]) => url)
      : [];
    
    if (writeRelays.length > 0) {
      await nostr.connect(writeRelays);
    } else {
      await nostr.connect();
    }
  });
  
  onDestroy(() => {
    // Clean up provisioning tracking
    if (provisioningCleanup) {
      provisioningCleanup();
      provisioningCleanup = null;
    }
    // Note: Do not disconnect nostr client as it's shared globally
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
  
  <!-- Auth Status Banner (Step 2) -->
  {#if step === 2}
    <div class="auth-status" class:authenticated={isAuthenticated} class:error={authError}>
      {#if authError}
        <span class="icon">⚠️</span>
        <div class="status-content">
          <strong>Extension Error</strong>
          <p>{authError}</p>
        </div>
      {:else if !hasExtension}
        <span class="icon">🔌</span>
        <div class="status-content">
          <strong>NIP-07 Extension Required</strong>
          <p>Please install a Nostr browser extension (Alby, nos2x, etc.) to sign events</p>
        </div>
      {:else if isAuthenticated && userPubkey}
        <span class="icon">✓</span>
        <div class="status-content">
          <strong>Authenticated</strong>
          <p>Pubkey: {userPubkey.slice(0, 8)}...{userPubkey.slice(-8)}</p>
        </div>
      {:else if $authState.status === 'authenticating'}
        <span class="icon">⏳</span>
        <div class="status-content">
          <strong>Requesting Permission</strong>
          <p>Check your extension for approval prompt</p>
        </div>
      {:else}
        <span class="icon">🔑</span>
        <div class="status-content">
          <strong>NIP-07 Login Required</strong>
          <p>You'll be prompted to authorize signing when you provision the soul</p>
        </div>
        {#if hasExtension && !isAuthenticated}
          <button class="btn-login" on:click={handleLogin}>
            Login Now
          </button>
        {/if}
      {/if}
    </div>
  {/if}
  
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
          disabled={submitting || (!hasExtension && !isAuthenticated)}
        >
          {#if publishing}
            <span class="spinner"></span>
            Signing & Publishing...
          {:else if submitting}
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
      <div class="publish-success">
        <span class="icon">✓</span>
        <h3>Event Signed & Published</h3>
        <div class="event-details">
          <div class="detail-row">
            <span class="label">Request ID:</span>
            <code>{requestEventId}</code>
          </div>
          <div class="detail-row">
            <span class="label">Published to:</span>
            <span>{publishResults.filter(r => r.sent).length} relay(s)</span>
          </div>
        </div>
      </div>
      
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
  
  .auth-status {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 1rem 1.25rem;
    background: rgba(99, 102, 241, 0.1);
    border: 1px solid rgba(99, 102, 241, 0.3);
    border-radius: 8px;
    margin-bottom: 1.5rem;
  }
  
  .auth-status.authenticated {
    background: rgba(34, 197, 94, 0.1);
    border-color: rgba(34, 197, 94, 0.3);
    color: #22c55e;
  }
  
  .auth-status.error {
    background: rgba(239, 68, 68, 0.1);
    border-color: rgba(239, 68, 68, 0.3);
    color: #ef4444;
  }
  
  .auth-status .icon {
    font-size: 1.5rem;
    flex-shrink: 0;
  }
  
  .auth-status .status-content {
    flex: 1;
  }
  
  .auth-status .status-content strong {
    display: block;
    font-size: 0.9rem;
    margin-bottom: 0.25rem;
  }
  
  .auth-status .status-content p {
    margin: 0;
    font-size: 0.8rem;
    opacity: 0.9;
  }
  
  .auth-status .btn-login {
    background: var(--primary);
    color: white;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    font-size: 0.85rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s;
  }
  
  .auth-status .btn-login:hover {
    background: #5558e3;
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
  
  .publish-success {
    text-align: center;
    padding: 2rem;
    background: rgba(34, 197, 94, 0.05);
    border: 1px solid rgba(34, 197, 94, 0.2);
    border-radius: 8px;
    margin-bottom: 2rem;
  }
  
  .publish-success .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 48px;
    height: 48px;
    background: #22c55e;
    color: white;
    border-radius: 50%;
    font-size: 1.5rem;
    margin-bottom: 1rem;
  }
  
  .publish-success h3 {
    font-size: 1.25rem;
    margin: 0 0 1rem 0;
    color: var(--text-primary);
  }
  
  .event-details {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-width: 500px;
    margin: 0 auto;
  }
  
  .detail-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 1rem;
    background: var(--card-bg);
    border-radius: 6px;
    font-size: 0.85rem;
  }
  
  .detail-row .label {
    color: var(--text-muted);
    font-weight: 500;
  }
  
  .detail-row code {
    background: var(--bg);
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-family: monospace;
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
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
