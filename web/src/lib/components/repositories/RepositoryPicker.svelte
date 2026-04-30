<script>
  import { createEventDispatcher } from 'svelte';
  import Badge from '$lib/components/Badge.svelte';
  import RepositorySearchModal from './RepositorySearchModal.svelte';

  export let value = null;
  export let label = 'Source Repository';
  export let placeholder = 'No repository selected';
  export let required = false;
  export let disabled = false;
  export let context = 'service';
  export let requirePrimaryUrl = context === 'service';

  const dispatch = createEventDispatcher();

  let modalOpen = false;

  $: hasValue = value && (value.repoUrl || value.displayName);
  $: providerLabel = value?.provider === 'nostr' ? 'Nostr' : value?.provider === 'manual' ? 'Manual' : '';
  $: providerVariant = value?.provider === 'nostr' ? 'info' : 'default';

  function openModal() {
    if (!disabled) {
      modalOpen = true;
    }
  }

  function handleSelect(event) {
    value = event.detail;
    modalOpen = false;
    dispatch('change', value);
  }

  function handleModalClose() {
    modalOpen = false;
  }

  function clearSelection() {
    value = null;
    dispatch('change', null);
  }
</script>

<div class="repo-picker">
  {#if label}
    <label class="picker-label">
      {label}
      {#if required}
        <span class="required">*</span>
      {/if}
    </label>
  {/if}

  <div class="picker-display" class:disabled class:has-value={hasValue}>
    {#if hasValue}
      <div class="selection-summary">
        <div class="selection-icon">📦</div>
        <div class="selection-info">
          <div class="selection-header">
            <span class="selection-name">{value.displayName}</span>
            {#if providerLabel}
              <Badge variant={providerVariant} size="sm">{providerLabel}</Badge>
            {/if}
          </div>
          {#if value.repoUrl && value.repoUrl !== value.displayName}
            <span class="selection-url">{value.repoUrl}</span>
          {/if}
        </div>
        <div class="selection-actions">
          <button class="action-link" on:click={openModal} {disabled}>Change</button>
          <button class="action-link danger" on:click={clearSelection} {disabled}>Clear</button>
        </div>
      </div>
    {:else}
      <div class="empty-display">
        <span class="placeholder-text">{placeholder}</span>
        <button
          class="browse-button"
          on:click={openModal}
          {disabled}
        >
          Choose Repository
        </button>
      </div>
    {/if}
  </div>
</div>

<RepositorySearchModal
  open={modalOpen}
  {value}
  {requirePrimaryUrl}
  {context}
  on:select={handleSelect}
  on:close={handleModalClose}
/>

<style>
  .repo-picker {
    width: 100%;
  }

  .picker-label {
    display: block;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
    margin-bottom: 0.5rem;
  }

  .required {
    color: var(--error, #ef4444);
  }

  .picker-display {
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 0.75rem 1rem;
    background: var(--card-bg);
    transition: border-color 0.15s;
  }

  .picker-display:not(.disabled):hover {
    border-color: var(--primary);
  }

  .picker-display.disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .selection-summary {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .selection-icon {
    font-size: 1.25rem;
    flex-shrink: 0;
    width: 36px;
    height: 36px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(99, 102, 241, 0.1);
    border-radius: 6px;
  }

  .selection-info {
    flex: 1;
    min-width: 0;
  }

  .selection-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .selection-name {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .selection-url {
    font-size: 0.75rem;
    color: var(--text-muted);
    font-family: monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
    margin-top: 0.125rem;
  }

  .selection-actions {
    display: flex;
    gap: 0.5rem;
    flex-shrink: 0;
  }

  .action-link {
    background: none;
    border: none;
    font-size: 0.8rem;
    color: var(--primary);
    cursor: pointer;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    transition: background-color 0.15s;
  }

  .action-link:hover:not(:disabled) {
    background: var(--hover-bg);
  }

  .action-link:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .action-link.danger {
    color: var(--error, #ef4444);
  }

  .empty-display {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  .placeholder-text {
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .browse-button {
    padding: 0.375rem 0.75rem;
    font-size: 0.8rem;
    font-weight: 500;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--card-bg);
    color: var(--text-primary);
    cursor: pointer;
    transition: background-color 0.15s;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .browse-button:hover:not(:disabled) {
    background: var(--hover-bg);
    border-color: var(--primary);
  }

  .browse-button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
