<script>
  import { onMount } from 'svelte';
  import { acceptInvite as acceptPrivateInvite, loadOrgsOverview } from '$lib/stores/orgs.svelte.js';
  import { toast } from '$lib/components/toast.js';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  let orgs = $state([]);
  let myInvites = $state([]);
  let loading = $state(true);
  let error = $state(null);
  let loadGeneration = 0;

  onMount(() => {
    void loadData();
  });

  function describeLoadError(message) {
    const raw = String(message || 'Unknown error');
    if (/method not found|not implemented|unknown method|not available/i.test(raw)) {
      return 'Organizations aren’t available yet — the control-plane service hasn’t enabled this method. Retry once it has been deployed.';
    }
    return raw;
  }

  async function loadData() {
    const generation = ++loadGeneration;
    loading = true;
    error = null;
    try {
      const data = await loadOrgsOverview();
      if (generation !== loadGeneration) return;
      orgs = data.orgs;
      myInvites = data.myInvites;
    } catch (e) {
      if (generation !== loadGeneration) return;
      // Surface a single, inline error with a Retry affordance instead of an
      // additional transient toast so recovery is obvious once the backend lands.
      error = describeLoadError(e?.message);
    } finally {
      if (generation === loadGeneration) {
        loading = false;
      }
    }
  }

  async function acceptInvite(invite) {
    try {
      await acceptPrivateInvite(invite.id);
      toast.success(`Joined ${invite.org_name || 'organization'}`);
      await loadData();
    } catch (e) {
      toast.error(`Failed to accept invite: ${e.message}`);
    }
  }

  function getRoleBadgeType(role) {
    switch (role) {
      case 'owner': return 'primary';
      case 'admin': return 'warning';
      case 'deployer': return 'success';
      default: return 'default';
    }
  }
</script>

<svelte:head>
  <title>Organizations | Bahia</title>
</svelte:head>

<div class="page-header">
  <h1>Organizations</h1>
  <a href="/orgs/new" class="btn-primary">+ New Organization</a>
</div>

{#if loading}
  <div class="loading">Loading...</div>
{:else if error}
  <div class="error-state">
    <p>{error}</p>
    <button onclick={loadData}>Retry</button>
  </div>
{:else}
  {#if myInvites.length > 0}
    <section class="invites-section">
      <h2>Pending Invites</h2>
      <div class="invites-list">
        {#each myInvites as invite}
          <Card>
            <div class="invite-card">
              <div class="invite-info">
                <strong>{invite.org_display_name || invite.org_name}</strong>
                <Badge variant={getRoleBadgeType(invite.role)}>{invite.role}</Badge>
              </div>
              <button class="btn-success" onclick={() => acceptInvite(invite)}>
                Accept
              </button>
            </div>
          </Card>
        {/each}
      </div>
    </section>
  {/if}

  <section class="orgs-section">
    <h2>Your Organizations</h2>
    {#if orgs.length === 0}
      <EmptyState
        title="No organizations yet"
        message="Create your first organization to manage members, roles, and deployment access."
      />
    {:else}
      <div class="orgs-grid">
        {#each orgs as org}
          <a href="/orgs/{org.id}" class="org-card">
            <Card>
              <div class="org-content">
                <h3>{org.display_name || org.name}</h3>
                <p class="org-name">@{org.name}{org.nip05_domain ? ` · ${org.nip05_domain}` : ''}</p>
                <Badge variant={getRoleBadgeType(org.role)}>{org.role}</Badge>
              </div>
            </Card>
          </a>
        {/each}
      </div>
    {/if}
  </section>
{/if}

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 2rem;
  }

  h1 {
    font-size: 1.75rem;
    font-weight: 600;
  }

  h2 {
    font-size: 1.25rem;
    font-weight: 500;
    margin-bottom: 1rem;
    color: var(--text-muted);
  }

  .btn-primary {
    background: var(--primary);
    color: white;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    text-decoration: none;
    font-size: 0.875rem;
    transition: filter 0.15s;
  }

  .btn-primary:hover {
    filter: brightness(1.1);
  }

  .btn-success {
    background: var(--success);
    color: white;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.875rem;
  }

  .loading {
    text-align: center;
    color: var(--text-muted);
    padding: 2rem;
  }

  .error-state {
    text-align: center;
    padding: 2rem;
    color: var(--error);
  }

  .invites-section {
    margin-bottom: 2rem;
  }

  .invites-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .invite-card {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .invite-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .orgs-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 1rem;
  }

  .org-card {
    text-decoration: none;
    color: inherit;
    transition: transform 0.15s;
  }

  .org-card:hover {
    transform: translateY(-2px);
  }

  .org-content h3 {
    font-size: 1.125rem;
    margin-bottom: 0.25rem;
  }

  .org-name {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin-bottom: 0.5rem;
  }
</style>
