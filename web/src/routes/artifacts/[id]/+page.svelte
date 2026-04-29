<script>
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { api } from '$lib/api/client.js';

  let artifact = null;
  let service = null;
  let sbomPackages = [];
  let signatures = [];
  let hasVerifiedSig = false;
  let loading = true;
  let error = null;
  
  // Tab state
  let activeTab = 'overview'; // overview, sbom, signatures
  
  // Verification state
  let verifying = false;
  let verifyError = null;

  $: artifactId = $page.params.id;

  // SBOM table columns
  $: sbomColumns = [
    { key: 'name', label: 'Package Name' },
    { key: 'version', label: 'Version' },
    { key: 'license', label: 'License' },
    { key: 'type', label: 'Type' }
  ];

  // Signatures table columns
  $: signatureColumns = [
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
          return '<span style="color: #10b981; font-weight: 500;">✓ Verified</span>';
        } else if (verified === false) {
          return '<span style="color: #ef4444; font-weight: 500;">✗ Failed</span>';
        }
        return '<span style="color: #888;">○ Pending</span>';
      }
    }
  ];

  onMount(async () => {
    await loadArtifact();
  });

  async function loadArtifact() {
    loading = true;
    error = null;

    try {
      // Load artifact details (another agent is adding this method)
      artifact = await api.getArtifact(artifactId);

      // Load service if service_id exists
      if (artifact.service_id) {
        try {
          service = await api.getService(artifact.service_id);
        } catch (err) {
          console.warn('Failed to load service:', err);
        }
      }

      // Load SBOM packages
      try {
        sbomPackages = await api.getSBOMPackages(artifactId);
        if (!Array.isArray(sbomPackages)) {
          sbomPackages = [];
        }
      } catch (err) {
        console.warn('Failed to load SBOM packages:', err);
        sbomPackages = [];
      }

      // Load signatures
      try {
        signatures = await api.listSignatures(artifactId);
        if (!Array.isArray(signatures)) {
          signatures = [];
        }
      } catch (err) {
        console.warn('Failed to load signatures:', err);
        signatures = [];
      }

      // Check verification status
      try {
        const result = await api.hasVerifiedSignature(artifactId);
        hasVerifiedSig = result?.verified || false;
      } catch (err) {
        console.warn('Failed to check verification status:', err);
        hasVerifiedSig = false;
      }

    } catch (err) {
      error = err.message || 'Failed to load artifact';
      console.error('Error loading artifact:', err);
    } finally {
      loading = false;
    }
  }

  async function handleVerifySignatures() {
    verifying = true;
    verifyError = null;

    try {
      await api.verifySignatures(artifactId);
      
      // Reload signatures and verification status
      signatures = await api.listSignatures(artifactId);
      const result = await api.hasVerifiedSignature(artifactId);
      hasVerifiedSig = result?.verified || false;
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
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading artifact...</p>
  {:else if error}
    <div class="error-state">
      <p class="error">⚠️ {error}</p>
      <LoadingButton variant="secondary" on:click={() => goto('/services')}>
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
        <h1>Artifact Details</h1>
        <p class="artifact-id"><code>{artifact.id}</code></p>
      </div>
    </div>

    <!-- Tabs -->
    <div class="tabs">
      <button 
        class="tab" 
        class:active={activeTab === 'overview'}
        on:click={() => activeTab = 'overview'}
      >
        Overview
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'sbom'}
        on:click={() => activeTab = 'sbom'}
      >
        SBOM {sbomPackages.length > 0 ? `(${sbomPackages.length})` : ''}
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'signatures'}
        on:click={() => activeTab = 'signatures'}
      >
        Signatures {signatures.length > 0 ? `(${signatures.length})` : ''}
      </button>
    </div>

    <!-- Tab Content -->
    <div class="tab-content">
      {#if activeTab === 'overview'}
        <!-- Overview Tab -->
        <div class="overview-grid">
          <Card title="Name" value={artifact.name || artifact.image_tag || '-'} />
          <Card title="Version" value={artifact.version || artifact.image_tag || '-'} />
          <Card title="Digest" value={formatDigest(artifact.digest || artifact.image_digest)} />
          <Card title="Size" value={formatBytes(artifact.size_bytes)} />
        </div>

        <section class="detail-section">
          <h2>Details</h2>
          <div class="details-grid">
            {#if service}
              <div class="detail-item">
                <span class="detail-label">Service</span>
                <span class="detail-value">
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
              <h3>Metadata</h3>
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
        <section class="sbom-section">
          <div class="section-header">
            <h2>Software Bill of Materials</h2>
          </div>
          
          {#if sbomPackages.length > 0}
            <Table columns={sbomColumns} data={sbomPackages} />
          {:else}
            <EmptyState
              icon="📦"
              title="No SBOM available"
              message="This artifact does not have an SBOM or it has not been ingested yet"
            />
          {/if}
        </section>

      {:else if activeTab === 'signatures'}
        <!-- Signatures Tab -->
        <section class="signatures-section">
          <div class="section-header">
            <h2>Signatures</h2>
            <div class="header-actions">
              {#if hasVerifiedSig}
                <Badge variant="success">✓ Verified</Badge>
              {:else if signatures.length > 0}
                <Badge variant="warning">⚠ Unverified</Badge>
              {:else}
                <Badge variant="default">No Signatures</Badge>
              {/if}
              {#if signatures.length > 0}
                <LoadingButton
                  variant="primary"
                  loading={verifying}
                  on:click={handleVerifySignatures}
                >
                  Verify Signatures
                </LoadingButton>
              {/if}
            </div>
          </div>

          {#if verifyError}
            <p class="error-message">{verifyError}</p>
          {/if}

          {#if signatures.length > 0}
            <Table columns={signatureColumns} data={signatures} />
          {:else}
            <EmptyState
              icon="🔏"
              title="No signatures found"
              message="This artifact has not been signed yet"
            />
          {/if}
        </section>
      {/if}
    </div>
  {:else}
    <EmptyState
      icon="❓"
      title="Artifact not found"
      message="The requested artifact does not exist"
    >
      <LoadingButton variant="secondary" on:click={() => goto('/services')}>
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
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
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
  .sbom-section,
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
