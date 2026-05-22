<script>
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RepositoryCard from './RepositoryCard.svelte';
  import {
    EditIcon,
    EmptyIcon,
    RepositoryIcon,
    SearchIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';
  import {
    repositories,
    loading,
    error,
    loadRepositories,
    filterRepositories,
    createManualRepositorySelection,
    createNip34RepositorySelection
  } from '$lib/stores/repositories.js';

  let {
    open = false,
    requirePrimaryUrl = false,
    allowManual = true,
    onSelect,
    onClose
  } = $props();

  let activeTab = $state('nostr');
  let searchQuery = $state('');
  let manualUrl = $state('');
  let selectedRepo = $state(null);
  let hasLoadedOnce = $state(false);

  const filteredRepos = $derived(
    filterRepositories(repositories, searchQuery, {
      requirePrimaryUrl: false
    })
  );

  $effect(() => {
    if (open && !hasLoadedOnce) {
      hasLoadedOnce = true;
      loadRepositories();
    }
  });

  function isRepoDisabled(repo) {
    return requirePrimaryUrl && !repo?.primaryUrl;
  }

  function getDisabledReason(repo) {
    if (requirePrimaryUrl && !repo?.primaryUrl) {
      return 'No clone or web URL available';
    }
    return '';
  }

  function handleRepoSelect(repo) {
    if (isRepoDisabled(repo)) return;
    selectedRepo = repo;
  }

  function confirmNostrSelection() {
    if (!selectedRepo) return;
    const selection = createNip34RepositorySelection(selectedRepo);
    onSelect?.(selection);
    resetAndClose();
  }

  function confirmManualEntry() {
    const trimmed = (manualUrl || '').trim();
    if (!trimmed) return;
    const selection = createManualRepositorySelection(trimmed);
    onSelect?.(selection);
    resetAndClose();
  }

  function handleClose() {
    resetAndClose();
  }

  function resetAndClose() {
    searchQuery = '';
    manualUrl = '';
    selectedRepo = null;
    activeTab = 'nostr';
    onClose?.();
  }

  function switchTab(tab) {
    activeTab = tab;
    selectedRepo = null;
  }
</script>

<Modal {open} title="Choose Repository" titleIcon={RepositoryIcon} size="lg" onClose={handleClose}>
  <form onsubmit={(event) => event.preventDefault()} class="search-modal" novalidate>
    <div class="tabs">
      <button
        class="tab"
        class:active={activeTab === 'nostr'}
        onclick={() => switchTab('nostr')}
      >
        <SearchIcon size={16} strokeWidth={1.75} ariaHidden="true" />
        Nostr Repositories
      </button>
      {#if allowManual}
        <button
          class="tab"
          class:active={activeTab === 'manual'}
          onclick={() => switchTab('manual')}
        >
          <EditIcon size={16} strokeWidth={1.75} ariaHidden="true" />
          Manual Entry
        </button>
      {/if}
    </div>

    {#if activeTab === 'nostr'}
      <div class="tab-content">
        <div class="search-bar">
          <Input
            type="search"
            placeholder="Search repositories..."
            bind:value={searchQuery}
          />
        </div>

        {#if loading.list}
          <div class="loading">
            <div class="spinner"></div>
            Loading repositories...
          </div>
        {:else if error.value}
          <EmptyState
            iconComponent={WarningIcon}
            title="Failed to load repositories"
            message={error.value}
            actionLabel="Retry"
            onAction={() => loadRepositories({ force: true })}
          />
        {:else if filteredRepos.length === 0}
          <EmptyState
            iconComponent={EmptyIcon}
            title={searchQuery ? 'No matching repositories' : 'No repositories found'}
            message={searchQuery
              ? 'Try a different search term or add a repository manually.'
              : 'No NIP-34 repository announcements found on connected relays.'}
          />
        {:else}
          <div class="repo-list">
            {#each filteredRepos as repo (repo.repoCoordinate || repo.id)}
              <RepositoryCard
                repository={repo}
                selected={selectedRepo?.repoCoordinate === repo.repoCoordinate || selectedRepo?.id === repo.id}
                disabled={isRepoDisabled(repo)}
                disabledReason={getDisabledReason(repo)}
                onSelect={handleRepoSelect}
              />
            {/each}
          </div>
        {/if}

        <div class="actions">
          <LoadingButton
            variant="secondary"
            onclick={handleClose}
          >
            Cancel
          </LoadingButton>
          <LoadingButton
            variant="primary"
            disabled={!selectedRepo}
            onclick={confirmNostrSelection}
          >
            Select Repository
          </LoadingButton>
        </div>
      </div>
    {/if}

    {#if activeTab === 'manual'}
      <div class="tab-content">
        <p class="manual-hint">Enter a repository URL directly (clone URL or web URL).</p>
        <Input
          placeholder="https://github.com/user/repo.git"
          bind:value={manualUrl}
        />

        <div class="actions">
          <LoadingButton
            variant="secondary"
            onclick={handleClose}
          >
            Cancel
          </LoadingButton>
          <LoadingButton
            variant="primary"
            disabled={!(manualUrl || '').trim()}
            onclick={confirmManualEntry}
          >
            Use This URL
          </LoadingButton>
        </div>
      </div>
    {/if}
  </form>
</Modal>

<style>
  .search-modal {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border-color);
    margin: -1.5rem -1.5rem 0 -1.5rem;
    padding: 0 1.5rem;
  }

  .tab {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.75rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.15s;
  }

  .tab:hover {
    color: var(--text-primary);
  }

  .tab.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  .tab-content {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .search-bar {
    position: sticky;
    top: 0;
    z-index: 1;
  }

  .repo-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-height: 400px;
    overflow-y: auto;
  }

  .loading {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    color: var(--text-muted);
    padding: 2rem;
    justify-content: center;
  }

  .spinner {
    width: 20px;
    height: 20px;
    border: 2px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .manual-hint {
    font-size: 0.875rem;
    color: var(--text-muted);
    margin: 0;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--border-color);
  }
</style>
