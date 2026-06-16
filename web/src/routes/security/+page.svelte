<script>
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { SecurityIcon, ErrorIcon, WarningIcon, SuccessIcon } from '$lib/icons/domain-icons.js';
  import {
    securityState,
    listSecurityFindings,
    listSecuritySchedules,
    rescanSecurityTarget,
    computeSeverityCounts
  } from '$lib/stores/security.svelte.js';

  let loadStarted = false;

  $effect(() => {
    if (loadStarted) return;
    loadStarted = true;
    void untrack(() => loadData());
  });

  async function loadData() {
    await Promise.allSettled([
      listSecurityFindings(),
      listSecuritySchedules()
    ]);
  }

  let findings = $derived(securityState.findings);
  let schedules = $derived(securityState.schedules);
  let severity = $derived(computeSeverityCounts(findings));
  let loading = $derived(securityState.findingsLoading || securityState.schedulesLoading);
  let error = $derived(securityState.findingsError || securityState.schedulesError);

  // Active tab
  let activeTab = $state('findings');

  // Rescan handling
  let rescanningHash = $state(null);

  async function handleRescan(targetKeyHash) {
    rescanningHash = targetKeyHash;
    try {
      await rescanSecurityTarget(targetKeyHash);
      await listSecurityFindings();
    } catch (err) {
      console.error('Rescan failed:', err);
    } finally {
      rescanningHash = null;
    }
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

  function formatDate(dateStr) {
    if (!dateStr) return '-';
    const date = new Date(dateStr);
    if (isNaN(date.getTime())) return '-';
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
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

  // Unique target hashes in findings for rescan buttons
  let targetHashes = $derived([...new Set(findings.map((f) => f.target_key_hash).filter(Boolean))]);

  let findingsColumns = $derived([
    {
      key: 'osv_id',
      label: 'OSV ID',
      text: (r) => r.osv_id || '-'
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
    }
  ]);

  let schedulesColumns = $derived([
    {
      key: 'target_key_hash',
      label: 'Target',
      text: (r) => r.target_key_hash ? r.target_key_hash.slice(0, 16) + '...' : '-'
    },
    {
      key: 'enabled',
      label: 'Enabled',
      render: (r) => r.enabled
        ? '<span class="severity-badge severity-success">Yes</span>'
        : '<span class="severity-badge severity-default">No</span>'
    },
    {
      key: 'interval_seconds',
      label: 'Interval',
      text: (r) => {
        const secs = r.interval_seconds;
        if (!secs) return '-';
        if (secs >= 86400) return `${Math.round(secs / 86400)}d`;
        if (secs >= 3600) return `${Math.round(secs / 3600)}h`;
        return `${Math.round(secs / 60)}m`;
      }
    },
    {
      key: 'next_due_at',
      label: 'Next Due',
      render: (r) => formatDate(r.next_due_at)
    },
    {
      key: 'last_dispatched_at',
      label: 'Last Run',
      render: (r) => formatDate(r.last_dispatched_at)
    }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>
        <SecurityIcon size={28} strokeWidth={1.75} ariaHidden="true" />
        Security
      </h1>
      <span class="count">
        {#if severity.total > 0}
          {severity.total} findings
        {:else}
          No findings
        {/if}
      </span>
    </div>
  </div>

  <!-- Severity Summary Cards -->
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
      {#if severity.unknown > 0}
        <div class="severity-card unknown">
          <span class="severity-count">{severity.unknown}</span>
          <span class="severity-label">Unknown</span>
        </div>
      {/if}
    </div>
  {/if}

  <!-- Tabs -->
  <div class="tabs">
    <button
      class="tab"
      class:active={activeTab === 'findings'}
      onclick={() => (activeTab = 'findings')}
    >
      Findings
    </button>
    <button
      class="tab"
      class:active={activeTab === 'schedules'}
      onclick={() => (activeTab = 'schedules')}
    >
      Schedules
    </button>
  </div>

  <!-- Findings Tab -->
  {#if activeTab === 'findings'}
    {#if loading}
      <p class="loading">Loading security findings...</p>
    {:else if error}
      <EmptyState
        iconComponent={ErrorIcon}
        title="Error loading findings"
        message={error}
      />
    {:else if findings.length === 0}
      <EmptyState
        iconComponent={SecurityIcon}
        title="No vulnerability findings"
        message="Security scan findings will appear here after scans complete. Scans run automatically when SBOMs are imported, or on a configured schedule."
      />
    {:else}
      {#if targetHashes.length > 0}
        <div class="actions-bar">
          <span class="actions-label">Rescan targets:</span>
          {#each targetHashes.slice(0, 5) as hash}
            <LoadingButton
              loading={rescanningHash === hash}
              onclick={() => handleRescan(hash)}
              label={hash.slice(0, 12) + '...'}
              loadingLabel="Scanning..."
            />
          {/each}
        </div>
      {/if}
      <Table
        columns={findingsColumns}
        data={findings}
        onRowClick={(row) => {
          if (row.run_id) goto(`/security/${row.run_id}`);
        }}
      />
    {/if}
  {/if}

  <!-- Schedules Tab -->
  {#if activeTab === 'schedules'}
    {#if securityState.schedulesLoading}
      <p class="loading">Loading scan schedules...</p>
    {:else if securityState.schedulesError}
      <EmptyState
        iconComponent={ErrorIcon}
        title="Error loading schedules"
        message={securityState.schedulesError}
      />
    {:else if schedules.length === 0}
      <EmptyState
        iconComponent={SecurityIcon}
        title="No scan schedules"
        message="Security scan schedules are derived from policies. Create a policy with security scan rules to enable scheduled scanning."
      />
    {:else}
      <Table columns={schedulesColumns} data={schedules} />
    {/if}
  {/if}
</div>

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
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
    margin-bottom: 1.5rem;
    flex-wrap: wrap;
  }
  .severity-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 1rem 1.5rem;
    border-radius: 8px;
    border: 1px solid var(--border);
    min-width: 100px;
  }
  .severity-count {
    font-size: 1.5rem;
    font-weight: 700;
    line-height: 1;
  }
  .severity-label {
    font-size: 0.75rem;
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
  .severity-card.unknown {
    background: #1f2937;
    border-color: #6b7280;
    color: #d1d5db;
  }

  /* Tabs */
  .tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border);
    margin-bottom: 1.5rem;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1.5rem;
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    font-size: 0.9rem;
    cursor: pointer;
    transition: all 0.2s;
  }
  .tab:hover {
    color: var(--text);
    background: var(--bg-hover);
  }
  .tab.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  /* Actions Bar */
  .actions-bar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }
  .actions-label {
    font-size: 0.85rem;
    color: var(--text-muted);
  }

  /* Severity badges in table */
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
  :global(.severity-success) {
    background: #065f46;
    color: #6ee7b7;
  }
</style>
