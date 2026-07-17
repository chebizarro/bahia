<script>
  import { page } from '$app/stores';
  import { untrack } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { runSecurityRescan } from '$lib/security-rescan.js';
  import { SecurityIcon, ErrorIcon } from '$lib/icons/domain-icons.js';
  import {
    securityState,
    listSecurityFindings,
    computeSeverityCounts
  } from '$lib/stores/security.svelte.js';

  let loadStarted = false;
  let runId = $derived($page.params.id);

  $effect(() => {
    const id = runId;
    if (loadStarted) return;
    loadStarted = true;
    void untrack(() => loadRunFindings(id));
  });

  async function loadRunFindings(id) {
    await listSecurityFindings({ run_id: id });
  }

  let findings = $derived(securityState.findings);
  let severity = $derived(computeSeverityCounts(findings));
  let loading = $derived(securityState.findingsLoading);
  let error = $derived(securityState.findingsError);

  // Derive target info from first finding
  let targetHash = $derived(findings.length > 0 ? findings[0].target_key_hash : null);

  // Rescan state
  let rescanning = $state(false);

  async function handleRescan() {
    if (!targetHash) return;
    rescanning = true;
    await runSecurityRescan(targetHash);
    rescanning = false;
  }

  function severityVariant(sev) {
    switch (String(sev || '').toLowerCase()) {
      case 'critical': return 'error';
      case 'high': return 'warning';
      case 'moderate': return 'default';
      case 'low': return 'default';
      default: return 'default';
    }
  }

  function severityLabel(sev) {
    const s = String(sev || '').toLowerCase();
    return s.charAt(0).toUpperCase() + s.slice(1) || 'Unknown';
  }

  function escapeHtml(value) {
    return String(value ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function packageLabel(finding) {
    const pkg = finding.package;
    if (!pkg) return finding.package_name || '-';
    const name = pkg.name || '';
    const eco = pkg.ecosystem || '';
    const ver = pkg.version || '';
    if (!name) return '-';
    let label = eco ? `${eco}/${name}` : name;
    if (ver) label += `@${ver}`;
    return label;
  }

  function formatReferences(refs) {
    if (!Array.isArray(refs) || refs.length === 0) return '-';
    return refs
      .map((r) => `<a href="${escapeHtml(r)}" target="_blank" rel="noopener noreferrer" class="ref-link">${escapeHtml(new URL(r).hostname)}</a>`)
      .join(', ');
  }

  let columns = $derived([
    {
      key: 'osv_id',
      label: 'OSV ID',
      render: (r) => r.osv_id
        ? `<a href="https://osv.dev/vulnerability/${escapeHtml(r.osv_id)}" target="_blank" rel="noopener noreferrer" class="osv-link">${escapeHtml(r.osv_id)}</a>`
        : '-'
    },
    {
      key: 'cve',
      label: 'CVE',
      text: (r) => r.cve || '-'
    },
    {
      key: 'package',
      label: 'Package',
      text: (r) => packageLabel(r)
    },
    {
      key: 'severity',
      label: 'Severity',
      render: (r) => {
        const sev = severityLabel(r.severity);
        const cls = severityVariant(r.severity);
        return `<span class="severity-badge severity-${cls}">${escapeHtml(sev)}</span>`;
      }
    },
    {
      key: 'summary',
      label: 'Summary',
      text: (r) => r.summary || '-'
    },
    {
      key: 'aliases',
      label: 'Aliases',
      text: (r) => Array.isArray(r.aliases) && r.aliases.length > 0 ? r.aliases.join(', ') : '-'
    },
    {
      key: 'references',
      label: 'References',
      render: (r) => formatReferences(r.references)
    }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <a href="/security" class="back-link">&larr; Security</a>
      <h1>
        <SecurityIcon size={24} strokeWidth={1.75} ariaHidden="true" />
        Scan Run
      </h1>
      <span class="run-id">{runId}</span>
    </div>
    {#if targetHash}
      <LoadingButton
        loading={rescanning}
        onclick={handleRescan}
        label="Rescan Target"
        loadingLabel="Submitting..."
      />
    {/if}
  </div>

  <!-- Severity Summary -->
  {#if !loading && findings.length > 0}
    <div class="severity-summary">
      <div class="severity-card critical">
        <span class="severity-count">{severity.critical}</span>
        <span class="severity-label">Critical</span>
      </div>
      <div class="severity-card high">
        <span class="severity-count">{severity.high}</span>
        <span class="severity-label">High</span>
      </div>
      <div class="severity-card moderate">
        <span class="severity-count">{severity.moderate}</span>
        <span class="severity-label">Moderate</span>
      </div>
      <div class="severity-card low">
        <span class="severity-count">{severity.low}</span>
        <span class="severity-label">Low</span>
      </div>
    </div>

    <div class="meta-bar">
      <span class="meta-item"><strong>Target:</strong> {targetHash ? targetHash.slice(0, 24) + '...' : '-'}</span>
      <span class="meta-item"><strong>Total findings:</strong> {findings.length}</span>
    </div>
  {/if}

  <!-- Findings Table -->
  {#if loading}
    <p class="loading">Loading scan run findings...</p>
  {:else if error}
    <EmptyState
      iconComponent={ErrorIcon}
      title="Error loading findings"
      message={error}
    />
  {:else if findings.length === 0}
    <EmptyState
      iconComponent={SecurityIcon}
      title="No findings for this scan run"
      message="This scan run did not produce any vulnerability findings."
    />
  {:else}
    <Table columns={columns} data={findings} />
  {/if}
</div>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  .back-link {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.9rem;
  }
  .back-link:hover {
    color: var(--primary);
  }
  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 1.25rem;
  }
  .run-id {
    color: var(--text-muted);
    font-size: 0.8rem;
    font-family: monospace;
  }
  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  /* Severity Summary */
  .severity-summary {
    display: flex;
    gap: 1rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }
  .severity-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 0.75rem 1.25rem;
    border-radius: 8px;
    border: 1px solid var(--border);
    min-width: 80px;
  }
  .severity-count {
    font-size: 1.25rem;
    font-weight: 700;
    line-height: 1;
  }
  .severity-label {
    font-size: 0.7rem;
    font-weight: 500;
    margin-top: 0.25rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .severity-card.critical {
    background: #450a0a;
    border-color: #dc2626;
    color: #fca5a5;
  }
  .severity-card.high {
    background: #451a03;
    border-color: #ea580c;
    color: #fed7aa;
  }
  .severity-card.moderate {
    background: #422006;
    border-color: #d97706;
    color: #fde68a;
  }
  .severity-card.low {
    background: #1a2e05;
    border-color: #65a30d;
    color: #d9f99d;
  }

  /* Meta bar */
  .meta-bar {
    display: flex;
    gap: 2rem;
    margin-bottom: 1.5rem;
    padding: 0.75rem 1rem;
    background: var(--bg-secondary);
    border-radius: 6px;
    font-size: 0.85rem;
    color: var(--text-muted);
  }
  .meta-item strong {
    color: var(--text);
  }

  /* Table links */
  :global(.osv-link) {
    color: var(--primary);
    text-decoration: none;
    font-weight: 600;
  }
  :global(.osv-link:hover) {
    text-decoration: underline;
  }
  :global(.ref-link) {
    color: var(--primary);
    text-decoration: none;
    font-size: 0.85rem;
  }
  :global(.ref-link:hover) {
    text-decoration: underline;
  }
  :global(.severity-badge) {
    display: inline-flex;
    align-items: center;
    padding: 0.2rem 0.5rem;
    border-radius: 4px;
    font-weight: 600;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  :global(.severity-error) {
    background: #450a0a;
    color: #fca5a5;
  }
  :global(.severity-warning) {
    background: #451a03;
    color: #fed7aa;
  }
  :global(.severity-default) {
    background: #374151;
    color: #d1d5db;
  }
</style>
