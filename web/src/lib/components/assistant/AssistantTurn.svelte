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
  const toolCalls = $derived(toolCallsFor(item));
  const observation = $derived(observationFor(item));
  const asyncWait = $derived(asyncWaitFor(item, observation));
  const subagentRun = $derived(subagentFor(item, observation));
  const timeline = $derived(timelineFor(item));

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
    if (value.type === 'transcript') {
      if (value.text) return value.text;
      if (Array.isArray(value.toolCalls) && value.toolCalls.length > 0) return `Assistant requested ${value.toolCalls.length} tool call${value.toolCalls.length === 1 ? '' : 's'}.`;
      if (value.observation?.summary) return value.observation.summary;
      return value.encrypted ? 'Encrypted transcript entry recorded.' : '';
    }
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

  function toolCallsFor(value) {
    const calls = [];
    const rawCalls = value?.toolCalls || value?.message?.tool_calls || value?.message?.toolCalls || value?.content?.tool_calls || [];
    for (const call of Array.isArray(rawCalls) ? rawCalls : []) {
      const id = call.id || call.ID || call.tool_call_id || call.toolCallId || '';
      const name = call.name || call.Name || call.tool_name || call.toolName || '';
      const args = call.arguments || call.Arguments || call.input || call.tool_args || call.toolArgs || null;
      if (id || name) calls.push({ id, name, args });
    }
    if (calls.length === 0 && (value?.phase === 'tool_call_requested' || value?.phase === 'tool_submitted') && (value.toolCallId || value.toolName)) {
      calls.push({ id: value.toolCallId, name: value.toolName, args: value.argsPreview || null });
    }
    return calls;
  }

  function observationFor(value) {
    const raw = value?.observation || value?.message?.observation || value?.content?.observation || null;
    if (raw) return raw;
    if (value?.observationId || value?.observationStatus || value?.phase === 'tool_observed') {
      return {
        observation_id: value.observationId,
        tool_call_id: value.toolCallId,
        tool_name: value.toolName,
        status: value.observationStatus || value.status,
        summary: value.summary || value.message,
        error: value.error,
        receipt: value.receipt
      };
    }
    return null;
  }

  function asyncWaitFor(value, obs) {
    const receipt = value?.receipt || obs?.receipt || obs?.Receipt || null;
    const waiting = value?.phase === 'tool_submitted' || value?.status === 'waiting_async' || obs?.status === 'waiting_async' || Boolean(receipt);
    if (!waiting) return null;
    return {
      requestId: value?.downstreamRequestId || receipt?.request_event_id || receipt?.requestEventId || receipt?.RequestEventID || '',
      resultKinds: receipt?.result_kinds || receipt?.resultKinds || receipt?.ResultKinds || [],
      summary: value?.summary || value?.message || obs?.summary || 'Awaiting downstream Nostr result.'
    };
  }

  function subagentFor(value, obs) {
    const metadata = obs?.metadata || obs?.Metadata || {};
    const name = value?.subagent || metadata.subagent || value?.content?.subagent || '';
    const phase = value?.phase || '';
    if (!name && !phase.startsWith('subagent_')) return null;
    return {
      name,
      phase,
      iterations: metadata.subagent_iterations || value?.content?.subagent_iterations || '',
      summary: value?.summary || value?.message || obs?.summary || ''
    };
  }

  function timelineFor(value) {
    const steps = [];
    if (value?.phase) steps.push({ label: value.phase, detail: value.status || '' });
    if (value?.runId) steps.push({ label: 'run', detail: value.runId });
    if (value?.turnId) steps.push({ label: 'turn', detail: value.turnId });
    if (value?.iteration) steps.push({ label: 'iteration', detail: String(value.iteration) });
    return steps;
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

  {#if timeline.length > 0}
    <ol class="phase-timeline" aria-label="Assistant agentic phase timeline">
      {#each timeline as step}
        <li>
          <span class="dot" aria-hidden="true"></span>
          <span class="phase-label">{step.label}</span>
          {#if step.detail}<code>{step.detail}</code>{/if}
        </li>
      {/each}
    </ol>
  {/if}

  {#if item?.phase || item?.toolName || item?.toolCallId || item?.actionId}
    <div class="agentic" aria-label="Assistant agentic phase details">
      {#if item?.phase}<span class="badge phase">{item.phase}</span>{/if}
      {#if item?.toolName}<code>{item.toolName}</code>{/if}
      {#if item?.toolCallId}<code>{item.toolCallId}</code>{/if}
      {#if item?.actionId}<code>{item.actionId}</code>{/if}
    </div>
  {/if}

  {#if toolCalls.length > 0}
    <div class="tool-calls" aria-label="Assistant tool calls">
      <strong>Tool calls</strong>
      {#each toolCalls as call}
        <div class="tool-call">
          <span class="badge executing">requested</span>
          {#if call.name}<code>{call.name}</code>{/if}
          {#if call.id}<code>{call.id}</code>{/if}
          {#if call.args}<pre class="args-preview">{formatArgs(call.args)}</pre>{/if}
        </div>
      {/each}
    </div>
  {/if}

  {#if observation}
    <div class="tool-observation" aria-label="Assistant tool observation">
      <strong>Tool observation</strong>
      <span class="badge {observation.status || observation.Status || 'completed'}">{observation.status || observation.Status || 'observed'}</span>
      {#if observation.tool_name || observation.ToolName}<code>{observation.tool_name || observation.ToolName}</code>{/if}
      {#if observation.tool_call_id || observation.ToolCallID}<code>{observation.tool_call_id || observation.ToolCallID}</code>{/if}
      {#if observation.summary || observation.Summary}<span>{observation.summary || observation.Summary}</span>{/if}
      {#if observation.error || observation.Error}<span class="error-text">{observation.error || observation.Error}</span>{/if}
    </div>
  {/if}

  {#if asyncWait}
    <div class="async-wait" aria-label="Assistant async downstream wait">
      <span class="spinner" aria-hidden="true"></span>
      <strong>Waiting for downstream result</strong>
      {#if asyncWait.summary}<span>{asyncWait.summary}</span>{/if}
      {#if asyncWait.requestId}<code>{asyncWait.requestId}</code>{/if}
      {#if asyncWait.resultKinds?.length}<span class="muted">kinds: {asyncWait.resultKinds.join(', ')}</span>{/if}
    </div>
  {/if}

  {#if subagentRun}
    <div class="subagent-run" aria-label="Assistant subagent run">
      <strong>{subagentRun.phase === 'subagent_completed' ? 'Subagent completed' : 'Subagent run'}</strong>
      {#if subagentRun.name}<code>{subagentRun.name}</code>{/if}
      {#if subagentRun.iterations}<span>{subagentRun.iterations} iteration{subagentRun.iterations === 1 ? '' : 's'}</span>{/if}
      {#if subagentRun.summary}<span>{subagentRun.summary}</span>{/if}
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
  .phase-timeline { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 0.4rem; color: var(--text-muted); font-size: 0.75rem; }
  .phase-timeline li { display: inline-flex; align-items: center; gap: 0.25rem; }
  .dot { width: 0.45rem; height: 0.45rem; border-radius: 50%; background: var(--warning); display: inline-block; }
  .phase-label { font-weight: 700; color: var(--text-primary); }
  .tool-calls, .tool-observation, .async-wait, .subagent-run { display: grid; gap: 0.35rem; border: 1px solid var(--border-color); border-radius: 8px; padding: 0.55rem; background: color-mix(in srgb, var(--card-bg) 86%, var(--bg)); }
  .tool-call, .tool-observation, .async-wait, .subagent-run { align-items: center; }
  .tool-call { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .async-wait { grid-template-columns: auto auto 1fr; }
  .async-wait code, .async-wait .muted { grid-column: 2 / -1; }
  .muted { color: var(--text-muted); font-size: 0.75rem; }
  .error-text { color: var(--error); }
  code { font-size: 0.72rem; color: var(--text-muted); word-break: break-all; }
  .args-preview { margin: 0; max-height: 8rem; overflow: auto; background: var(--bg); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem; color: var(--text-primary); font: 0.75rem ui-monospace, SFMono-Regular, Menlo, monospace; }
  .badge { border-radius: 999px; padding: 0.15rem 0.45rem; font-size: 0.7rem; border: 1px solid var(--border-color); color: var(--text-muted); }
  .badge.completed { color: var(--success); border-color: var(--success); }
  .badge.failed, .badge.blocked, .badge.denied, .badge.cancelled { color: var(--error); border-color: var(--error); }
  .badge.completed, .badge.succeeded { color: var(--success); border-color: var(--success); }
  .badge.executing, .badge.planning, .badge.phase, .badge.waiting_async, .badge.deferred { color: var(--warning); border-color: var(--warning); }
</style>
