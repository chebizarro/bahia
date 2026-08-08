<script>
  import { page } from '$app/state';
  import { BACKUP_SECTIONS } from '$lib/backup/model.js';
  import { backupAttestations, operations } from '$lib/stores';
  import { RepositoryIcon } from '$lib/icons/domain-icons.js';

  let { title = 'Backup', subtitle = '', children } = $props();
  const pathname = $derived(page.url.pathname);
  const backupOperations = $derived(operations.filter((operation) => operation.domain === 'backup').slice(0, 12));
  const recentAttestations = $derived(backupAttestations.slice(0, 12));
</script>

<div class="backup-shell">
  <div class="backup-heading">
    <div>
      <p class="eyebrow">Fleet backup control plane</p>
      <h1><RepositoryIcon size={28} strokeWidth={1.75} ariaHidden="true" /> {title}</h1>
      {#if subtitle}
        <p class="subtitle">{subtitle}</p>
      {/if}
    </div>
  </div>

  <nav class="backup-nav" aria-label="Backup operator sections">
    <a href="/backup" class:active={pathname === '/backup'}>Dashboard</a>
    {#each BACKUP_SECTIONS as section}
      <a href={`/backup/${section.id}`} class:active={pathname === `/backup/${section.id}` || pathname.startsWith(`/backup/${section.id}/`)}>{section.label}</a>
    {/each}
  </nav>

  <section class="live-events" data-testid="backup-live-events">
    <div class="live-heading">
      <strong>Live backup operations</strong>
      <span>{backupOperations.length} outcomes · {recentAttestations.length} attestations</span>
    </div>
    {#if backupOperations.length > 0 || recentAttestations.length > 0}
      <div class="live-grid">
        {#each backupOperations as operation}
          <article>
            <span>{operation.updated_at ? new Date(operation.updated_at).toLocaleString() : 'Live event'}</span>
            <strong>{operation.operation || `Kind ${operation.result_event_kind || operation.status_event_kind}`}</strong>
            <small>{operation.status || 'processing'}{operation.message ? ` · ${operation.message}` : ''}</small>
          </article>
        {/each}
        {#each recentAttestations as attestation}
          <article>
            <span>{attestation.created_at ? new Date(attestation.created_at).toLocaleString() : 'Attestation'}</span>
            <strong>{attestation.attestation_type === 'verification' ? 'Verification attestation' : 'Run attestation'}</strong>
            <small>{attestation.status || 'signed'} · {attestation.backup_run_id || attestation.verification_id || attestation.id}</small>
          </article>
        {/each}
      </div>
    {:else}
      <p class="live-empty">No relay-fed backup outcomes or attestations received yet.</p>
    {/if}
  </section>

  {@render children?.()}
</div>

<style>
  .backup-shell { display: flex; flex-direction: column; gap: 1.25rem; }
  .backup-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .eyebrow { color: var(--primary); font-size: 0.78rem; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase; }
  h1 { display: flex; align-items: center; gap: 0.5rem; margin: 0.1rem 0; }
  h1 :global(svg) { flex-shrink: 0; }
  .subtitle { color: var(--text-muted); max-width: 72rem; }
  .backup-nav { display: flex; flex-wrap: wrap; gap: 0.5rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; }
  .backup-nav a { border: 1px solid var(--border-color); border-radius: 999px; color: var(--text-muted); padding: 0.4rem 0.75rem; text-decoration: none; }
  .backup-nav a:hover, .backup-nav a.active { border-color: var(--primary); color: var(--text-primary); background: rgba(99, 102, 241, 0.12); }
  .live-events { border: 1px solid var(--border-color); border-radius: 0.75rem; background: var(--card-bg); padding: 0.9rem; }
  .live-heading { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
  .live-heading span, .live-empty, .live-grid article span, .live-grid article small { color: var(--text-muted); font-size: 0.8rem; }
  .live-grid { display: grid; gap: 0.5rem; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); margin-top: 0.75rem; }
  .live-grid article { border: 1px solid var(--border-color); border-radius: 0.5rem; display: flex; flex-direction: column; gap: 0.2rem; padding: 0.65rem; min-width: 0; }
  .live-grid article strong, .live-grid article small { overflow-wrap: anywhere; }
  .live-empty { margin: 0.65rem 0 0; }
</style>
