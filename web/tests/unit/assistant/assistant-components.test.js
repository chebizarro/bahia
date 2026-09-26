import { describe, it, expect, beforeEach, vi } from 'vitest';
import AssistantComposer from '../../../src/lib/components/assistant/AssistantComposer.svelte';
import AssistantPlanApproval from '../../../src/lib/components/assistant/AssistantPlanApproval.svelte';
import AssistantActionApproval from '../../../src/lib/components/assistant/AssistantActionApproval.svelte';
import AssistantTurn from '../../../src/lib/components/assistant/AssistantTurn.svelte';
import { mergeAssistantRefs, safeAssistantRefHref } from '../../../src/lib/components/assistant/assistant-refs.js';
import { renderComponent, textOf, tick } from '../utils/svelte-component-test';

const assistantStoreMock = vi.hoisted(() => ({
  assistantConnection: { status: 'live', operatorPubkey: 'a'.repeat(64) },
  publishAssistantActionDecision: vi.fn(),
  publishAssistantApproval: vi.fn(),
  publishAssistantPrompt: vi.fn(),
  publishAssistantCancellation: vi.fn(),
  publishAssistantReconciliation: vi.fn(),
  downstreamRequestsForTurn: (item) => item?.downstreamRequestId ? [item.downstreamRequestId] : []
}));

vi.mock('../../../src/lib/stores/assistant.svelte.js', () => assistantStoreMock);

async function setTextAreaValue(target, value) {
  const textarea = target.querySelector('textarea');
  textarea.value = value;
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
  await tick();
}

async function flush() {
  await tick();
  await Promise.resolve();
  await tick();
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('assistant refs model', () => {
  it('allows only docs and HTTP(S) hrefs for assistant reference pills', () => {
    expect(safeAssistantRefHref('/docs/features-services')).toBe('/docs/features-services');
    expect(safeAssistantRefHref('https://example.com/doc')).toBe('https://example.com/doc');
    expect(safeAssistantRefHref('http://example.com/doc')).toBe('http://example.com/doc');
    expect(safeAssistantRefHref('javascript:alert(1)')).toBe('');
    expect(safeAssistantRefHref('/settings')).toBe('');
  });

  it('merges default docs refs without replacing selected operational refs', () => {
    expect(mergeAssistantRefs({
      selectedRefs: ['service:svc-1'],
      defaultSelectedRefs: [{ ref: 'docs:features-services', label: 'Services documentation', href: '/docs/features-services' }]
    })).toEqual([
      expect.objectContaining({ ref: 'service:svc-1', type: 'operational', dismissible: false }),
      expect.objectContaining({ ref: 'docs:features-services', type: 'docs', dismissible: true })
    ]);

    expect(mergeAssistantRefs({
      selectedRefs: ['service:svc-1'],
      defaultSelectedRefs: [{ ref: 'docs:features-services', label: 'Services documentation' }],
      dismissedRefs: ['docs:features-services']
    }).map((ref) => ref.ref)).toEqual(['service:svc-1']);
  });
});

describe('assistant components', () => {
  beforeEach(() => {
    assistantStoreMock.publishAssistantActionDecision.mockReset();
    assistantStoreMock.publishAssistantApproval.mockReset();
    assistantStoreMock.publishAssistantPrompt.mockReset();
    assistantStoreMock.publishAssistantActionDecision.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantPrompt.mockResolvedValue({ ok: true });
    assistantStoreMock.assistantConnection.status = 'live';
  });
  it('shows route-derived docs refs in the composer and submits them through selectedRefs', async () => {
    const routeContext = { route: '/services', params: {} };
    const target = renderComponent(AssistantComposer, {
      routeContext,
      defaultSelectedRefs: [{ ref: 'docs:features-services', label: 'Services documentation', href: '/docs/features-services' }]
    });

    expect(textOf(target)).toContain('References');
    expect(textOf(target)).toContain('Services documentation');
    expect(textOf(target)).toContain('docs:features-services');
    expect(target.querySelector('a[href="/docs/features-services"]')).toBeTruthy();

    await setTextAreaValue(target, 'Explain services');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();

    expect(assistantStoreMock.publishAssistantPrompt).toHaveBeenCalledWith({
      prompt: 'Explain services',
      workflow: '',
      sessionId: undefined,
      routeContext,
      selectedRefs: ['docs:features-services']
    });
  });

  it('clears the composer input immediately while the assistant response is pending', async () => {
    const pending = deferred();
    assistantStoreMock.publishAssistantPrompt.mockReturnValueOnce(pending.promise);
    const target = renderComponent(AssistantComposer, {});

    await setTextAreaValue(target, 'Plan this deployment');
    const textarea = target.querySelector('textarea');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await tick();

    expect(textarea.value).toBe('');
    expect(target.querySelector('button[type="submit"]')?.textContent).toBe('Sending…');
    expect(assistantStoreMock.publishAssistantPrompt).toHaveBeenCalledWith({
      prompt: 'Plan this deployment',
      workflow: '',
      sessionId: undefined,
      routeContext: null,
      selectedRefs: []
    });

    pending.resolve({ ok: true });
    await flush();
  });

  it('dismisses route docs refs without removing selected operational refs', async () => {
    const routeContext = { route: '/services', params: {} };
    const target = renderComponent(AssistantComposer, {
      routeContext,
      selectedRefs: ['service:svc-1'],
      defaultSelectedRefs: [{ ref: 'docs:features-services', label: 'Services documentation', href: '/docs/features-services' }]
    });

    const removeDocs = target.querySelector('button[aria-label="Remove Services documentation reference"]');
    expect(removeDocs).toBeTruthy();
    removeDocs.click();
    await tick();

    expect(textOf(target)).toContain('service:svc-1');
    expect(textOf(target)).not.toContain('docs:features-services');

    await setTextAreaValue(target, 'Use service context only');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();

    expect(assistantStoreMock.publishAssistantPrompt).toHaveBeenCalledWith({
      prompt: 'Use service context only',
      workflow: '',
      sessionId: undefined,
      routeContext,
      selectedRefs: ['service:svc-1']
    });
  });

  it('renders plan approval steps with tool names, args previews, and decisions', () => {
    const target = renderComponent(AssistantPlanApproval, {
      session: {
        sessionId: 'assistant-session-1', currentRunId: 'run-1',
        proposal: { proposal_id: 'proposal-1', revision: 1, hash: 'plan-hash-1', plan: {
          summary: 'Deploy the chat route', risk_level: 'medium',
          steps: [
            { step_id: 'step-1', title: 'Deploy LLM route', description: 'Roll the route out to staging.',
              tool_name: 'llm.deploy', tool_args: { route_id: 'route-1', environment_id: 'staging' } },
            { step_id: 'step-2', title: 'Approve deployment', tool_name: 'llm.approve', tool_args: { deployment_id: 'deploy-1' } }
          ]
        } }
      }
    });

    const text = textOf(target);
    expect(text).toContain('Plan review');
    expect(text).toContain('Deploy the chat route');
    expect(text).toContain('medium');
    expect(text).toContain('Deploy LLM route');
    expect(text).toContain('llm.deploy');
    expect(text).toContain('route-1');
    expect(text).toContain('staging');
    expect(text).toContain('Approve deployment');
    expect(text).toContain('llm.approve');
    expect(text).toContain('deploy-1');
    expect(target.querySelector('button.approve')?.textContent).toBe('Approve');
    expect(target.querySelector('button.reject')?.textContent).toBe('Reject');
  });

  it("keeps a different step's invalid JSON intact and preserves edits on a stale response", async () => {
    const plan = { summary: 'Two steps', needs_clarification: false, risk_level: 'low', steps: [
      { step_id: 'one', title: 'One', tool_name: 'tool.one', tool_args: { first: 1 } },
      { step_id: 'two', title: 'Two', tool_name: 'tool.two', tool_args: { second: 2 } }
    ] };
    const target = renderComponent(AssistantPlanApproval, { session: { sessionId: 's1', currentRunId: 'r1',
      proposal: { proposal_id: 'p1', revision: 1, hash: 'h1', plan } } });
    await flush();
    const args = target.querySelectorAll('textarea');
    args[0].value = '{"first":';
    args[0].dispatchEvent(new Event('input', { bubbles: true }));
    args[1].value = '{"second":3}';
    args[1].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(args[0].value).toBe('{"first":');
    expect(textOf(target)).toContain('Invalid JSON');
    expect(target.querySelector('button.approve').disabled).toBe(true);

    args[0].value = '{"first":4}';
    args[0].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(target.querySelector('button.approve').disabled).toBe(false);
    assistantStoreMock.publishAssistantApproval.mockRejectedValueOnce(new Error('stale proposal revision'));
    target.querySelector('button.approve').click();
    await flush();
    expect(textOf(target)).toContain('Your local edits are preserved');
    expect(args[0].value).toBe('{"first":4}');
    expect(args[1].value).toBe('{"second":3}');
    expect(target.querySelector('button.approve').disabled).toBe(true);
    expect(target.querySelector('button.reload')).toBeTruthy();
  });

  it('offers explicit workflow selection and run cancellation without a plan hash', async () => {
    const newSession = renderComponent(AssistantComposer, {});
    const selector = newSession.querySelector('select[aria-label="Assistant workflow"]');
    selector.value = 'iterative';
    selector.dispatchEvent(new Event('change', { bubbles: true }));
    await setTextAreaValue(newSession, 'Investigate');
    newSession.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(assistantStoreMock.publishAssistantPrompt).toHaveBeenCalledWith(expect.objectContaining({ workflow: 'iterative' }));

    const active = renderComponent(AssistantComposer, { session: { sessionId: 's2', executionVersion: 2,
      authoritative: true, workflow: 'iterative', phase: 'proposing', currentRunId: 'r2' } });
    active.querySelector('button.cancel').click();
    await flush();
    expect(assistantStoreMock.publishAssistantCancellation).toHaveBeenCalledWith({ sessionId: 's2', runId: 'r2', scope: 'run' });
    expect(active.querySelector('button[type="submit"]').disabled).toBe(true);
  });

  it('renders a spinner bubble for pending assistant responses', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'planning', lastPlanHash: '' },
      item: {
        id: 'assistant-pending-session-turn',
        type: 'status',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        status: 'planning',
        pending: true
      }
    });

    expect(textOf(target)).toContain('Waiting for assistant response…');
    expect(target.querySelector('.spinner')).toBeTruthy();
    expect(textOf(target)).not.toContain('planning assistant response');
  });

  it('surfaces assistant planning failure details alongside the summary', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'idle', lastPlanHash: '' },
      item: {
        id: 'assistant-failed',
        type: 'result',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        status: 'failed',
        failed: true,
        summary: 'assistant planning failed',
        error: 'ContextVM request timed out after 120000ms waiting for result'
      }
    });

    const text = textOf(target);
    expect(text).toContain('assistant planning failed');
    expect(text).toContain('ContextVM request timed out after 120000ms waiting for result');
  });

  it('renders current run-bound action approval and publishes a decision without removing the card', async () => {
    const target = renderComponent(AssistantActionApproval, {
      sessionId: 'assistant-session-1',
      action: { actionId: 'action-rollback-1', runId: 'run-1', toolCallId: 'tool-call-1',
        toolName: 'bahia_assistant_llm_rollback', approvalPrompt: 'Rollback production requires approval',
        argsPreview: { route_id: 'route-prod' }, permission: { risk: 'high' } }
    });
    expect(textOf(target)).toContain('Rollback production requires approval');
    target.querySelector('input').value = 'operator approved rollback';
    target.querySelector('input').dispatchEvent(new Event('input', { bubbles: true }));
    target.querySelector('button.approve').click();
    await flush();
    expect(assistantStoreMock.publishAssistantActionDecision).toHaveBeenCalledWith({
      sessionId: 'assistant-session-1', runId: 'run-1', actionId: 'action-rollback-1',
      decision: 'approve', reason: 'operator approved rollback'
    });
    expect(target.querySelector('.action-card')).toBeTruthy();
  });

  it('does not reactivate controls from a historical planned row', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 's1', executionVersion: 2, phase: 'completed', proposal: null, pendingActions: [] },
      item: { id: 'old-plan', type: 'status', pubkey: 'b'.repeat(64), createdAt: 1,
        status: 'planned', planHash: 'legacy-hash', plan: { steps: [] } }
    });
    expect(textOf(target)).toContain('Historical plan record');
    expect(target.querySelector('.plan-card')).toBeNull();
    expect(target.querySelector('.action-card')).toBeNull();
  });

  it('renders agentic tool calls, async waits, subagents, and phase timeline', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'executing', lastPlanHash: '' },
      item: {
        id: 'status-tool-submitted',
        type: 'status',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        status: 'executing',
        phase: 'tool_submitted',
        runId: 'run-1',
        turnId: 'turn-1',
        iteration: 2,
        toolCallId: 'call-1',
        toolName: 'bahia_assistant_dns_zone_create',
        argsPreview: { zone: 'staging.example' },
        receipt: { request_event_id: 'downstream-1', result_kinds: [30315, 4903] },
        subagent: 'researcher',
        summary: 'submitted DNS command'
      }
    });

    const text = textOf(target);
    expect(text).toContain('tool_submitted');
    expect(text).toContain('run-1');
    expect(text).toContain('turn-1');
    expect(text).toContain('iteration');
    expect(text).toContain('Tool calls');
    expect(text).toContain('bahia_assistant_dns_zone_create');
    expect(text).toContain('staging.example');
    expect(text).toContain('Waiting for downstream result');
    expect(text).toContain('downstream-1');
    expect(text).toContain('30315, 4903');
    expect(text).toContain('Subagent run');
    expect(text).toContain('researcher');
    expect(target.querySelector('.phase-timeline')).toBeTruthy();
    expect(target.querySelector('.tool-calls')).toBeTruthy();
    expect(target.querySelector('.async-wait')).toBeTruthy();
    expect(target.querySelector('.subagent-run')).toBeTruthy();
  });

  it('renders transcript tool observations', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'executing', lastPlanHash: '' },
      item: {
        id: 'transcript-tool-observed',
        type: 'transcript',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        role: 'tool',
        phase: 'tool_observed',
        observation: {
          tool_call_id: 'call-1',
          tool_name: 'bahia_list_services',
          status: 'succeeded',
          summary: 'Found two services'
        }
      }
    });

    const text = textOf(target);
    expect(text).toContain('Found two services');
    expect(text).toContain('Tool observation');
    expect(text).toContain('bahia_list_services');
    expect(text).toContain('succeeded');
  });

  it('shows blocked visual state for a relay-closed assistant turn', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'blocked', lastPlanHash: '' },
      item: {
        id: 'result-blocked',
        type: 'result',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        status: 'blocked',
        blocked: true,
        error: 'relay closed before terminal result',
        downstreamRequestId: 'downstream-1'
      }
    });

    const text = textOf(target);
    expect(text).toContain('Assistant');
    expect(text).toContain('blocked');
    expect(text).toContain('relay closed before terminal result');
    expect(text).toContain('downstream-1');
    expect(target.querySelector('.badge.blocked')?.textContent).toBe('blocked');
  });
});
