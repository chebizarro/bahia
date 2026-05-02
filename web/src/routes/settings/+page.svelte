<script>
  import Card from '$lib/components/Card.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { api } from '$lib/api/client.js';
  import { nostr, saveRelayConfig, getDefaultRelays } from '$lib/nostr/client.js';
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import { toast } from '$lib/components/toast.js';
  import { authState, loginWithNostrConnect, canUseNostrConnectUri } from '$lib/stores/auth.js';

  // System info from server
  let systemInfo = $state(null);
  let systemLoading = $state(true);
  let systemError = $state(null);

  // Relay configuration (client-side)
  let relayInput = $state('');
  let relays = $state([]);
  let relaysSaving = $state(false);
  let connectionStatus = $state({});

  $effect(() => {
    const unsubscribe = nostr.connectionStatus.subscribe(status => {
      connectionStatus = status;
    });

    // Load current relay config
    relays = nostr.getRelays();
    void loadSystemInfo();

    return () => {
      unsubscribe();
    };
  });

  async function loadSystemInfo() {
    // Load system info from server
    try {
      systemInfo = await api.getSystemInfo();
    } catch (err) {
      systemError = err?.message || 'Failed to load system info';
    } finally {
      systemLoading = false;
    }
  }

  function addRelay() {
    const url = relayInput.trim();
    if (!url) return;
    
    // Validate URL format
    if (!url.startsWith('wss://') && !url.startsWith('ws://')) {
      toast.error('Relay URL must start with wss:// or ws://');
      return;
    }
    
    if (relays.includes(url)) {
      toast.warning('Relay already in list');
      return;
    }
    
    relays = [...relays, url];
    relayInput = '';
  }

  function removeRelay(url) {
    relays = relays.filter(r => r !== url);
  }

  async function saveRelays() {
    relaysSaving = true;
    try {
      saveRelayConfig(relays);
      nostr.setRelays(relays, true);
      await nostr.connect(relays);
      toast.success('Relay configuration saved');
    } catch (err) {
      toast.error('Failed to save relay configuration');
    } finally {
      relaysSaving = false;
    }
  }

  function resetToDefaults() {
    relays = getDefaultRelays();
  }

  function getStatusColor(status) {
    switch (status) {
      case 'connected': return 'var(--success)';
      case 'connecting': return 'var(--warning)';
      case 'error':
      case 'failed':
      case 'disconnected': return 'var(--error)';
      default: return 'var(--text-muted)';
    }
  }

  function getStatusLabel(status) {
    switch (status) {
      case 'connected': return 'Connected';
      case 'connecting': return 'Connecting...';
      case 'error': return 'Error';
      case 'failed': return 'Failed';
      case 'disconnected': return 'Disconnected';
      default: return 'Unknown';
    }
  }

  let nostrConnectUri = $state('');
  let nostrConnectLoading = $state(false);

  async function connectNostrConnect() {
    const uri = nostrConnectUri.trim();
    if (!uri) {
      toast.error('Enter a nostrconnect:// URI');
      return;
    }

    nostrConnectLoading = true;
    try {
      await canUseNostrConnectUri(uri);
      await loginWithNostrConnect(uri);
      nostrConnectUri = '';
      toast.success('Nostr Connect session saved');
    } catch (err) {
      toast.error(err?.message || 'Failed to connect Nostr signer');
    } finally {
      nostrConnectLoading = false;
    }
  }

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text).then(() => {
      toast.success('Copied to clipboard');
    }).catch(() => {
      toast.error('Failed to copy');
    });
  }
</script>

<div class="page">
  <div class="header">
    <h1>Settings</h1>
    <p class="subtitle">Configure your Bahia instance</p>
  </div>

  <div class="settings-grid">
    <!-- Nostr Relays Section -->
    <section class="settings-section">
      <h2>🔗 Nostr Relays</h2>
      <p class="section-description">
        Configure which Nostr relays this app connects to for fetching repositories and publishing events.
      </p>

      <div class="relay-list">
        {#each relays as relay}
          <div class="relay-item">
            <span class="relay-status" style="background: {getStatusColor(connectionStatus[relay])}"></span>
            <span class="relay-url">{relay}</span>
            <span class="relay-status-text">{getStatusLabel(connectionStatus[relay])}</span>
            <button class="relay-remove" onclick={() => removeRelay(relay)} title="Remove relay">×</button>
          </div>
        {/each}
      </div>

      <div class="relay-add">
        <Input
          placeholder="wss://relay.example.com"
          bind:value={relayInput}
          onkeydown={(e) => e.key === 'Enter' && addRelay()}
        />
        <LoadingButton variant="secondary" onclick={addRelay}>Add</LoadingButton>
      </div>

      <div class="relay-actions">
        <LoadingButton variant="secondary" onclick={resetToDefaults}>Reset to Defaults</LoadingButton>
        <LoadingButton variant="primary" loading={relaysSaving} onclick={saveRelays}>Save & Reconnect</LoadingButton>
      </div>
    </section>

    <!-- NIP-46 Nostr Connect -->
    <section class="settings-section">
      <h2>🔐 Nostr Connect (NIP-46)</h2>
      <p class="section-description">
        Paste a <code>nostrconnect://</code> URI to use a remote signer session.
      </p>

      <div class="relay-add">
        <Input
          placeholder="nostrconnect://<pubkey>?relay=wss://...&secret=..."
          bind:value={nostrConnectUri}
          onkeydown={(e) => e.key === 'Enter' && connectNostrConnect()}
        />
        <LoadingButton variant="primary" loading={nostrConnectLoading} onclick={connectNostrConnect}>Connect</LoadingButton>
      </div>
      <p class="section-description">
        Provider status: {authState.nip46Available ? 'available' : 'not detected'}
      </p>
    </section>

    <!-- Theme Section -->
    <section class="settings-section">
      <h2>🎨 Appearance</h2>
      <p class="section-description">Customize the look and feel of the application.</p>

      <div class="theme-option">
        <span class="theme-label">Theme</span>
        <div class="theme-toggle-group">
          <button
            class="theme-btn"
            class:active={theme.value === 'light'}
            onclick={() => theme.value !== 'light' && toggleTheme()}
          >
            ☀️ Light
          </button>
          <button
            class="theme-btn"
            class:active={theme.value === 'dark'}
            onclick={() => theme.value !== 'dark' && toggleTheme()}
          >
            🌙 Dark
          </button>
        </div>
      </div>
    </section>

    <!-- Server Configuration Section -->
    <section class="settings-section">
      <h2>⚙️ Server Configuration</h2>
      <p class="section-description">
        Server-side configuration (read-only). These settings are configured on the Bahia server.
      </p>

      {#if systemLoading}
        <div class="loading">Loading server configuration...</div>
      {:else if systemError}
        <div class="error-box">{systemError}</div>
      {:else if systemInfo}
        <!-- Nostr Server Config -->
        <div class="config-group">
          <h3>Nostr</h3>
          {#if systemInfo.nostr?.service_npub}
            <div class="config-row">
              <span class="config-label">Service Identity</span>
              <button type="button" class="config-value monospace clickable" onclick={() => copyToClipboard(systemInfo.nostr.service_npub)} title="Click to copy">
                {systemInfo.nostr.service_npub.slice(0, 20)}...
              </button>
            </div>
          {/if}
          {#if systemInfo.nostr?.relays?.length > 0}
            <div class="config-row">
              <span class="config-label">Server Relays</span>
              <span class="config-value">
                {systemInfo.nostr.relays.join(', ')}
              </span>
            </div>
          {/if}
          <div class="config-row">
            <span class="config-label">Publishing</span>
            <span class="config-value">
              {systemInfo.nostr?.publish_enabled ? '✓ Enabled' : '✗ Disabled'}
            </span>
          </div>
        </div>

        <!-- Blossom Config -->
        {#if systemInfo.blossom}
          <div class="config-group">
            <h3>Blossom Storage</h3>
            <div class="config-row">
              <span class="config-label">Status</span>
              <span class="config-value">
                {systemInfo.blossom.enabled ? '✓ Enabled' : '✗ Disabled'}
              </span>
            </div>
            {#if systemInfo.blossom.enabled}
              {#if systemInfo.blossom.url}
                <div class="config-row">
                  <span class="config-label">Primary Server</span>
                  <span class="config-value monospace">{systemInfo.blossom.url}</span>
                </div>
              {/if}
              {#if systemInfo.blossom.servers?.length > 0}
                <div class="config-row">
                  <span class="config-label">Servers</span>
                  <span class="config-value">{systemInfo.blossom.servers.join(', ')}</span>
                </div>
              {/if}
            {/if}
          </div>
        {/if}

        <!-- OCI Registry Config -->
        {#if systemInfo.oci}
          <div class="config-group">
            <h3>Container Registry</h3>
            <div class="config-row">
              <span class="config-label">Native Registry</span>
              <span class="config-value">
                {systemInfo.oci.enabled ? '✓ Enabled' : '✗ Disabled'}
              </span>
            </div>
            {#if systemInfo.oci.enabled && systemInfo.oci.public_host}
              <div class="config-row">
                <span class="config-label">Public Host</span>
                <span class="config-value monospace">{systemInfo.oci.public_host}</span>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Runtime Config -->
        {#if systemInfo.runtime}
          <div class="config-group">
            <h3>Runtime</h3>
            <div class="config-row">
              <span class="config-label">Type</span>
              <span class="config-value">{systemInfo.runtime.type || 'Not configured'}</span>
            </div>
            {#if systemInfo.runtime.environments?.length > 0}
              <div class="config-row">
                <span class="config-label">Environments</span>
                <span class="config-value">{systemInfo.runtime.environments.join(', ')}</span>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Feature Flags -->
        {#if systemInfo.features}
          <div class="config-group">
            <h3>Features</h3>
            <div class="features-grid">
              {#each Object.entries(systemInfo.features) as [feature, enabled]}
                <div class="feature-badge" class:enabled>
                  {enabled ? '✓' : '✗'} {feature}
                </div>
              {/each}
            </div>
          </div>
        {/if}
      {/if}
    </section>

    <!-- Available Registries Section -->
    {#if systemInfo?.registries?.length > 0}
      <section class="settings-section">
        <h2>📦 Available Registries</h2>
        <p class="section-description">
          Container registries available for artifact storage.
        </p>

        <div class="registry-list">
          {#each systemInfo.registries as registry}
            <div class="registry-item" class:default={registry.default}>
              <div class="registry-info">
                <span class="registry-name">{registry.name}</span>
                {#if registry.default}
                  <span class="default-badge">Default</span>
                {/if}
              </div>
              <span class="registry-url monospace">{registry.base_url}</span>
              <span class="registry-type">{registry.type}</span>
            </div>
          {/each}
        </div>
      </section>
    {/if}
  </div>
</div>

<style>
  .header {
    margin-bottom: 2rem;
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

  .settings-section {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1.5rem;
  }

  .settings-section h2 {
    font-size: 1.125rem;
    font-weight: 600;
    margin: 0 0 0.5rem 0;
  }

  .section-description {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0 0 1rem 0;
  }

  /* Relay styles */
  .relay-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .relay-item {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .relay-status {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .relay-url {
    flex: 1;
    font-family: monospace;
    font-size: 0.875rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .relay-status-text {
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .relay-remove {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 1.25rem;
    padding: 0;
    line-height: 1;
  }

  .relay-remove:hover {
    color: var(--error);
  }

  .relay-add {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .relay-add :global(input) {
    flex: 1;
  }

  .relay-actions {
    display: flex;
    gap: 0.5rem;
    justify-content: flex-end;
  }

  /* Theme styles */
  .theme-option {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  .theme-label {
    font-weight: 500;
  }

  .theme-toggle-group {
    display: flex;
    gap: 0.5rem;
  }

  .theme-btn {
    padding: 0.5rem 1rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text-primary);
    cursor: pointer;
    transition: all 0.15s;
  }

  .theme-btn:hover {
    background: var(--hover-bg);
  }

  .theme-btn.active {
    background: var(--primary);
    border-color: var(--primary);
    color: white;
  }

  /* Config display styles */
  .config-group {
    margin-bottom: 1.5rem;
  }

  .config-group:last-child {
    margin-bottom: 0;
  }

  .config-group h3 {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin: 0 0 0.75rem 0;
    padding-bottom: 0.5rem;
    border-bottom: 1px solid var(--border-color);
  }

  .config-row {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    padding: 0.5rem 0;
    gap: 1rem;
  }

  .config-label {
    color: var(--text-muted);
    font-size: 0.875rem;
    flex-shrink: 0;
  }

  .config-value {
    font-size: 0.875rem;
    text-align: right;
    word-break: break-all;
  }

  .config-value.monospace {
    font-family: monospace;
  }

  .config-value.clickable {
    background: transparent;
    border: none;
    color: inherit;
    cursor: pointer;
    padding: 0;
    text-decoration: underline;
    text-decoration-style: dotted;
  }

  .config-value.clickable:hover {
    color: var(--primary);
  }

  /* Features grid */
  .features-grid {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .feature-badge {
    font-size: 0.75rem;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    background: var(--bg);
    border: 1px solid var(--border-color);
    color: var(--text-muted);
  }

  .feature-badge.enabled {
    background: rgba(16, 185, 129, 0.1);
    border-color: var(--success);
    color: var(--success);
  }

  /* Registry list */
  .registry-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .registry-item {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.75rem 1rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .registry-item.default {
    border-color: var(--primary);
    background: rgba(99, 102, 241, 0.05);
  }

  .registry-info {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 200px;
  }

  .registry-name {
    font-weight: 500;
  }

  .default-badge {
    font-size: 0.625rem;
    font-weight: 600;
    text-transform: uppercase;
    padding: 0.125rem 0.375rem;
    background: var(--primary);
    color: white;
    border-radius: 3px;
  }

  .registry-url {
    flex: 1;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .registry-type {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
  }

  .loading {
    color: var(--text-muted);
    padding: 1rem;
    text-align: center;
  }

  .error-box {
    color: var(--error);
    background: rgba(239, 68, 68, 0.1);
    padding: 1rem;
    border-radius: 6px;
    font-size: 0.875rem;
  }

  .monospace {
    font-family: monospace;
  }
</style>
