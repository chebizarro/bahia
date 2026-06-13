<script>
  import { WORKER_COMMANDS, workerCommandPublishPayload } from './actions.js';
  import { publishCommand, resultContent } from '$lib/stores/public-controlplane.svelte.js';
  import { currentRequesterPubkey } from '$lib/nostr/controlplane-requests.js';

  let {
    open = false,
    worker = null,
    activeCleanup = null,
    defaultMode = 'reclaimable_only',
    source = 'web.workers.cleanup-dialog',
    onClose = () => {},
    onSubmitted = () => {}
  } = $props();

  const CLEANUP_ACTION = { command: WORKER_COMMANDS.CLEANUP_REQUEST };
  const MODES = [
    {
      value: 'reclaimable_only',
      label: 'Safe reclaim',
      description: 'Prune reclaimable images, stopped containers, stale build/cache artifacts, and logs while preserving continuity reserves and standby runtimes.'
    },
    {
      value: 'aggressive',
      label: 'Aggressive cleanup',
      description: 'Allow deeper local cleanup. Requires confirmation because it can remove more cached runtime material while still enforcing Bahia protected refs.'
    }
  ];

  let mode = $state('reclaimable_only');
  let reason = $state('');
  let targetFreeGB = $state('');
  let aggressiveConfirmed = $state(false);
  let pending = $state(false);
  let error = $state('');
  let success = $state('');

  $effect(() => {
    if (!open) return;
    mode = defaultMode || 'reclaimable_only';
    reason = '';
    targetFreeGB = '';
    aggressiveConfirmed = false;
    pending = false;
    error = '';
    success = '';
  });

  let selectedMode = $derived(MODES.find((candidate) => candidate.value === mode) || MODES[0]);
  let submitDisabled = $derived(Boolean(pending || !worker?.pubkey || activeCleanup || (mode === 'aggressive' && !aggressiveConfirmed)));

  function randomId() {
    const cryptoApi = globalThis.crypto;
    if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
    if (cryptoApi?.getRandomValues) {
      const bytes = new Uint8Array(16);
      cryptoApi.getRandomValues(bytes);
      return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
    }
    throw new Error('Browser cryptographic random ID generation is unavailable');
  }

  function idempotencyKey() {
    return `${WORKER_COMMANDS.CLEANUP_REQUEST}:${worker.pubkey}:${randomId()}`;
  }

  function cleanupReason() {
    const parts = [];
    const trimmedReason = reason.trim();
    if (trimmedReason) parts.push(trimmedReason);
    const target = Number(targetFreeGB);
    if (Number.isFinite(target) && target > 0) parts.push(`target_free_gb=${target}`);
    return parts.join(' · ');
  }

  async function submitCleanup() {
    if (submitDisabled) return;
    pending = true;
    error = '';
    success = '';
    try {
      const result = await publishCommand(workerCommandPublishPayload({
        action: CLEANUP_ACTION,
        worker,
        key: idempotencyKey(),
        reason: cleanupReason(),
        requesterPubkey: currentRequesterPubkey() || '',
        cleanupMode: mode,
        source
      }));
      const content = resultContent(result);
      success = content.message || `Cleanup request accepted for ${worker.name || worker.pubkey}`;
      onSubmitted({ worker, mode, content });
    } catch (err) {
      error = err?.message || 'Failed to publish cleanup request';
    } finally {
      pending = false;
    }
  }

  function closeDialog() {
    if (pending) return;
    onClose();
  }
</script>

{#if open}
  <div class="dialog-backdrop" role="presentation" onclick={closeDialog}></div>
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="cleanup-title">
    <header>
      <div>
        <p class="eyebrow">Cleanup mode</p>
        <h2 id="cleanup-title">Request worker cleanup</h2>
        <p>{worker?.name || worker?.pubkey || 'Selected worker'}</p>
      </div>
      <button type="button" class="close" disabled={pending} onclick={closeDialog} aria-label="Close cleanup dialog">×</button>
    </header>

    {#if activeCleanup}
      <div class="notice warning" role="status">
        Cleanup is already {activeCleanup.status || 'active'} for this worker{activeCleanup.loom_job_id ? ` via Loom job ${activeCleanup.loom_job_id}` : ''}.
      </div>
    {/if}

    <div class="mode-grid" aria-label="Cleanup mode choices">
      {#each MODES as candidate}
        <label class:active={mode === candidate.value}>
          <input type="radio" name="cleanup-mode" bind:group={mode} value={candidate.value} />
          <span>
            <strong>{candidate.label}</strong>
            <em>{candidate.description}</em>
          </span>
        </label>
      {/each}
    </div>

    <div class="mode-details">
      <strong>{selectedMode.label}</strong>
      <p>{selectedMode.description}</p>
      <ul>
        <li>Bahia sends a protected cleanup request to the selected worker.</li>
        <li>The worker executes locally and reports durable cleanup status back to Bahia.</li>
        <li>Protected refs from continuity and standby assignments remain policy-owned by Bahia.</li>
      </ul>
    </div>

    <label class="field">
      <span>Reason</span>
      <textarea bind:value={reason} rows="3" placeholder="Storage pressure from stale images, cleanup before deployment, operator remediation…"></textarea>
    </label>

    <label class="field compact">
      <span>Target free disk, GB</span>
      <input bind:value={targetFreeGB} type="number" min="0" step="1" placeholder="optional" />
    </label>

    {#if mode === 'aggressive'}
      <label class="confirm">
        <input bind:checked={aggressiveConfirmed} type="checkbox" />
        <span>I understand aggressive cleanup may remove deeper local caches, while Bahia still preserves protected continuity artifacts.</span>
      </label>
    {/if}

    {#if error}
      <div class="notice error" role="alert">{error}</div>
    {/if}
    {#if success}
      <div class="notice success" role="status">{success}</div>
    {/if}

    <footer>
      <button type="button" class="secondary" disabled={pending} onclick={closeDialog}>Close</button>
      <button type="button" class="primary" disabled={submitDisabled} onclick={submitCleanup}>
        {pending ? 'Publishing…' : 'Publish cleanup intent'}
      </button>
    </footer>
  </div>
{/if}

<style>
  .dialog-backdrop {
    position: fixed;
    inset: 0;
    z-index: 120;
    background: rgba(3, 7, 18, 0.68);
    backdrop-filter: blur(4px);
  }

  .dialog {
    position: fixed;
    z-index: 130;
    top: 50%;
    left: 50%;
    width: min(720px, calc(100vw - 2rem));
    max-height: calc(100vh - 2rem);
    overflow: auto;
    transform: translate(-50%, -50%);
    border: 1px solid var(--border-color);
    border-radius: 18px;
    background: var(--card-bg, #111827);
    color: var(--text-primary, #f8fafc);
    box-shadow: 0 24px 80px rgba(0, 0, 0, 0.42);
    padding: 1.25rem;
  }

  header,
  footer {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }

  header h2 { margin: 0.15rem 0; }
  header p { margin: 0; color: var(--text-muted); }
  .eyebrow { margin: 0; color: var(--primary, #818cf8); text-transform: uppercase; font-size: 0.75rem; letter-spacing: 0.08em; font-weight: 800; }

  .close {
    border: 1px solid var(--border-color);
    border-radius: 999px;
    background: transparent;
    color: var(--text-muted);
    width: 34px;
    height: 34px;
    cursor: pointer;
    font-size: 1.2rem;
  }

  .mode-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    gap: 0.85rem;
    margin: 1.25rem 0;
  }

  .mode-grid label {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 14px;
    padding: 1rem;
    background: var(--bg-secondary, rgba(15, 23, 42, 0.7));
    cursor: pointer;
  }

  .mode-grid label.active {
    border-color: var(--primary, #818cf8);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--primary, #818cf8) 35%, transparent);
  }

  .mode-grid strong,
  .mode-grid em {
    display: block;
  }

  .mode-grid em {
    margin-top: 0.35rem;
    color: var(--text-muted);
    font-style: normal;
    line-height: 1.4;
  }

  .mode-details,
  .notice {
    border: 1px solid var(--border-color);
    border-radius: 14px;
    padding: 1rem;
    background: rgba(15, 23, 42, 0.55);
    margin-bottom: 1rem;
  }

  .mode-details p { color: var(--text-muted); }
  .mode-details ul { margin: 0.75rem 0 0; padding-left: 1.1rem; color: var(--text-muted); }
  .field {
    display: grid;
    gap: 0.4rem;
    margin-bottom: 0.9rem;
  }

  .field span,
  .confirm span { color: var(--text-muted); font-size: 0.85rem; font-weight: 700; }

  textarea,
  input[type='number'] {
    border: 1px solid var(--border-color);
    border-radius: 10px;
    background: var(--input-bg, #0f172a);
    color: var(--text-primary, #f8fafc);
    padding: 0.75rem;
  }

  .compact { max-width: 240px; }

  .confirm {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.65rem;
    border: 1px solid rgba(249, 115, 22, 0.45);
    border-radius: 12px;
    padding: 0.85rem;
    margin-bottom: 1rem;
    background: rgba(249, 115, 22, 0.08);
  }

  .notice.warning { border-color: rgba(249, 115, 22, 0.55); color: #fed7aa; }
  .notice.error { border-color: rgba(239, 68, 68, 0.55); color: #fecaca; }
  .notice.success { border-color: rgba(34, 197, 94, 0.55); color: #bbf7d0; }

  footer {
    align-items: center;
    margin-top: 1rem;
  }

  button.primary,
  button.secondary {
    border-radius: 10px;
    padding: 0.7rem 1rem;
    cursor: pointer;
    font-weight: 800;
  }

  button.primary {
    border: 1px solid var(--primary, #818cf8);
    background: var(--primary, #6366f1);
    color: white;
  }

  button.secondary {
    border: 1px solid var(--border-color);
    background: transparent;
    color: var(--text-primary);
  }

  button:disabled { opacity: 0.55; cursor: not-allowed; }

  @media (max-width: 640px) {
    .dialog { padding: 1rem; }
    footer { flex-direction: column-reverse; align-items: stretch; }
  }
</style>
