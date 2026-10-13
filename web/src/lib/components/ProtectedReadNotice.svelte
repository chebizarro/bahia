<script>
  import { authState } from '$lib/stores/auth.js';
  import { controlplaneConnection } from '$lib/stores/controlplane.svelte.js';

  let { auth = authState, connection = controlplaneConnection } = $props();
</script>

{#if auth.status === 'authenticated' && connection.protectedReadError && !connection.protectedReadsReady}
  <section class="protected-read-notice" role="alert" data-testid="protected-read-notice">
    <strong>Relay denied protected reads for this signed-in account.</strong>
    <p>Protected data may be missing. Check this account's relay read-admission policy, then reconnect.</p>
    <p class="relay-reason">Relay response: {connection.protectedReadError}</p>
  </section>
{/if}

<style>
  .protected-read-notice {
    border: 1px solid var(--error);
    border-left-width: 4px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--error) 10%, var(--card-bg));
    color: var(--text-primary);
    padding: 0.9rem 1rem;
    margin-bottom: 1.5rem;
  }
  .protected-read-notice p { margin-top: 0.35rem; }
  .relay-reason { color: var(--text-muted); overflow-wrap: anywhere; }
</style>
