<script>
  let { columns = [], data = [], onRowClick = null, rowClickable = Boolean(onRowClick) } = $props();

  function handleRowClick(row, event) {
    onRowClick?.(row, event);
  }

  function resolveColumnIcon(col, row) {
    if (!col?.icon) return null;
    if (typeof col.icon !== 'function') return col.icon;

    // Svelte 5 components are functions too. Distinguish actual icon components
    // from row-resolver callbacks so we don't accidentally invoke the component
    // with a row object as its props bag.
    if (col.icon.length >= 2) return col.icon;

    return col.icon(row);
  }

  function resolveColumnText(col, row) {
    if (typeof col.text === 'function') return col.text(row);
    if (col.text !== undefined) return col.text;
    return row[col.key] ?? '-';
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
        <tr class:clickable={rowClickable} onclick={(event) => handleRowClick(row, event)}>
          {#each columns as col}
            <td>
              {#if col.icon}
                {@const CellIcon = resolveColumnIcon(col, row)}
                <span class="cell-with-icon">
                  {#if CellIcon}
                    <span class="cell-icon" aria-hidden="true">
                      <CellIcon size={16} strokeWidth={1.75} />
                    </span>
                  {/if}
                  <span>{resolveColumnText(col, row)}</span>
                </span>
              {:else if col.render}
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
  .cell-with-icon {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .cell-icon {
    color: var(--text-muted, #888);
    flex-shrink: 0;
  }
  .empty {
    text-align: center;
    color: var(--text-muted, #888);
    padding: 2rem;
  }
</style>
