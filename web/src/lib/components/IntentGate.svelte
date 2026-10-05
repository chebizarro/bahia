<!--
  IntentGate — disables the mutation controls it wraps until a signed intent
  for `domain` can be submitted (stores/intent-readiness.svelte.js).

  A native disabled fieldset disables every descendant form control, so a page
  wraps a control or a region once instead of repeating the readiness check on
  each button. Readiness comes from local state only; a disconnected relay
  never disables a control.

  While the session is still opening, the reason ("Connecting…") is exposed to
  assistive technology and as a tooltip. When the session knows no organization
  for an org-scoped intent, the reason is also shown beside the control, since
  that state can last. When only the operator can resolve readiness (signed
  out, several organizations) the controls stay enabled and submitting reports
  the explicit error.

  The fieldset generates no box, so wrapping never changes page layout. The
  reason follows the wrapped controls so they keep their :first-child styling.
-->
<script>
  import { intentReadiness } from '$lib/stores/intent-readiness.svelte.js';

  /**
   * @typedef {Object} Props
   * @property {string} domain Intent domain the wrapped controls submit to.
   * @property {string} [orgId] Org id the form or page already holds.
   * @property {object | null} [record] Record the intent acts on; its org id is
   *   resolved the way the stores resolve it (org_id, service_id, route_id).
   * @property {boolean} [orgField] The controls sit beside an organization field.
   * @property {import('svelte').Snippet} [children]
   */

  /** @type {Props} */
  let { domain, orgId = '', record = null, orgField = false, children } = $props();

  const reasonId = $props.id();
  const readiness = $derived(intentReadiness(domain, { orgId, record, orgField }));
</script>

<fieldset
  class="intent-gate"
  disabled={readiness.pending}
  aria-busy={readiness.waitingOn === 'session'}
  aria-describedby={readiness.pending ? reasonId : undefined}
  title={readiness.pending ? readiness.reason : undefined}
  data-intent-domain={domain}
  data-intent-ready={readiness.ready}
>
  {@render children?.()}
  {#if readiness.pending}
    <span id={reasonId} class="intent-gate-reason" class:shown={readiness.waitingOn === 'organization'}>{readiness.reason}</span>
  {/if}
</fieldset>

<style>
  .intent-gate {
    display: contents;
  }
  .intent-gate:disabled :global(:is(button, input, select, textarea)) {
    cursor: not-allowed;
  }
  .intent-gate[aria-busy='true'] :global(:is(button, input, select, textarea)) {
    cursor: progress;
  }
  .intent-gate-reason:not(.shown) {
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
  .intent-gate-reason.shown {
    align-self: center;
    color: var(--text-muted);
    font-size: 0.85rem;
  }
</style>
