<script>
  import AssistantActionApproval from './AssistantActionApproval.svelte';
  import AssistantPlanApproval from './AssistantPlanApproval.svelte';
  import { downstreamRequestsForTurn } from '$lib/stores/assistant.svelte.js';

  let { item, session, operatorPubkey = '' } = $props();

  const isOperator = $derived(item?.pubkey && operatorPubkey && item.pubkey === operatorPubkey);
  const downstreamIds = $derived(downstreamRequestsForTurn(item));
  const title = $derived(titleFor(item));
  const text = $derived(textFor(item));
  const streamingContent = $derived(item?.streamingContent || '');
  const plan = $derived(item?.plan || ((item?.status === 'planned' || session?.state === 'awaiting_approval') ? session?.currentPlan : null));
  const planHash = $derived(item?.planHash || session?.lastPlanHash || '');
  const isPending = $derived(Boolean(item?.pending));
  const pendingAction = $derived((session?.pendingActions || []).find((action) => action.actionId === item?.actionId) || null);

  function titleFor(value) {
    if (!value) return 'Event';
    if (value.type === 'prompt') return 'Operator prompt';
    if (value.type === 'approval') return value.actionId ? `Action ${value.decision || 'decision'}` : `Plan ${value.decision || 'approval'}`;
    if (value.type === 'status') return value.phase || value.status || 'Assistant status';
    if (value.type === 'transcript') return value.role ? `Transcript ${value.role}` : 'Assistant transcript';
    if (value.type === 'result') return value.phase || value.status || 'Assistant result';
    return 'Assistant event';
  }

  function textFor(value) {
    if (!value) return '';
    if (value.type === 'prompt') return value.prompt;
    if (value.type === 'approval') return value.message || `Decision: ${value.decision}`;
    if (value.type === 'status') return value.message;
    if (value.type === 'transcript') return value.text || (value.encrypted ? 'Encrypted transcript entry recorded.' : '');
    if (value.type === 'result') {
      const summary = value.summary || '';
      const error = value.error || '';
      if (error && summary) return `${summary}\n${error}`;
      return summary || error;
    }
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

  function formatArgs(value) {
    if (!value) return '';
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }
</script>

<article class="turn" class:operator={isOperator}>
  <div class="meta">
    <span class="role">{isOperator ? 'You' : 'Assistant'}</span>
    <span class="kind">{title}</span>
    <span>{formatTime(item?.createdAt)}</span>
  </div>

  {#if isPending}
    <div class="pending-response" role="status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>Waiting for assistant response…</span>
    </div>
  {:else if streamingContent}
    <p class="streaming-text">{streamingContent}<span class="cursor" aria-hidden="true">▌</span></p>
  {:else if text}
    <p>{text}</p>
  {/if}

  {#if item?.phase || item?.toolName || item?.toolCallId || item?.actionId}
    <div class="agentic" aria-label="Assistant agentic phase details">
      {#if item?.phase}<span class="badge phase">{item.phase}</span>{/if}
      {#if item?.toolName}<code>{item.toolName}</code>{/if}
      {#if item?.toolCallId}<code>{item.toolCallId}</code>{/if}
      {#if item?.actionId}<code>{item.actionId}</code>{/if}
    </div>
  {/if}

  {#if item?.argsPreview}
    <pre class="args-preview">{formatArgs(item.argsPreview)}</pre>
  {/if}

  {#if downstreamIds.length > 0}
    <div class="downstream" aria-label="Downstream request IDs">
      {#each downstreamIds as id}
        <span class="badge {statusForRequest()}">{statusForRequest()}</span>
        <code>{id}</code>
      {/each}
    </div>
  {/if}

  {#if pendingAction}
    <AssistantActionApproval sessionId={session.sessionId} action={pendingAction} />
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
  .streaming-text { font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace); }
  .pending-response { display: inline-flex; align-items: center; gap: 0.55rem; color: var(--text-muted); font-size: 0.875rem; }
  .spinner { width: 0.95rem; height: 0.95rem; border-radius: 999px; border: 2px solid color-mix(in srgb, var(--text-muted) 35%, transparent); border-top-color: var(--primary); animation: spin 800ms linear infinite; }
  .cursor { display: inline-block; margin-left: 0.1rem; animation: blink 1s steps(2, start) infinite; color: var(--accent-color, currentColor); }
  @keyframes blink { 50% { opacity: 0; } }
  @keyframes spin { to { transform: rotate(360deg); } }
  .agentic, .downstream { display: flex; flex-wrap: wrap; gap: 0.35rem; align-items: center; }
  code { font-size: 0.72rem; color: var(--text-muted); word-break: break-all; }
  .args-preview { margin: 0; max-height: 8rem; overflow: auto; background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; color: var(--text-primary); font: 0.75rem ui-monospace, SFMono-Regular, Menlo, monospace; }
  .badge { border-radius: 999px; padding: 0.15rem 0.45rem; font-size: 0.7rem; border: 1px solid var(--border-color); color: var(--text-muted); }
  .badge.completed { color: var(--success); border-color: var(--success); }
  .badge.failed, .badge.blocked { color: var(--error); border-color: var(--error); }
  .badge.executing, .badge.planning, .badge.phase { color: var(--warning); border-color: var(--warning); }
</style>
