<script>
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { nostr, fetchSoul, parseSoulEvent } from '$lib/nostr/client.js';
  import { updateSoulDetails } from '$lib/stores/souls.js';

  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let success = $state('');
  let soul = $state(null);

  let agentId = $derived(page.params.id);

  let name = $state('');
  let purpose = $state('');
  let tier = $state('standard');
  let brief = $state('');
  let reason = $state('Updated via Soul Gallery edit route');

  async function loadSoulForEdit(id = agentId) {
    loading = true;
    error = '';

    try {
      const event = await fetchSoul(id);
      if (!event) {
        throw new Error('Soul not found');
      }

      soul = parseSoulEvent(event);
      name = soul.name || '';
      purpose = soul.purpose || '';
      tier = soul.tier || 'standard';
      brief = soul.content || '';
    } catch (err) {
      error = err.message || 'Failed to load soul';
    } finally {
      loading = false;
    }
  }

  async function saveChanges() {
    if (!soul || saving) return;

    saving = true;
    error = '';
    success = '';

    try {
      await updateSoulDetails(soul, { name, purpose, tier, brief, reason });
      success = 'Soul update submitted to relays.';
    } catch (err) {
      error = err.message || 'Failed to submit soul update';
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    const id = agentId;
    if (!id) return;

    let cancelled = false;

    async function initialize() {
      await nostr.connect();
      if (cancelled) return;
      await loadSoulForEdit(id);
    }

    void initialize();

    return () => {
      cancelled = true;
    };
  });
</script>

<svelte:head>
  <title>Edit Soul | Soul Factory</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <a href={`/souls/${agentId}`} class="back-link">← Back to Soul</a>
    <h1>Edit Soul Details</h1>
  </header>

  {#if loading}
    <p class="muted">Loading soul...</p>
  {:else if error && !soul}
    <p class="error">{error}</p>
  {:else if soul}
    {#if error}
      <p class="error">{error}</p>
    {/if}
    {#if success}
      <p class="success">{success}</p>
    {/if}

    <form class="form" onsubmit={(event) => { event.preventDefault(); saveChanges(); }}>
      <label>
        Name
        <input type="text" bind:value={name} />
      </label>

      <label>
        Purpose
        <input type="text" bind:value={purpose} />
      </label>

      <label>
        Tier
        <select bind:value={tier}>
          <option value="lightweight">Lightweight</option>
          <option value="standard">Standard</option>
          <option value="heavy">Heavy</option>
        </select>
      </label>

      <label>
        Brief / Soul content
        <textarea rows="8" bind:value={brief}></textarea>
      </label>

      <label>
        Update reason
        <input type="text" bind:value={reason} />
      </label>

      <div class="actions">
        <button type="button" class="btn-secondary" onclick={() => goto(`/souls/${agentId}`)}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={saving}>
          {saving ? 'Submitting…' : 'Submit Update'}
        </button>
      </div>
    </form>
  {/if}
</div>

<style>
  .page {
    max-width: 760px;
    margin: 0 auto;
  }

  .page-header {
    margin-bottom: 1rem;
  }

  .back-link {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
  }

  .back-link:hover {
    color: var(--primary);
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 1.25rem;
  }

  label {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    font-size: 0.85rem;
  }

  input,
  select,
  textarea {
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    color: var(--text-primary);
    padding: 0.6rem 0.75rem;
    font-size: 0.9rem;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }

  .btn-primary,
  .btn-secondary {
    border: none;
    border-radius: 8px;
    font-size: 0.875rem;
    cursor: pointer;
    padding: 0.55rem 0.95rem;
  }

  .btn-primary {
    background: var(--primary);
    color: #fff;
  }

  .btn-secondary {
    background: transparent;
    color: var(--text-muted);
    border: 1px solid var(--border-color);
  }

  .error,
  .success,
  .muted {
    margin: 0 0 1rem;
    font-size: 0.85rem;
  }

  .error {
    color: var(--error);
  }

  .success {
    color: var(--success);
  }

  .muted {
    color: var(--text-muted);
  }
</style>
