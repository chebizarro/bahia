<script>
  import Badge from '$lib/components/Badge.svelte';
  import { configFabricHref, shortEventId } from './model.js';

  let { rows = [] } = $props();
</script>

<div class="table-container">
  <table aria-label="Config Fabric drift">
    <thead>
      <tr>
        <th>Service / Policy</th>
        <th>Scope</th>
        <th>Desired</th>
        <th>Applied</th>
        <th>Drift</th>
        <th>Last rejection</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as row}
        <tr>
          <td>
            <a class="coordinate" href={configFabricHref(row)}>
              <strong>{row.service_id}</strong>
              <span>{row.policy_name}</span>
            </a>
          </td>
          <td><code>{row.scope}</code></td>
          <td>
            <span class="version">v{row.desired_version}</span>
            <code title={row.desired_event_id}>{shortEventId(row.desired_event_id)}</code>
          </td>
          <td>
            {#if row.applied_event_id}
              <span class="version">v{row.applied_version}</span>
              <code title={row.applied_event_id}>{shortEventId(row.applied_event_id)}</code>
            {:else}
              <span class="muted">Not applied</span>
            {/if}
          </td>
          <td>
            <Badge variant={row.drift ? 'warning' : 'success'}>
              {row.drift ? 'Drifted' : 'In sync'}
            </Badge>
          </td>
          <td>
            {#if row.last_rejection_reason}
              <span class="rejection">{row.last_rejection_reason}</span>
            {:else}
              <span class="muted">None</span>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .table-container { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; }
  th, td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color, #2a2a4a);
    vertical-align: top;
  }
  th {
    background: var(--card-bg, #1a1a2e);
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
  }
  .coordinate { color: var(--primary); display: flex; flex-direction: column; gap: 0.2rem; text-decoration: none; }
  .coordinate:hover { text-decoration: underline; }
  .coordinate span, .muted { color: var(--text-muted); font-size: 0.8rem; }
  .version { display: block; font-weight: 600; margin-bottom: 0.2rem; }
  code { font-size: 0.75rem; }
  .rejection { color: var(--error); font-size: 0.8rem; }
</style>
