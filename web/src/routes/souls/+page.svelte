<script>
  import SoulCard from '$lib/components/SoulCard.svelte';
  import Card from '$lib/components/Card.svelte';
  import {
    SoulIcon,
    SeedIcon,
    SuccessIcon,
    PendingIcon,
    WarningIcon,
    SuspendedIcon,
    EditIcon
  } from '$lib/icons/domain-icons.js';
  import {
    emptyStateMessage,
    filterSouls,
    unresolvedDrafts,
    SOUL_STATUS_FILTERS
  } from './page-model.js';
  import {
    souls,
    drafts,
    soulCounts,
    loading,
    error,
    runtimeCapabilities,
    subscribeToSoulFactoryUpdates,
    unsubscribeFromSoulUpdates
  } from '$lib/stores/souls.js';

  let filter = $state('all');
  let search = $state('');

  let filteredSouls = $derived(filterSouls(souls, filter, search));
  let savedDrafts = $derived(unresolvedDrafts(drafts, souls));
  
  $effect(() => {
    subscribeToSoulFactoryUpdates();

    return () => {
      unsubscribeFromSoulUpdates();
    };
  });
</script>

<svelte:head>
  <title>Soul Gallery | Bahia</title>
</svelte:head>

<div class="page">
  <header class="page-header">
    <div class="header-content">
      <h1><SoulIcon size={28} strokeWidth={1.75} ariaHidden="true" /> Soul Gallery</h1>
      <p class="subtitle">Agent identities provisioned by Soul Factory</p>
    </div>
    <a href="/souls/new" class="btn-primary">
      <SeedIcon size={18} strokeWidth={1.75} ariaHidden="true" />
      New
    </a>
  </header>
  
  <!-- Stats Cards -->
  <div class="stats-grid">
    <Card title="Total Souls" titleIcon={SoulIcon} value={soulCounts().total} />
    <Card title="Active" titleIcon={SuccessIcon} value={soulCounts().active} status="success" />
    <Card title="Provisioning" titleIcon={PendingIcon} value={soulCounts().provisioning} status="warning" />
    <Card title="Suspended" titleIcon={SuspendedIcon} value={soulCounts().suspended} />
    <Card title="Runtime Targets" titleIcon={SuccessIcon} value={runtimeCapabilities.filter((capability) => capability.compatible).length} />
  </div>

  <!-- Saved drafts -->
  {#if savedDrafts.length > 0}
    <section class="drafts-section">
      <div class="drafts-header">
        <h2><SeedIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Saved drafts</h2>
        <p>Signed 31952 drafts you can resume and provision later.</p>
      </div>
      <div class="drafts-list">
        {#each savedDrafts as draft (draft.agentId || draft.id)}
          <div class="draft-row">
            <div class="draft-info">
              <strong>{draft.name || draft.agentId || 'Untitled draft'}</strong>
              <span class="draft-meta">
                {draft.tier || 'standard'}
                {#if draft.agentId} · <code>{draft.agentId}</code>{/if}
                {#if draft.createdAt} · saved {new Date(draft.createdAt * 1000).toLocaleString()}{/if}
              </span>
            </div>
            <a class="btn-resume" href={`/souls/new?draft=${encodeURIComponent(draft.agentId || '')}`}>
              <EditIcon size={16} strokeWidth={1.75} ariaHidden="true" />
              Resume
            </a>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  <!-- Filters -->
  <div class="filters">
    <div class="filter-tabs">
      {#each SOUL_STATUS_FILTERS as option}
        <button
          class="filter-tab"
          class:active={filter === option.value}
          onclick={() => filter = option.value}
        >
          {option.label}
        </button>
      {/each}
    </div>
    
    <div class="search-box">
      <input 
        type="text" 
        placeholder="Search souls, runtimes, deploy states..." 
        bind:value={search}
      />
    </div>
  </div>
  
  <!-- Error -->
  {#if error.value}
    <div class="error-banner">
      <WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" />
      {error.value}
    </div>
  {/if}
  
  <!-- Loading -->
  {#if loading.souls}
    <div class="loading">
      <div class="spinner"></div>
      <span>Loading souls...</span>
    </div>
  {:else if filteredSouls.length === 0}
    <div class="empty-state">
      <div class="empty-icon" aria-hidden="true"><SoulIcon size={64} strokeWidth={1.5} /></div>
      <h3>No souls found</h3>
      <p>{emptyStateMessage(filter, search)}</p>
      {#if !search && filter === 'all'}
        <a href="/souls/new" class="btn-primary">
          <SeedIcon size={18} strokeWidth={1.75} ariaHidden="true" />
          New
        </a>
      {/if}
    </div>
  {:else}
    <!-- Soul Grid -->
    <div class="souls-grid">
      {#each filteredSouls as soul (soul.agentId)}
        <SoulCard {soul} />
      {/each}
    </div>
  {/if}
</div>

<style>
  .page {
    max-width: 1200px;
    margin: 0 auto;
  }
  
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: 2rem;
  }
  
  .header-content h1 {
    font-size: 1.75rem;
    font-weight: 700;
    margin: 0 0 0.25rem 0;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  
  .subtitle {
    color: var(--text-muted);
    margin: 0;
    font-size: 0.9rem;
  }
  
  .btn-primary {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    background: var(--primary, #6366f1);
    color: white;
    padding: 0.75rem 1.25rem;
    border-radius: 8px;
    text-decoration: none;
    font-weight: 500;
    transition: background 0.15s, transform 0.15s;
  }
  
  .btn-primary:hover {
    background: #5558e3;
    transform: translateY(-1px);
  }
  
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .drafts-section {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 1rem 1.25rem;
    margin-bottom: 2rem;
  }

  .drafts-header h2 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 1.05rem;
    margin: 0;
  }

  .drafts-header p {
    color: var(--text-muted);
    font-size: 0.85rem;
    margin: 0.25rem 0 0.75rem;
  }

  .drafts-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .draft-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
    padding: 0.65rem 0.85rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
  }

  .draft-info {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    min-width: 0;
  }

  .draft-meta {
    color: var(--text-muted);
    font-size: 0.8rem;
    overflow-wrap: anywhere;
  }

  .draft-meta code {
    background: var(--card-bg);
    border-radius: 4px;
    padding: 0.05rem 0.3rem;
  }

  .btn-resume {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex-shrink: 0;
    background: transparent;
    color: var(--text-primary);
    border: 1px solid var(--border-color);
    padding: 0.45rem 0.85rem;
    border-radius: 8px;
    text-decoration: none;
    font-size: 0.85rem;
    font-weight: 500;
    transition: border-color 0.15s, color 0.15s;
  }

  .btn-resume:hover {
    border-color: var(--primary);
    color: var(--primary);
  }
  
  .filters {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
    margin-bottom: 1.5rem;
    flex-wrap: wrap;
  }
  
  .filter-tabs {
    display: flex;
    gap: 0.25rem;
    background: var(--card-bg);
    padding: 0.25rem;
    border-radius: 8px;
  }
  
  .filter-tab {
    background: transparent;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 0.875rem;
    transition: all 0.15s;
  }
  
  .filter-tab:hover {
    color: var(--text-primary);
  }
  
  .filter-tab.active {
    background: var(--primary);
    color: white;
  }
  
  .search-box input {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    padding: 0.5rem 1rem;
    border-radius: 8px;
    color: var(--text-primary);
    width: 250px;
    font-size: 0.875rem;
  }
  
  .search-box input::placeholder {
    color: var(--text-muted);
  }
  
  .search-box input:focus {
    outline: none;
    border-color: var(--primary);
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
  
  .empty-state {
    text-align: center;
    padding: 4rem 2rem;
    color: var(--text-muted);
  }
  
  .empty-icon {
    color: var(--text-muted);
    margin-bottom: 1rem;
    opacity: 0.65;
    display: flex;
    justify-content: center;
  }
  
  .empty-state h3 {
    color: var(--text-primary);
    margin: 0 0 0.5rem 0;
  }
  
  .empty-state p {
    margin: 0 0 1.5rem 0;
    max-width: 400px;
    margin-left: auto;
    margin-right: auto;
  }
  
  .souls-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 1.25rem;
  }
</style>
