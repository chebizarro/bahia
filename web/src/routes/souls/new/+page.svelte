<script>
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import TemplateSelector from '$lib/components/TemplateSelector.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import ProvisioningProgress from '$lib/components/ProvisioningProgress.svelte';
  import { KINDS, SOUL_RUNTIME_METHODS } from '$lib/nostr/client.js';
  import {
    loadRuntimeCapabilities,
    publishProvisioningRequest,
    publishSoulDraft,
    provisioningRuns,
    runtimeCapabilities,
    trackProvisioningRun
  } from '$lib/stores/souls.js';
  import { authState, initializeAuth, login, refreshExtensionStatus } from '$lib/stores/auth.js';
  import {
    capabilityLabel,
    capabilityRef,
    compatibleCapabilities,
    parseKindList,
    parseToolGrantList,
    slugifyAgentId,
    splitList
  } from '../page-model.js';
  import {
    ConfiguredIcon,
    LoginIcon,
    PendingIcon,
    SeedIcon,
    SoulIcon,
    SuccessIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let step = $state(1);
  let selectedTemplate = $state(null);
  let selectedRepository = $state(null);
  let selectedCapabilityRef = $state('');

  let agentId = $state('');
  let agentName = $state('');
  let purpose = $state('');
  let tier = $state('standard');
  let nip05 = $state('');
  let allowedKinds = $state('1, 3, 31951, 31952, 5950, 6950, 7950, 1950');
  let toolGrants = $state('');
  let approvalPolicy = $state('operator');
  let readRelays = $state('');
  let writeRelays = $state('');
  let controlRelays = $state('');
  let nip65Discovery = $state(true);
  let branch = $state('main');
  let environment = $state('production');
  let avatarRef = $state('');
  let voiceRef = $state('');

  let requestEventId = $state(null);
  let currentRun = $state(null);
  let provisioningCleanup = null;
  let nostrInitialized = false;
  let submitting = $state(false);
  let publishing = $state(false);
  let error = $state(null);
  let publishResults = $state([]);
  let draftEventId = $state('');
  let draftSpecHash = $state('');

  let isAuthenticated = $derived(authState.status === 'authenticated');
  let hasExtension = $derived(authState.extensionAvailable);
  let authError = $derived(authState.error);
  let userPubkey = $derived(authState.pubkey);
  let runtimeChoices = $derived(compatibleCapabilities(runtimeCapabilities, SOUL_RUNTIME_METHODS.PROVISION));
  let selectedCapability = $derived(runtimeChoices.find((capability) => capabilityRef(capability) === selectedCapabilityRef) || null);

  $effect(() => {
    if (runtimeChoices.length > 0 && (!selectedCapabilityRef || !selectedCapability)) {
      selectedCapabilityRef = capabilityRef(runtimeChoices[0]);
    }
  });

  $effect(() => {
    if (requestEventId && provisioningRuns.has(requestEventId)) {
      currentRun = provisioningRuns.get(requestEventId);
    }
  });

  function handleTemplateSelect(template) {
    selectedTemplate = template;
    if (template?.tier) tier = template.tier;
  }

  function generateAgentId() {
    agentId = slugifyAgentId(agentName) || `agent-${Math.random().toString(36).slice(2, 10)}`;
  }

  function nextStep() {
    if (step === 1 && !agentId) generateAgentId();
    step = Math.min(step + 1, 4);
  }

  function prevStep() {
    step = Math.max(step - 1, 1);
  }

  async function handleLogin() {
    try {
      error = null;
      await login();
    } catch (err) {
      error = `Login failed: ${err.message}`;
    }
  }

  function templateRef() {
    return selectedTemplate ? `${KINDS.SOUL_TEMPLATE}:${selectedTemplate.pubkey}:${selectedTemplate.identifier}` : '';
  }

  function repositoryRef() {
    if (!selectedRepository) return '';
    return selectedRepository.repoCoordinate || selectedRepository.primaryUrl || selectedRepository.cloneUrl || selectedRepository.repoUrl || selectedRepository.webUrl || '';
  }

  function buildDraftContent() {
    const baseBrief = purpose || selectedTemplate?.basePrompt || '';
    return {
      schema: 'soulfactory-draft/v1',
      brief: baseBrief,
      template_ref: templateRef(),
      identity: {
        name: agentName || agentId,
        purpose: baseBrief,
        tier,
        nip05
      },
      runtime: {
        target: selectedCapability?.runtime || '',
        runtime_pubkey: selectedCapability?.pubkey || '',
        capability_ref: selectedCapability ? capabilityRef(selectedCapability) : ''
      },
      permissions: {
        allowed_kinds: parseKindList(allowedKinds),
        tool_grants: parseToolGrantList(toolGrants),
        approval_policy: approvalPolicy
      },
      relay_policy: {
        read: splitList(readRelays),
        write: splitList(writeRelays),
        control: splitList(controlRelays),
        nip65_discovery: nip65Discovery
      },
      workspace: {
        repo: repositoryRef(),
        branch,
        environment
      },
      assets: {
        avatar_ref: avatarRef,
        voice_ref: voiceRef
      }
    };
  }

  async function submitProvisioning() {
    error = null;
    submitting = true;
    publishing = true;

    try {
      if (!agentId) generateAgentId();
      if (!agentName && !agentId) throw new Error('Name or agent id is required');
      if (!purpose && !selectedTemplate) throw new Error('Purpose or template is required');
      if (!selectedCapability) throw new Error('Select a discovered compatible runtime capability before provisioning');

      if (!isAuthenticated) {
        await login();
        if (authState.status !== 'authenticated') throw new Error('Authentication required to provision a soul');
      }

      const draftContent = buildDraftContent();
      const draft = await publishSoulDraft({
        agentId,
        content: draftContent,
        templateRef: templateRef()
      });
      draftEventId = draft.event.id;
      draftSpecHash = draft.specHash;

      const draftCoordinate = `${KINDS.SOUL_DRAFT}:${draft.event.pubkey}:${agentId}`;
      const request = await publishProvisioningRequest({
        agentId,
        name: agentName || agentId,
        tier,
        brief: draftContent.brief,
        draftRef: draftCoordinate,
        draftEvent: draft.event,
        draftContent,
        templateRef: templateRef(),
        specHash: draft.specHash,
        beforePublish: (event) => {
          requestEventId = event.id;
          provisioningCleanup = trackProvisioningRun(event.id, {
            onError: (message) => console.error('[souls/new] provisioning failed:', message)
          });
        }
      });

      publishResults = request.publishResults;
      step = 4;
    } catch (err) {
      error = err.message || 'Failed to publish provisioning request';
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

  $effect(() => {
    if (nostrInitialized) return;
    nostrInitialized = true;

    async function initializeNostr() {
      if (authState.status === 'unknown') {
        await initializeAuth();
      } else if (!authState.extensionAvailable) {
        await refreshExtensionStatus();
      }
      await loadRuntimeCapabilities({ method: SOUL_RUNTIME_METHODS.PROVISION });
    }

    void untrack(() => initializeNostr());

    return () => {
      if (provisioningCleanup) {
        provisioningCleanup();
        provisioningCleanup = null;
      }
    };
  });
</script>

<svelte:head>
  <title>Create Soul | Bahia</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <a href="/souls" class="back-link">← Back to Gallery</a>
    <h1><SoulIcon size={28} stroke={1.75} aria-hidden="true" /> Create Runtime-Aware Soul</h1>
    <p class="subtitle">Save a signed 31952 draft, then publish a 5950 provisioning request that references it.</p>
  </header>

  <div class="wizard-progress">
    {#each ['Identity', 'Runtime', 'Scope', 'Provision'] as label, index}
      <div class="progress-step" class:active={step >= index + 1} class:complete={step > index + 1}>
        <span class="step-num">{index + 1}</span>
        <span class="step-label">{label}</span>
      </div>
      {#if index < 3}<div class="progress-line" class:active={step > index + 1}></div>{/if}
    {/each}
  </div>

  {#if step < 4}
    <div class="auth-status" class:authenticated={isAuthenticated} class:error={authError}>
      {#if authError}
        <WarningIcon size={22} stroke={1.75} aria-hidden="true" />
        <div><strong>Signer error</strong><p>{authError}</p></div>
      {:else if !hasExtension}
        <ConfiguredIcon size={22} stroke={1.75} aria-hidden="true" />
        <div><strong>Nostr signer required</strong><p>Use NIP-07 or Nostr Connect to sign the draft and provisioning events.</p></div>
      {:else if isAuthenticated && userPubkey}
        <SuccessIcon size={22} stroke={1.75} aria-hidden="true" />
        <div><strong>Authenticated</strong><p>{userPubkey.slice(0, 8)}…{userPubkey.slice(-8)}</p></div>
      {:else if authState.status === 'authenticating'}
        <PendingIcon size={22} stroke={1.75} aria-hidden="true" />
        <div><strong>Requesting permission</strong><p>Approve the signer prompt.</p></div>
      {:else}
        <LoginIcon size={22} stroke={1.75} aria-hidden="true" />
        <div><strong>Login required</strong><p>You will sign a draft and request before provisioning.</p></div>
        <button class="btn-secondary" onclick={handleLogin}>Login</button>
      {/if}
    </div>
  {/if}

  {#if error}
    <div class="error-banner"><WarningIcon size={18} stroke={1.75} aria-hidden="true" /> {error}</div>
  {/if}

  {#if step === 1}
    <section class="wizard-content">
      <div class="form-section">
        <h3>Identity draft</h3>
        <label>Name<input bind:value={agentName} onblur={generateAgentId} placeholder="Scout" /></label>
        <label>Agent ID<input bind:value={agentId} placeholder="scout" /></label>
        <label>Purpose<textarea rows="5" bind:value={purpose} placeholder="What should this soul do?"></textarea></label>
        <div class="two-col">
          <label>Tier<select bind:value={tier}><option value="lightweight">Lightweight</option><option value="standard">Standard</option><option value="heavy">Heavy</option></select></label>
          <label>NIP-05 target<input bind:value={nip05} placeholder="optional" /></label>
        </div>
      </div>
      <TemplateSelector selected={selectedTemplate} onSelect={handleTemplateSelect} />
      <div class="wizard-actions"><span></span><button class="btn-primary" onclick={nextStep}>Continue →</button></div>
    </section>
  {:else if step === 2}
    <section class="wizard-content">
      <div class="form-section">
        <h3>Runtime capability</h3>
        {#if runtimeChoices.length === 0}
          <div class="warning-box">No compatible 30317 runtime capability has been discovered for {SOUL_RUNTIME_METHODS.PROVISION}. Provisioning is disabled until OpenClaw or Metiq advertises support.</div>
        {:else}
          <label>Runtime target<select bind:value={selectedCapabilityRef}>{#each runtimeChoices as capability}<option value={capabilityRef(capability)}>{capabilityLabel(capability)}</option>{/each}</select></label>
          <div class="runtime-summary">
            <strong>{selectedCapability?.runtime}</strong>
            <span>runtime pubkey: {selectedCapability?.pubkey?.slice(0, 12)}…</span>
            <span>capability: {capabilityRef(selectedCapability)}</span>
          </div>
        {/if}
      </div>
      <div class="wizard-actions"><button class="btn-secondary" onclick={prevStep}>← Back</button><button class="btn-primary" onclick={nextStep} disabled={runtimeChoices.length === 0}>Continue →</button></div>
    </section>
  {:else if step === 3}
    <section class="wizard-content">
      <div class="form-section">
        <h3>Permissions</h3>
        <label>Allowed Nostr kinds<input bind:value={allowedKinds} placeholder="1, 3, 31951" /></label>
        <label>Tool grants<textarea rows="3" bind:value={toolGrants} placeholder="mcp-server: read, write"></textarea></label>
        <label>Approval policy<select bind:value={approvalPolicy}><option value="operator">Operator approval</option><option value="auto-safe">Auto-approve safe tools</option><option value="manual">Manual only</option></select></label>
      </div>
      <div class="form-section">
        <h3>Relays</h3>
        <label><input type="checkbox" bind:checked={nip65Discovery} /> Use NIP-65 relay discovery when available</label>
        <label>Read relays<textarea rows="2" bind:value={readRelays} placeholder="wss://relay.example"></textarea></label>
        <label>Write relays<textarea rows="2" bind:value={writeRelays}></textarea></label>
        <label>Control relays<textarea rows="2" bind:value={controlRelays}></textarea></label>
      </div>
      <div class="form-section">
        <h3>Repository and assets</h3>
        <RepositoryPicker bind:value={selectedRepository} context="soul" requirePrimaryUrl={false} />
        <div class="two-col">
          <label>Branch<input bind:value={branch} /></label>
          <label>Environment<input bind:value={environment} /></label>
        </div>
        <label>Avatar ref<input bind:value={avatarRef} placeholder="blob:, blossom:, https://…" /></label>
        <label>Voice ref<input bind:value={voiceRef} placeholder="optional existing voice asset ref" /></label>
      </div>
      <div class="wizard-actions">
        <button class="btn-secondary" onclick={prevStep}>← Back</button>
        <button class="btn-primary" onclick={submitProvisioning} disabled={submitting || runtimeChoices.length === 0 || (!hasExtension && !isAuthenticated)}>
          {#if publishing}<span class="spinner"></span>Signing & publishing…{:else}<SeedIcon size={18} stroke={1.75} aria-hidden="true" />Save draft & provision{/if}
        </button>
      </div>
    </section>
  {:else if step === 4}
    <section class="wizard-content">
      <div class="publish-success">
        <SuccessIcon size={28} stroke={2} aria-hidden="true" />
        <h3>Draft and request published</h3>
        <p>Terminal status will come only from explicit 7950 result events.</p>
        <dl>
          <dt>31952 draft</dt><dd><code>{draftEventId}</code></dd>
          <dt>spec hash</dt><dd><code>{draftSpecHash}</code></dd>
          <dt>5950 request</dt><dd><code>{requestEventId}</code></dd>
          <dt>accepted relays</dt><dd>{publishResults.filter((result) => result.accepted).length}</dd>
        </dl>
      </div>
      <ProvisioningProgress run={currentRun} onComplete={viewSoul} />
    </section>
  {/if}
</div>

<style>
  .page { max-width: 920px; margin: 0 auto; }
  .page-header { margin-bottom: 1.5rem; }
  .back-link { color: var(--text-muted); text-decoration: none; font-size: 0.875rem; }
  h1 { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.25rem; }
  .subtitle, p { color: var(--text-muted); }
  .wizard-progress, .auth-status, .wizard-content, .publish-success { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 12px; }
  .wizard-progress { display: flex; align-items: center; justify-content: center; gap: 0.5rem; padding: 1rem; margin-bottom: 1rem; }
  .progress-step { display: flex; align-items: center; gap: 0.4rem; color: var(--text-muted); }
  .progress-step.active { color: var(--primary); }
  .progress-step.complete { color: var(--success); }
  .step-num { width: 26px; height: 26px; border: 2px solid currentColor; border-radius: 999px; display: grid; place-items: center; font-size: 0.8rem; font-weight: 700; }
  .progress-step.active .step-num, .progress-step.complete .step-num { background: currentColor; color: white; }
  .progress-line { width: 48px; height: 2px; background: var(--border-color); }
  .progress-line.active { background: var(--primary); }
  .auth-status { display: flex; align-items: center; gap: 0.75rem; padding: 0.85rem 1rem; margin-bottom: 1rem; }
  .auth-status.authenticated { border-color: rgba(34, 197, 94, 0.4); }
  .auth-status.error, .error-banner { border-color: rgba(239, 68, 68, 0.4); color: var(--error); }
  .auth-status p { margin: 0.15rem 0 0; font-size: 0.8rem; }
  .wizard-content { padding: 1.25rem; }
  .form-section { display: grid; gap: 0.9rem; margin-bottom: 1.25rem; }
  .form-section h3 { margin: 0; padding-bottom: 0.5rem; border-bottom: 1px solid var(--border-color); }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; color: var(--text-primary); }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.85rem; }
  .wizard-actions { display: flex; justify-content: space-between; gap: 0.75rem; padding-top: 1rem; border-top: 1px solid var(--border-color); }
  .btn-primary, .btn-secondary { display: inline-flex; align-items: center; gap: 0.45rem; border-radius: 8px; padding: 0.65rem 1rem; border: 1px solid transparent; cursor: pointer; font-weight: 600; text-decoration: none; }
  .btn-primary { background: var(--primary); color: white; }
  .btn-secondary { background: transparent; color: var(--text-muted); border-color: var(--border-color); }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  .error-banner, .warning-box { padding: 0.85rem 1rem; border-radius: 8px; margin-bottom: 1rem; background: rgba(239, 68, 68, 0.08); border: 1px solid rgba(239, 68, 68, 0.25); }
  .warning-box { color: var(--warning); background: rgba(245, 158, 11, 0.08); border-color: rgba(245, 158, 11, 0.3); }
  .runtime-summary { display: grid; gap: 0.35rem; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.75rem; font-size: 0.82rem; color: var(--text-muted); }
  .publish-success { text-align: center; padding: 1.25rem; margin-bottom: 1rem; }
  .publish-success dl { display: grid; grid-template-columns: 120px 1fr; gap: 0.4rem 0.75rem; text-align: left; }
  .publish-success dt { color: var(--text-muted); }
  code { background: var(--bg); border-radius: 4px; padding: 0.15rem 0.35rem; overflow-wrap: anywhere; }
  .spinner { width: 16px; height: 16px; border: 2px solid rgba(255,255,255,0.35); border-top-color: white; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (max-width: 720px) { .wizard-progress { align-items: flex-start; flex-direction: column; } .progress-line { display: none; } .two-col { grid-template-columns: 1fr; } }
</style>
