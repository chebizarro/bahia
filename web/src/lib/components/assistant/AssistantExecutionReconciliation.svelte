<script>
  import { publishAssistantReconciliation } from '$lib/stores/assistant.svelte.js';
  import { requestEncryptedResult } from '$lib/nostr/encrypted-controlplane.js';
  import { describeAssistantRequestError, publishAssistantAbandonment } from '$lib/nostr/assistant.js';

  // Evidence first: the operator names the exact downstream request event and
  // the service verifies it. When no such event can ever be found, the only
  // other resolution is an attested abandonment, which the service accepts
  // only for uncertain work. There is deliberately no "mark complete" control.
  const REQUEST_EVENT_PATTERN = '[0-9a-f]{64}';

  let { session = null } = $props();
  let workId = $state('');
  let requestEventId = $state('');
  let submitting = $state(false);
  let message = $state('');
  const validReference = $derived(new RegExp(`^${REQUEST_EVENT_PATTERN}$`).test(requestEventId));
  let abandonWorkId = $state('');
  let abandonReason = $state('');
  let attested = $state(false);
  let abandoning = $state(false);
  let abandonMessage = $state('');
  const canAbandon = $derived(Boolean(abandonWorkId.trim() && abandonReason.trim() && attested && !abandoning));

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

  async function abandon(event) {
    event.preventDefault();
    if (!canAbandon) return;
    abandoning = true;
    abandonMessage = '';
    try {
      await publishAssistantAbandonment({ session, workId: abandonWorkId, reason: abandonReason, attested, request: requestEncryptedResult });
      abandonMessage = 'Abandonment recorded with your attestation. The outcome of this work remains unknown; await the canonical execution projection.';
      attested = false;
    } catch (err) {
      abandonMessage = describeAssistantRequestError(err, 'Abandonment').message;
    } finally {
      abandoning = false;
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
  <form onsubmit={abandon} aria-label="Abandon uncertain work">
    <p>If no request event can ever be found, you may abandon the work with an attestation. This records who, when and why. It does not mark the work complete and does not claim it did or did not take effect.</p>
    <label>Work ID <input bind:value={abandonWorkId} required aria-label="Uncertain work ID to abandon" /></label>
    <label>Reason <textarea bind:value={abandonReason} required aria-label="Reason for abandoning uncertain work"></textarea></label>
    <label class="attest"><input type="checkbox" bind:checked={attested} aria-label="Attest the outcome is unknown" /> I attest this work cannot be reconciled and its outcome is unknown.</label>
    <button type="submit" class="abandon" disabled={!canAbandon}>Abandon with attestation</button>
  </form>
  {#if abandonMessage}<p role="status">{abandonMessage}</p>{/if}
</section>

<style>
  .reconciliation { margin: 0.65rem; padding: 0.75rem; border: 1px solid var(--warning); border-radius: 8px; display: grid; gap: 0.5rem; }
  p { margin: 0; color: var(--text-muted); font-size: 0.8rem; }
  form, label { display: grid; gap: 0.4rem; }
  label { color: var(--text-muted); font-size: 0.8rem; }
  label.attest { display: flex; gap: 0.4rem; align-items: flex-start; }
  label.attest input { width: auto; }
  textarea { width: 100%; min-height: 3rem; padding: 0.45rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 6px; }
  button.abandon { background: var(--warning); }
  input { width: 100%; padding: 0.45rem; background: var(--bg); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 6px; }
  button { justify-self: start; padding: 0.5rem 0.75rem; border: 0; border-radius: 6px; background: var(--primary); color: white; }
  button:disabled { opacity: 0.5; }
</style>
