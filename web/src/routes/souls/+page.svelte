<script>
  import SoulCard from '$lib/components/SoulCard.svelte';
  import Card from '$lib/components/Card.svelte';
  import { nostr } from '$lib/nostr/client.js';
  import { 
    souls, 
    soulCounts, 
    loading, 
    error,
    loadSouls, 
    subscribeToSoulUpdates,
    unsubscribeFromSoulUpdates
  } from '$lib/stores/souls.js';
  
  let filter = $state('all');
  let search = $state('');
  
  let filteredSouls = $derived(souls.filter(soul => {
    // Status filter
    if (filter !== 'all' && soul.status !== filter) return false;
    
    // Search filter
    if (search) {
      const q = search.toLowerCase();
      return (
        soul.name?.toLowerCase().includes(q) ||
        soul.agentId?.toLowerCase().includes(q) ||
        soul.purpose?.toLowerCase().includes(q)
      );
    }
    
    return true;
  }));
  
  $effect(() => {
    let cancelled = false;

    async function initializeSouls() {
      await nostr.connect();
      if (cancelled) return;
      await loadSouls();
      if (cancelled) return;
      subscribeToSoulUpdates();
    }

    void initializeSouls();

    return () => {
      cancelled = true;
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
      <h1>Soul Gallery</h1>
      <p class="subtitle">Agent identities provisioned by Soul Factory</p>
    </div>
    <a href="/souls/new" class="btn-primary">
      <span class="icon">✨</span>
      New Soul
    </a>
  </header>
  
  <!-- Stats Cards -->
  <div class="stats-grid">
    <Card title="Total Souls" value={soulCounts().total} />
    <Card title="Active" value={soulCounts().active} status="success" />
    <Card title="Provisioning" value={soulCounts().provisioning} status="warning" />
    <Card title="Suspended" value={soulCounts().suspended} />
  </div>
  
  <!-- Filters -->
  <div class="filters">
    <div class="filter-tabs">
      <button 
        class="filter-tab" 
        class:active={filter === 'all'} 
        onclick={() => filter = 'all'}
      >
        All
      </button>
      <button 
        class="filter-tab" 
        class:active={filter === 'active'} 
        onclick={() => filter = 'active'}
      >
        Active
      </button>
      <button 
        class="filter-tab" 
        class:active={filter === 'provisioning'} 
        onclick={() => filter = 'provisioning'}
      >
        Provisioning
      </button>
      <button 
        class="filter-tab" 
        class:active={filter === 'suspended'} 
        onclick={() => filter = 'suspended'}
      >
        Suspended
      </button>
    </div>
    
    <div class="search-box">
      <input 
        type="text" 
        placeholder="Search souls..." 
        bind:value={search}
      />
    </div>
  </div>
  
  <!-- Error -->
  {#if $error}
    <div class="error-banner">
      <span class="icon">⚠️</span>
      {$error}
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
      <div class="empty-icon">🤖</div>
      <h3>No souls found</h3>
      <p>
        {#if search}
          No souls match your search. Try a different query.
        {:else if filter !== 'all'}
          No souls with status "{filter}".
        {:else}
          Get started by creating your first agent soul.
        {/if}
      </p>
      {#if !search && filter === 'all'}
        <a href="/souls/new" class="btn-primary">Create Soul</a>
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
    font-size: 4rem;
    margin-bottom: 1rem;
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
