<script>
  import { controlplaneConnection } from '$lib/stores';

  let { connection = controlplaneConnection } = $props();
  let expanded = $state(false);

  const STATUS_PRESENTATION = {
    idle: { tone: 'disconnected', label: 'Disconnected', detail: 'Not connected' },
    disconnected: { tone: 'disconnected', label: 'Disconnected', detail: 'Not connected' },
    discovering: { tone: 'connecting', label: 'Connecting', detail: 'Discovering relays' },
    connecting: { tone: 'connecting', label: 'Connecting', detail: 'Connecting to relays' },
    syncing: { tone: 'syncing', label: 'Syncing', detail: 'EOSE pending' },
    bootstrapping: { tone: 'syncing', label: 'Syncing', detail: 'EOSE pending' },
    live: { tone: 'live', label: 'Live', detail: 'Connected, EOSE received' },
    error: { tone: 'error', label: 'Error', detail: 'Connection error' }
  };

  let presentation = $derived(STATUS_PRESENTATION[connection.status] || STATUS_PRESENTATION.disconnected);
  let relays = $derived(Array.isArray(connection.relays) ? connection.relays : []);
  let errorMessage = $derived(connection.lastError?.message || connection.lastError || 'Relay connection error');
  let title = $derived(connection.status === 'error' ? errorMessage : `${presentation.label}: ${presentation.detail}`);
  let lastEventLabel = $derived(formatTimestamp(connection.lastEventAt));
  let lastEoseLabel = $derived(formatTimestamp(connection.lastEoseAt));

  function formatTimestamp(value) {
    if (!value) return 'Never';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString();
  }

  function toggleExpanded() {
    expanded = !expanded;
  }
</script>

<div class="connection-status" data-tone={presentation.tone}>
  <button
    type="button"
    class="status-trigger"
    aria-expanded={expanded}
    aria-controls="connection-status-details"
    title={title}
    onclick={toggleExpanded}
  >
    <span class="status-dot" aria-hidden="true"></span>
    <span class="status-copy">
      <span class="status-label">{presentation.label}</span>
      <span class="status-detail">{presentation.detail}</span>
    </span>
  </button>

  {#if expanded}
    <div id="connection-status-details" class="status-details" role="status">
      {#if connection.status === 'error'}
        <p class="status-error">{errorMessage}</p>
      {/if}

      <dl>
        <div>
          <dt>Relays</dt>
          <dd>{relays.length}</dd>
        </div>
        <div>
          <dt>Last event</dt>
          <dd>{lastEventLabel}</dd>
        </div>
        <div>
          <dt>Last EOSE</dt>
          <dd>{lastEoseLabel}</dd>
        </div>
      </dl>

      {#if relays.length > 0}
        <ul class="relay-list" aria-label="Connected relays">
          {#each relays as relay}
            <li>{relay}</li>
          {/each}
        </ul>
      {:else}
        <p class="empty-relays">No relays connected</p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .connection-status {
    position: relative;
    display: inline-flex;
    align-items: center;
  }

  .status-trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    border: 1px solid var(--border-color);
    border-radius: 999px;
    background: var(--card-bg);
    color: var(--text-primary);
    padding: 0.35rem 0.7rem;
    cursor: pointer;
    transition: all 0.15s;
  }

  .status-trigger:hover,
  .status-trigger:focus-visible {
    background: var(--hover-bg);
  }

  .status-dot {
    width: 0.65rem;
    height: 0.65rem;
    border-radius: 999px;
    flex-shrink: 0;
    background: var(--connection-status-color, var(--text-muted));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--connection-status-color, var(--text-muted)) 18%, transparent);
  }

  .status-copy {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    line-height: 1.1;
  }

  .status-label {
    color: var(--text-primary);
    font-size: 0.78rem;
    font-weight: 700;
  }

  .status-detail {
    color: var(--text-muted);
    font-size: 0.66rem;
    white-space: nowrap;
  }

  .status-details {
    position: absolute;
    top: calc(100% + 0.5rem);
    right: 0;
    z-index: 20;
    width: min(320px, calc(100vw - 2rem));
    padding: 0.9rem;
    border: 1px solid var(--border-color);
    border-radius: 12px;
    background: var(--card-bg);
    box-shadow: 0 18px 40px color-mix(in srgb, #000 26%, transparent);
    color: var(--text-primary);
  }

  .status-error,
  .empty-relays {
    margin: 0 0 0.75rem;
    color: var(--text-muted);
    font-size: 0.8rem;
  }

  .status-error {
    color: var(--error, #ef4444);
  }

  dl {
    display: grid;
    gap: 0.5rem;
    margin: 0 0 0.75rem;
  }

  dl > div {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
  }

  dt {
    color: var(--text-muted);
    font-size: 0.72rem;
  }

  dd {
    margin: 0;
    color: var(--text-primary);
    font-size: 0.72rem;
    text-align: right;
  }

  .relay-list {
    display: grid;
    gap: 0.35rem;
    max-height: 9rem;
    overflow: auto;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .relay-list li {
    overflow-wrap: anywhere;
    border-radius: 6px;
    background: color-mix(in srgb, var(--hover-bg) 72%, transparent);
    color: var(--text-muted);
    padding: 0.35rem 0.45rem;
    font-size: 0.72rem;
  }

  .connection-status[data-tone='disconnected'] {
    --connection-status-color: var(--text-muted, #9ca3af);
  }

  .connection-status[data-tone='connecting'],
  .connection-status[data-tone='syncing'] {
    --connection-status-color: var(--warning, #f59e0b);
  }

  .connection-status[data-tone='live'] {
    --connection-status-color: var(--success, #22c55e);
  }

  .connection-status[data-tone='error'] {
    --connection-status-color: var(--error, #ef4444);
  }

  @media (max-width: 720px) {
    .status-detail {
      display: none;
    }
  }
</style>
