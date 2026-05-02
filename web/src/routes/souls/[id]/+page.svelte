<script>
  import { page } from '$app/state';
  import Badge from '$lib/components/Badge.svelte';
  import { nostr, fetchSoul, parseSoulEvent, KINDS } from '$lib/nostr/client.js';
  import { fetchSoulHistory, publishSoulAction } from '$lib/stores/souls.js';
  
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
  
  let agentId = $derived(page.params.id);
  
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
  
  async function loadSoul(id = agentId) {
    loading = true;
    error = null;
    
    try {
      const event = await fetchSoul(id);
      if (event) {
        soul = parseSoulEvent(event);
      } else {
        error = 'Soul not found';
      }
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }
  
  function subscribeToUpdates(id = agentId) {
    unsub = nostr.subscribe([{
      kinds: [KINDS.AGENT_SOUL],
      '#d': [id]
    }], {
      onEvent: (event) => {
        soul = parseSoulEvent(event);
        void loadHistory();
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
      await publishSoulAction({ soul, action, reason });
      actionNotice = `Action "${displayAction}" submitted to relays.`;
      await loadHistory();
    } catch (err) {
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
      await nostr.connect();
      if (cancelled) return;
      await loadSoul(id);
      if (cancelled) return;
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
      <span class="icon">😵</span>
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
              {#if soul.tier === 'lightweight'}⚡{:else if soul.tier === 'heavy'}🦾{:else}🤖{/if}
              {soul.tier}
            </span>
          </div>
          
          {#if soul.nip05}
            <span class="nip05">✓ {soul.nip05}</span>
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
        <div class="notice-banner">{actionNotice}</div>
      {/if}

      {#if actionError}
        <div class="error-banner">{actionError}</div>
      {/if}
      
      <!-- Info Grid -->
      <div class="info-grid">
        <!-- Identity Section -->
        <section class="info-section">
          <h3>🔐 Identity</h3>
          <dl>
            <dt>npub</dt>
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_element_to_interactive_role -->
            <dd class="copyable" onclick={copyNpub} onkeydown={copyNpub} role="button" tabindex="0" title="Click to copy">
              <code>{soul.npub || 'N/A'}</code>
              <span class="copy-icon">📋</span>
            </dd>
            
            <dt>Public Key</dt>
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_element_to_interactive_role -->
            <dd class="copyable" onclick={copyPubkey} onkeydown={copyPubkey} role="button" tabindex="0" title="Click to copy">
              <code>{soul.agentPubkey?.slice(0, 16)}...{soul.agentPubkey?.slice(-8) || 'N/A'}</code>
              <span class="copy-icon">📋</span>
            </dd>
            
            {#if soul.bahiaServiceId}
              <dt>Bahia Service</dt>
              <dd><code>{soul.bahiaServiceId.slice(0, 8)}...</code></dd>
            {/if}
          </dl>
        </section>
        
        <!-- Infrastructure Section -->
        <section class="info-section">
          <h3>🏗️ Infrastructure</h3>
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
          <h3>🛡️ Permissions</h3>
          
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
          <h3>📜 Soul Content</h3>
          <div class="soul-content">
            <pre>{soul.content || 'No soul content available'}</pre>
          </div>
        </section>

        <section class="info-section wide">
          <h3>🕰️ Activity & History</h3>
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
    font-size: 4rem;
    display: block;
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
  .error-banner {
    border-radius: 8px;
    padding: 0.75rem 1rem;
    margin-bottom: 1rem;
    font-size: 0.875rem;
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
  }
  
  .nip05 {
    font-size: 0.85rem;
    color: var(--success);
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
    font-size: 0.75rem;
    opacity: 0.5;
  }
  
  .copyable:hover .copy-icon {
    opacity: 1;
  }
  
  /* Permissions */
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
    
    .info-grid {
      grid-template-columns: 1fr;
    }
    
    .permissions-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
