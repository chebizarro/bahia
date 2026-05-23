<script>
  import { page } from '$app/state';
  import { BACKUP_SECTIONS } from '$lib/backup/model.js';
  import { RepositoryIcon } from '$lib/icons/domain-icons.js';

  let { title = 'Backup', subtitle = '', children } = $props();
  const pathname = $derived(page.url.pathname);
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
</style>
