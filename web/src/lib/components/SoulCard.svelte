<script>
  let { soul } = $props();

  const statusColors = {
    active: 'success',
    provisioning: 'warning',
    suspended: 'muted',
    revoked: 'error',
    draft: 'muted'
  };

  const deployStatusColors = {
    deployed: 'success',
    healthy: 'success',
    deploying: 'warning',
    failed: 'error',
    unhealthy: 'error',
    stopped: 'muted',
    pending: 'muted'
  };

  const tierIcons = {
    lightweight: '⚡',
    standard: '🤖',
    heavy: '🦾'
  };

  const statusColor = $derived(statusColors[soul.status] || 'default');
  const deployColor = $derived(deployStatusColors[soul.deployStatus] || 'muted');
</script>

<a href="/souls/{soul.agentId}" class="soul-card {statusColor}">
  <div class="soul-header">
    <div class="avatar">
      {#if soul.avatarUrl}
        <img src={soul.avatarUrl} alt={soul.name} />
      {:else}
        <div class="avatar-placeholder">{soul.name?.[0] || '?'}</div>
      {/if}
    </div>
    <div class="soul-info">
      <h3 class="soul-name">{soul.name || soul.agentId}</h3>
      <span class="soul-id">@{soul.agentId}</span>
    </div>
    <span class="tier-badge" title={soul.tier}>
      {tierIcons[soul.tier] || '🤖'}
    </span>
  </div>
  
  <p class="soul-purpose">{soul.purpose || 'No description'}</p>
  
  <div class="soul-footer">
    <div class="status-badges">
      <span class="badge {statusColor}">{soul.status}</span>
      {#if soul.deployStatus}
        <span class="badge {deployColor}">{soul.deployStatus}</span>
      {/if}
    </div>
    <div class="soul-meta">
      {#if soul.nip05}
        <span class="nip05">{soul.nip05}</span>
      {/if}
    </div>
  </div>
</a>

<style>
  .soul-card {
    display: block;
    background: var(--card-bg, #1a1a2e);
    border-radius: 12px;
    padding: 1.25rem;
    border: 1px solid var(--border-color, #2a2a4a);
    text-decoration: none;
    color: inherit;
    transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
  }
  
  .soul-card:hover {
    transform: translateY(-2px);
    border-color: var(--primary, #6366f1);
    box-shadow: 0 4px 20px rgba(99, 102, 241, 0.15);
  }
  
  .soul-card.success { border-left: 3px solid var(--success, #10b981); }
  .soul-card.warning { border-left: 3px solid var(--warning, #f59e0b); }
  .soul-card.error { border-left: 3px solid var(--error, #ef4444); }
  .soul-card.muted { border-left: 3px solid var(--text-muted, #888); }
  
  .soul-header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 0.75rem;
  }
  
  .avatar {
    width: 48px;
    height: 48px;
    border-radius: 50%;
    overflow: hidden;
    flex-shrink: 0;
  }
  
  .avatar img {
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
    font-size: 1.25rem;
    font-weight: bold;
    color: white;
    text-transform: uppercase;
  }
  
  .soul-info {
    flex: 1;
    min-width: 0;
  }
  
  .soul-name {
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary, #e5e5e5);
    margin: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  
  .soul-id {
    font-size: 0.75rem;
    color: var(--text-muted, #888);
  }
  
  .tier-badge {
    font-size: 1.5rem;
    flex-shrink: 0;
  }
  
  .soul-purpose {
    font-size: 0.875rem;
    color: var(--text-muted, #888);
    margin: 0 0 1rem 0;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    line-height: 1.4;
  }
  
  .soul-footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.5rem;
  }
  
  .status-badges {
    display: flex;
    gap: 0.5rem;
  }
  
  .badge {
    font-size: 0.7rem;
    padding: 0.2rem 0.5rem;
    border-radius: 4px;
    text-transform: uppercase;
    font-weight: 500;
    letter-spacing: 0.03em;
  }
  
  .badge.success { background: rgba(16, 185, 129, 0.15); color: #10b981; }
  .badge.warning { background: rgba(245, 158, 11, 0.15); color: #f59e0b; }
  .badge.error { background: rgba(239, 68, 68, 0.15); color: #ef4444; }
  .badge.muted { background: rgba(136, 136, 136, 0.15); color: #888; }
  
  .soul-meta {
    font-size: 0.75rem;
    color: var(--text-muted, #888);
  }
  
  .nip05 {
    font-family: monospace;
  }
</style>
