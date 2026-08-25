<script>
  import { page } from '$app/state';
  import Badge from '$lib/components/Badge.svelte';
  import AvatarStudio from '$lib/components/souls/AvatarStudio.svelte';
  import MemoryConfig from '$lib/components/MemoryConfig.svelte';
  import PersonalityBuilder from '$lib/components/souls/PersonalityBuilder.svelte';
  import VoiceStudio from '$lib/components/souls/VoiceStudio.svelte';
  import {
    CopyIcon,
    HeavyIcon,
    IdentityIcon,
    LightweightIcon,
    MemoryIcon,
    ProtectedIcon,
    SoulIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon,
    WorkspaceIcon
  } from '$lib/icons/domain-icons.js';
  import { nostr, parseSoulEvent, KINDS, normalizeSoulDraftContent } from '$lib/nostr/client.js';
  import { buildSoulRef, fetchSoulHistory, subscribeToSoulFactoryUpdates, unsubscribeFromSoulUpdates, publishSoulAction, publishSoulDraft, publishSoulUpdateAction, provisioningRuns, souls, trackLifecycleRun } from '$lib/stores/souls.js';
  
  let soul = $state(null);
  let loading = $state(true);
  let error = $state(null);
  let unsub = null;
  let actionSubmitting = $state(false);
  let actionError = $state('');
  let actionNotice = $state('');
  let historyLoading = $state(false);
  let historyError = $state('');
  let activityHistory = $state([]);
  let actionRunId = $state('');
  let actionCleanup = null;
  let editingSection = $state('');
  let savingCustomization = $state(false);
  let customizationDraft = $state(null);
  let customizationNotice = $state('');
  let customizationError = $state('');
  let cloneCustomizationSoulId = $state('');
  
  let agentId = $derived(page.params.id);
  let currentActionRun = $derived(actionRunId ? provisioningRuns.get(actionRunId) : null);
  let soulDraftContent = $derived(extractDraftContent(soul));
  let customization = $derived({
    avatar: soulDraftContent.avatar || defaultAvatarSpec(),
    voice: soulDraftContent.voice || defaultVoiceSpec(),
    memory: soulDraftContent.memory || defaultMemorySpec(),
    persona: soulDraftContent.persona || defaultPersonaSpec()
  });
  
  const statusColors = {
    active: 'success',
    provisioning: 'warning',
    suspended: 'default',
    revoked: 'error',
    draft: 'default'
  };
  
  const deployStatusColors = {
    deployed: 'success',
    healthy: 'success',
    deploying: 'warning',
    failed: 'error',
    unhealthy: 'error',
    stopped: 'default',
    pending: 'default'
  };

  function defaultAvatarSpec() {
    return {
      generation: { prompt: '', style_preset: 'pixel-art', seed: '', width: 512, height: 512, provider: 'flux-comfyui' },
      uploaded_ref: '',
      generated_ref: '',
      current: 'generated'
    };
  }

  function defaultVoiceSpec() {
    return {
      provider: 'openai',
      persona_id: '',
      persona: { label: '', profile: '', style: 'articulate', accent: 'neutral american', pacing: 'measured' },
      auto_mode: 'tagged',
      sample_text: ''
    };
  }

  function defaultMemorySpec() {
    return {
      embedding_provider: 'openai',
      embedding_model: 'text-embedding-3-small',
      search: { top_k: 10, score_threshold: 0.7, rerank: false, rerank_model: '' },
      strategy: 'session-aware',
      auto_index: true,
      retention_days: 90
    };
  }

  function defaultPersonaSpec() {
    return {
      traits: [],
      style: 'conversational',
      tone: 'friendly professional',
      constraints: [],
      system_prompt_sections: { role: '', guidelines: '', red_lines: '' }
    };
  }

  function clone(value) {
    return JSON.parse(JSON.stringify(value || {}));
  }

  function extractDraftContent(currentSoul) {
    if (!currentSoul) return normalizeSoulDraftContent({ schema: 'soulfactory-draft/v2' });
    let parsed = {};
    try {
      parsed = currentSoul.content ? JSON.parse(currentSoul.content) : {};
    } catch {
      parsed = {};
    }

    return normalizeSoulDraftContent({
      schema: parsed.schema || 'soulfactory-draft/v2',
      ...parsed,
      identity: {
        ...(parsed.identity || {}),
        name: parsed.identity?.name || currentSoul.name || currentSoul.agentId,
        purpose: parsed.identity?.purpose || currentSoul.purpose || '',
        tier: parsed.identity?.tier || currentSoul.tier || 'standard',
        nip05: parsed.identity?.nip05 || currentSoul.nip05 || ''
      },
      runtime: { ...(parsed.runtime || {}), ...(currentSoul.runtime || {}) },
      permissions: parsed.permissions || currentSoul.permissions || {},
      relay_policy: parsed.relay_policy || currentSoul.relayPolicy || {},
      workspace: parsed.workspace || currentSoul.workspaceSpec || {},
      assets: parsed.assets || currentSoul.assets || {}
    });
  }

  function startEdit(section) {
    editingSection = section;
    customizationDraft = clone(customization[section]);
    customizationNotice = '';
    customizationError = '';
  }

  function cancelEdit() {
    editingSection = '';
    customizationDraft = null;
    cloneCustomizationSoulId = '';
  }

  function cloneCustomizationFromSoul(sourceAgentId) {
    cloneCustomizationSoulId = sourceAgentId;
    const sourceSoul = souls.find((item) => item.agentId === sourceAgentId);
    if (!sourceSoul || !editingSection) return;
    const sourceContent = extractDraftContent(sourceSoul);
    customizationDraft = clone(sourceContent[editingSection] || customization[editingSection]);
  }

  async function saveCustomization(section) {
    if (!soul || !customizationDraft || savingCustomization) return;
    savingCustomization = true;
    customizationNotice = '';
    customizationError = '';

    try {
      const nextContent = normalizeSoulDraftContent({
        ...soulDraftContent,
        schema: 'soulfactory-draft/v2',
        [section]: clone(customizationDraft)
      });
      const draftResult = await publishSoulDraft({
        agentId: soul.agentId,
        content: nextContent,
        previousSpecHash: soul.specHash || soul.previousSpecHash || ''
      });
      const draftRef = `${KINDS.SOUL_DRAFT}:${draftResult.event.pubkey}:${soul.agentId}`;
      await publishSoulUpdateAction({
        soul,
        draft: { id: draftResult.event.id, content: nextContent, specHash: draftResult.specHash, coordinate: draftRef },
        draftRef,
        draftEventId: draftResult.event.id,
        resolvedSpec: nextContent,
        newSpecHash: draftResult.specHash,
        updateMode: 'merge',
        reason: `Updated ${section} customization from Soul detail`
      });
      customizationNotice = `${section} draft saved and update action submitted.`;
      editingSection = '';
      customizationDraft = null;
      await loadHistory();
    } catch (err) {
      customizationError = err.message || `Failed to save ${section}`;
    } finally {
      savingCustomization = false;
    }
  }

  function handleCopyKeydown(event, copyAction) {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    copyAction();
  }
  
  function lookupSoul(id = agentId) {
    const found = souls.find((s) => s.agentId === id);
    if (found) {
      soul = found;
      loading = false;
    }
    return found;
  }
  
  function tagValue(event, name) {
    return (event?.tags || []).find((tag) => tag[0] === name)?.[1] || '';
  }

  function eventBelongsToSoul(event) {
    if (!soul || event.kind === KINDS.AGENT_SOUL) return true;
    return tagValue(event, 'soul') === buildSoulRef(soul);
  }

  function subscribeToUpdates(id = agentId) {
    const filters = [{
      kinds: [KINDS.AGENT_SOUL],
      '#d': [id]
    }];

    if (soul) {
      filters.push({
        kinds: [KINDS.PROVISIONING_STATUS, KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT],
        since: Math.floor(Date.now() / 1000),
        limit: 100
      });
    }

    unsub = nostr.subscribe(filters, {
      onEvent: (event) => {
        if (!eventBelongsToSoul(event)) return;
        if (event.kind === KINDS.AGENT_SOUL) {
          soul = parseSoulEvent(event);
        }
        void loadHistory();
      },
      onClosed: () => {
        // Relay interruptions are informational here; terminal lifecycle state comes only from result events.
      }
    });
  }

  async function loadHistory() {
    if (!soul) return;
    historyLoading = true;
    historyError = '';

    try {
      activityHistory = await fetchSoulHistory(soul, { limit: 25 });
    } catch (err) {
      historyError = err.message || 'Failed to load soul activity';
    } finally {
      historyLoading = false;
    }
  }

  async function handleAction(action) {
    if (!soul || actionSubmitting) return;

    actionSubmitting = true;
    actionError = '';
    actionNotice = '';

    try {
      const displayAction = action === 'resume' ? 'reactivate' : action;
      const reason = `Requested from Soul Gallery (${displayAction})`;
      if (actionCleanup) {
        actionCleanup();
        actionCleanup = null;
      }
      await publishSoulAction({
        soul,
        action,
        reason,
        beforePublish: (event) => {
          actionRunId = event.id;
          actionCleanup = trackLifecycleRun(event.id, { type: 'lifecycle', action });
        }
      });
      actionNotice = `Action "${displayAction}" submitted to relays. Waiting for explicit 7950 result.`;
      await loadHistory();
    } catch (err) {
      if (actionCleanup) {
        actionCleanup();
        actionCleanup = null;
      }
      actionRunId = '';
      actionError = err.message || `Failed to submit ${action}`;
    } finally {
      actionSubmitting = false;
    }
  }
  
  function copyNpub() {
    navigator.clipboard.writeText(soul.npub);
  }
  
  function copyPubkey() {
    navigator.clipboard.writeText(soul.agentPubkey);
  }
  
  $effect(() => {
    const id = agentId;
    if (!id) return;

    let cancelled = false;
    if (unsub) {
      unsub();
      unsub = null;
    }

    async function initializeSoul() {
      await subscribeToSoulFactoryUpdates();
      if (cancelled) return;
      if (!lookupSoul(id)) {
        loading = false;
        error = 'Soul not found';
        return;
      }
      await loadHistory();
      if (cancelled) return;
      subscribeToUpdates(id);
    }

    void initializeSoul();

    return () => {
      cancelled = true;
      if (unsub) {
        unsub();
        unsub = null;
      }
      if (actionCleanup) {
        actionCleanup();
        actionCleanup = null;
      }
    };
  });
</script>

<svelte:head>
  <title>{soul?.name || agentId} | Soul Factory</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <a href="/souls" class="back-link">← Back to Gallery</a>
  </header>
  
  {#if loading}
    <div class="loading">
      <div class="spinner"></div>
      <span>Loading soul...</span>
    </div>
  {:else if error}
    <div class="error-state">
      <span class="icon" aria-hidden="true"><UnknownIcon size={64} strokeWidth={1.5} /></span>
      <h2>Soul Not Found</h2>
      <p>{error}</p>
      <a href="/souls" class="btn-secondary">Back to Gallery</a>
    </div>
  {:else if soul}
    <div class="soul-detail">
      <!-- Hero Section -->
      <div class="soul-hero">
        <div class="avatar-large">
          {#if soul.avatarUrl}
            <img src={soul.avatarUrl} alt={soul.name} />
          {:else}
            <div class="avatar-placeholder">{soul.name?.[0] || '?'}</div>
          {/if}
        </div>
        
        <div class="hero-info">
          <h1>{soul.name}</h1>
          <span class="agent-id">@{soul.agentId}</span>
          
          <div class="status-row">
            <Badge variant={statusColors[soul.status]}>{soul.status}</Badge>
            {#if soul.deployStatus}
              <Badge variant={deployStatusColors[soul.deployStatus]}>{soul.deployStatus}</Badge>
            {/if}
            <span class="tier-badge">
              {#if soul.tier === 'lightweight'}
                <LightweightIcon size={16} strokeWidth={1.75} ariaHidden="true" />
              {:else if soul.tier === 'heavy'}
                <HeavyIcon size={16} strokeWidth={1.75} ariaHidden="true" />
              {:else}
                <SoulIcon size={16} strokeWidth={1.75} ariaHidden="true" />
              {/if}
              {soul.tier}
            </span>
          </div>
          
          {#if soul.nip05}
            <span class="nip05"><SuccessIcon size={16} strokeWidth={1.75} ariaHidden="true" /> {soul.nip05}</span>
          {/if}
        </div>
        
        <div class="hero-actions">
          <a class="btn-secondary" href={`/souls/${soul.agentId}/edit`}>
            Edit Details
          </a>
          {#if soul.status === 'active'}
            <button class="btn-warning" onclick={() => handleAction('suspend')} disabled={actionSubmitting}>
              Suspend
            </button>
          {:else if soul.status === 'suspended'}
            <button class="btn-primary" onclick={() => handleAction('resume')} disabled={actionSubmitting}>
              Reactivate
            </button>
          {/if}
        </div>
      </div>

      {#if actionNotice}
        <div class="notice-banner"><SuccessIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{actionNotice}</span></div>
      {/if}

      {#if actionError}
        <div class="error-banner"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{actionError}</span></div>
      {/if}

      {#if currentActionRun}
        <div class="run-banner" class:failed={currentActionRun.status === 'failed'} class:completed={currentActionRun.status === 'completed'}>
          <strong>Lifecycle run: {currentActionRun.action || 'action'}</strong>
          <span>{currentActionRun.message}</span>
          {#if currentActionRun.result?.id}<code>{currentActionRun.result.id}</code>{/if}
        </div>
      {/if}

      <section class="customization-shell">
        <div class="customization-header">
          <div>
            <h2>Customization</h2>
            <p>Draft v2 avatar, voice, memory, and personality settings for this soul.</p>
          </div>
          <div class="runtime-controls" aria-label="Runtime controls">
            <button class="btn-secondary" onclick={() => handleAction('hot-reload')} disabled={actionSubmitting}>Hot-reload</button>
            <button class="btn-secondary" onclick={() => handleAction('restart')} disabled={actionSubmitting}>Restart</button>
            <button class="btn-warning" onclick={() => handleAction('pause')} disabled={actionSubmitting}>Pause</button>
          </div>
        </div>

        {#if customizationNotice}
          <div class="notice-banner"><SuccessIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{customizationNotice}</span></div>
        {/if}
        {#if customizationError}
          <div class="error-banner"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{customizationError}</span></div>
        {/if}

        <div class="customization-grid">
          <article class="custom-card">
            <header>
              <h3>Avatar</h3>
              <button class="btn-secondary" onclick={() => startEdit('avatar')} disabled={savingCustomization}>Edit</button>
            </header>
            <dl>
              <dt>Current</dt><dd>{customization.avatar.current || 'generated'}</dd>
              <dt>Style</dt><dd>{customization.avatar.generation?.style_preset || 'N/A'}</dd>
              <dt>Provider</dt><dd>{customization.avatar.generation?.provider || 'N/A'}</dd>
              <dt>Size</dt><dd>{customization.avatar.generation?.width || 512}×{customization.avatar.generation?.height || 512}</dd>
              <dt>Prompt</dt><dd>{customization.avatar.generation?.prompt || 'No prompt configured'}</dd>
              <dt>Ref</dt><dd><code>{customization.avatar.generated_ref || customization.avatar.uploaded_ref || soul.assets?.avatar_ref || 'N/A'}</code></dd>
            </dl>
          </article>

          <article class="custom-card">
            <header>
              <h3>Voice</h3>
              <button class="btn-secondary" onclick={() => startEdit('voice')} disabled={savingCustomization}>Edit</button>
            </header>
            <dl>
              <dt>Provider</dt><dd>{customization.voice.provider || 'N/A'}</dd>
              <dt>Persona</dt><dd>{customization.voice.persona?.label || customization.voice.persona_id || 'N/A'}</dd>
              <dt>Profile</dt><dd>{customization.voice.persona?.profile || 'No profile configured'}</dd>
              <dt>Style</dt><dd>{customization.voice.persona?.style || 'N/A'} · {customization.voice.persona?.accent || 'N/A'} · {customization.voice.persona?.pacing || 'N/A'}</dd>
              <dt>Auto mode</dt><dd>{customization.voice.auto_mode || 'tagged'}</dd>
              <dt>Sample</dt><dd>{customization.voice.sample_text || 'No sample text configured'}</dd>
            </dl>
          </article>

          <article class="custom-card">
            <header>
              <h3>Memory</h3>
              <button class="btn-secondary" onclick={() => startEdit('memory')} disabled={savingCustomization}>Edit</button>
            </header>
            <dl>
              <dt>Embedding</dt><dd>{customization.memory.embedding_provider || 'N/A'} / {customization.memory.embedding_model || 'N/A'}</dd>
              <dt>Strategy</dt><dd>{customization.memory.strategy || 'session-aware'}</dd>
              <dt>Search</dt><dd>top {customization.memory.search?.top_k || 10}, threshold {customization.memory.search?.score_threshold ?? 0.7}</dd>
              <dt>Rerank</dt><dd>{customization.memory.search?.rerank ? customization.memory.search?.rerank_model || 'enabled' : 'disabled'}</dd>
              <dt>Auto-index</dt><dd>{(customization.memory.auto_index ?? true) ? 'enabled' : 'disabled'}</dd>
              <dt>Retention</dt><dd>{customization.memory.retention_days || 90} days</dd>
            </dl>
          </article>

          <article class="custom-card">
            <header>
              <h3>Personality</h3>
              <button class="btn-secondary" onclick={() => startEdit('persona')} disabled={savingCustomization}>Edit</button>
            </header>
            <dl>
              <dt>Traits</dt><dd>{customization.persona.traits?.join(', ') || 'No traits configured'}</dd>
              <dt>Style</dt><dd>{customization.persona.style || 'conversational'}</dd>
              <dt>Tone</dt><dd>{customization.persona.tone || 'friendly professional'}</dd>
              <dt>Constraints</dt><dd>{customization.persona.constraints?.join('; ') || 'No constraints configured'}</dd>
              <dt>Role</dt><dd>{customization.persona.system_prompt_sections?.role || 'No role section configured'}</dd>
            </dl>
          </article>
        </div>

        {#if editingSection}
          <div class="edit-panel">
            <div class="edit-header">
              <h3>Edit {editingSection}</h3>
              <div class="edit-actions">
                <button class="btn-secondary" onclick={cancelEdit} disabled={savingCustomization}>Cancel</button>
                <button class="btn-primary" onclick={() => saveCustomization(editingSection)} disabled={savingCustomization}>{savingCustomization ? 'Saving...' : 'Save draft + update'}</button>
              </div>
            </div>
            <div class="clone-row">
              <label>Clone this section from another soul
                <select value={cloneCustomizationSoulId} onchange={(event) => cloneCustomizationFromSoul(event.currentTarget.value)} disabled={savingCustomization}>
                  <option value="">Choose a soul…</option>
                  {#each souls.filter((item) => item.agentId !== soul.agentId) as existingSoul}
                    <option value={existingSoul.agentId}>{existingSoul.name || existingSoul.agentId}</option>
                  {/each}
                </select>
              </label>
            </div>
            {#if editingSection === 'avatar'}
              <AvatarStudio bind:value={customizationDraft} showAdvanced={true} />
            {:else if editingSection === 'voice'}
              <VoiceStudio bind:value={customizationDraft} assetRef={soul.assets?.voice_ref || ''} showAdvanced={true} {soul} />
            {:else if editingSection === 'memory'}
              <MemoryConfig bind:value={customizationDraft} showAdvanced={true} {soul} />
            {:else if editingSection === 'persona'}
              <PersonalityBuilder bind:value={customizationDraft} showAdvanced={true} />
            {/if}
          </div>
        {/if}
      </section>
      
      <!-- Info Grid -->
      <div class="info-grid">
        <!-- Identity Section -->
        <section class="info-section">
          <h3><IdentityIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Identity</h3>
          <dl>
            <dt>npub</dt>
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_element_to_interactive_role -->
            <dd class="copyable" onclick={copyNpub} onkeydown={(event) => handleCopyKeydown(event, copyNpub)} role="button" tabindex="0" title="Click to copy" aria-label="Copy npub">
              <code>{soul.npub || 'N/A'}</code>
              <span class="copy-icon" aria-hidden="true"><CopyIcon size={14} strokeWidth={1.75} /></span>
            </dd>
            
            <dt>Public Key</dt>
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_element_to_interactive_role -->
            <dd class="copyable" onclick={copyPubkey} onkeydown={(event) => handleCopyKeydown(event, copyPubkey)} role="button" tabindex="0" title="Click to copy" aria-label="Copy public key">
              <code>{soul.agentPubkey?.slice(0, 16)}...{soul.agentPubkey?.slice(-8) || 'N/A'}</code>
              <span class="copy-icon" aria-hidden="true"><CopyIcon size={14} strokeWidth={1.75} /></span>
            </dd>
            
            {#if soul.bahiaServiceId}
              <dt>Bahia Service</dt>
              <dd><code>{soul.bahiaServiceId.slice(0, 8)}...</code></dd>
            {/if}
            {#if soul.draftRef || soul.specHash}
              <dt>Draft ref</dt>
              <dd><code>{soul.draftRef || 'N/A'}</code></dd>
              <dt>Spec hash</dt>
              <dd><code>{soul.specHash || 'N/A'}</code></dd>
            {/if}
          </dl>
        </section>

        <section class="info-section">
          <h3><WorkspaceIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Runtime</h3>
          <dl>
            <dt>Target</dt>
            <dd>{soul.runtime?.target || 'N/A'}</dd>
            <dt>Runtime state</dt>
            <dd>{soul.runtime?.state || 'N/A'}</dd>
            <dt>Runtime pubkey</dt>
            <dd><code>{soul.runtime?.runtime_pubkey || 'N/A'}</code></dd>
            <dt>Capability</dt>
            <dd><code>{soul.capabilityRef || soul.runtime?.capability_ref || 'N/A'}</code></dd>
            <dt>Binding</dt>
            <dd><code>{soul.runtime?.runtime_binding || 'N/A'}</code></dd>
          </dl>
        </section>
        
        <!-- Infrastructure Section -->
        <section class="info-section">
          <h3><WorkspaceIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Infrastructure</h3>
          <dl>
            {#if soul.workspace}
              <dt>Workspace</dt>
              <dd>
                <a href={soul.workspace} target="_blank" rel="noopener">
                  {soul.workspace}
                </a>
              </dd>
            {/if}
            
            {#if soul.qdrant}
              <dt>Vector Memory</dt>
              <dd><code>{soul.qdrant}</code></dd>
            {/if}
            
            <dt>Created</dt>
            <dd>{new Date(soul.createdAt * 1000).toLocaleString()}</dd>
          </dl>
        </section>
        
        <!-- Permissions Section -->
        <section class="info-section wide">
          <h3><ProtectedIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Permissions</h3>
          <p class="policy-warning"><strong>Runtime enforcement note:</strong> allowed event kinds are provisioned into Signet policy. Tool grants and approval policy are signed draft intent; the owned OpenClaw wrapper does not yet translate them into tools, MCP, or plugin enforcement.</p>
          
          <div class="permissions-grid">
            <div class="perm-group">
              <h4>Allowed Event Kinds</h4>
              <div class="kind-tags">
                {#each soul.allowedKinds as kind}
                  <span class="kind-tag">{kind}</span>
                {:else}
                  <span class="empty-hint">No kinds configured</span>
                {/each}
              </div>
            </div>
            
            <div class="perm-group">
              <h4>Tool Grants</h4>
              <div class="tool-list">
                {#each soul.tools as tool}
                  <div class="tool-item">
                    <span class="tool-server">{tool.server}</span>
                    <div class="tool-scopes">
                      {#each tool.scopes as scope}
                        <span class="scope-tag">{scope}</span>
                      {/each}
                    </div>
                  </div>
                {:else}
                  <span class="empty-hint">No tools granted</span>
                {/each}
              </div>
            </div>
          </div>
        </section>
        
        <!-- Soul Content Section -->
        <section class="info-section wide">
          <h3><SoulIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Soul Content</h3>
          <div class="soul-content">
            <pre>{soul.content || 'No soul content available'}</pre>
          </div>
        </section>

        <section class="info-section wide">
          <h3><MemoryIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Activity & History</h3>
          {#if historyLoading}
            <p class="history-muted">Loading activity history...</p>
          {:else if historyError}
            <p class="history-error">{historyError}</p>
          {:else if activityHistory.length === 0}
            <p class="history-muted">No activity recorded yet.</p>
          {:else}
            <ul class="history-list">
              {#each activityHistory as item (item.id)}
                <li>
                  <div class="history-summary">{item.summary}</div>
                  <div class="history-meta">
                    {new Date(item.createdAt * 1000).toLocaleString()} · {item.pubkey?.slice(0, 8)}...{item.pubkey?.slice(-8)}
                  </div>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      </div>
    </div>
  {/if}
</div>

<style>
  .page {
    max-width: 1000px;
    margin: 0 auto;
  }
  
  .page-header {
    margin-bottom: 1.5rem;
  }
  
  .back-link {
    font-size: 0.875rem;
    color: var(--text-muted);
    text-decoration: none;
  }
  
  .back-link:hover {
    color: var(--primary);
  }
  
  .loading {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 4rem;
    color: var(--text-muted);
    gap: 1rem;
  }
  
  .spinner {
    width: 32px;
    height: 32px;
    border: 3px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  
  .error-state {
    text-align: center;
    padding: 4rem 2rem;
  }
  
  .error-state .icon {
    color: var(--text-muted);
    display: flex;
    justify-content: center;
    margin-bottom: 1rem;
  }
  
  .error-state h2 {
    margin: 0 0 0.5rem 0;
  }
  
  .error-state p {
    color: var(--text-muted);
    margin: 0 0 1.5rem 0;
  }

  .notice-banner,
  .error-banner,
  .run-banner {
    border-radius: 8px;
    padding: 0.75rem 1rem;
    margin-bottom: 1rem;
    font-size: 0.875rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .notice-banner {
    background: rgba(34, 197, 94, 0.12);
    border: 1px solid rgba(34, 197, 94, 0.35);
    color: var(--success);
  }

  .error-banner {
    background: rgba(239, 68, 68, 0.12);
    border: 1px solid rgba(239, 68, 68, 0.35);
    color: var(--error);
  }

  .run-banner {
    background: rgba(99, 102, 241, 0.1);
    border: 1px solid rgba(99, 102, 241, 0.3);
    color: var(--text-primary);
    align-items: flex-start;
    flex-direction: column;
  }

  .run-banner.completed {
    background: rgba(34, 197, 94, 0.1);
    border-color: rgba(34, 197, 94, 0.35);
  }

  .run-banner.failed {
    background: rgba(239, 68, 68, 0.1);
    border-color: rgba(239, 68, 68, 0.35);
  }
  
  /* Hero */
  .soul-hero {
    display: flex;
    gap: 1.5rem;
    align-items: flex-start;
    padding: 1.5rem;
    background: var(--card-bg);
    border-radius: 12px;
    border: 1px solid var(--border-color);
    margin-bottom: 1.5rem;
  }
  
  .avatar-large {
    width: 120px;
    height: 120px;
    border-radius: 16px;
    overflow: hidden;
    flex-shrink: 0;
  }
  
  .avatar-large img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  
  .avatar-placeholder {
    width: 100%;
    height: 100%;
    background: linear-gradient(135deg, var(--primary) 0%, #8b5cf6 100%);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 3rem;
    font-weight: bold;
    color: white;
    text-transform: uppercase;
  }
  
  .hero-info {
    flex: 1;
  }
  
  .hero-info h1 {
    font-size: 1.75rem;
    margin: 0 0 0.25rem 0;
  }
  
  .agent-id {
    font-size: 1rem;
    color: var(--text-muted);
    display: block;
    margin-bottom: 0.75rem;
  }
  
  .status-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  
  .tier-badge {
    font-size: 0.8rem;
    color: var(--text-muted);
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  
  .nip05 {
    font-size: 0.85rem;
    color: var(--success);
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  
  .hero-actions {
    display: flex;
    gap: 0.5rem;
    flex-shrink: 0;
  }
  
  .btn-primary, .btn-secondary, .btn-warning {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    border: none;
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s;
  }
  
  .btn-primary {
    background: var(--primary);
    color: white;
  }
  
  .btn-secondary {
    background: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-muted);
    text-decoration: none;
  }
  
  .btn-warning {
    background: rgba(245, 158, 11, 0.15);
    color: var(--warning);
    border: 1px solid var(--warning);
  }
  
  .btn-primary:disabled,
  .btn-warning:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  
  /* Customization */
  .customization-shell {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 1.25rem;
    margin-bottom: 1.5rem;
    display: grid;
    gap: 1rem;
  }

  .customization-header,
  .edit-header,
  .custom-card header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
  }

  .customization-header h2,
  .customization-header p,
  .custom-card h3,
  .edit-header h3 {
    margin: 0;
  }

  .customization-header p {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin-top: 0.25rem;
  }

  .runtime-controls,
  .edit-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .customization-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 1rem;
  }

  .custom-card,
  .edit-panel {
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 1rem;
  }

  .custom-card dl {
    margin: 1rem 0 0 0;
  }

  .custom-card dt {
    color: var(--text-muted);
    font-size: 0.72rem;
    margin-bottom: 0.2rem;
  }

  .custom-card dd {
    margin: 0 0 0.75rem 0;
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }

  .custom-card dd:last-child {
    margin-bottom: 0;
  }

  .custom-card code {
    background: var(--card-bg);
    padding: 0.2rem 0.4rem;
    border-radius: 4px;
    font-size: 0.78rem;
  }

  .edit-panel {
    display: grid;
    gap: 1rem;
  }

  .clone-row {
    border: 1px dashed var(--border-color);
    border-radius: 10px;
    padding: 0.85rem;
    background: rgba(255, 255, 255, 0.02);
  }

  .clone-row label {
    display: grid;
    gap: 0.4rem;
    font-size: 0.85rem;
  }

  .clone-row select {
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    color: var(--text-primary);
    padding: 0.6rem 0.75rem;
    font: inherit;
  }

  /* Info Grid */
  .info-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 1rem;
  }
  
  .info-section {
    background: var(--card-bg);
    border-radius: 12px;
    padding: 1.25rem;
    border: 1px solid var(--border-color);
  }
  
  .info-section.wide {
    grid-column: 1 / -1;
  }
  
  .info-section h3 {
    font-size: 0.9rem;
    margin: 0 0 1rem 0;
    padding-bottom: 0.5rem;
    border-bottom: 1px solid var(--border-color);
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  
  .info-section dl {
    margin: 0;
  }
  
  .info-section dt {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-bottom: 0.25rem;
  }
  
  .info-section dd {
    margin: 0 0 1rem 0;
    font-size: 0.875rem;
  }
  
  .info-section dd:last-child {
    margin-bottom: 0;
  }
  
  .info-section code {
    background: var(--bg);
    padding: 0.2rem 0.4rem;
    border-radius: 4px;
    font-size: 0.8rem;
  }
  
  .info-section a {
    color: var(--primary);
    text-decoration: none;
  }
  
  .info-section a:hover {
    text-decoration: underline;
  }
  
  .copyable {
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  
  .copyable:hover {
    color: var(--primary);
  }
  
  .copy-icon {
    opacity: 0.5;
    display: inline-flex;
    align-items: center;
  }
  
  .copyable:hover .copy-icon {
    opacity: 1;
  }
  
  /* Permissions */
  .policy-warning {
    margin: 0 0 1rem;
    padding: 0.75rem;
    border: 1px solid rgba(245, 158, 11, 0.35);
    border-radius: 8px;
    background: rgba(245, 158, 11, 0.1);
    color: var(--warning);
    font-size: 0.82rem;
    line-height: 1.45;
  }

  .permissions-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5rem;
  }
  
  .perm-group h4 {
    font-size: 0.8rem;
    font-weight: 500;
    margin: 0 0 0.5rem 0;
    color: var(--text-muted);
  }
  
  .kind-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  
  .kind-tag {
    background: rgba(99, 102, 241, 0.15);
    color: var(--primary);
    padding: 0.2rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-family: monospace;
  }
  
  .tool-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  
  .tool-item {
    background: var(--bg);
    padding: 0.5rem 0.75rem;
    border-radius: 6px;
  }
  
  .tool-server {
    font-size: 0.8rem;
    font-weight: 500;
    display: block;
    margin-bottom: 0.25rem;
  }
  
  .tool-scopes {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  
  .scope-tag {
    font-size: 0.65rem;
    padding: 0.1rem 0.3rem;
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border-radius: 3px;
  }
  
  .empty-hint {
    color: var(--text-muted);
    font-size: 0.8rem;
    font-style: italic;
  }
  
  /* Soul Content */
  .soul-content {
    background: var(--bg);
    border-radius: 8px;
    padding: 1rem;
    max-height: 400px;
    overflow-y: auto;
  }
  
  .soul-content pre {
    margin: 0;
    font-size: 0.85rem;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .history-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .history-list li {
    background: var(--bg);
    border-radius: 8px;
    border: 1px solid var(--border-color);
    padding: 0.75rem;
  }

  .history-summary {
    font-size: 0.85rem;
    margin-bottom: 0.25rem;
  }

  .history-meta,
  .history-muted {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin: 0;
  }

  .history-error {
    color: var(--error);
    font-size: 0.8rem;
    margin: 0;
  }
  
  @media (max-width: 768px) {
    .soul-hero {
      flex-direction: column;
      align-items: center;
      text-align: center;
    }
    
    .hero-actions {
      margin-top: 1rem;
    }
    
    .customization-header,
    .edit-header {
      flex-direction: column;
    }

    .customization-grid,
    .info-grid {
      grid-template-columns: 1fr;
    }
    
    .permissions-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
