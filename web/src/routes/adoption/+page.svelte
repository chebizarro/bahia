<script>
  import { scanAdoption } from '$lib/stores/public-controlplane.svelte.js';
  import { orgsState } from '$lib/stores/orgs.svelte.js';

  let orgId = $state('');
  let targetName = $state('');
  let endpointRef = $state('');
  let findings = $state([]);
  let nextOffset = $state(0);
  let totalFindings = $state(0);
  let truncated = $state(false);
  let loading = $state(false);
  let error = $state('');

  $effect(() => {
    if (!orgId && orgsState.orgs.length === 1) orgId = orgsState.orgs[0].id;
  });

  async function scan(offset = 0) {
    loading = true;
    error = '';
    try {
      const page = await scanAdoption({ org_id: orgId, targets: [{ name: targetName, endpoint_ref: endpointRef }],
        limit: 20, offset });
      findings = offset === 0 ? page.findings : [...findings, ...page.findings];
      nextOffset = page.next_offset;
      totalFindings = page.total_findings;
      truncated = page.truncated === true;
    } catch (cause) {
      error = cause?.message || 'Adoption scan failed';
    } finally {
      loading = false;
    }
  }

  function submitScan(event) {
    event.preventDefault();
    return scan(0);
  }
</script>

<svelte:head><title>Adoption scan · Bahia</title></svelte:head>

<div class="page">
  <header><h1>Adoption scan</h1><p>Scan configured runtime targets with a signed intent. Findings below come from the bounded daemon status event.</p></header>
  <form onsubmit={submitScan} data-testid="adoption-scan-form">
    <label>Organization ID<input bind:value={orgId} required /></label>
    <label>Target name<input bind:value={targetName} required /></label>
    <label>Runtime endpoint reference<input bind:value={endpointRef} required /></label>
    <button type="submit" disabled={loading}>{loading ? 'Scanning…' : 'Scan target'}</button>
  </form>
  {#if error}<p role="alert">{error}</p>{/if}
  {#if findings.length > 0}
    <p role="status">Showing {findings.length} of {totalFindings} findings</p>
    <div class="table-scroll"><table><thead><tr><th>Target</th><th>Container</th><th>Image</th><th>Proposed service</th><th>Adoptable</th><th>Warnings</th></tr></thead>
      <tbody>{#each findings as finding, index (`${finding.target_name}:${finding.container_id}:${index}`)}
        <tr><td>{finding.target_name || '-'}</td><td>{finding.container_name || finding.container_id || '-'}</td><td>{finding.image_ref || '-'}</td>
          <td>{finding.proposed_service_name || '-'}</td><td>{finding.adoptable ? 'Yes' : 'No'}</td><td>{finding.warnings_count ?? 0}</td></tr>
      {/each}</tbody></table></div>
    {#if truncated}<button type="button" disabled={loading} onclick={() => scan(nextOffset)}>Load more findings</button>{/if}
  {:else if !loading && !error}
    <p>No scan findings loaded.</p>
  {/if}
</div>

<style>
  .page { display: grid; gap: 1rem; }
  form { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: end; }
  label { display: grid; gap: 0.25rem; }
  .table-scroll { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid var(--border-color); }
</style>
