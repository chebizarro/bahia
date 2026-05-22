<script>
  import Card from '$lib/components/Card.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { nostr, saveRelayConfig, getDefaultRelays } from '$lib/nostr/client.js';
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import { toast } from '$lib/components/toast.js';
  import { authState, loginWithNostrConnect, canUseNostrConnectUri } from '$lib/stores/auth.js';
  import { systemInfo as sharedSystemInfo, loadSystemInfo as loadSharedSystemInfo } from '$lib/stores';
  import QRCode from 'qrcode';
  import jsQR from 'jsqr';
  import {
    AppearanceIcon,
    ArtifactIcon,
    CameraIcon,
    ConfiguredIcon,
    MoonIcon,
    ProtectedIcon,
    RelayIcon,
    SunIcon
  } from '$lib/icons/domain-icons.js';

  // Shared public Nostr discovery from app bootstrap
  const systemInfo = $derived(sharedSystemInfo.data);
  const systemLoading = $derived(sharedSystemInfo.loading);
  const systemError = $derived(sharedSystemInfo.error);
  const serviceRelayList = $derived(systemInfo?.nostr?.service_relays || []);

  // Relay configuration (client-side)
  let relayInput = $state('');
  let relays = $state([]);
  let relaysSaving = $state(false);
  let connectionStatus = $state({});
  let relaySummary = $derived(relayConnectionSummary(relays, connectionStatus));

  $effect(() => {
    const unsubscribe = nostr.connectionStatus.subscribe(status => {
      connectionStatus = status;
    });

    // Load current relay config
    relays = nostr.getRelays();
    void loadSharedSystemInfo().catch(() => {});

    return () => {
      unsubscribe();
    };
  });

  $effect(() => {
    const advertisedRelays = systemInfo?.nostr?.browser_relays || [];
    if (relays.length === 0 && advertisedRelays.length > 0) {
      relays = [...advertisedRelays];
    }
  });


  async function addRelay() {
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
    // Auto-save and reconnect
    await saveRelays();
  }

  async function removeRelay(url) {
    relays = relays.filter(r => r !== url);
    await saveRelays();
  }

  function relayConnectionSummary(relayList, statusMap) {
    const total = relayList.length;
    const connected = relayList.filter((relay) => statusMap[relay] === 'connected').length;
    const connecting = relayList.filter((relay) => statusMap[relay] === 'connecting').length;
    const failed = relayList.filter((relay) =>
      ['error', 'failed', 'disconnected'].includes(statusMap[relay])
    ).length;

    return { total, connected, connecting, failed };
  }

  async function saveRelays() {
    relaysSaving = true;
    try {
      saveRelayConfig(relays);
      nostr.setRelays(relays, true);
      const summary = await nostr.connect(relays, { force: true });
      if (summary.total === 0) {
        toast.warning('Relay configuration saved with no relays configured');
      } else if (summary.connected === summary.total) {
        toast.success(`Relay configuration saved — connected to ${summary.connected}/${summary.total} relays`);
      } else if (summary.connected > 0) {
        toast.warning(`Relay configuration saved — connected to ${summary.connected}/${summary.total} relays`);
      } else {
        toast.error(`Relay configuration saved, but no relays connected (${summary.connected}/${summary.total})`);
      }
    } catch (err) {
      toast.error('Failed to save relay configuration');
    } finally {
      relaysSaving = false;
    }
  }

  async function resetToDefaults() {
    relays = getDefaultRelays();
    await saveRelays();
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
  let nostrConnectQrDataUrl = $state('');
  let scanning = $state(false);
  let scanError = $state(null);
  let videoEl = $state(null);
  let canvasEl = $state(null);
  let animFrameId = null;
  let mediaStream = null;

  $effect(() => {
    const uri = nostrConnectUri.trim();
    if (uri) {
      QRCode.toDataURL(uri, { width: 200, margin: 1 })
        .then(url => { nostrConnectQrDataUrl = url; })
        .catch(() => { nostrConnectQrDataUrl = ''; });
    } else {
      nostrConnectQrDataUrl = '';
    }
  });

  async function startQrScanner() {
    scanning = true;
    scanError = null;
    try {
      mediaStream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
      videoEl.srcObject = mediaStream;
      await videoEl.play();
      scanFrame();
    } catch (e) {
      scanError = e.message || 'Camera access denied';
      scanning = false;
    }
  }

  function scanFrame() {
    if (!scanning || !videoEl || !canvasEl) return;
    if (videoEl.readyState < videoEl.HAVE_ENOUGH_DATA) {
      animFrameId = requestAnimationFrame(scanFrame);
      return;
    }
    const ctx = canvasEl.getContext('2d');
    canvasEl.width = videoEl.videoWidth;
    canvasEl.height = videoEl.videoHeight;
    ctx.drawImage(videoEl, 0, 0);
    const img = ctx.getImageData(0, 0, canvasEl.width, canvasEl.height);
    const code = jsQR(img.data, img.width, img.height);
    if (code?.data) {
      nostrConnectUri = code.data;
      stopQrScanner();
      return;
    }
    animFrameId = requestAnimationFrame(scanFrame);
  }

  function stopQrScanner() {
    scanning = false;
    if (animFrameId) { cancelAnimationFrame(animFrameId); animFrameId = null; }
    if (mediaStream) { mediaStream.getTracks().forEach(t => t.stop()); mediaStream = null; }
  }

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
      <h2><RelayIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Nostr Relays</h2>
      <p class="section-description">
        Configure browser relays for Nostr reads and publishes. Changes autosave and reconnect immediately.
      </p>

      <div class="relay-list">
        {#each relays as relay}
          <div class="relay-item">
            <span class="relay-status" style="background: {getStatusColor(connectionStatus[relay])}"></span>
            <span class="relay-url">{relay}</span>
            <span class="relay-status-text">{getStatusLabel(connectionStatus[relay])}</span>
            <button class="relay-remove" onclick={() => removeRelay(relay)} title="Remove and reconnect" disabled={relaysSaving}>×</button>
          </div>
        {/each}
      </div>

      <div class="relay-add">
        <Input
          placeholder="wss://relay.example.com"
          bind:value={relayInput}
          onkeydown={(e) => e.key === 'Enter' && addRelay()}
        />
        <LoadingButton variant="secondary" loading={relaysSaving} onclick={addRelay}>Add & Reconnect</LoadingButton>
      </div>

      <div class="relay-actions">
        <LoadingButton variant="secondary" loading={relaysSaving} onclick={resetToDefaults}>Reset Defaults & Reconnect</LoadingButton>
        <LoadingButton variant="primary" loading={relaysSaving} onclick={saveRelays}>Reconnect Now</LoadingButton>
      </div>

      <p class="section-description relay-summary">
        {#if relaySummary.total === 0}
          No relays configured.
        {:else if relaySummary.connecting > 0}
          Connecting to {relaySummary.connecting} relay{relaySummary.connecting === 1 ? '' : 's'}…
        {:else}
          Connected {relaySummary.connected}/{relaySummary.total} relays{relaySummary.failed > 0 ? `; ${relaySummary.failed} unavailable` : ''}.
        {/if}
      </p>
    </section>

    <!-- NIP-46 Nostr Connect -->
    <section class="settings-section">
      <h2><ProtectedIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Nostr Connect (NIP-46)</h2>
      <p class="section-description">
        Connect this browser session to a remote signer using Nostr Connect. Paste or scan a <code>nostrconnect://</code> URI from your signer app.
      </p>

      <div class="relay-add">
        <Input
          placeholder="nostrconnect://<pubkey>?relay=wss://...&secret=..."
          bind:value={nostrConnectUri}
          onkeydown={(e) => e.key === 'Enter' && connectNostrConnect()}
        />
        <LoadingButton variant="primary" loading={nostrConnectLoading} onclick={connectNostrConnect}>Connect</LoadingButton>
      </div>

      <!-- QR display: show when URI is entered -->
      {#if nostrConnectQrDataUrl}
        <div class="qr-section">
          <p class="section-description">Preview of the entered URI as a QR code:</p>
          <img class="qr-image" src={nostrConnectQrDataUrl} alt="Nostr Connect QR code" />
        </div>
      {/if}

      <!-- QR scanner -->
      <div class="qr-scanner-section">
        {#if !scanning}
          <button class="btn-scan icon-button" onclick={startQrScanner}><CameraIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Scan QR Code</button>
        {:else}
          <div class="scanner-wrap">
            <!-- svelte-ignore a11y_media_has_caption -->
            <video bind:this={videoEl} class="scanner-video" playsinline></video>
            <canvas bind:this={canvasEl} class="scanner-canvas" aria-hidden="true"></canvas>
            <button class="btn-scan-stop" onclick={stopQrScanner}>Stop scanning</button>
          </div>
        {/if}
        {#if scanError}<p class="scan-error">{scanError}</p>{/if}
      </div>

      <p class="section-description">
        Nostr Connect signer: {authState.authMethod === 'nip46' ? 'connected for this browser session' : authState.nip46Available ? 'available' : 'not connected'}
      </p>
    </section>

    <!-- Theme Section -->
    <section class="settings-section">
      <h2><AppearanceIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Appearance</h2>
      <p class="section-description">Customize the look and feel of the application.</p>

      <div class="theme-option">
        <span class="theme-label">Theme</span>
        <div class="theme-toggle-group">
          <button
            class="theme-btn"
            class:active={theme.value === 'light'}
            onclick={() => theme.value !== 'light' && toggleTheme()}
          >
            <SunIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Light
          </button>
          <button
            class="theme-btn"
            class:active={theme.value === 'dark'}
            onclick={() => theme.value !== 'dark' && toggleTheme()}
          >
            <MoonIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Dark
          </button>
        </div>
      </div>
    </section>

    <!-- Server Configuration Section -->
    <section class="settings-section">
      <h2><ConfiguredIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Server Configuration</h2>
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
          {#if serviceRelayList.length > 0}
            <div class="config-row">
              <span class="config-label">Service Relay List (NIP-51)</span>
              <span class="config-value">
                {serviceRelayList.join(', ')}
              </span>
            </div>
          {:else}
            <div class="config-row">
              <span class="config-label">Service Relay List (NIP-51)</span>
              <span class="config-value">Not advertised</span>
            </div>
          {/if}
          <div class="config-row">
            <span class="config-label">Publishing</span>
            <span class="config-value">
              {systemInfo.nostr?.publish_enabled ? 'Enabled' : 'Disabled'}
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
                {systemInfo.blossom.enabled ? 'Enabled' : 'Disabled'}
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
                {systemInfo.oci.enabled ? 'Enabled' : 'Disabled'}
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
                  {enabled ? 'Enabled' : 'Disabled'} · {feature}
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
        <h2><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Available Registries</h2>
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
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .settings-section h2 :global(svg) {
    color: var(--text-muted);
    flex-shrink: 0;
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

  .relay-summary {
    margin-top: 0.75rem;
    margin-bottom: 0;
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
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
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

  /* QR styles */
  .qr-section {
    margin: 0.75rem 0;
  }

  .qr-image {
    display: block;
    width: 180px;
    height: 180px;
    border-radius: 6px;
    border: 1px solid var(--border-color);
    background: #fff;
    padding: 4px;
  }

  .qr-scanner-section {
    margin: 0.75rem 0;
  }

  .btn-scan {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    color: var(--text-primary);
    padding: 0.5rem 1rem;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.875rem;
    transition: background 0.15s;
  }

  .btn-scan:hover {
    background: var(--hover-bg);
  }

  .icon-button {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
  }

  .btn-scan-stop {
    background: var(--error);
    color: #fff;
    border: none;
    padding: 0.375rem 0.75rem;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.875rem;
    margin-top: 0.5rem;
    display: block;
  }

  .scanner-wrap {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
  }

  .scanner-video {
    width: 100%;
    max-width: 320px;
    border-radius: 6px;
    border: 1px solid var(--border-color);
    background: #000;
  }

  .scanner-canvas {
    display: none;
  }

  .scan-error {
    color: var(--error);
    font-size: 0.875rem;
    margin-top: 0.25rem;
  }
</style>

