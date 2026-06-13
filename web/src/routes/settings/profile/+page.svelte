<script>
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { toast } from '$lib/components/toast.js';
  import { authState, isAuthenticated } from '$lib/stores/auth.js';
  import {
    profileFormFromMetadata,
    profileWriteRelayUrls,
    publishProfileMetadata,
    validateProfileMetadata
  } from '$lib/nostr/profile.js';

  let form = $state(profileFormFromMetadata(authState.profile));
  let fieldErrors = $state({});
  let saving = $state(false);
  let dirty = $state(false);
  let publishOutcome = $state(null);
  let publishError = $state('');

  const writableRelays = $derived(profileWriteRelayUrls(authState.relays));
  const authenticated = $derived(isAuthenticated());
  const canPublish = $derived(authenticated && writableRelays.length > 0 && !saving);

  $effect(() => {
    const profile = authState.profile;
    if (!dirty) {
      form = profileFormFromMetadata(profile);
    }
  });

  function markDirty() {
    dirty = true;
    publishOutcome = null;
    publishError = '';
  }

  function validateCurrentForm() {
    const result = validateProfileMetadata(form);
    fieldErrors = result.errors;
    return result;
  }

  async function saveProfile(event) {
    event?.preventDefault?.();
    publishError = '';
    publishOutcome = null;

    const validation = validateCurrentForm();
    if (!validation.valid) {
      toast.error('Fix profile validation errors before publishing');
      return;
    }

    saving = true;
    try {
      const result = await publishProfileMetadata(form);
      dirty = false;
      publishOutcome = result;
      const accepted = result.acceptedRelays.length;
      toast.success(`Profile metadata published to ${accepted} relay${accepted === 1 ? '' : 's'}`);
    } catch (error) {
      if (error?.validation) {
        fieldErrors = error.validation.errors;
      }
      publishError = error?.message || 'Failed to publish profile metadata';
      toast.error(publishError);
    } finally {
      saving = false;
    }
  }
</script>

<div class="page">
  <div class="header">
    <a class="back-link" href="/settings">← Settings</a>
    <h1>Nostr Profile</h1>
    <p class="subtitle">Edit your kind-0 metadata and publish it with the active NIP-07 or NIP-46 signer.</p>
  </div>

  <section class="settings-section profile-section">
    {#if !authenticated}
      <div class="status-card error" role="alert">
        Sign in with a Nostr signer before editing your profile metadata.
      </div>
    {:else if writableRelays.length === 0}
      <div class="status-card error" role="alert">
        No writable Nostr relays are available from your signer or NIP-65 relay list.
      </div>
    {:else}
      <div class="status-card" role="status">
        Publishing through your {authState.authMethod === 'nip46' ? 'remote signer' : 'browser signer'} to {writableRelays.length} writable relay{writableRelays.length === 1 ? '' : 's'}.
      </div>
    {/if}

    <form class="profile-form" onsubmit={saveProfile} novalidate>
      <div class="field-grid">
        <label class="field">
          <span>Name</span>
          <Input name="name" bind:value={form.name} error={fieldErrors.name} disabled={saving} oninput={markDirty} placeholder="alice" />
          {#if fieldErrors.name}<small class="validation-error">{fieldErrors.name}</small>{/if}
        </label>

        <label class="field">
          <span>Display name</span>
          <Input name="display_name" bind:value={form.display_name} error={fieldErrors.display_name} disabled={saving} oninput={markDirty} placeholder="Alice Example" />
          {#if fieldErrors.display_name}<small class="validation-error">{fieldErrors.display_name}</small>{/if}
        </label>
      </div>

      <label class="field">
        <span>About</span>
        <Textarea name="about" rows={4} bind:value={form.about} error={fieldErrors.about} disabled={saving} oninput={markDirty} placeholder="What should other Nostr clients show about you?" />
        <small class:warn={form.about.length > 450}>{form.about.length}/500 characters</small>
        {#if fieldErrors.about}<small class="validation-error">{fieldErrors.about}</small>{/if}
      </label>

      <div class="field-grid">
        <label class="field">
          <span>Picture URL</span>
          <Input name="picture" type="url" bind:value={form.picture} error={fieldErrors.picture} disabled={saving} oninput={markDirty} placeholder="https://example.com/avatar.png" />
          {#if fieldErrors.picture}<small class="validation-error">{fieldErrors.picture}</small>{/if}
        </label>

        <label class="field">
          <span>Banner URL</span>
          <Input name="banner" type="url" bind:value={form.banner} error={fieldErrors.banner} disabled={saving} oninput={markDirty} placeholder="https://example.com/banner.png" />
          {#if fieldErrors.banner}<small class="validation-error">{fieldErrors.banner}</small>{/if}
        </label>
      </div>

      <div class="field-grid">
        <label class="field">
          <span>Website</span>
          <Input name="website" type="url" bind:value={form.website} error={fieldErrors.website} disabled={saving} oninput={markDirty} placeholder="https://example.com" />
          {#if fieldErrors.website}<small class="validation-error">{fieldErrors.website}</small>{/if}
        </label>

        <label class="field">
          <span>NIP-05 identifier</span>
          <Input name="nip05" bind:value={form.nip05} error={fieldErrors.nip05} disabled={saving} oninput={markDirty} placeholder="alice@example.com" />
          {#if fieldErrors.nip05}<small class="validation-error">{fieldErrors.nip05}</small>{/if}
        </label>
      </div>

      <label class="field">
        <span>Lightning address</span>
        <Input name="lud16" bind:value={form.lud16} error={fieldErrors.lud16} disabled={saving} oninput={markDirty} placeholder="alice@example.com" />
        {#if fieldErrors.lud16}<small class="validation-error">{fieldErrors.lud16}</small>{/if}
      </label>

      <div class="actions">
        <LoadingButton type="submit" variant="primary" loading={saving} disabled={!canPublish}>Publish Profile Metadata</LoadingButton>
      </div>
    </form>

    {#if publishError}
      <div class="status-card error" role="alert">{publishError}</div>
    {/if}

    {#if publishOutcome}
      <div class="publish-results" role="status">
        <h2>Relay OK outcomes</h2>
        <p>Event <code>{publishOutcome.event.id}</code> was accepted by {publishOutcome.acceptedRelays.length} relay{publishOutcome.acceptedRelays.length === 1 ? '' : 's'}.</p>
        <ul>
          {#each publishOutcome.ok as result}
            <li class:accepted={result.accepted} class:rejected={!result.accepted}>
              <span>{result.relay}</span>
              <strong>{result.accepted ? 'OK accepted' : 'OK rejected'}</strong>
              {#if result.message}<small>{result.message}</small>{/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </section>
</div>

<style>
  .header {
    margin-bottom: 2rem;
  }

  .back-link {
    display: inline-flex;
    margin-bottom: 0.75rem;
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
  }

  .back-link:hover {
    color: var(--primary);
  }

  h1, h2 {
    margin: 0;
  }

  h1 {
    font-size: 1.75rem;
    font-weight: 600;
  }

  h2 {
    font-size: 1rem;
    font-weight: 600;
  }

  .subtitle {
    color: var(--text-muted);
    margin: 0.5rem 0 0 0;
  }

  .settings-section {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1.5rem;
  }

  .profile-section {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
  }

  .status-card {
    border: 1px solid var(--border-color);
    background: var(--bg);
    color: var(--text-secondary);
    border-radius: 6px;
    padding: 0.75rem 1rem;
    font-size: 0.875rem;
  }

  .status-card.error {
    border-color: var(--error);
    color: var(--error);
  }

  .profile-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .field-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    gap: 1rem;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    color: var(--text-primary);
    font-weight: 500;
  }

  small {
    color: var(--text-muted);
    font-weight: 400;
  }

  small.warn,
  .validation-error {
    color: var(--error);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
  }

  .publish-results {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 1rem;
    background: var(--bg);
  }

  .publish-results p {
    color: var(--text-secondary);
  }

  .publish-results ul {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .publish-results li {
    display: grid;
    gap: 0.25rem;
    border-left: 3px solid var(--border-color);
    padding-left: 0.75rem;
  }

  .publish-results li.accepted {
    border-left-color: var(--success);
  }

  .publish-results li.rejected {
    border-left-color: var(--error);
  }

  code {
    font-size: 0.85em;
  }
</style>
