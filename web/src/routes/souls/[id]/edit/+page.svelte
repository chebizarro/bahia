<script>
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { KINDS, SOUL_RUNTIME_METHODS, parseSoulDraftEvent, parseSoulEvent } from '$lib/nostr/client.js';
  import {
    subscribeToSoulFactoryUpdates,
    drafts,
    souls,
    publishSoulDraft,
    publishSoulUpdateAction,
    runtimeCapabilities
  } from '$lib/stores/souls.js';
  import {
    capabilityLabel,
    capabilityRef,
    compatibleCapabilities,
    formatKindList,
    formatToolGrantList,
    parseKindList,
    parseToolGrantList,
    splitList
  } from '../../page-model.js';

  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let success = $state('');
  let soul = $state(null);
  let existingDraft = $state(null);
  let selectedCapabilityRef = $state('');
  let templateRef = $state('');

  let agentId = $derived(page.params.id);
  let updateCapabilities = $derived(compatibleCapabilities(runtimeCapabilities, SOUL_RUNTIME_METHODS.UPDATE));
  let selectedCapability = $derived(updateCapabilities.find((capability) => capabilityRef(capability) === selectedCapabilityRef) || null);

  let name = $state('');
  let purpose = $state('');
  let tier = $state('standard');
  let nip05 = $state('');
  let allowedKinds = $state('');
  let toolGrants = $state('');
  let approvalPolicy = $state('operator');
  let readRelays = $state('');
  let writeRelays = $state('');
  let controlRelays = $state('');
  let nip65Discovery = $state(true);
  let repo = $state('');
  let branch = $state('main');
  let environment = $state('production');
  let avatarRef = $state('');
  let voiceRef = $state('');
  let reason = $state('Updated via Soul Gallery edit route');

  $effect(() => {
    if (updateCapabilities.length > 0 && (!selectedCapabilityRef || !selectedCapability)) {
      const current = updateCapabilities.find((capability) => capabilityRef(capability) === soul?.capabilityRef);
      selectedCapabilityRef = capabilityRef(current || updateCapabilities[0]);
    }
  });

  function hydrateForm(parsedSoul, parsedDraft = null) {
    const content = parsedDraft?.content || {};
    const identity = content.identity || {};
    const permissions = content.permissions || parsedSoul.permissions || {};
    const relays = content.relay_policy || parsedSoul.relayPolicy || {};
    const workspace = content.workspace || parsedSoul.workspaceSpec || {};
    const assets = content.assets || parsedSoul.assets || {};

    name = identity.name || parsedSoul.name || '';
    purpose = identity.purpose || parsedSoul.purpose || parsedSoul.content || '';
    tier = identity.tier || parsedSoul.tier || 'standard';
    nip05 = identity.nip05 || parsedSoul.nip05 || '';
    allowedKinds = formatKindList(permissions.allowed_kinds || parsedSoul.allowedKinds || []);
    toolGrants = formatToolGrantList(permissions.tool_grants || parsedSoul.tools || []);
    approvalPolicy = permissions.approval_policy || 'operator';
    readRelays = (relays.read || []).join('\n');
    writeRelays = (relays.write || []).join('\n');
    controlRelays = (relays.control || []).join('\n');
    nip65Discovery = relays.nip65_discovery ?? true;
    repo = workspace.repo || parsedSoul.workspace || '';
    branch = workspace.branch || 'main';
    environment = workspace.environment || 'production';
    avatarRef = assets.avatar_ref || parsedSoul.avatarUrl || '';
    voiceRef = assets.voice_ref || '';
    selectedCapabilityRef = content.runtime?.capability_ref || parsedSoul.capabilityRef || parsedSoul.runtime?.capability_ref || '';
    templateRef = content.template_ref || content.templateRef || parsedDraft?.templateRef || parsedSoul.templateRef || '';
  }

  async function loadSoulForEdit(id = agentId) {
    loading = true;
    error = '';

    try {
      await subscribeToSoulFactoryUpdates();

      const found = souls.find((s) => s.agentId === id);
      if (!found) throw new Error('Soul not found');

      soul = found;
      const draftCoordinate = soul.draftRef || '';
      const coordinateParts = draftCoordinate.match(/^31952:([^:]+):(.+)$/);
      const draftAgentId = coordinateParts?.[2] || soul.agentId;
      const draftAuthor = coordinateParts?.[1] || undefined;
      const draftEntry = drafts.find((d) => d.agentId === draftAgentId && (!draftAuthor || d.pubkey === draftAuthor));
      existingDraft = draftEntry || null;
      hydrateForm(soul, existingDraft);
    } catch (err) {
      error = err.message || 'Failed to load soul';
    } finally {
      loading = false;
    }
  }

  function buildDraftContent() {
    return {
      schema: 'soulfactory-draft/v1',
      brief: purpose,
      template_ref: templateRef,
      identity: {
        name,
        purpose,
        tier,
        nip05
      },
      runtime: {
        target: selectedCapability?.runtime || soul?.runtime?.target || '',
        runtime_pubkey: selectedCapability?.pubkey || soul?.runtime?.runtime_pubkey || '',
        capability_ref: selectedCapability ? capabilityRef(selectedCapability) : soul?.capabilityRef || ''
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
      workspace: { repo, branch, environment },
      assets: { avatar_ref: avatarRef, voice_ref: voiceRef },
      previous_spec_hash: soul?.specHash || ''
    };
  }

  async function saveChanges() {
    if (!soul || saving) return;

    saving = true;
    error = '';
    success = '';

    try {
      if (updateCapabilities.length === 0) {
        throw new Error('No compatible runtime capability has been discovered for soulfactory.update');
      }
      const id = soul.agentId || agentId;
      const draftContent = buildDraftContent();
      const draft = await publishSoulDraft({
        agentId: id,
        content: draftContent,
        templateRef,
        previousSpecHash: soul.specHash || ''
      });
      const draftCoordinate = `${KINDS.SOUL_DRAFT}:${draft.event.pubkey}:${id}`;

      await publishSoulUpdateAction({
        soul,
        draftRef: draftCoordinate,
        draftEventId: draft.event.id,
        resolvedSpec: draftContent,
        previousSpecHash: soul.specHash || '',
        newSpecHash: draft.specHash,
        updateMode: 'replace',
        reason
      });
      success = `Saved 31952 draft ${draft.event.id} and submitted update action. Completion requires an explicit 7950 result.`;
    } catch (err) {
      error = err.message || 'Failed to submit soul update';
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    const id = agentId;
    if (!id) return;
    void loadSoulForEdit(id);
  });
</script>

<svelte:head>
  <title>Edit Soul | Soul Factory</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <a href={`/souls/${agentId}`} class="back-link">← Back to Soul</a>
    <h1>Edit Runtime-Aware Soul Draft</h1>
    <p class="muted">Edits are saved as a new 31952 draft, then applied through a signed 1950 update action.</p>
  </header>

  {#if loading}
    <p class="muted">Loading soul...</p>
  {:else if error && !soul}
    <p class="error">{error}</p>
  {:else if soul}
    {#if error}<p class="error">{error}</p>{/if}
    {#if success}<p class="success">{success}</p>{/if}

    <form class="form" onsubmit={(event) => { event.preventDefault(); saveChanges(); }}>
      <section>
        <h2>Identity</h2>
        <label>Name<input type="text" bind:value={name} /></label>
        <label>Purpose<textarea rows="5" bind:value={purpose}></textarea></label>
        <div class="two-col">
          <label>Tier<select bind:value={tier}><option value="lightweight">Lightweight</option><option value="standard">Standard</option><option value="heavy">Heavy</option></select></label>
          <label>Profile identifier target<input type="text" bind:value={nip05} /></label>
        </div>
      </section>

      <section>
        <h2>Runtime</h2>
        {#if updateCapabilities.length === 0}
          <div class="warning">No compatible 30317 capability currently supports {SOUL_RUNTIME_METHODS.UPDATE}; update submission is disabled.</div>
        {:else}
          <label>Runtime capability<select bind:value={selectedCapabilityRef}>{#each updateCapabilities as capability}<option value={capabilityRef(capability)}>{capabilityLabel(capability)}</option>{/each}</select></label>
        {/if}
      </section>

      <section>
        <h2>Permissions</h2>
        <label>Allowed Nostr kinds<input bind:value={allowedKinds} /></label>
        <label>Tool grants<textarea rows="3" bind:value={toolGrants} placeholder="mcp-server: read, write"></textarea></label>
        <label>Approval policy<input bind:value={approvalPolicy} /></label>
      </section>

      <section>
        <h2>Relays, repository, assets</h2>
        <label><input type="checkbox" bind:checked={nip65Discovery} /> Use signer relay discovery</label>
        <label>Read relays<textarea rows="2" bind:value={readRelays}></textarea></label>
        <label>Write relays<textarea rows="2" bind:value={writeRelays}></textarea></label>
        <label>Control relays<textarea rows="2" bind:value={controlRelays}></textarea></label>
        <label>Repository/workspace<input bind:value={repo} /></label>
        <div class="two-col">
          <label>Branch<input bind:value={branch} /></label>
          <label>Environment<input bind:value={environment} /></label>
        </div>
        <label>Avatar ref<input bind:value={avatarRef} /></label>
        <label>Voice ref<input bind:value={voiceRef} /></label>
      </section>

      <label>Update reason<input type="text" bind:value={reason} /></label>

      <div class="actions">
        <button type="button" class="btn-secondary" onclick={() => goto(`/souls/${agentId}`)}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={saving || updateCapabilities.length === 0}>
          {saving ? 'Saving draft…' : 'Save Draft & Submit Update'}
        </button>
      </div>
    </form>
  {/if}
</div>

<style>
  .page { max-width: 820px; margin: 0 auto; }
  .page-header { margin-bottom: 1rem; }
  .back-link { color: var(--text-muted); text-decoration: none; font-size: 0.875rem; }
  .back-link:hover { color: var(--primary); }
  .form { display: grid; gap: 1rem; background: var(--card-bg); border: 1px solid var(--border-color); border-radius: 12px; padding: 1.25rem; }
  section { display: grid; gap: 0.8rem; }
  h2 { font-size: 1rem; margin: 0; padding-bottom: 0.4rem; border-bottom: 1px solid var(--border-color); }
  label { display: grid; gap: 0.4rem; font-size: 0.85rem; }
  input, select, textarea { background: var(--bg); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); padding: 0.6rem 0.75rem; font: inherit; }
  textarea { resize: vertical; }
  .two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.75rem; }
  .actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
  .btn-primary, .btn-secondary { border: none; border-radius: 8px; font-size: 0.875rem; cursor: pointer; padding: 0.55rem 0.95rem; }
  .btn-primary { background: var(--primary); color: #fff; }
  .btn-primary:disabled { opacity: 0.55; cursor: not-allowed; }
  .btn-secondary { background: transparent; color: var(--text-muted); border: 1px solid var(--border-color); }
  .error, .success, .muted { margin: 0 0 1rem; font-size: 0.85rem; }
  .error { color: var(--error); }
  .success { color: var(--success); }
  .muted { color: var(--text-muted); }
  .warning { background: rgba(245, 158, 11, 0.1); border: 1px solid rgba(245, 158, 11, 0.35); color: var(--warning); border-radius: 8px; padding: 0.75rem; }
  @media (max-width: 720px) { .two-col { grid-template-columns: 1fr; } }
</style>
