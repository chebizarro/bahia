<script>
  let { columns = [], data = [], onRowClick = null } = $props();

  function handleRowClick(row, event) {
    onRowClick?.(row, event);
  }
</script>

<div class="table-container">
  <table>
    <thead>
      <tr>
        {#each columns as col}
          <th>{col.label}</th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each data as row}
        <tr class:clickable={onRowClick} onclick={(event) => handleRowClick(row, event)}>
          {#each columns as col}
            <td>
              {#if col.render}
                {@html col.render(row)}
              {:else}
                {row[col.key] ?? '-'}
              {/if}
            </td>
          {/each}
        </tr>
      {/each}
      {#if data.length === 0}
        <tr><td colspan={columns.length} class="empty">No data</td></tr>
      {/if}
    </tbody>
  </table>
</div>

<style>
  .table-container {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color, #2a2a4a);
  }
  th {
    background: var(--card-bg, #1a1a2e);
    font-weight: 600;
    font-size: 0.75rem;
    text-transform: uppercase;
    color: var(--text-muted, #888);
  }
  tr.clickable {
    cursor: pointer;
  }
  tr.clickable:hover {
    background: var(--hover-bg, #252540);
  }
  .empty {
    text-align: center;
    color: var(--text-muted, #888);
    padding: 2rem;
  }
</style>
