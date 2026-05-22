<script>
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import AvatarStudio from '$lib/components/souls/AvatarStudio.svelte';
  import MemoryConfig from '$lib/components/MemoryConfig.svelte';
  import PersonalityBuilder from '$lib/components/souls/PersonalityBuilder.svelte';
  import VoiceStudio from '$lib/components/souls/VoiceStudio.svelte';
  import TemplateSelector from '$lib/components/TemplateSelector.svelte';
  import RepositoryPicker from '$lib/components/repositories/RepositoryPicker.svelte';
  import ProvisioningProgress from '$lib/components/ProvisioningProgress.svelte';
  import { KINDS, SOUL_RUNTIME_METHODS } from '$lib/nostr/client.js';
  import {
    createDefaultAvatarSpec,
    createDefaultMemorySpec,
    createDefaultPersonaSpec,
    createDefaultVoiceSpec,
    loadRuntimeCapabilities,
    loadSouls,
    publishProvisioningRequest,
    publishSoulDraft,
    provisioningRuns,
    runtimeCapabilities,
    souls,
    trackProvisioningRun
  } from '$lib/stores/souls.js';
  import { customizationPresets } from '$lib/data/customization-presets';
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

  const customizationTabs = [
    { id: 'identity', label: 'Identity', description: 'Name, brief, and template' },
    { id: 'avatar', label: 'Avatar', description: 'Portrait prompt and refs' },
    { id: 'voice', label: 'Voice', description: 'TTS persona draft' },
    { id: 'memory', label: 'Memory', description: 'Retrieval settings' },
    { id: 'personality', label: 'Personality', description: 'Traits and prompt sections' },
    { id: 'runtime', label: 'Runtime', description: 'Capability, scope, relays' }
  ];


  let step = $state(1);
  let activeTab = $state('identity');
  let disclosureMode = $state('basic');
  let selectedTemplate = $state(null);
  let selectedRepository = $state(null);
  let selectedCapabilityRef = $state('');
  let selectedPresetId = $state('');
  let cloneSoulId = $state('');

  let agentId = $state('');
  let agentName = $state('');
  let purpose = $state('');
  let tier = $state('standard');
  let nip05 = $state('');
  let identityTheme = $state('warm');
  let identityEmoji = $state('✨');
  let allowedKinds = $state('1, 3, 31951, 31952, 5950, 6950, 7950, 1950');
  let toolGrants = $state('');
  let approvalPolicy = $state('operator');
  let readRelays = $state('');
  let writeRelays = $state('');
  let controlRelays = $state('');
  let nip65Discovery = $state(true);
  let branch = $state('main');
  let environment = $state('production');
  let voiceRef = $state('');
  let avatarSpec = $state(createDefaultAvatarSpec());
  let voiceSpec = $state(createDefaultVoiceSpec());
  let memorySpec = $state(createDefaultMemorySpec());
  let personaSpec = $state(createDefaultPersonaSpec());

  let requestEventId = $state(null);
  let currentRun = $state(null);
  let provisioningCleanup = null;
  let nostrInitialized = false;
  let submitting = $state(false);
  let publishing = $state(false);
  let savingDraft = $state(false);
  let error = $state(null);
  let publishResults = $state([]);
  let draftPublishResults = $state([]);
  let draftEventId = $state('');
  let draftSpecHash = $state('');
  let draftSaveStatus = $state('');
  let lastDraftSavedAt = $state('');

  let isAuthenticated = $derived(authState.status === 'authenticated');
  let hasExtension = $derived(authState.extensionAvailable);
  let authError = $derived(authState.error);
  let userPubkey = $derived(authState.pubkey);
  let runtimeChoices = $derived(compatibleCapabilities(runtimeCapabilities, SOUL_RUNTIME_METHODS.PROVISION));
  let selectedCapability = $derived(runtimeChoices.find((capability) => capabilityRef(capability) === selectedCapabilityRef) || null);
  let showAdvanced = $derived(disclosureMode === 'advanced');
  let activeTabIndex = $derived(customizationTabs.findIndex((tab) => tab.id === activeTab));
  let previewDraftContent = $derived(buildDraftContent());

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

  function clone(value) {
    return JSON.parse(JSON.stringify(value || {}));
  }

  function mergeSpec(base, updates) {
    if (!updates) return clone(base);
    const next = clone(base);
    for (const [key, value] of Object.entries(updates)) {
      next[key] = value && typeof value === 'object' && !Array.isArray(value)
        ? mergeSpec(next[key] || {}, value)
        : value;
    }
    return next;
  }

  function applyCustomizationContent(content = {}) {
    if (content.identity) {
      if (content.identity.tier) tier = content.identity.tier;
      if (content.identity.theme) identityTheme = content.identity.theme;
      if (content.identity.emoji) identityEmoji = content.identity.emoji;
    }
    if (content.brief && !purpose) purpose = content.brief;
    if (content.persona) personaSpec = createDefaultPersonaSpec(mergeSpec(personaSpec, content.persona));
    if (content.avatar) avatarSpec = createDefaultAvatarSpec(mergeSpec(avatarSpec, content.avatar));
    if (content.voice) voiceSpec = createDefaultVoiceSpec(mergeSpec(voiceSpec, content.voice));
    if (content.memory) memorySpec = createDefaultMemorySpec(mergeSpec(memorySpec, content.memory));
  }

  function handleTemplateSelect(template) {
    selectedTemplate = template;
    if (template?.tier) tier = template.tier;
    if (template?.basePrompt && !purpose) purpose = template.basePrompt;
    if (template?.defaultCustomization) applyCustomizationContent(template.defaultCustomization);
  }

  function applyPreset(presetId) {
    selectedPresetId = presetId;
    const preset = customizationPresets.find((item) => item.id === presetId);
    if (preset) applyCustomizationContent(preset.content);
  }

  function parseSoulContent(sourceSoul) {
    try {
      return sourceSoul?.content ? JSON.parse(sourceSoul.content) : {};
    } catch {
      return {};
    }
  }

  function cloneFromSoul(sourceAgentId) {
    cloneSoulId = sourceAgentId;
    const sourceSoul = souls.find((item) => item.agentId === sourceAgentId);
    if (!sourceSoul) return;
    applyCustomizationContent(parseSoulContent(sourceSoul));
  }

  function generateAgentId() {
    if (!agentName) return;
    agentId = slugifyAgentId(agentName) || agentId;
  }

  function templateRef() {
    return selectedTemplate ? `${KINDS.SOUL_TEMPLATE}:${selectedTemplate.pubkey}:${selectedTemplate.identifier}` : '';
  }

  function repositoryRef() {
    if (!selectedRepository) return '';
    return selectedRepository.repoCoordinate || selectedRepository.primaryUrl || selectedRepository.cloneUrl || selectedRepository.repoUrl || selectedRepository.webUrl || '';
  }

  function resolvedAvatarRef() {
    const current = avatarSpec?.current || 'generated';
    if (current === 'uploaded') return avatarSpec?.uploaded_ref || avatarSpec?.generated_ref || '';
    return avatarSpec?.generated_ref || avatarSpec?.uploaded_ref || '';
  }

  function buildDraftContent() {
    const baseBrief = purpose || selectedTemplate?.basePrompt || '';
    return {
      schema: 'soulfactory-draft/v2',
      brief: baseBrief,
      template_ref: templateRef(),
      identity: {
        name: agentName || agentId,
        purpose: baseBrief,
        tier,
        nip05,
        theme: identityTheme,
        emoji: identityEmoji
      },
      persona: personaSpec,
      avatar: avatarSpec,
      voice: voiceSpec,
      memory: memorySpec,
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
        avatar_ref: resolvedAvatarRef(),
        voice_ref: voiceRef
      }
    };
  }

  function validateDraftReady() {
    if (!agentId && agentName) generateAgentId();
    if (!agentId) throw new Error('Enter a stable name or agent id before saving a signed draft checkpoint');
  }

  function validateProvisioningReady() {
    validateDraftReady();
    if (!agentName && !agentId) throw new Error('Name or agent id is required');
    if (!purpose && !selectedTemplate) throw new Error('Purpose or template is required');
    if (!selectedCapability) throw new Error('Select a discovered compatible runtime capability before provisioning');
  }

  async function saveDraft(panelLabel = 'current panel') {
    error = null;
    savingDraft = true;

    try {
      validateDraftReady();
      if (!isAuthenticated) {
        await login();
        if (authState.status !== 'authenticated') throw new Error('Authentication required to save a soul draft');
      }

      const draftContent = buildDraftContent();
      const draft = await publishSoulDraft({
        agentId,
        content: draftContent,
        templateRef: templateRef(),
        previousSpecHash: draftSpecHash
      });

      draftEventId = draft.event.id;
      draftSpecHash = draft.specHash;
      draftPublishResults = draft.publishResults;
      lastDraftSavedAt = new Date().toLocaleTimeString();
      draftSaveStatus = `Saved ${panelLabel} to signed 31952 draft`;
      return { ...draft, content: draftContent };
    } catch (err) {
      error = err.message || 'Failed to save soul draft';
      throw err;
    } finally {
      savingDraft = false;
    }
  }

  async function goToTab(tabId) {
    if (tabId === activeTab || savingDraft || submitting) return;
    const current = customizationTabs.find((tab) => tab.id === activeTab);
    try {
      await saveDraft(current?.label || 'current panel');
      activeTab = tabId;
    } catch {
      // saveDraft already exposes the error; keep the user on the current panel.
    }
  }

  async function nextPanel() {
    if (activeTabIndex < customizationTabs.length - 1) {
      const next = customizationTabs[activeTabIndex + 1];
      await goToTab(next.id);
      return;
    }
    await enterPreview();
  }

  async function prevPanel() {
    if (step === 2) {
      step = 1;
      activeTab = 'runtime';
      return;
    }
    if (activeTabIndex > 0) {
      const previous = customizationTabs[activeTabIndex - 1];
      await goToTab(previous.id);
    }
  }

  async function enterPreview() {
    try {
      validateProvisioningReady();
      await saveDraft('Runtime panel');
      step = 2;
    } catch {
      // error is shown above the wizard.
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
    submitting = true;
    publishing = true;

    try {
      validateProvisioningReady();

      if (!isAuthenticated) {
        await login();
        if (authState.status !== 'authenticated') throw new Error('Authentication required to provision a soul');
      }

      const draft = await saveDraft('Preview');
      const draftContent = draft.content;
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
      step = 3;
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
      await Promise.all([
        loadRuntimeCapabilities({ method: SOUL_RUNTIME_METHODS.PROVISION }),
        loadSouls()
      ]);
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
    <h1><SoulIcon size={28} strokeWidth={1.75} aria-hidden="true" /> Create Runtime-Aware Soul</h1>
    <p class="subtitle">Customize a v2 31952 draft, preview the full spec, then publish a 5950 provisioning request that references it.</p>
  </header>

  <div class="wizard-progress">
    {#each ['Customize', 'Preview', 'Provision'] as label, index}
      <div class="progress-step" class:active={step >= index + 1} class:complete={step > index + 1}>
        <span class="step-num">{index + 1}</span>
        <span class="step-label">{label}</span>
      </div>
      {#if index < 2}<div class="progress-line" class:active={step > index + 1}></div>{/if}
    {/each}
  </div>

  {#if step < 3}
    <div class="auth-status" class:authenticated={isAuthenticated} class:error={authError}>
      {#if authError}
        <WarningIcon size={22} strokeWidth={1.75} aria-hidden="true" />
        <div><strong>Signer error</strong><p>{authError}</p></div>
      {:else if !hasExtension}
        <ConfiguredIcon size={22} strokeWidth={1.75} aria-hidden="true" />
        <div><strong>Nostr signer required</strong><p>Use NIP-07 or Nostr Connect to sign each draft checkpoint and provisioning event.</p></div>
      {:else if isAuthenticated && userPubkey}
        <SuccessIcon size={22} strokeWidth={1.75} aria-hidden="true" />
        <div><strong>Authenticated</strong><p>{userPubkey.slice(0, 8)}…{userPubkey.slice(-8)}</p></div>
      {:else if authState.status === 'authenticating'}
        <PendingIcon size={22} strokeWidth={1.75} aria-hidden="true" />
        <div><strong>Requesting permission</strong><p>Approve the signer prompt.</p></div>
      {:else}
        <LoginIcon size={22} strokeWidth={1.75} aria-hidden="true" />
        <div><strong>Login required</strong><p>You will sign draft checkpoints and the provisioning request.</p></div>
        <button class="btn-secondary" onclick={handleLogin}>Login</button>
      {/if}
    </div>
  {/if}

  {#if error}
    <div class="error-banner"><WarningIcon size={18} strokeWidth={1.75} aria-hidden="true" /> {error}</div>
  {/if}

  {#if draftSaveStatus && step < 3}
    <div class="draft-status">
      <SuccessIcon size={18} strokeWidth={1.75} aria-hidden="true" />
      <span>{draftSaveStatus}{#if lastDraftSavedAt} at {lastDraftSavedAt}{/if}</span>
      {#if draftEventId}<code>{draftEventId.slice(0, 12)}…</code>{/if}
      {#if draftPublishResults.length}<span>{draftPublishResults.filter((result) => result.accepted).length} relay accepts</span>{/if}
    </div>
  {/if}

  {#if step === 1}
    <section class="wizard-content">
      <div class="wizard-toolbar">
        <div>
          <h2>Customization panels</h2>
          <p>Leaving a panel signs and publishes the current draft checkpoint.</p>
        </div>
        <div class="disclosure-toggle" role="group" aria-label="Progressive disclosure mode">
          <button type="button" class:active={disclosureMode === 'basic'} onclick={() => disclosureMode = 'basic'}>Basic</button>
          <button type="button" class:active={disclosureMode === 'advanced'} onclick={() => disclosureMode = 'advanced'}>Advanced</button>
        </div>
      </div>

      <div class="tab-layout">
        <nav class="tab-list" aria-label="Soul customization tabs">
          {#each customizationTabs as tab}
            <button type="button" class:active={activeTab === tab.id} onclick={() => goToTab(tab.id)} disabled={savingDraft || submitting || (!hasExtension && !isAuthenticated)}>
              <strong>{tab.label}</strong>
              <span>{tab.description}</span>
            </button>
          {/each}
        </nav>

        <div class="tab-panel">
          {#if activeTab === 'identity'}
            <div class="form-section">
              <h3>Identity draft</h3>
              <label>Name<input bind:value={agentName} onblur={generateAgentId} placeholder="Scout" /></label>
              <label>Agent ID<input bind:value={agentId} placeholder="scout" /></label>
              <label>Purpose<textarea rows="5" bind:value={purpose} placeholder="What should this soul do?"></textarea></label>
              <div class="two-col">
                <label>Tier<select bind:value={tier}><option value="lightweight">Lightweight</option><option value="standard">Standard</option><option value="heavy">Heavy</option></select></label>
                <label>NIP-05 target<input bind:value={nip05} placeholder="optional" /></label>
              </div>
              {#if showAdvanced}
                <div class="two-col advanced-inline">
                  <label>Identity theme<input bind:value={identityTheme} placeholder="warm" /></label>
                  <label>Emoji<input bind:value={identityEmoji} placeholder="✨" /></label>
                </div>
              {/if}
            </div>

            <div class="form-section preset-panel">
              <h3>Customization starting point</h3>
              <label>Preset library
                <select value={selectedPresetId} onchange={(event) => applyPreset(event.currentTarget.value)}>
                  <option value="">Choose a preset…</option>
                  {#each customizationPresets as preset}
                    <option value={preset.id}>{preset.label}</option>
                  {/each}
                </select>
              </label>
              {#if selectedPresetId}
                {@const preset = customizationPresets.find((item) => item.id === selectedPresetId)}
                <p class="hint-inline">{preset?.description}</p>
              {/if}
              <label>Clone customization from existing soul
                <select value={cloneSoulId} onchange={(event) => cloneFromSoul(event.currentTarget.value)}>
                  <option value="">Choose a soul…</option>
                  {#each souls as existingSoul}
                    <option value={existingSoul.agentId}>{existingSoul.name || existingSoul.agentId}</option>
                  {/each}
                </select>
              </label>
              <p class="hint-inline">Presets and clones fill persona, voice, memory, avatar, and identity defaults. You can still edit every panel before provisioning.</p>
            </div>

            <TemplateSelector selected={selectedTemplate} onSelect={handleTemplateSelect} />
          {:else if activeTab === 'avatar'}
            <AvatarStudio bind:value={avatarSpec} {showAdvanced} />
          {:else if activeTab === 'voice'}
            <VoiceStudio bind:value={voiceSpec} bind:assetRef={voiceRef} {showAdvanced} />
          {:else if activeTab === 'memory'}
            <MemoryConfig bind:value={memorySpec} {showAdvanced} />
          {:else if activeTab === 'personality'}
            <PersonalityBuilder bind:value={personaSpec} {showAdvanced} />
          {:else if activeTab === 'runtime'}
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

            <div class="form-section">
              <h3>Permissions</h3>
              <label>Allowed Nostr kinds<input bind:value={allowedKinds} placeholder="1, 3, 31951" /></label>
              <label>Tool grants<textarea rows="3" bind:value={toolGrants} placeholder="mcp-server: read, write"></textarea></label>
              <label>Approval policy<select bind:value={approvalPolicy}><option value="operator">Operator approval</option><option value="auto-safe">Auto-approve safe tools</option><option value="manual">Manual only</option></select></label>
            </div>

            {#if showAdvanced}
              <div class="form-section advanced-inline">
                <h3>Relays</h3>
                <label class="checkbox-row"><input type="checkbox" bind:checked={nip65Discovery} /> Use NIP-65 relay discovery when available</label>
                <label>Read relays<textarea rows="2" bind:value={readRelays} placeholder="wss://relay.example"></textarea></label>
                <label>Write relays<textarea rows="2" bind:value={writeRelays}></textarea></label>
                <label>Control relays<textarea rows="2" bind:value={controlRelays}></textarea></label>
              </div>
            {/if}

            <div class="form-section">
              <h3>Repository</h3>
              <RepositoryPicker bind:value={selectedRepository} context="soul" requirePrimaryUrl={false} />
              {#if showAdvanced}
                <div class="two-col">
                  <label>Branch<input bind:value={branch} /></label>
                  <label>Environment<input bind:value={environment} /></label>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      </div>

      <div class="wizard-actions">
        <button class="btn-secondary" onclick={prevPanel} disabled={activeTabIndex === 0 || savingDraft || submitting}>← Back</button>
        <button class="btn-secondary" onclick={() => saveDraft(customizationTabs[activeTabIndex]?.label || 'current panel')} disabled={savingDraft || submitting || (!hasExtension && !isAuthenticated)}>
          {savingDraft ? 'Saving draft…' : 'Save panel draft'}
        </button>
        <button class="btn-primary" onclick={nextPanel} disabled={savingDraft || submitting || (activeTab === 'runtime' && runtimeChoices.length === 0) || (!hasExtension && !isAuthenticated)}>
          {#if savingDraft}<span class="spinner"></span>Saving…{:else if activeTabIndex === customizationTabs.length - 1}Preview draft →{:else}Save & continue →{/if}
        </button>
      </div>
    </section>
  {:else if step === 2}
    <section class="wizard-content">
      <div class="preview-header">
        <div>
          <h2>Preview full draft</h2>
          <p>Review the signed v2 draft content before provisioning. The next action publishes a final 31952 checkpoint and a 5950 provisioning request.</p>
        </div>
        <button class="btn-secondary" onclick={() => { step = 1; activeTab = 'runtime'; }} disabled={submitting || savingDraft}>Edit runtime</button>
      </div>

      <div class="preview-grid">
        <article><span>Identity</span><strong>{previewDraftContent.identity.name || agentId}</strong><p>{previewDraftContent.identity.purpose || 'No purpose yet'}</p></article>
        <article><span>Avatar</span><strong>{previewDraftContent.avatar?.generation?.style_preset || 'default'}</strong><p>{previewDraftContent.assets.avatar_ref || 'No avatar ref'}</p></article>
        <article><span>Voice</span><strong>{previewDraftContent.voice?.provider || 'unset'}</strong><p>{previewDraftContent.voice?.persona?.label || previewDraftContent.assets.voice_ref || 'No voice label'}</p></article>
        <article><span>Memory</span><strong>{previewDraftContent.memory?.strategy || 'unset'}</strong><p>{previewDraftContent.memory?.embedding_provider || 'No provider'} · top {previewDraftContent.memory?.search?.top_k || 0}</p></article>
        <article><span>Personality</span><strong>{previewDraftContent.persona?.style || 'unset'}</strong><p>{previewDraftContent.persona?.traits?.join(', ') || 'No traits'}</p></article>
        <article><span>Runtime</span><strong>{previewDraftContent.runtime.target || 'No runtime'}</strong><p>{previewDraftContent.runtime.capability_ref || 'No capability selected'}</p></article>
      </div>

      <details class="draft-json" open>
        <summary>Full draft JSON</summary>
        <pre>{JSON.stringify(previewDraftContent, null, 2)}</pre>
      </details>

      <div class="wizard-actions">
        <button class="btn-secondary" onclick={prevPanel} disabled={submitting || savingDraft}>← Back</button>
        <button class="btn-primary" onclick={submitProvisioning} disabled={submitting || savingDraft || runtimeChoices.length === 0 || (!hasExtension && !isAuthenticated)}>
          {#if publishing}<span class="spinner"></span>Signing & publishing…{:else}<SeedIcon size={18} strokeWidth={1.75} aria-hidden="true" />Provision from preview{/if}
        </button>
      </div>
    </section>
  {:else if step === 3}
    <section class="wizard-content">
      <div class="publish-success">
        <SuccessIcon size={28} strokeWidth={2} aria-hidden="true" />
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
  .page { max-width: 1080px; margin: 0 auto; }
  .page-header { margin-bottom: 1.5rem; }
  .back-link { color: var(--text-muted); text-decoration: none; font-size: 0.875rem; }
  h1 { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.25rem; }
  h2 { margin: 0; font-size: 1.1rem; }
  .subtitle, p { color: var(--text-muted); }
  .wizard-progress, .auth-status, .wizard-content, .publish-success, .draft-status { background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 12px; }
  .wizard-progress { display: flex; align-items: center; justify-content: center; gap: 0.5rem; padding: 1rem; margin-bottom: 1rem; }
  .progress-step { display: flex; align-items: center; gap: 0.4rem; color: var(--text-muted); }
  .progress-step.active { color: var(--primary); }
  .progress-step.complete { color: var(--success); }
  .step-num { width: 26px; height: 26px; border: 2px solid currentColor; border-radius: 999px; display: grid; place-items: center; font-size: 0.8rem; font-weight: 700; }
  .progress-step.active .step-num, .progress-step.complete .step-num { background: currentColor; color: white; }
  .progress-line { width: 48px; height: 2px; background: var(--border-color); }
  .progress-line.active { background: var(--primary); }
  .auth-status, .draft-status { display: flex; align-items: center; gap: 0.75rem; padding: 0.85rem 1rem; margin-bottom: 1rem; }
  .draft-status { color: var(--success); flex-wrap: wrap; }
  .auth-status.authenticated { border-color: rgba(34, 197, 94, 0.4); }
  .auth-status.error, .error-banner { border-color: rgba(239, 68, 68, 0.4); color: var(--error); }
  .auth-status p { margin: 0.15rem 0 0; font-size: 0.8rem; }
  .wizard-content { padding: 1.25rem; }
  .wizard-toolbar, .preview-header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; margin-bottom: 1rem; }
  .wizard-toolbar p, .preview-header p { margin: 0.25rem 0 0; font-size: 0.85rem; }
  .disclosure-toggle { display: inline-flex; border: 1px solid var(--border-color); border-radius: 10px; overflow: hidden; }
  .disclosure-toggle button { border: none; background: transparent; color: var(--text-muted); padding: 0.55rem 0.8rem; cursor: pointer; }
  .disclosure-toggle button.active { background: var(--primary); color: white; }
  .tab-layout { display: grid; grid-template-columns: 230px minmax(0, 1fr); gap: 1rem; align-items: start; }
  .tab-list { display: grid; gap: 0.55rem; }
  .tab-list button { text-align: left; display: grid; gap: 0.2rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.75rem; cursor: pointer; }
  .tab-list button span { color: var(--text-muted); font-size: 0.78rem; }
  .tab-list button.active { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
  .tab-panel { min-width: 0; display: grid; gap: 1rem; }
  .form-section { display: grid; gap: 0.9rem; margin-bottom: 1.25rem; }
  .form-section h3 { margin: 0; padding-bottom: 0.5rem; border-bottom: 1px solid var(--border-color); }
  label { display: grid; gap: 0.35rem; font-size: 0.85rem; color: var(--text-primary); }
  input, select, textarea { width: 100%; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.65rem 0.75rem; font: inherit; box-sizing: border-box; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.85rem; }
  .checkbox-row { display: flex; align-items: center; gap: 0.5rem; }
  .checkbox-row input { width: auto; }
  .advanced-inline, .preset-panel { border: 1px dashed var(--border-color); border-radius: 12px; padding: 0.9rem; background: rgba(255,255,255,0.02); }
  .hint-inline { margin: -0.35rem 0 0; font-size: 0.82rem; color: var(--text-muted); }
  .wizard-actions { display: flex; justify-content: space-between; gap: 0.75rem; padding-top: 1rem; border-top: 1px solid var(--border-color); margin-top: 1rem; }
  .btn-primary, .btn-secondary { display: inline-flex; align-items: center; justify-content: center; gap: 0.45rem; border-radius: 8px; padding: 0.65rem 1rem; border: 1px solid transparent; cursor: pointer; font-weight: 600; text-decoration: none; }
  .btn-primary { background: var(--primary); color: white; }
  .btn-secondary { background: transparent; color: var(--text-muted); border-color: var(--border-color); }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  .error-banner, .warning-box { padding: 0.85rem 1rem; border-radius: 8px; margin-bottom: 1rem; background: rgba(239, 68, 68, 0.08); border: 1px solid rgba(239, 68, 68, 0.25); }
  .warning-box { color: var(--warning); background: rgba(245, 158, 11, 0.08); border-color: rgba(245, 158, 11, 0.3); }
  .runtime-summary { display: grid; gap: 0.35rem; background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; padding: 0.75rem; font-size: 0.82rem; color: var(--text-muted); }
  .preview-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.75rem; margin-bottom: 1rem; }
  .preview-grid article { background: var(--bg); border: 1px solid var(--border-color); border-radius: 10px; padding: 0.85rem; display: grid; gap: 0.25rem; min-width: 0; }
  .preview-grid span { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .preview-grid strong, .preview-grid p { overflow-wrap: anywhere; }
  .preview-grid p { margin: 0; font-size: 0.82rem; }
  .draft-json { border: 1px solid var(--border-color); border-radius: 10px; background: var(--bg); overflow: hidden; }
  .draft-json summary { cursor: pointer; padding: 0.75rem 0.9rem; font-weight: 700; }
  .draft-json pre { margin: 0; padding: 1rem; max-height: 460px; overflow: auto; border-top: 1px solid var(--border-color); font-size: 0.78rem; }
  .publish-success { text-align: center; padding: 1.25rem; margin-bottom: 1rem; }
  .publish-success dl { display: grid; grid-template-columns: 120px 1fr; gap: 0.4rem 0.75rem; text-align: left; }
  .publish-success dt { color: var(--text-muted); }
  code { background: var(--bg); border-radius: 4px; padding: 0.15rem 0.35rem; overflow-wrap: anywhere; }
  .spinner { width: 16px; height: 16px; border: 2px solid rgba(255,255,255,0.35); border-top-color: white; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (max-width: 860px) { .tab-layout, .preview-grid { grid-template-columns: 1fr; } .tab-list { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  @media (max-width: 720px) { .wizard-progress, .wizard-toolbar, .preview-header { align-items: stretch; flex-direction: column; } .progress-line { display: none; } .two-col, .tab-list { grid-template-columns: 1fr; } .wizard-actions { flex-direction: column; } }
</style>
