<!--
  IntentGate — disables the mutation controls it wraps until a signed intent
  for `domain` can be submitted (stores/intent-readiness.svelte.js).

  A native disabled fieldset disables every descendant form control, so a page
  wraps a mutation region once instead of repeating the readiness check on each
  button. Only the self-resolving "pending" state disables; when readiness needs
  the operator (signed out, no organization) the controls stay enabled and
  submitting reports the explicit error.

  The fieldset generates no box, so wrapping never changes page layout. The
  reason follows the wrapped controls so they keep their :first-child styling.
-->
<script>
  import { intentReadiness } from '$lib/stores/intent-readiness.svelte.js';

  /**
   * @typedef {Object} Props
   * @property {string} domain Intent domain the wrapped controls submit to.
   * @property {string} [orgId] Org id of the record or form, when the page knows it.
   * @property {import('svelte').Snippet} [children]
   */

  /** @type {Props} */
  let { domain, orgId = '', children } = $props();

  const reasonId = $props.id();
  const readiness = $derived(intentReadiness(domain, orgId));
</script>

<fieldset
  class="intent-gate"
  disabled={readiness.pending}
  aria-busy={readiness.pending}
  aria-describedby={readiness.pending ? reasonId : undefined}
  title={readiness.pending ? readiness.reason : undefined}
  data-intent-domain={domain}
  data-intent-ready={readiness.ready}
>
  {@render children?.()}
  {#if readiness.pending}<span id={reasonId} class="intent-gate-reason">{readiness.reason}</span>{/if}
</fieldset>

<style>
  .intent-gate {
    display: contents;
  }
  .intent-gate:disabled :global(:is(button, input, select, textarea)) {
    cursor: progress;
  }
  .intent-gate-reason {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
    border: 0;
  }
</style>
