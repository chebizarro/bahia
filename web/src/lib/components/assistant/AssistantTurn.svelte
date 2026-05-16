<script>
  import AssistantPlanApproval from './AssistantPlanApproval.svelte';
  import { downstreamRequestsForTurn } from '$lib/stores/assistant.svelte.js';

  let { item, session, operatorPubkey = '' } = $props();

  const isOperator = $derived(item?.pubkey && operatorPubkey && item.pubkey === operatorPubkey);
  const downstreamIds = $derived(downstreamRequestsForTurn(item));
  const title = $derived(titleFor(item));
  const text = $derived(textFor(item));
  const plan = $derived(item?.plan || ((item?.status === 'planned' || session?.state === 'awaiting_approval') ? session?.currentPlan : null));
  const planHash = $derived(item?.planHash || session?.lastPlanHash || '');

  function titleFor(value) {
    if (!value) return 'Event';
    if (value.type === 'prompt') return 'Operator prompt';
    if (value.type === 'approval') return `Plan ${value.decision || 'approval'}`;
    if (value.type === 'status') return value.status || 'Assistant status';
    if (value.type === 'result') return value.status || 'Assistant result';
    return 'Assistant event';
  }

  function textFor(value) {
    if (!value) return '';
    if (value.type === 'prompt') return value.prompt;
    if (value.type === 'approval') return value.message || `Decision: ${value.decision}`;
    if (value.type === 'status') return value.message;
    if (value.type === 'result') return value.summary || value.error;
    return '';
  }

  function statusForRequest() {
    if (item?.failed) return 'failed';
    if (item?.blocked) return 'blocked';
    if (item?.success) return 'completed';
    if (item?.status === 'executing') return 'executing';
    if (item?.status === 'planned') return 'planning';
    return item?.status || 'planning';
  }

  function formatTime(unix) {
    if (!unix) return '';
    return new Date(unix * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
</script>

<article class="turn" class:operator={isOperator}>
  <div class="meta">
    <span class="role">{isOperator ? 'You' : 'Assistant'}</span>
    <span class="kind">{title}</span>
    <span>{formatTime(item?.createdAt)}</span>
  </div>

  {#if text}
    <p>{text}</p>
  {/if}

  {#if downstreamIds.length > 0}
    <div class="downstream" aria-label="Downstream request IDs">
      {#each downstreamIds as id}
        <span class="badge {statusForRequest()}">{statusForRequest()}</span>
        <code>{id}</code>
      {/each}
    </div>
  {/if}

  {#if plan && planHash && (item?.status === 'planned' || session?.state === 'awaiting_approval')}
    <AssistantPlanApproval sessionId={session.sessionId} {plan} {planHash} />
  {/if}
</article>

<style>
  .turn { padding: 0.85rem; border: 1px solid var(--border-color); border-radius: 10px; background: var(--card-bg); display: grid; gap: 0.5rem; }
  .turn.operator { background: rgba(99, 102, 241, 0.12); border-color: rgba(99, 102, 241, 0.45); }
  .meta { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; color: var(--text-muted); font-size: 0.75rem; }
  .role { color: var(--text-primary); font-weight: 700; }
  .kind { text-transform: capitalize; }
  p { margin: 0; color: var(--text-primary); white-space: pre-wrap; word-break: break-word; }
  .downstream { display: flex; flex-wrap: wrap; gap: 0.35rem; align-items: center; }
  code { font-size: 0.72rem; color: var(--text-muted); word-break: break-all; }
  .badge { border-radius: 999px; padding: 0.15rem 0.45rem; font-size: 0.7rem; border: 1px solid var(--border-color); color: var(--text-muted); }
  .badge.completed { color: var(--success); border-color: var(--success); }
  .badge.failed, .badge.blocked { color: var(--error); border-color: var(--error); }
  .badge.executing, .badge.planning { color: var(--warning); border-color: var(--warning); }
</style>
