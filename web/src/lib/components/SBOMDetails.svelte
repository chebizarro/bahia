<script>
  import Badge from './Badge.svelte';
  import Table from './Table.svelte';
  import LoadingButton from './LoadingButton.svelte';
  import EmptyState from './EmptyState.svelte';
  import { api } from '$lib/api/client.js';
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

  // Raw SBOM viewer state
  let rawSBOM = $state(null);
  let rawSBOMFormat = $state(null);
  let rawSBOMLoading = $state(false);
  let rawSBOMError = $state(null);
  let rawSBOMExpanded = $state(false);
  let rawViewTab = $state('summary'); // summary, packages, raw

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

  // Full SBOM package table (parsed from raw)
  let fullPackageColumns = $derived([
    { key: 'name', label: 'Package Name' },
    { key: 'version', label: 'Version' },
    { key: 'license', label: 'License' },
    { key: 'purl', label: 'PURL', render: (r) => r.purl ? `<code class="truncate-wide">${r.purl}</code>` : '-' },
    { key: 'ecosystem', label: 'Type' }
  ]);

  let blossomURI = $derived(attestation?.predicate?.location?.uri || sbom?.source_url || null);
  let sbomFormat = $derived(sbom?.format || attestation?.predicate?.format || null);

  // Parsed SBOM document info
  let docInfo = $derived(rawSBOM ? parseSBOMDocInfo(rawSBOM, rawSBOMFormat) : null);
  let fullPackages = $derived(rawSBOM ? parseSBOMPackages(rawSBOM, rawSBOMFormat) : []);

  function storageTypePresentation(type) {
    const labels = {
      'blossom': { label: 'Blossom', icon: BlossomIcon },
      'oci-referrer': { label: 'OCI Referrer', icon: ArtifactIcon },
      'package-backend': { label: 'Package Backend', icon: GenericFileIcon }
    };
    return labels[type] || { label: type || 'Unknown', icon: UnknownIcon };
  }

  function formatFormat(format) {
    const labels = { 'spdx': 'SPDX', 'cyclonedx': 'CycloneDX' };
    return labels[format] || format || 'Unknown';
  }

  function formatDate(dateStr) {
    if (!dateStr) return '-';
    try { return new Date(dateStr).toLocaleString(); } catch { return dateStr; }
  }

  function truncateHash(hash, len = 16) {
    if (!hash) return '-';
    if (hash.length <= len * 2) return hash;
    return `${hash.slice(0, len)}...${hash.slice(-8)}`;
  }


  function blossomHashFromURI(uri) {
    try {
      const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost';
      const parsed = new URL(uri, origin);
      const last = parsed.pathname.split('/').filter(Boolean).pop() || '';
      const match = last.match(/[0-9a-f]{64}/i);
      return match ? match[0].toLowerCase() : '';
    } catch {
      const match = String(uri || '').match(/[0-9a-f]{64}/i);
      return match ? match[0].toLowerCase() : '';
    }
  }

  function sbomBlobHash() {
    const explicit = String(attestation?.predicate?.digest?.sha256 || sbom?.raw_hash || '').replace(/^sha256:/i, '').trim();
    if (/^[0-9a-f]{64}$/i.test(explicit)) return explicit.toLowerCase();
    return blossomHashFromURI(blossomURI);
  }

  function isMixedContentURL(uri) {
    try {
      if (typeof window === 'undefined' || window.location?.protocol !== 'https:') return false;
      return new URL(uri, window.location.origin).protocol === 'http:';
    } catch {
      return false;
    }
  }

  async function loadSBOMText() {
    const hash = sbomBlobHash();
    // Prefer the same-origin Blossom proxy (/blossom/blob/<hash>): it avoids
    // browser mixed-content blocks when the SBOM lives on an http:// Blossom
    // server while the dashboard is served over https, and sidesteps CORS.
    if (hash && api?.fetchBlossomBlob) {
      try {
        const resp = await api.fetchBlossomBlob(hash);
        return await resp.text();
      } catch (proxyErr) {
        // If a direct fetch would be blocked as mixed content, surface the
        // proxy failure rather than triggering a guaranteed browser block.
        if (isMixedContentURL(blossomURI)) throw proxyErr;
      }
    }
    if (isMixedContentURL(blossomURI)) {
      throw new Error('This SBOM is stored on an insecure (http) Blossom endpoint and cannot be loaded from a secure (https) page. Configure the Blossom server for HTTPS or route it through Bahia.');
    }
    const resp = await fetch(blossomURI);
    if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`);
    return await resp.text();
  }

  async function fetchSBOMContents() {
    if (!blossomURI || rawSBOMLoading) return;
    rawSBOMLoading = true;
    rawSBOMError = null;
    rawSBOM = null;
    try {
      const text = await loadSBOMText();
      const parsed = JSON.parse(text);
      rawSBOM = parsed;
      rawSBOMFormat = detectSBOMFormat(parsed) || sbomFormat;
      rawSBOMExpanded = true;
      rawViewTab = 'summary';
    } catch (err) {
      rawSBOMError = `Could not load SBOM: ${err.message || 'unknown error'}`;
    } finally {
      rawSBOMLoading = false;
    }
  }

  function detectSBOMFormat(data) {
    if (!data || typeof data !== 'object') return null;
    if (data.spdxVersion || data.SPDXID) return 'spdx';
    if (data.bomFormat === 'CycloneDX' || data.specVersion) return 'cyclonedx';
    return null;
  }

  function parseSBOMDocInfo(data, format) {
    if (!data) return null;
    if (format === 'spdx') {
      const creation = data.creationInfo || {};
      return {
        name: data.name || '-',
        version: data.spdxVersion || '-',
        namespace: data.documentNamespace || null,
        created: creation.created || null,
        creators: Array.isArray(creation.creators) ? creation.creators : [],
        dataLicense: data.dataLicense || null,
        documentDescribes: Array.isArray(data.documentDescribes) ? data.documentDescribes : [],
        packageCount: Array.isArray(data.packages) ? data.packages.length : 0,
        relationshipCount: Array.isArray(data.relationships) ? data.relationships.length : 0
      };
    }
    if (format === 'cyclonedx') {
      const metadata = data.metadata || {};
      const tool = metadata.tools?.[0] || metadata.tools?.components?.[0] || {};
      return {
        name: metadata.component?.name || '-',
        version: `CycloneDX ${data.specVersion || '?'}`,
        namespace: data.serialNumber || null,
        created: metadata.timestamp || null,
        creators: tool.name ? [`Tool: ${tool.name}${tool.version ? `@${tool.version}` : ''}`] : [],
        dataLicense: null,
        documentDescribes: metadata.component ? [metadata.component.name] : [],
        packageCount: Array.isArray(data.components) ? data.components.length : 0,
        relationshipCount: Array.isArray(data.dependencies) ? data.dependencies.length : 0
      };
    }
    return { name: '-', version: '-', namespace: null, created: null, creators: [], dataLicense: null, documentDescribes: [], packageCount: 0, relationshipCount: 0 };
  }

  function parseSBOMPackages(data, format) {
    if (!data) return [];
    if (format === 'spdx') {
      return (data.packages || []).map((pkg) => ({
        name: pkg.name || '-',
        version: pkg.versionInfo || '-',
        license: pkg.licenseConcluded || pkg.licenseDeclared || '-',
        purl: extractPurl(pkg.externalRefs) || '',
        ecosystem: pkg.primaryPackagePurpose || extractEcosystem(pkg.externalRefs) || '-'
      }));
    }
    if (format === 'cyclonedx') {
      return (data.components || []).map((comp) => ({
        name: comp.name || '-',
        version: comp.version || '-',
        license: extractCDXLicense(comp.licenses) || '-',
        purl: comp.purl || '',
        ecosystem: comp.type || '-'
      }));
    }
    return [];
  }

  function extractPurl(externalRefs) {
    if (!Array.isArray(externalRefs)) return '';
    const purlRef = externalRefs.find((r) => r.referenceType === 'purl' || r.referenceCategory === 'PACKAGE-MANAGER');
    return purlRef?.referenceLocator || '';
  }

  function extractEcosystem(externalRefs) {
    const purl = extractPurl(externalRefs);
    if (!purl) return '';
    const match = purl.match(/^pkg:([^/]+)\//);
    return match ? match[1] : '';
  }

  function extractCDXLicense(licenses) {
    if (!Array.isArray(licenses)) return '';
    return licenses.map((l) => l.license?.id || l.license?.name || l.expression || '').filter(Boolean).join(', ');
  }

  function downloadSBOM() {
    if (!rawSBOM) return;
    const json = JSON.stringify(rawSBOM, null, 2);
    const blob = new Blob([json], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    const ext = rawSBOMFormat === 'cyclonedx' ? 'cdx.json' : 'spdx.json';
    a.download = `sbom.${ext}`;
    a.click();
    URL.revokeObjectURL(url);
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
        <h3 class="section-title"><SignatureIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>Attestation Details</span></h3>
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
              <span class="value value-with-icon"><StorageIcon size={16} strokeWidth={1.75} ariaHidden="true" /> {storage.label}</span>
            </div>
            <div class="attestation-item full-width">
              <span class="label">Location URI</span>
              <span class="value uri-row">
                <code class="uri">{loc?.uri || sbom?.source_url || '-'}</code>
                {#if blossomURI}
                  <LoadingButton
                    variant="secondary"
                    loading={rawSBOMLoading}
                    onclick={fetchSBOMContents}
                  >
                    {rawSBOM ? 'Reload' : 'View Contents'}
                  </LoadingButton>
                {/if}
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

    <!-- Loaded SBOM Contents -->
    {#if rawSBOMError}
      <div class="section">
        <p class="error-message">{rawSBOMError}</p>
      </div>
    {/if}

    {#if rawSBOM && rawSBOMExpanded}
      <div class="section sbom-viewer">
        <div class="viewer-header">
          <h3 class="section-title"><GenericFileIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>SBOM Contents</span></h3>
          <div class="viewer-actions">
            <button class="small-button" onclick={downloadSBOM}>Download</button>
            <button class="small-button" onclick={() => rawSBOMExpanded = false}>Collapse</button>
          </div>
        </div>

        <div class="viewer-tabs">
          <button class="viewer-tab" class:active={rawViewTab === 'summary'} onclick={() => rawViewTab = 'summary'}>Summary</button>
          <button class="viewer-tab" class:active={rawViewTab === 'packages'} onclick={() => rawViewTab = 'packages'}>
            Components ({fullPackages.length})
          </button>
          <button class="viewer-tab" class:active={rawViewTab === 'raw'} onclick={() => rawViewTab = 'raw'}>Raw JSON</button>
        </div>

        {#if rawViewTab === 'summary' && docInfo}
          <div class="doc-info-grid">
            <div class="doc-info-item">
              <span class="label">Document</span>
              <span class="value">{docInfo.name}</span>
            </div>
            <div class="doc-info-item">
              <span class="label">Spec Version</span>
              <span class="value">{docInfo.version}</span>
            </div>
            {#if docInfo.namespace}
              <div class="doc-info-item full-width">
                <span class="label">Namespace</span>
                <span class="value"><code class="uri">{docInfo.namespace}</code></span>
              </div>
            {/if}
            {#if docInfo.created}
              <div class="doc-info-item">
                <span class="label">Created</span>
                <span class="value">{formatDate(docInfo.created)}</span>
              </div>
            {/if}
            {#if docInfo.dataLicense}
              <div class="doc-info-item">
                <span class="label">Data License</span>
                <span class="value">{docInfo.dataLicense}</span>
              </div>
            {/if}
            {#if docInfo.creators.length > 0}
              <div class="doc-info-item full-width">
                <span class="label">Creators</span>
                <span class="value creators-list">
                  {#each docInfo.creators as creator}
                    <code>{creator}</code>
                  {/each}
                </span>
              </div>
            {/if}
            <div class="doc-info-item">
              <span class="label">Components</span>
              <span class="value">{docInfo.packageCount}</span>
            </div>
            <div class="doc-info-item">
              <span class="label">Relationships</span>
              <span class="value">{docInfo.relationshipCount}</span>
            </div>
            {#if docInfo.documentDescribes.length > 0}
              <div class="doc-info-item full-width">
                <span class="label">Describes</span>
                <span class="value creators-list">
                  {#each docInfo.documentDescribes as ref}
                    <code>{ref}</code>
                  {/each}
                </span>
              </div>
            {/if}
          </div>
        {:else if rawViewTab === 'packages'}
          {#if fullPackages.length > 0}
            <Table columns={fullPackageColumns} data={fullPackages} />
          {:else}
            <p class="muted">No components found in the SBOM document.</p>
          {/if}
        {:else if rawViewTab === 'raw'}
          <div class="raw-json-container">
            <pre class="raw-json">{JSON.stringify(rawSBOM, null, 2)}</pre>
          </div>
        {/if}
      </div>
    {/if}

    <!-- NTIA Compliance -->
    {#if ntiaCompliance}
      <div class="section">
        <div class="section-header">
          <h3 class="section-title"><SbomIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>NTIA Minimum Elements</span></h3>
          <Badge variant={ntiaCompliant ? 'success' : 'warning'}>
            <span class="badge-with-icon">
              {#if ntiaCompliant}
                <SuccessIcon size={14} strokeWidth={1.75} ariaHidden="true" /> Compliant
              {:else}
                <WarningIcon size={14} strokeWidth={1.75} ariaHidden="true" /> {passedCount}/7 Fields
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
                  <SuccessIcon size={16} strokeWidth={1.75} />
                {:else}
                  <WarningIcon size={16} strokeWidth={1.75} />
                {/if}
              </span>
              <span class="ntia-label"><FieldIcon size={16} strokeWidth={1.75} ariaHidden="true" /> {field.label}</span>
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

    <!-- Packages Table (from attestation projection, shown when raw SBOM is not loaded) -->
    {#if packages.length > 0 && !rawSBOM}
      <div class="section">
        <h3 class="section-title"><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>Packages ({packages.length})</span></h3>
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

  .attestation-item.full-width,
  .doc-info-item.full-width {
    grid-column: 1 / -1;
  }

  .label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }

  .value {
    font-size: 0.875rem;
    color: var(--text-primary);
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .value code {
    font-family: monospace;
    font-size: 0.8rem;
    background: var(--code-bg, rgba(0,0,0,0.2));
    padding: 0.125rem 0.375rem;
    border-radius: 3px;
  }

  .value code.uri {
    word-break: break-all;
    max-width: 100%;
  }

  .uri-row {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    flex-wrap: wrap;
  }

  .vuln-count {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  /* SBOM viewer */
  .sbom-viewer {
    border-color: var(--primary, #3b82f6);
  }

  .viewer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
  }

  .viewer-header h3 {
    margin: 0;
  }

  .viewer-actions {
    display: flex;
    gap: 0.5rem;
  }

  .small-button {
    background: var(--hover-bg, rgba(255,255,255,0.06));
    border: 1px solid var(--border-color);
    color: var(--text-primary);
    padding: 0.25rem 0.75rem;
    border-radius: 4px;
    font-size: 0.8rem;
    cursor: pointer;
    transition: background 0.15s;
  }

  .small-button:hover {
    background: var(--hover-bg-strong, rgba(255,255,255,0.1));
  }

  .viewer-tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border-color);
    margin-bottom: 1rem;
  }

  .viewer-tab {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    padding: 0.5rem 1rem;
    font-size: 0.8rem;
    font-weight: 500;
    color: var(--text-muted);
    cursor: pointer;
    margin-bottom: -1px;
    transition: all 0.15s;
  }

  .viewer-tab:hover {
    color: var(--text-primary);
  }

  .viewer-tab.active {
    color: var(--primary, #3b82f6);
    border-bottom-color: var(--primary, #3b82f6);
  }

  /* Doc info grid */
  .doc-info-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
  }

  .doc-info-item {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .creators-list {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .muted {
    color: var(--text-muted);
    font-size: 0.875rem;
    padding: 1rem 0;
  }

  /* Raw JSON viewer */
  .raw-json-container {
    max-height: 600px;
    overflow: auto;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--code-bg, rgba(0,0,0,0.3));
  }

  .raw-json {
    margin: 0;
    padding: 1rem;
    font-family: monospace;
    font-size: 0.75rem;
    line-height: 1.5;
    color: var(--text-primary);
    white-space: pre;
    tab-size: 2;
  }

  .error-message {
    font-size: 0.875rem;
    color: var(--error);
    background: rgba(239, 68, 68, 0.1);
    border: 1px solid rgba(239, 68, 68, 0.2);
    padding: 0.75rem;
    border-radius: 4px;
    margin: 0;
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

  :global(.truncate-wide) {
    display: inline-block;
    max-width: 350px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
  }
</style>
