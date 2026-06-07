<script>
  import RelaySettingsPanel from '$lib/components/settings/RelaySettingsPanel.svelte';
</script>

<div class="page">
  <div class="header">
    <a class="back-link" href="/settings">← Settings</a>
    <h1>Relay Settings</h1>
    <p class="subtitle">Manage persistent operator relay policy and local browser-session relay reconnects.</p>
  </div>

  <div class="settings-grid">
    <RelaySettingsPanel />
  </div>
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

  h1 {
    margin: 0;
    font-size: 1.75rem;
    font-weight: 600;
  }

  .subtitle {
    color: var(--text-muted);
    margin: 0.5rem 0 0 0;
  }

  .settings-grid {
    display: flex;
    flex-direction: column;
    gap: 2rem;
  }
</style>
