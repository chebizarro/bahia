<script>
  import { publishAssistantReconciliation } from '$lib/stores/assistant.svelte.js';
  import { describeAssistantRequestError } from '$lib/nostr/assistant.js';

  // Evidence-only: the operator names the exact downstream request event and
  // the service verifies it. There is deliberately no "mark complete" control.
  const REQUEST_EVENT_PATTERN = '[0-9a-f]{64}';

  let { session = null } = $props();
  let workId = $state('');
  let requestEventId = $state('');
  let submitting = $state(false);
  let message = $state('');
  const validReference = $derived(new RegExp(`^${REQUEST_EVENT_PATTERN}$`).test(requestEventId));

  async function reconcile(event) {
    event.preventDefault();
    if (!workId.trim() || !validReference || submitting) return;
    submitting = true;
    message = '';
    try {
      await publishAssistantReconciliation({ sessionId: session.sessionId, runId: session.currentRunId,
        workId: workId.trim(), requestEventId });
      message = 'Evidence submitted. Await the canonical execution projection before treating this work as resolved.';
    } catch (err) {
      message = describeAssistantRequestError(err, 'Evidence submission').message;
    } finally {
      submitting = false;
    }
  }
</script>

<section class="reconciliation" aria-label="Assistant execution reconciliation">
  <strong>Uncertain downstream work</strong>
  <p>Dispatch may have happened, but no durable receipt is known. The assistant will not replay it automatically. Supply only the exact downstream request event for server verification; absence is not proof of failure.</p>
  <form onsubmit={reconcile}>
    <label>Work ID <input bind:value={workId} required aria-label="Uncertain work ID" /></label>
    <label>Downstream request event ID <input bind:value={requestEventId} required pattern={REQUEST_EVENT_PATTERN} aria-label="Exact downstream request event ID" /></label>
    <button type="submit" disabled={!workId.trim() || !validReference || submitting}>Submit evidence</button>
  </form>
  {#if message}<p role="status">{message}</p>{/if}
</section>

<style>
  .reconciliation { margin: 0.65rem; padding: 0.75rem; border: 1px solid var(--warning); border-radius: 8px; display: grid; gap: 0.5rem; }
  p { margin: 0; color: var(--text-muted); font-size: 0.8rem; }
  form, label { display: grid; gap: 0.4rem; }
  label { color: var(--text-muted); font-size: 0.8rem; }
  input { width: 100%; padding: 0.45rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 6px; }
  button { justify-self: start; padding: 0.5rem 0.75rem; border: 0; border-radius: 6px; background: var(--primary); color: white; }
  button:disabled { opacity: 0.5; }
</style>
