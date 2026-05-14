<script>
  import Badge from './Badge.svelte';
  import Table from './Table.svelte';
  import EmptyState from './EmptyState.svelte';
  import {
    ArtifactIcon,
    BlossomIcon,
    GenericFileIcon,
    ProtectedIcon,
    SbomIcon,
    ServiceIcon,
    SignatureIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let { 
    sbom = null,
    packages = [],
    attestation = null,
    loading = false
  } = $props();

  // NTIA compliance fields
  const ntiaFields = [
    { key: 'hasSupplierName', label: 'Supplier Name', icon: ServiceIcon },
    { key: 'hasComponentName', label: 'Component Name', icon: ArtifactIcon },
    { key: 'hasComponentVersion', label: 'Component Version', icon: GenericFileIcon },
    { key: 'hasUniqueID', label: 'Unique ID (PURL/CPE)', icon: ProtectedIcon },
    { key: 'hasRelationship', label: 'Dependency Relationships', icon: SbomIcon },
    { key: 'hasAuthor', label: 'Author', icon: SignatureIcon },
    { key: 'hasTimestamp', label: 'Timestamp', icon: UnknownIcon }
  ];

  // Package table columns
  let packageColumns = $derived([
    { key: 'name', label: 'Package Name' },
    { key: 'version', label: 'Version' },
    { key: 'ecosystem', label: 'Ecosystem' },
    { key: 'license', label: 'License' },
    { 
      key: 'purl', 
      label: 'PURL',
      render: (r) => r.purl ? `<code class="truncate">${r.purl.slice(0, 40)}${r.purl.length > 40 ? '...' : ''}</code>` : '-'
    }
  ]);

  function storageTypePresentation(type) {
    const labels = {
      'blossom': { label: 'Blossom', icon: BlossomIcon },
      'oci-referrer': { label: 'OCI Referrer', icon: ArtifactIcon },
      'package-backend': { label: 'Package Backend', icon: GenericFileIcon }
    };
    return labels[type] || { label: type || 'Unknown', icon: UnknownIcon };
  }

  function formatFormat(format) {
    const labels = {
      'spdx': 'SPDX',
      'cyclonedx': 'CycloneDX'
    };
    return labels[format] || format || 'Unknown';
  }

  function formatDate(dateStr) {
    if (!dateStr) return '-';
    try {
      return new Date(dateStr).toLocaleString();
    } catch {
      return dateStr;
    }
  }

  function truncateHash(hash, len = 16) {
    if (!hash) return '-';
    if (hash.length <= len * 2) return hash;
    return `${hash.slice(0, len)}...${hash.slice(-8)}`;
  }

  let ntiaCompliance = $derived(attestation?.predicate?.ntia || sbom?.ntia || null);
  let ntiaCompliant = $derived(ntiaCompliance?.isCompliant || false);
  let passedCount = $derived(
    ntiaCompliance 
      ? ntiaFields.filter(f => ntiaCompliance[f.key]).length 
      : 0
  );
</script>

<div class="sbom-details">
  {#if loading}
    <p class="loading">Loading SBOM details...</p>
  {:else if !sbom && !attestation && packages.length === 0}
    <EmptyState
      iconComponent={SbomIcon}
      title="No SBOM available"
      message="This artifact does not have an SBOM or it has not been ingested yet"
    />
  {:else}
    <!-- Attestation Overview -->
    {#if attestation || sbom}
      <div class="section">
        <h3 class="section-title"><SignatureIcon size={18} stroke={1.75} aria-hidden="true" /> <span>Attestation Details</span></h3>
        <div class="attestation-grid">
          <div class="attestation-item">
            <span class="label">Format</span>
            <span class="value">
              <Badge variant={sbom?.format === 'spdx' ? 'info' : 'primary'}>
                {formatFormat(sbom?.format || attestation?.predicate?.format)}
              </Badge>
            </span>
          </div>

          {#if attestation?.predicate?.generator || sbom?.generator}
            {@const gen = attestation?.predicate?.generator || sbom?.generator}
            <div class="attestation-item">
              <span class="label">Generator</span>
              <span class="value">
                <code>{gen.id || 'Unknown'}{gen.version ? `@${gen.version}` : ''}</code>
              </span>
            </div>
          {/if}

          {#if attestation?.predicate?.location || sbom?.source_url}
            {@const loc = attestation?.predicate?.location}
            {@const storage = storageTypePresentation(loc?.type || 'blossom')}
            {@const StorageIcon = storage.icon}
            <div class="attestation-item">
              <span class="label">Storage</span>
              <span class="value value-with-icon"><StorageIcon size={16} stroke={1.75} aria-hidden="true" /> {storage.label}</span>
            </div>
            <div class="attestation-item full-width">
              <span class="label">Location URI</span>
              <span class="value">
                <code class="uri">{loc?.uri || sbom?.source_url || '-'}</code>
              </span>
            </div>
          {/if}

          {#if attestation?.predicate?.digest?.sha256 || sbom?.raw_hash}
            <div class="attestation-item">
              <span class="label">SBOM Hash</span>
              <span class="value">
                <code title={attestation?.predicate?.digest?.sha256 || sbom?.raw_hash}>
                  {truncateHash(attestation?.predicate?.digest?.sha256 || sbom?.raw_hash)}
                </code>
              </span>
            </div>
          {/if}

          {#if attestation?.subject?.[0]?.digest}
            {@const subjectDigest = Object.entries(attestation.subject[0].digest)[0]}
            <div class="attestation-item">
              <span class="label">Subject Digest</span>
              <span class="value">
                <code title={`${subjectDigest[0]}:${subjectDigest[1]}`}>
                  {subjectDigest[0]}:{truncateHash(subjectDigest[1])}
                </code>
              </span>
            </div>
          {/if}

          {#if attestation?.predicate?.timestamp || sbom?.created_at}
            <div class="attestation-item">
              <span class="label">Generated</span>
              <span class="value">{formatDate(attestation?.predicate?.timestamp || sbom?.created_at)}</span>
            </div>
          {/if}

          <div class="attestation-item">
            <span class="label">Package Count</span>
            <span class="value">{sbom?.package_count || packages.length || 0}</span>
          </div>

          {#if sbom?.vulnerability_count > 0}
            <div class="attestation-item">
              <span class="label">Vulnerabilities</span>
              <span class="value vuln-count">
                {sbom.vulnerability_count} total
                {#if sbom.critical_count > 0}
                  <Badge variant="error">{sbom.critical_count} critical</Badge>
                {/if}
                {#if sbom.high_count > 0}
                  <Badge variant="warning">{sbom.high_count} high</Badge>
                {/if}
              </span>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- NTIA Compliance -->
    {#if ntiaCompliance}
      <div class="section">
        <div class="section-header">
          <h3 class="section-title"><SbomIcon size={18} stroke={1.75} aria-hidden="true" /> <span>NTIA Minimum Elements</span></h3>
          <Badge variant={ntiaCompliant ? 'success' : 'warning'}>
            <span class="badge-with-icon">
              {#if ntiaCompliant}
                <SuccessIcon size={14} stroke={1.75} aria-hidden="true" /> Compliant
              {:else}
                <WarningIcon size={14} stroke={1.75} aria-hidden="true" /> {passedCount}/7 Fields
              {/if}
            </span>
          </Badge>
        </div>
        <div class="ntia-grid">
          {#each ntiaFields as field}
            {@const passed = ntiaCompliance[field.key]}
            {@const FieldIcon = field.icon}
            <div class="ntia-item" class:passed class:failed={!passed}>
              <span class="ntia-status-icon" aria-hidden="true">
                {#if passed}
                  <SuccessIcon size={16} stroke={1.75} />
                {:else}
                  <WarningIcon size={16} stroke={1.75} />
                {/if}
              </span>
              <span class="ntia-label"><FieldIcon size={16} stroke={1.75} aria-hidden="true" /> {field.label}</span>
            </div>
          {/each}
        </div>
        <p class="ntia-info">
          <a href="https://www.ntia.gov/page/software-bill-materials" target="_blank" rel="noopener">
            NTIA Minimum Elements for SBOM →
          </a>
        </p>
      </div>
    {/if}

    <!-- Packages Table -->
    {#if packages.length > 0}
      <div class="section">
        <h3 class="section-title"><ArtifactIcon size={18} stroke={1.75} aria-hidden="true" /> <span>Packages ({packages.length})</span></h3>
        <Table columns={packageColumns} data={packages} />
      </div>
    {/if}
  {/if}
</div>

<style>
  .sbom-details {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.25rem;
    border: 1px solid var(--border-color);
  }

  .section h3 {
    font-size: 1rem;
    color: var(--text-primary);
    margin: 0 0 1rem;
  }

  .section-title,
  .value-with-icon,
  .badge-with-icon {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
  }

  .section-header h3 {
    margin: 0;
  }

  /* Attestation Grid */
  .attestation-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
  }

  .attestation-item {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .attestation-item.full-width {
    grid-column: 1 / -1;
  }

  .attestation-item .label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }

  .attestation-item .value {
    font-size: 0.875rem;
    color: var(--text-primary);
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .attestation-item .value code {
    font-family: monospace;
    font-size: 0.8rem;
    background: var(--code-bg, rgba(0,0,0,0.2));
    padding: 0.125rem 0.375rem;
    border-radius: 3px;
  }

  .attestation-item .value code.uri {
    word-break: break-all;
    max-width: 100%;
  }

  .vuln-count {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  /* NTIA Grid */
  .ntia-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
  }

  .ntia-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    border-radius: 6px;
    font-size: 0.875rem;
    background: var(--hover-bg, rgba(255,255,255,0.03));
  }

  .ntia-item.passed {
    color: #10b981;
  }

  .ntia-item.failed {
    color: #f87171;
    opacity: 0.7;
  }

  .ntia-status-icon {
    display: inline-flex;
    align-items: center;
    width: 1rem;
    flex-shrink: 0;
  }

  .ntia-label {
    flex: 1;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .ntia-info {
    margin-top: 1rem;
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .ntia-info a {
    color: var(--primary);
    text-decoration: none;
  }

  .ntia-info a:hover {
    text-decoration: underline;
  }

  :global(.truncate) {
    display: inline-block;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
  }
</style>
