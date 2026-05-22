<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import SBOMDetails from '$lib/components/SBOMDetails.svelte';
  import { artifacts, services, loadArtifacts, loadServices } from '$lib/stores';
  import { toast } from '$lib/components/toast.js';
  import { verifyArtifactSignatures } from '$lib/stores/artifact-signatures.svelte.js';
  import { api } from '$lib/api/client.js';
  import {
    ArtifactIcon,
    CopyIcon,
    GenericFileIcon,
    SbomIcon,
    ServiceIcon,
    SignatureIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let artifact = $state(null);
  let service = $state(null);
  let sbomPackages = $state([]);
  let sbomData = $state(null);
  let sbomAttestation = $state(null);
  let sbomLoading = $state(false);
  let sbomLoaded = $state(false);
  let signatures = $state([]);
  let hasVerifiedSig = $state(false);
  let loading = $state(true);
  let error = $state(null);
  
  // Tab state
  let activeTab = $state('overview'); // overview, sbom, signatures
  
  // Verification state
  let verifying = $state(false);
  let verifyError = $state(null);

  let artifactId = $derived(page.params.id);

  // SBOM table columns
  let sbomColumns = $derived([
    { key: 'name', label: 'Package Name' },
    { key: 'version', label: 'Version' },
    { key: 'license', label: 'License' },
    { key: 'type', label: 'Type' }
  ]);

  // Signatures table columns
  let signatureColumns = $derived([
    { 
      key: 'signer', 
      label: 'Signer',
      render: (r) => r.signer ? `<code>${r.signer.slice(0, 16)}...</code>` : '-'
    },
    { 
      key: 'created_at', 
      label: 'Signed At',
      render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-'
    },
    {
      key: 'verified',
      label: 'Status',
      render: (r) => {
        const verified = r.verified;
        if (verified === true) {
          return '<span style="color: #10b981; font-weight: 500;">Verified</span>';
        } else if (verified === false) {
          return '<span style="color: #ef4444; font-weight: 500;">Failed</span>';
        }
        return '<span style="color: #888;">Pending</span>';
      }
    }
  ]);

  $effect(() => {
    const id = artifactId;
    if (!id) return;
    void loadArtifact(id);
  });

  async function loadArtifact(id = artifactId) {
    loading = true;
    error = null;

    try {
      await Promise.all([loadArtifacts(), loadServices()]);
      artifact = artifacts.find((candidate) => candidate.id === id) || null;
      if (!artifact) {
        throw new Error('Artifact not found');
      }

      service = artifact.service_id ? services.find((candidate) => candidate.id === artifact.service_id) || null : null;
      sbomData = null;
      sbomAttestation = null;
      sbomLoaded = false;
      sbomPackages = Array.isArray(artifact.sbom_packages) ? artifact.sbom_packages : [];
      signatures = Array.isArray(artifact.signatures) ? artifact.signatures : [];
      hasVerifiedSig = signatures.some((signature) => signature?.verified === true) || Boolean(artifact.signature_ref || artifact.verified_signature);
    } catch (err) {
      error = err.message || 'Failed to load artifact';
      console.error('Error loading artifact:', err);
    } finally {
      loading = false;
    }
  }

  async function loadSBOMDetails() {
    if (!artifactId || sbomLoading) return;
    sbomLoading = true;
    
    try {
      // Load SBOM data and attestation in parallel
      const [sbomResult, attestationResult] = await Promise.allSettled([
        api?.getSBOM(artifactId),
        api?.getSBOMAttestation(artifactId)
      ]);
      
      if (sbomResult.status === 'fulfilled' && sbomResult.value) {
        sbomData = sbomResult.value;
        // If packages weren't loaded from artifact, try from SBOM
        if (sbomPackages.length === 0 && sbomResult.value.packages) {
          sbomPackages = sbomResult.value.packages;
        }
      }
      
      if (attestationResult.status === 'fulfilled' && attestationResult.value) {
        sbomAttestation = attestationResult.value;
      }
    } catch (err) {
      console.error('Error loading SBOM details:', err);
    } finally {
      sbomLoaded = true;
      sbomLoading = false;
    }
  }

  // Load SBOM details when switching to SBOM tab
  $effect(() => {
    if (activeTab === 'sbom' && artifactId && !sbomLoaded && !sbomLoading) {
      void loadSBOMDetails();
    }
  });

  async function handleVerifySignatures() {
    verifying = true;
    verifyError = null;

    try {
      const result = await verifyArtifactSignatures(artifactId);
      const verifiedSignatures = Array.isArray(result?.signatures) ? result.signatures : [];
      if (verifiedSignatures.length > 0) {
        signatures = verifiedSignatures;
        hasVerifiedSig = verifiedSignatures.some((signature) => signature?.verified === true || signature?.verification_status === 'verified');
      }
      if (result?.found === 0) {
        verifyError = 'No signatures were discovered for this artifact.';
      }
    } catch (err) {
      verifyError = err.message || 'Failed to verify signatures';
    } finally {
      verifying = false;
    }
  }

  function formatBytes(bytes) {
    if (!bytes) return '-';
    const mb = bytes / (1024 * 1024);
    return `${mb.toFixed(1)} MB`;
  }

  function formatDigest(digest) {
    if (!digest) return '-';
    // Show first 12 characters after the algorithm prefix (sha256:...)
    const parts = digest.split(':');
    if (parts.length === 2) {
      return `${parts[0]}:${parts[1].slice(0, 12)}...`;
    }
    return digest.slice(0, 16) + '...';
  }

  // Middle-truncate: sha256:abc123…xyz789
  function formatDigestMiddle(digest) {
    if (!digest) return '-';
    const parts = digest.split(':');
    if (parts.length === 2) {
      const hash = parts[1];
      if (hash.length > 20) return `${parts[0]}:${hash.slice(0, 8)}\u2026${hash.slice(-8)}`;
      return digest;
    }
    if (digest.length > 20) return `${digest.slice(0, 10)}\u2026${digest.slice(-8)}`;
    return digest;
  }

  async function copyDigest(digest) {
    try {
      await navigator.clipboard.writeText(digest);
      toast.success('Digest copied to clipboard');
    } catch {
      toast.error('Failed to copy digest');
    }
  }

  function artifactTypeLabel(artifact) {
    const t = artifact?.artifact_type || artifact?.type || artifact?.kind;
    if (!t) return 'Container Image';
    // Prettify snake_case / kebab-case
    return String(t).replace(/[_-]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading artifact...</p>
  {:else if error}
    <div class="error-state">
      <p class="error title-with-icon"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{error}</span></p>
      <LoadingButton variant="secondary" onclick={() => goto('/services')}>
        Back to Services
      </LoadingButton>
    </div>
  {:else if artifact}
    <div class="header">
      <div>
        {#if service}
          <a href="/services/{service.id}" class="back-link">← {service.name}</a>
        {:else}
          <a href="/services" class="back-link">← Services</a>
        {/if}
        <h1 class="title-with-icon"><ArtifactIcon size={28} strokeWidth={1.75} ariaHidden="true" /> <span>{artifact.name || artifact.image_tag || 'Artifact Details'}</span></h1>
        <p class="artifact-id"><code>{artifact.id}</code></p>
      </div>
    </div>

    <!-- Tabs -->
    <div class="tabs">
      <button 
        class="tab" 
        class:active={activeTab === 'overview'}
        onclick={() => activeTab = 'overview'}
      >
        <span class="tab-with-icon"><ArtifactIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Overview</span>
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'sbom'}
        onclick={() => activeTab = 'sbom'}
      >
        <span class="tab-with-icon"><SbomIcon size={16} strokeWidth={1.75} ariaHidden="true" /> SBOM {sbomPackages.length > 0 ? `(${sbomPackages.length})` : ''}</span>
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'signatures'}
        onclick={() => activeTab = 'signatures'}
      >
        <span class="tab-with-icon"><SignatureIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Signatures {signatures.length > 0 ? `(${signatures.length})` : ''}</span>
      </button>
    </div>

    <!-- Tab Content -->
    <div class="tab-content">
      {#if activeTab === 'overview'}
        <!-- Overview Tab -->
        <div class="overview-grid">
          <Card title="Name" titleIcon={ArtifactIcon} value={artifact.name || artifact.image_tag || '-'} />
          <Card title="Type" titleIcon={GenericFileIcon} value={artifactTypeLabel(artifact)} />
          <Card title="Version" titleIcon={UnknownIcon} value={artifact.version || artifact.image_tag || '-'} />
          <Card title="Size" titleIcon={ArtifactIcon} value={formatBytes(artifact.size_bytes)} />
          <Card title="Signature" titleIcon={hasVerifiedSig ? SuccessIcon : WarningIcon} value={hasVerifiedSig ? 'Verified' : signatures.length > 0 ? 'Needs verification' : 'Not signed'} />
          <!-- Digest card: small font, middle-truncated, tooltip + copy on click -->
          {#if artifact.digest || artifact.image_digest}
            {@const fullDigest = artifact.digest || artifact.image_digest}
            <div class="card digest-card">
              <div class="card-label title-with-icon"><CopyIcon size={16} strokeWidth={1.75} ariaHidden="true" /> <span>Digest</span></div>
              <button
                class="digest-value"
                title={fullDigest}
                onclick={() => copyDigest(fullDigest)}
                aria-label="Copy digest to clipboard"
              >
                <code class="digest-text">{formatDigestMiddle(fullDigest)}</code>
                <span class="digest-copy-hint" aria-hidden="true"><CopyIcon size={16} strokeWidth={1.75} /></span>
              </button>
            </div>
          {:else}
            <Card title="Digest" titleIcon={CopyIcon} value="-" />
          {/if}
        </div>

        <section class="detail-section">
          <h2 class="section-title"><ArtifactIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>Details</span></h2>
          <div class="details-grid">
            {#if service}
              <div class="detail-item">
                <span class="detail-label">Service</span>
                <span class="detail-value service-ref">
                  <ServiceIcon size={16} strokeWidth={1.75} ariaHidden="true" />
                  <a href="/services/{service.id}" class="service-link">{service.name}</a>
                </span>
              </div>
            {/if}
            <div class="detail-item">
              <span class="detail-label">Created</span>
              <span class="detail-value">
                {artifact.created_at ? new Date(artifact.created_at).toLocaleString() : '-'}
              </span>
            </div>
            {#if artifact.updated_at}
              <div class="detail-item">
                <span class="detail-label">Updated</span>
                <span class="detail-value">
                  {new Date(artifact.updated_at).toLocaleString()}
                </span>
              </div>
            {/if}
            {#if artifact.build_id}
              <div class="detail-item">
                <span class="detail-label">Build ID</span>
                <span class="detail-value">
                  <code>{artifact.build_id}</code>
                </span>
              </div>
            {/if}
          </div>

          {#if artifact.metadata && Object.keys(artifact.metadata).length > 0}
            <div class="metadata-section">
              <h3 class="section-title"><GenericFileIcon size={16} strokeWidth={1.75} ariaHidden="true" /> <span>Metadata</span></h3>
              <div class="metadata-grid">
                {#each Object.entries(artifact.metadata) as [key, value]}
                  <div class="detail-item">
                    <span class="detail-label">{key}</span>
                    <span class="detail-value">{value}</span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </section>

      {:else if activeTab === 'sbom'}
        <!-- SBOM Tab -->
        <SBOMDetails
          sbom={sbomData}
          packages={sbomPackages}
          attestation={sbomAttestation}
          loading={sbomLoading}
        />

      {:else if activeTab === 'signatures'}
        <!-- Signatures Tab -->
        <section class="signatures-section">
          <div class="section-header">
            <h2 class="section-title"><SignatureIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>Signatures</span></h2>
            <div class="header-actions">
              {#if hasVerifiedSig}
                <Badge variant="success"><span class="badge-with-icon"><SuccessIcon size={14} strokeWidth={1.75} ariaHidden="true" /> Verified</span></Badge>
              {:else if signatures.length > 0}
                <Badge variant="warning"><span class="badge-with-icon"><WarningIcon size={14} strokeWidth={1.75} ariaHidden="true" /> Unverified</span></Badge>
              {:else}
                <Badge variant="default">No Signatures</Badge>
              {/if}
              <LoadingButton
                variant="primary"
                loading={verifying}
                onclick={handleVerifySignatures}
              >
                Verify via Encrypted Nostr
              </LoadingButton>
            </div>
          </div>

          {#if verifyError}
            <p class="error-message">{verifyError}</p>
          {/if}

          {#if signatures.length > 0}
            <Table columns={signatureColumns} data={signatures} />
          {:else}
            <EmptyState
              iconComponent={SignatureIcon}
              title="No signatures found"
              message="This artifact has not been signed yet"
            />
          {/if}
        </section>
      {/if}
    </div>
  {:else}
    <EmptyState
      iconComponent={UnknownIcon}
      title="Artifact not found"
      message="The requested artifact does not exist"
    >
      <LoadingButton variant="secondary" onAction={() => goto('/services')}>
        Back to Services
      </LoadingButton>
    </EmptyState>
  {/if}
</div>

<style>
  .page {
    padding: 0;
    max-width: 1200px;
  }

  .header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }

  .title-with-icon,
  .section-title,
  .tab-with-icon,
  .badge-with-icon,
  .service-ref {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .digest-copy-hint {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .back-link {
    display: inline-block;
    color: var(--primary);
    text-decoration: none;
    font-size: 0.875rem;
    margin-bottom: 0.5rem;
  }

  .back-link:hover {
    text-decoration: underline;
  }

  .artifact-id {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .error-state {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0 0 1rem;
  }

  /* Tabs */
  .tabs {
    display: flex;
    gap: 0.5rem;
    border-bottom: 2px solid var(--border-color);
    margin-bottom: 1.5rem;
  }

  .tab {
    background: none;
    border: none;
    padding: 0.75rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-muted);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -2px;
    transition: all 0.2s;
  }

  .tab:hover {
    color: var(--text-primary);
  }

  .tab.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  /* Tab Content */
  .tab-content {
    min-height: 300px;
  }

  /* Overview Tab */
  .overview-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
    align-items: stretch;
  }

  .digest-card {
    background: var(--card-bg, #1a1a2e);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color, #2a2a4a);
    min-width: 0;
  }

  .card-label {
    font-size: 0.875rem;
    color: var(--text-muted, #888);
    margin-bottom: 0.5rem;
  }

  .digest-value {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    width: 100%;
  }

  .digest-value:hover .digest-copy-hint {
    opacity: 1;
  }

  .digest-text {
    font-family: monospace;
    font-size: 0.7rem;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }

  .digest-copy-hint {
    display: inline-flex;
    align-items: center;
    font-size: 0.75rem;
    flex-shrink: 0;
    opacity: 0.4;
    transition: opacity 0.15s;
  }

  .detail-section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }

  .detail-section h2 {
    font-size: 1.125rem;
    color: var(--text-primary);
    margin: 0 0 1rem;
  }

  .detail-section h3 {
    font-size: 0.875rem;
    color: var(--text-muted);
    margin: 1.5rem 0 1rem;
    text-transform: uppercase;
    font-weight: 600;
  }

  .details-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
  }

  .metadata-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0.75rem;
  }

  .detail-item {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .detail-label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }

  .detail-value {
    font-size: 0.875rem;
    color: var(--text-primary);
  }

  .service-link {
    color: var(--primary);
    text-decoration: none;
  }

  .service-link:hover {
    text-decoration: underline;
  }

  /* SBOM and Signatures Sections */
  .signatures-section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }

  .section-header h2 {
    font-size: 1.125rem;
    color: var(--text-primary);
    margin: 0;
  }

  .header-actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .error-message {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0 0 1rem;
    padding: 0.75rem;
    background: rgba(239, 68, 68, 0.1);
    border-radius: 4px;
    border: 1px solid rgba(239, 68, 68, 0.2);
  }
</style>
