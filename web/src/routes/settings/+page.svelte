<script>
  import { theme, toggleTheme } from '$lib/stores/theme.js';
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { toast } from '$lib/components/toast.js';
  import { authState, loginWithNostrConnect, canUseNostrConnectUri } from '$lib/stores/auth.js';
  import { systemInfo as sharedSystemInfo, loadSystemInfo as loadSharedSystemInfo } from '$lib/stores';
  import { buildInformationRows } from '$lib/version.js';
  import {
    deploymentInventory,
    deploymentInventoryView,
    startDeploymentInventory,
    stopDeploymentInventory
  } from '$lib/stores/deployment-inventory.svelte.js';
  import * as QRCode from 'qrcode';
  import jsQR from 'jsqr';
  import {
    AppearanceIcon,
    ArtifactIcon,
    CameraIcon,
    ConfiguredIcon,
    NotificationIcon,
    MoonIcon,
    ProtectedIcon,
    SunIcon
  } from '$lib/icons/domain-icons.js';

  // Shared public Nostr discovery from app bootstrap
  const systemInfo = $derived(sharedSystemInfo.data);
  const systemLoading = $derived(sharedSystemInfo.loading);
  const systemError = $derived(sharedSystemInfo.error);
  const serviceRelayList = $derived(systemInfo?.nostr?.service_relays || []);
  const featureEntries = $derived(Object.entries(systemInfo?.features || {}).sort(([a], [b]) => a.localeCompare(b)));
  const registryRows = $derived(systemInfo?.registries || []);
  const buildInfoVersionRows = $derived(buildInformationRows(systemInfo));

  // UI clock: re-evaluates observation age so rows age into "stale" even when
  // no new event arrives. It never gates event delivery or completion.
  let inventoryNowMs = $state(Date.now());
  $effect(() => {
    startDeploymentInventory();
    const clock = setInterval(() => { inventoryNowMs = Date.now(); }, 30_000);
    return () => {
      clearInterval(clock);
      stopDeploymentInventory();
    };
  });
  const inventoryView = $derived.by(() => {
    void deploymentInventory.revision;
    return deploymentInventoryView(inventoryNowMs);
  });

  function formatAge(seconds) {
    if (seconds === null || seconds === undefined) return 'unknown age';
    if (seconds < 90) return `${seconds}s ago`;
    if (seconds < 5400) return `${Math.round(seconds / 60)}m ago`;
    if (seconds < 172800) return `${Math.round(seconds / 3600)}h ago`;
    return `${Math.round(seconds / 86400)}d ago`;
  }

  const badgeLabels = {
    stale: 'stale observation',
    desired_only: 'desired, not observed',
    observed_only: 'observed, no desired state',
    unknown: 'no desired or observed state',
    drift_pending: 'drift not yet evaluated',
    reconcile_failing: 'reconcile failing'
  };
  const provenanceLabels = {
    relay: 'verified from relay',
    cache: 'verified cache, awaiting relay',
    cache_unconfirmed: 'cached; not confirmed by any relay'
  };
  const settingsAreas = [
    {
      href: '/settings/profile',
      title: 'Profile',
      description: 'Edit and publish your Nostr kind-0 profile metadata with signer-backed relay OK verification.'
    },
    {
      href: '/settings/relays',
      title: 'Relays',
      description: 'Manage persistent operator relay policy and local browser session relays.'
    },
    {
      href: '/settings/fleet',
      title: 'OpenClaw Fleet',
      description: 'Edit and publish the trusted fleet-wide OpenClaw configuration template.'
    },
    {
      href: '/notifications',
      title: 'Notifications',
      description: 'Configure webhook and Nostr DM notification channels.'
    },
    {
      href: '/notifications/log',
      title: 'Notification log',
      description: 'Review delivery attempts, failures, and event history.'
    },
    {
      href: '/policies',
      title: 'Policies',
      description: 'Manage approval and deployment policy configuration.'
    },
    {
      href: '/payments',
      title: 'Payments',
      description: 'Review cost and payment configuration surfaces.'
    },
    {
      href: '/dns',
      title: 'DNS',
      description: 'Manage DNS orchestration records and requests.'
    },
    {
      href: '/backup',
      title: 'Backup',
      description: 'Configure and monitor backup operations.'
    }
  ];

  $effect(() => {
    void loadSharedSystemInfo().catch(() => {});
  });

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
    <!-- Remote signer connection -->
    <section class="settings-section">
      <h2><ProtectedIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Remote Signer</h2>
      <p class="section-description">
        Connect this browser session to a remote signer using Nostr Connect. Paste or scan a <code>nostrconnect://</code> URI from your signer app.
      </p>

      <div class="settings-input-row">
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

    <!-- Operational Settings Section -->
    <section class="settings-section">
      <h2><NotificationIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Operational Settings</h2>
      <p class="section-description">
        Configuration areas with dedicated management screens. These are linked here so documented settings are not hidden behind sidebar navigation.
      </p>

      <div class="settings-area-grid">
        {#each settingsAreas as area}
          <a class="settings-area-card" href={area.href}>
            <span class="settings-area-title">{area.title}</span>
            <span class="settings-area-description">{area.description}</span>
          </a>
        {/each}
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
      {/if}

      <!-- Nostr Server Config -->
      <div class="config-group">
        <h3>Nostr</h3>
        {#if systemInfo?.nostr?.service_npub}
          <div class="config-row">
            <span class="config-label">Service Identity</span>
            <button type="button" class="config-value monospace clickable" onclick={() => copyToClipboard(systemInfo.nostr.service_npub)} title="Click to copy">
              {systemInfo.nostr.service_npub.slice(0, 20)}...
            </button>
          </div>
        {:else}
          <div class="config-row">
            <span class="config-label">Service Identity</span>
            <span class="config-value">Not advertised</span>
          </div>
        {/if}
        {#if serviceRelayList.length > 0}
          <div class="config-row">
            <span class="config-label">Service Relay List</span>
            <span class="config-value">
              {serviceRelayList.join(', ')}
            </span>
          </div>
        {:else}
          <div class="config-row">
            <span class="config-label">Service Relay List</span>
            <span class="config-value">Not advertised</span>
          </div>
        {/if}
        <div class="config-row">
          <span class="config-label">Publishing</span>
          <span class="config-value">
            {systemInfo?.nostr?.publish_enabled ? 'Enabled' : 'Disabled'}
          </span>
        </div>
      </div>

      <!-- Blossom Config -->
      <div class="config-group">
        <h3>Blossom Storage</h3>
        {#if systemInfo?.blossom}
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
        {:else}
          <div class="config-row">
            <span class="config-label">Status</span>
            <span class="config-value">Not advertised</span>
          </div>
        {/if}
      </div>

      <!-- OCI Registry Config -->
      <div class="config-group">
        <h3>Container Registry</h3>
        {#if systemInfo?.oci}
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
        {:else}
          <div class="config-row">
            <span class="config-label">Native Registry</span>
            <span class="config-value">Not advertised</span>
          </div>
        {/if}
      </div>

      <!-- Runtime Config -->
      <div class="config-group">
        <h3>Runtime</h3>
        <div class="config-row">
          <span class="config-label">Type</span>
          <span class="config-value">{systemInfo?.runtime?.type || 'Not configured'}</span>
        </div>
        {#if systemInfo?.runtime?.environments?.length > 0}
          <div class="config-row">
            <span class="config-label">Environments</span>
            <span class="config-value">{systemInfo.runtime.environments.join(', ')}</span>
          </div>
        {/if}
      </div>

      <!-- Feature Flags -->
      <div class="config-group">
        <h3>Features</h3>
        {#if featureEntries.length > 0}
          <div class="features-grid">
            {#each featureEntries as [feature, enabled]}
              <div class="feature-badge" class:enabled>
                {enabled ? 'Enabled' : 'Disabled'} · {feature}
              </div>
            {/each}
          </div>
        {:else}
          <div class="config-row">
            <span class="config-label">Feature discovery</span>
            <span class="config-value">Unavailable</span>
          </div>
        {/if}
      </div>
    </section>

    <!-- Available Registries Section -->
    <section class="settings-section">
      <h2><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Available Registries</h2>
      <p class="section-description">
        Container registries available for artifact storage.
      </p>

      {#if registryRows.length > 0}
        <div class="registry-list">
          {#each registryRows as registry}
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
      {:else}
        <div class="empty-config">
          {#if systemLoading}
            Loading registry configuration…
          {:else if systemError}
            Registry configuration unavailable: {systemError}
          {:else}
            No registries are advertised by discovery.
          {/if}
        </div>
      {/if}
    </section>

    <!-- Observed deployments + build provenance -->
    <section class="settings-section" data-testid="observed-deployments">
      <h2><ConfiguredIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Observed deployments</h2>
      <p class="section-description">
        Signed deployment inventory published by Bahia from desired state and runtime observations. Every snapshot is verified against the trusted service key before it is shown.
      </p>

      {#if deploymentInventory.error}
        <div class="empty-config" role="alert">Deployment inventory unavailable: {deploymentInventory.error}</div>
      {/if}
      {#if deploymentInventory.reconnecting}
        <p class="version-package" role="status">Relay connection lost ({deploymentInventory.lastClosedReason || 'closed'}); reconnecting. Showing the last verified inventory.</p>
      {/if}
      {#if deploymentInventory.status === 'cached' && !deploymentInventory.caughtUp}
        <p class="version-package" role="status">Showing {deploymentInventory.cachedRestored} verified cached snapshot(s) while relays catch up.</p>
      {/if}
      {#if deploymentInventory.rejectedCount > 0}
        <p class="version-package" role="status">{deploymentInventory.rejectedCount} inventory event(s) were rejected (untrusted, invalid signature, or malformed).</p>
      {/if}

      {#if inventoryView.environments.length === 0}
        <div class="empty-config">
          {#if deploymentInventory.status === 'loading' || deploymentInventory.status === 'idle'}
            Loading signed deployment inventory…
          {:else if !deploymentInventory.error}
            No deployment inventory has been published to the configured relays.
          {/if}
        </div>
      {/if}

      {#each inventoryView.environments as environment (environment.name)}
        <div class="config-group">
          <h3>{environment.name}</h3>
          {#if environment.inventory}
            <p class="version-package">
              {environment.inventory.deployments.length} deployment(s) · published {environment.inventory.publishedAt} · {provenanceLabels[environment.inventory.provenance]} · instances {environment.inventory.instanceCoverage === 'supervised' ? 'supervised' : 'not supervised'}
            </p>
            {#if environment.inventory.deployments.length === 0}
              <div class="empty-config">No Bahia deployments are recorded in this environment.</div>
            {/if}
            <div class="version-list">
              {#each environment.inventory.deployments as deployment (deployment.key)}
                <div class="version-item" data-testid="deployment-row">
                  <div class="version-info">
                    <span class="version-name">{deployment.service}</span>
                    <span class="version-kind">{deployment.unit}{deployment.target ? ` · ${deployment.target}` : ''}{deployment.runtimeType ? ` · ${deployment.runtimeType}` : ''}</span>
                    <span class="version-package">Health: {deployment.health} · Drift: {deployment.driftEvaluated ? deployment.drift : 'pending evaluation'}</span>
                    {#if deployment.badges.length > 0}
                      <span class="version-package deployment-badges">
                        {#each deployment.badges as badge}
                          <span class="deployment-badge" data-badge={badge}>{badgeLabels[badge] || badge}</span>
                        {/each}
                      </span>
                    {/if}
                  </div>
                  <div class="version-values">
                    <span class="version-package monospace">Desired: {deployment.desiredRef || 'none'}{deployment.desiredRef && !deployment.desiredImmutable ? ' (mutable ref)' : ''}</span>
                    <span class="config-value monospace">Observed: {deployment.observedVersion || deployment.observedRef || 'not observed'}</span>
                    {#if deployment.observedRef && deployment.observedVersion}
                      <span class="version-package monospace">{deployment.observedRef}</span>
                    {/if}
                    {#if deployment.observedAt}
                      <span class="version-package">Observed {formatAge(deployment.ageSeconds)} via {deployment.observationSource || 'runtime'}</span>
                    {/if}
                    {#if deployment.instances.length > 0}
                      <details>
                        <summary class="version-package">{deployment.instances.length} instance(s)</summary>
                        {#each deployment.instances as instance (instance.target)}
                          <span class="version-package">{instance.target}: {instance.status}{instance.supervisor ? ` (${instance.supervisor})` : ''} · {formatAge(instance.ageSeconds)}{instance.stale ? ' · stale' : ''}</span>
                        {/each}
                      </details>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>
          {:else}
            <p class="version-package">No signed Bahia inventory for this environment; only runtime scan aggregates are known.</p>
          {/if}

          {#if environment.targetScans.length > 0}
            <h4 class="version-note">Runtime target scans (aggregate only)</h4>
            <div class="version-list">
              {#each environment.targetScans as scan (scan.target)}
                <div class="version-item" data-testid="target-scan-row">
                  <div class="version-info">
                    <span class="version-name">{scan.target}</span>
                    <span class="version-kind">{scan.endpointRef || 'runtime target'} · {scan.state}</span>
                  </div>
                  <div class="version-values">
                    {#if scan.counts}
                      <span class="config-value">{scan.counts.unmanaged} unmanaged · {scan.counts.managed} managed · {scan.counts.total} total</span>
                    {:else}
                      <span class="config-value">Target unavailable; instance counts unknown</span>
                    {/if}
                    <span class="version-package">Last complete scan {formatAge(scan.ageSeconds)}{scan.stale ? ' (stale)' : ''}</span>
                  </div>
                </div>
              {/each}
            </div>
            <p class="version-package">Per-instance details of unmanaged workloads are available only to authorized operators through an encrypted adoption scan.</p>
          {/if}
        </div>
      {/each}
    </section>

    <section class="settings-section" data-testid="build-provenance">
      <h2><ConfiguredIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Build provenance</h2>
      <p class="section-description version-note">
        Diagnostics: compile-time versions of this web bundle and the backend build that published discovery. These describe builds, not what is deployed or running.
      </p>
      <div class="version-list">
        {#each buildInfoVersionRows as component}
          <div class="version-item">
            <div class="version-info">
              <span class="version-name">{component.name}</span>
              <span class="version-kind">{component.kind} build</span>
            </div>
            <div class="version-values">
              <span class="config-value monospace">{component.version}</span>
              {#if component.packaged_as}
                <span class="version-package monospace">{component.packaged_as}</span>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </section>

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

  .settings-area-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.75rem;
  }

  .settings-area-card {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    padding: 1rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    color: inherit;
    text-decoration: none;
  }

  .settings-area-card:hover {
    border-color: var(--primary);
  }

  .settings-area-title {
    font-weight: 600;
  }

  .settings-area-description {
    color: var(--text-muted);
    font-size: 0.875rem;
    line-height: 1.4;
  }

  .empty-config {
    padding: 0.875rem 1rem;
    color: var(--text-muted);
    background: var(--bg);
    border: 1px dashed var(--border-color);
    border-radius: 6px;
    font-size: 0.875rem;
  }

  .settings-input-row {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .settings-input-row :global(input) {
    flex: 1;
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

  /* Version display styles */
  .deployment-badges {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }

  .deployment-badge {
    border: 1px solid var(--color-border, currentColor);
    border-radius: 999px;
    padding: 0 0.4rem;
  }

  .version-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .version-item {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    padding: 0.75rem 1rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .version-info,
  .version-values {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .version-values {
    align-items: flex-end;
    text-align: right;
  }

  .version-name {
    font-weight: 500;
  }

  .version-kind,
  .version-package,
  .version-note {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .version-kind {
    text-transform: uppercase;
    letter-spacing: 0.05em;
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
