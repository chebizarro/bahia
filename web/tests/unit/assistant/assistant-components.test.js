import { describe, it, expect, beforeEach, vi } from 'vitest';
import AssistantComposer from '../../../src/lib/components/assistant/AssistantComposer.svelte';
import AssistantPlanApproval from '../../../src/lib/components/assistant/AssistantPlanApproval.svelte';
import AssistantActionApproval from '../../../src/lib/components/assistant/AssistantActionApproval.svelte';
import AssistantTurn from '../../../src/lib/components/assistant/AssistantTurn.svelte';
import AssistantPanel from '../../../src/lib/components/assistant/AssistantPanel.svelte';
import AssistantExecutionReconciliation from '../../../src/lib/components/assistant/AssistantExecutionReconciliation.svelte';
import { mergeAssistantRefs, safeAssistantRefHref } from '../../../src/lib/components/assistant/assistant-refs.js';
import { assertAssistantRequestAccepted } from '../../../src/lib/nostr/assistant.js';
import { renderComponent, textOf, tick } from '../utils/svelte-component-test';
import { reactiveProps } from '../utils/reactive-props.svelte.js';

const assistantStoreMock = vi.hoisted(() => ({
  assistantConnection: { status: 'live', operatorPubkey: 'a'.repeat(64) },
  assistantUi: { panelOpen: true, activeSessionId: '' },
  assistantSessions: [],
  pendingAssistantRequests: {},
  activeAssistantSession: vi.fn(() => null),
  closeAssistantPanel: vi.fn(),
  createAssistantSessionId: vi.fn(() => 'assistant-new'),
  setActiveAssistantSession: vi.fn(),
  bootstrapAssistant: vi.fn(),
  publishAssistantActionDecision: vi.fn(),
  publishAssistantApproval: vi.fn(),
  publishAssistantPrompt: vi.fn(),
  publishAssistantCancellation: vi.fn(),
  publishAssistantReconciliation: vi.fn(),
  assistantWorkflowAvailable: vi.fn(() => true),
  downstreamRequestsForTurn: (item) => item?.downstreamRequestId ? [item.downstreamRequestId] : []
}));

// Built through the real transport contract: the service answers a refused
// request with `{status:"failed", step:<reason>}`.
function serviceRejection(step, error) {
  try {
    assertAssistantRequestAccepted({ result: { status: 'failed', step, error } });
  } catch (err) {
    return err;
  }
  throw new Error('expected a rejection');
}

const twoStepPlan = () => ({ summary: 'Two steps', needs_clarification: false, risk_level: 'low', steps: [
  { step_id: 'one', title: 'One', description: '', tool_name: 'tool.one', tool_args: { first: 1 } },
  { step_id: 'two', title: 'Two', description: '', tool_name: 'tool.two', tool_args: { second: 2 } }
] });

function batchSession(overrides = {}) {
  return { sessionId: 's1', currentRunId: 'r1', executionVersion: 2, authoritative: true, workflow: 'batch',
    phase: 'awaiting_approval', pendingApprovals: ['p1'], pendingActions: [], transcript: [], participants: [],
    proposal: { proposal_id: 'p1', revision: 1, hash: 'h1', plan: twoStepPlan() }, ...overrides };
}

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
    assistantStoreMock.publishAssistantCancellation.mockReset();
    assistantStoreMock.publishAssistantReconciliation.mockReset();
    assistantStoreMock.bootstrapAssistant.mockReset();
    assistantStoreMock.activeAssistantSession.mockReset();
    assistantStoreMock.activeAssistantSession.mockReturnValue(null);
    assistantStoreMock.assistantWorkflowAvailable.mockReset();
    assistantStoreMock.assistantWorkflowAvailable.mockReturnValue(true);
    assistantStoreMock.setActiveAssistantSession.mockReset();
    assistantStoreMock.publishAssistantActionDecision.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantApproval.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantPrompt.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantCancellation.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantReconciliation.mockResolvedValue({ ok: true });
    assistantStoreMock.assistantConnection.status = 'live';
    assistantStoreMock.assistantUi.activeSessionId = '';
    for (const key of Object.keys(assistantStoreMock.pendingAssistantRequests)) delete assistantStoreMock.pendingAssistantRequests[key];
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
    assistantStoreMock.publishAssistantApproval.mockRejectedValueOnce(serviceRejection('stale_approval', 'base revision 1 was superseded'));
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

  it('cancels a hashless run whose prompt request is still pending, with separate submitting states', async () => {
    const promptPending = deferred();
    const cancelPending = deferred();
    assistantStoreMock.publishAssistantPrompt.mockReturnValueOnce(promptPending.promise);
    assistantStoreMock.publishAssistantCancellation.mockReturnValueOnce(cancelPending.promise);
    const props = reactiveProps({ session: { sessionId: 's4', executionVersion: 0, workflow: '', phase: '' } });
    const target = renderComponent(AssistantComposer, props);
    const selector = target.querySelector('select[aria-label="Assistant workflow"]');
    selector.value = 'iterative';
    selector.dispatchEvent(new Event('change', { bubbles: true }));
    await setTextAreaValue(target, 'Investigate the outage');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await tick();
    expect(target.querySelector('button[type="submit"]').textContent).toBe('Sending…');
    expect(target.querySelector('button.cancel')).toBeNull(); // no run identity yet

    // The v2 projection for the new iterative run arrives while the prompt RPC is still pending.
    props.session = { sessionId: 's4', executionVersion: 2, authoritative: true, workflow: 'iterative',
      phase: 'proposing', currentRunId: 'r4', proposal: null, pendingApprovals: [] };
    await flush();
    const cancelRun = target.querySelector('button.cancel');
    expect(cancelRun.textContent).toBe('Cancel run');
    expect(cancelRun.disabled).toBe(false);
    expect(target.querySelector('button[type="submit"]').textContent).toBe('Sending…');

    cancelRun.click();
    await flush();
    expect(assistantStoreMock.publishAssistantCancellation).toHaveBeenCalledWith({ sessionId: 's4', runId: 'r4', scope: 'run' });
    expect(target.querySelector('button.cancel').textContent).toBe('Cancelling…');
    expect(target.querySelector('button.close-session').disabled).toBe(true);
    expect(target.querySelector('button[type="submit"]').textContent).toBe('Sending…');

    cancelPending.resolve({ ok: true });
    await flush();
    expect(target.querySelector('button.cancel').textContent).toBe('Cancel run');
    expect(target.querySelector('button[type="submit"]').textContent).toBe('Sending…');
    promptPending.resolve({ ok: true });
    await flush();
    expect(target.querySelector('button[type="submit"]').textContent).toBe('Send');
  });

  it('blocks a second prompt while this session has a pending request and closes a session explicitly', async () => {
    assistantStoreMock.pendingAssistantRequests['assistant-pending:s5:t1'] = { sessionId: 's5', turnId: 't1' };
    const target = renderComponent(AssistantComposer, { session: { sessionId: 's5', executionVersion: 2,
      authoritative: true, workflow: 'iterative', phase: 'executing', currentRunId: 'r5' } });
    expect(target.querySelector('textarea').disabled).toBe(true);
    expect(textOf(target)).toContain('A run is active in this session');
    target.querySelector('button.close-session').click();
    await flush();
    expect(assistantStoreMock.publishAssistantCancellation).toHaveBeenCalledWith({ sessionId: 's5', runId: 'r5', scope: 'session' });
    expect(assistantStoreMock.publishAssistantPrompt).not.toHaveBeenCalled();
  });

  it('reports a cancellation transport interruption as unknown, not as a failed run', async () => {
    assistantStoreMock.publishAssistantCancellation.mockRejectedValueOnce(new Error('Encrypted controlplane disconnected'));
    const target = renderComponent(AssistantComposer, { session: { sessionId: 's6', executionVersion: 2,
      authoritative: true, workflow: 'iterative', phase: 'waiting_async', currentRunId: 'r6' } });
    target.querySelector('button.cancel').click();
    await flush();
    expect(textOf(target)).toContain('Cancellation request outcome unknown / reconnecting: Encrypted controlplane disconnected');
    expect(textOf(target)).not.toMatch(/\bfailed\b/i);
    expect(target.querySelector('button.cancel').disabled).toBe(false);
  });

  it('keeps v1 sessions read-only and offers no cancel or workflow controls for them', () => {
    const target = renderComponent(AssistantComposer, { session: { sessionId: 'old', executionVersion: 1,
      state: 'awaiting_approval', lastPlanHash: 'legacy-hash' } });
    expect(textOf(target)).toContain('read-only history');
    expect(target.querySelector('textarea').disabled).toBe(true);
    expect(target.querySelector('select')).toBeNull();
    expect(target.querySelector('button.cancel')).toBeNull();
  });

  it('allows a workflow change only between runs and never with unresolved effects', () => {
    const finished = renderComponent(AssistantComposer, { session: { sessionId: 's7', executionVersion: 2,
      authoritative: true, workflow: 'batch', phase: 'completed', currentRunId: 'r7', uncertainEffects: 0 } });
    const select = finished.querySelector('select[aria-label="Assistant workflow"]');
    expect(select.disabled).toBe(false);
    expect(select.options[0].textContent).toBe('Keep Batch plan');
    const unresolved = renderComponent(AssistantComposer, { session: { sessionId: 's8', executionVersion: 2,
      authoritative: true, workflow: 'batch', phase: 'completed', currentRunId: 'r8', uncertainEffects: 1 } });
    expect(unresolved.querySelector('select[aria-label="Assistant workflow"]').disabled).toBe(true);
    expect(textOf(unresolved)).toContain('Resolve uncertain operations before changing the workflow');
  });

  it('submits a reordered, pruned and edited batch as the next revision of the base proposal', async () => {
    const plan = { ...twoStepPlan(), steps: [...twoStepPlan().steps,
      { step_id: 'three', title: 'Three', description: '', tool_name: 'tool.three', tool_args: { third: 3 } }] };
    const target = renderComponent(AssistantPlanApproval, { session: batchSession({
      proposal: { proposal_id: 'p1', revision: 1, hash: 'h1', plan } }) });
    await flush();
    target.querySelectorAll('button[aria-label="Move step down"]')[0].click();
    await flush();
    expect(Array.from(target.querySelectorAll('.step-title')).map((node) => node.textContent)).toEqual(['Two', 'One', 'Three']);
    target.querySelectorAll('button[aria-label="Remove step"]')[2].click();
    await flush();
    const args = target.querySelectorAll('textarea');
    expect(args).toHaveLength(2);
    args[1].value = '{"first":10,"extra":{"nested":[1,2]}}';
    args[1].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(textOf(target)).toContain('Approval submits it as revision 2');
    target.querySelector('button.approve').click();
    await flush();
    expect(assistantStoreMock.publishAssistantApproval).toHaveBeenCalledWith({
      sessionId: 's1', runId: 'r1', proposalId: 'p1', baseRevision: 1, basePlanHash: 'h1', decision: 'approve',
      modifiedPlan: { ...plan, steps: [plan.steps[1], { ...plan.steps[0], tool_args: { first: 10, extra: { nested: [1, 2] } } }] }
    });
  });

  it('does not treat argument key order as an edit and rejects without submitting edits', async () => {
    const target = renderComponent(AssistantPlanApproval, { session: batchSession() });
    await flush();
    const args = target.querySelectorAll('textarea');
    args[0].value = '{ "first" : 1 }';
    args[0].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(textOf(target)).not.toContain('Plan edited');
    target.querySelector('button.approve').click();
    await flush();
    expect(assistantStoreMock.publishAssistantApproval.mock.calls.at(-1)[0].modifiedPlan).toBeNull();
  });

  it('treats an approval transport interruption as outcome unknown and keeps the draft editable', async () => {
    assistantStoreMock.publishAssistantApproval.mockRejectedValueOnce(new Error('ContextVM request timed out after 180000ms waiting for result'));
    const target = renderComponent(AssistantPlanApproval, { session: batchSession() });
    await flush();
    const args = target.querySelectorAll('textarea');
    args[1].value = '{"second":22}';
    args[1].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    target.querySelector('button.approve').click();
    await flush();
    expect(textOf(target)).toContain('Approval outcome unknown / reconnecting: ContextVM request timed out');
    expect(textOf(target)).not.toContain('Proposal changed');
    expect(args[1].value).toBe('{"second":22}');
    expect(target.querySelector('button.approve').disabled).toBe(false);
  });

  it('preserves unsent edits when the canonical proposal moves and reloads only on request', async () => {
    const props = reactiveProps({ session: batchSession() });
    const target = renderComponent(AssistantPlanApproval, props);
    await flush();
    let args = target.querySelectorAll('textarea');
    args[0].value = '{"first":"operator-edit"}';
    args[0].dispatchEvent(new Event('input', { bubbles: true }));
    await flush();

    const revised = { ...twoStepPlan(), summary: 'Revised by the service', steps: [twoStepPlan().steps[1]] };
    props.session = batchSession({ proposal: { proposal_id: 'p1', revision: 2, hash: 'h2', plan: revised } });
    await flush();
    expect(textOf(target)).toContain('Your local edits are preserved and were not submitted');
    args = target.querySelectorAll('textarea');
    expect(args[0].value).toBe('{"first":"operator-edit"}');
    expect(target.querySelector('button.approve').disabled).toBe(true);
    expect(target.querySelector('button.refresh')).toBeNull();

    target.querySelector('button.reload').click();
    await flush();
    expect(textOf(target)).toContain('Revised by the service');
    expect(target.querySelectorAll('textarea')).toHaveLength(1);
    expect(target.querySelector('.previous-draft pre').textContent).toContain('operator-edit');
    expect(target.querySelector('button.approve').disabled).toBe(false);
    target.querySelector('button.approve').click();
    await flush();
    expect(assistantStoreMock.publishAssistantApproval).toHaveBeenLastCalledWith(expect.objectContaining({
      baseRevision: 2, basePlanHash: 'h2', modifiedPlan: null }));
  });

  it('offers a canonical refresh when the service reports staleness before the projection moves', async () => {
    assistantStoreMock.publishAssistantApproval.mockRejectedValueOnce(serviceRejection('plan_hash_mismatch', 'approved hash does not match'));
    const target = renderComponent(AssistantPlanApproval, { session: batchSession() });
    await flush();
    target.querySelector('button.approve').click();
    await flush();
    expect(target.querySelector('button.refresh')).toBeTruthy();
    target.querySelector('button.refresh').click();
    await flush();
    expect(assistantStoreMock.bootstrapAssistant).toHaveBeenCalledWith({ force: true });
  });

  it('locks an acknowledged plan decision until canonical state consumes the proposal', async () => {
    const target = renderComponent(AssistantPlanApproval, { session: batchSession() });
    await flush();
    target.querySelector('button.approve').click();
    await flush();
    expect(target.querySelector('.plan-card')).toBeTruthy();
    expect(textOf(target)).toContain('Decision sent (approve); waiting for the canonical execution state');
    expect(target.querySelector('button.approve').disabled).toBe(true);
    expect(target.querySelector('button.reject').disabled).toBe(true);
    target.querySelector('button.approve').click();
    await flush();
    expect(assistantStoreMock.publishAssistantApproval).toHaveBeenCalledTimes(1);
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
    expect(textOf(target)).toContain('Decision sent (approve)');
    expect(target.querySelector('button.approve').disabled).toBe(true);
    target.querySelector('button.reject').click();
    await flush();
    expect(assistantStoreMock.publishAssistantActionDecision).toHaveBeenCalledTimes(1);
  });

  it('distinguishes superseded, refused and unknown action decisions', async () => {
    const action = { actionId: 'action-9', runId: 'run-9', toolName: 'tool.nine' };
    assistantStoreMock.publishAssistantActionDecision
      .mockRejectedValueOnce(serviceRejection('stale_approval', 'action already consumed'))
      .mockRejectedValueOnce(serviceRejection('permission_denied', 'readonly posture'))
      .mockRejectedValueOnce(new Error('ContextVM result subscription auth closure: wss://relay'));
    const target = renderComponent(AssistantActionApproval, { sessionId: 's9', action });
    target.querySelector('button.approve').click();
    await flush();
    expect(textOf(target)).toContain('Decision refers to superseded state: stale_approval: action already consumed');
    target.querySelector('button.approve').click();
    await flush();
    expect(textOf(target)).toContain('Decision rejected by the assistant service: permission_denied: readonly posture');
    target.querySelector('button.approve').click();
    await flush();
    expect(textOf(target)).toContain('Decision outcome unknown / reconnecting');
    expect(target.querySelector('.action-card')).toBeTruthy();
    expect(target.querySelector('button.approve').disabled).toBe(false);
  });

  it('accepts only an exact downstream request-event reference for uncertain work', async () => {
    const target = renderComponent(AssistantExecutionReconciliation, { session: { sessionId: 's10', currentRunId: 'r10', uncertainEffects: 1 } });
    const [workInput, eventInput] = target.querySelectorAll('input');
    expect(eventInput.getAttribute('pattern')).toBe('[0-9a-f]{64}');
    // Evidence and the attested abandonment are the only resolutions; there is no "mark complete" control.
    expect(Array.from(target.querySelectorAll('button')).map((button) => button.textContent)).toEqual(['Submit evidence', 'Abandon with attestation']);
    workInput.value = 'work-1';
    workInput.dispatchEvent(new Event('input', { bubbles: true }));
    eventInput.value = 'A'.repeat(64);
    eventInput.dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(target.querySelector('button').disabled).toBe(true);
    eventInput.value = 'b'.repeat(64);
    eventInput.dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(assistantStoreMock.publishAssistantReconciliation).toHaveBeenCalledWith({
      sessionId: 's10', runId: 'r10', workId: 'work-1', requestEventId: 'b'.repeat(64) });
    expect(textOf(target)).toContain('Await the canonical execution projection');
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

describe('assistant panel: controls come only from eligible v2 state', () => {
  const plannedRow = { id: 'old-plan', type: 'status', pubkey: 'b'.repeat(64), createdAt: 1, sessionId: 's1',
    status: 'planned', planHash: 'legacy-hash', plan: twoStepPlan() };
  const approvalRow = { id: 'old-action', type: 'status', pubkey: 'b'.repeat(64), createdAt: 2, sessionId: 's1',
    status: 'awaiting_approval', phase: 'approval_required', actionId: 'action-old', runId: 'r0', toolName: 'tool.old' };

  function renderPanel(session) {
    assistantStoreMock.activeAssistantSession.mockReturnValue(session);
    return renderComponent(AssistantPanel, {});
  }

  beforeEach(() => {
    assistantStoreMock.activeAssistantSession.mockReset();
    assistantStoreMock.assistantConnection.status = 'live';
    for (const key of Object.keys(assistantStoreMock.pendingAssistantRequests)) delete assistantStoreMock.pendingAssistantRequests[key];
  });

  it('renders the batch card only for the current authoritative proposal', () => {
    const target = renderPanel(batchSession());
    expect(target.querySelector('.plan-card')?.getAttribute('data-revision')).toBe('1');
    expect(target.querySelector('[aria-label="Current assistant execution"]').getAttribute('data-run-id')).toBe('r1');
  });

  it('never reactivates approval cards from historical planned or approval rows after completion or cancellation', () => {
    for (const phase of ['completed', 'cancelled']) {
      const target = renderPanel(batchSession({ phase, pendingApprovals: [], transcript: [plannedRow, approvalRow] }));
      expect(target.querySelector('.plan-card')).toBeNull();
      expect(target.querySelector('.action-card')).toBeNull();
      expect(textOf(target)).toContain('Historical plan record');
    }
  });

  it('gives v1 history, cached v2 views and unlisted proposals no approval authority', () => {
    const v1 = renderPanel({ sessionId: 'old', executionVersion: 1, state: 'awaiting_approval', lastPlanHash: 'legacy-hash',
      currentPlan: twoStepPlan(), pendingSteps: twoStepPlan().steps, participants: [], pendingActions: [], transcript: [plannedRow, approvalRow] });
    expect(v1.querySelector('.plan-card')).toBeNull();
    expect(v1.querySelector('.action-card')).toBeNull();
    expect(v1.querySelector('button.cancel')).toBeNull();

    const cached = renderPanel(batchSession({ authoritative: false }));
    expect(cached.querySelector('.plan-card')).toBeNull();
    expect(cached.querySelector('button.cancel')).toBeNull();
    expect(textOf(cached)).toContain('Cached view of this session');

    const unlisted = renderPanel(batchSession({ pendingApprovals: [] }));
    expect(unlisted.querySelector('.plan-card')).toBeNull();
  });

  it('renders iterative action cards only from canonical pending actions of the current run', () => {
    const target = renderPanel(batchSession({ workflow: 'iterative', proposal: null, pendingApprovals: ['action-1'],
      pendingActions: [{ actionId: 'action-1', runId: 'r1', toolName: 'tool.current' }], transcript: [approvalRow] }));
    const cards = target.querySelectorAll('.action-card');
    expect(cards).toHaveLength(1);
    expect(cards[0].getAttribute('data-action-id')).toBe('action-1');
    expect(cards[0].getAttribute('data-run-id')).toBe('r1');
    expect(target.querySelector('.plan-card')).toBeNull();
  });

  it('explains cancellation accounting and offers evidence-only reconciliation for uncertain work', () => {
    const target = renderPanel(batchSession({ phase: 'cancelling', pendingApprovals: [], submittedEffects: 2, uncertainEffects: 1 }));
    expect(textOf(target)).toContain('Assistant stopped; submitted operations may still finish. No rollback was attempted.');
    expect(target.querySelector('.plan-card')).toBeNull();
    const reconciliation = target.querySelector('[aria-label="Assistant execution reconciliation"]');
    expect(reconciliation).toBeTruthy();
    expect(Array.from(target.querySelectorAll('button')).some((button) => /mark (as )?complete/i.test(button.textContent))).toBe(false);
  });

  it('reports a reconnecting relay as unknown outcomes rather than failures', () => {
    assistantStoreMock.assistantConnection.status = 'reconnecting';
    const target = renderPanel(batchSession({ phase: 'executing', pendingApprovals: [] }));
    expect(textOf(target)).toContain('Reconnecting to assistant relays. Request outcomes stay unknown until canonical state arrives.');
    expect(textOf(target)).not.toMatch(/\bfailed\b/i);
  });
});

describe('assistant closed sessions and deployment workflows', () => {
  const batchUnavailable = (workflow) => workflow !== 'batch';

  beforeEach(() => {
    assistantStoreMock.publishAssistantPrompt.mockReset();
    assistantStoreMock.publishAssistantPrompt.mockResolvedValue({ ok: true });
    assistantStoreMock.publishAssistantApproval.mockReset();
    assistantStoreMock.publishAssistantApproval.mockResolvedValue({ ok: true });
    assistantStoreMock.assistantWorkflowAvailable.mockReset();
    assistantStoreMock.assistantWorkflowAvailable.mockReturnValue(true);
    assistantStoreMock.setActiveAssistantSession.mockReset();
    assistantStoreMock.activeAssistantSession.mockReset();
    assistantStoreMock.assistantConnection.status = 'live';
    for (const key of Object.keys(assistantStoreMock.pendingAssistantRequests)) delete assistantStoreMock.pendingAssistantRequests[key];
  });

  it('renders a closed session as closed: no prompt input, no run controls, a way to start over', async () => {
    const target = renderComponent(AssistantComposer, { session: { sessionId: 'closed', executionVersion: 2, authoritative: true,
      workflow: 'iterative', phase: 'cancelling', currentRunId: 'r1', closed: true, closedAt: '2026-09-26T11:00:00Z' } });
    const status = target.querySelector('.session-closed[role="status"]');
    expect(status?.textContent).toContain('Session closed');
    expect(status.getAttribute('data-closed-at')).toBe('2026-09-26T11:00:00Z');
    expect(target.querySelector('textarea').disabled).toBe(true);
    expect(target.querySelector('textarea').placeholder).toBe('This session is closed');
    expect(target.querySelector('button[type="submit"]').disabled).toBe(true);
    expect(target.querySelector('button.cancel')).toBeNull();
    expect(target.querySelector('select')).toBeNull();
    status.querySelector('button').click();
    await flush();
    expect(assistantStoreMock.setActiveAssistantSession).toHaveBeenCalledWith('assistant-new');
  });

  it('keeps an open session writable', () => {
    const target = renderComponent(AssistantComposer, { session: { sessionId: 'open', executionVersion: 2, authoritative: true,
      workflow: 'iterative', phase: 'completed', currentRunId: 'r1', closed: false } });
    expect(target.querySelector('.session-closed')).toBeNull();
    expect(target.querySelector('textarea').disabled).toBe(false);
  });

  it('disables the batch workflow when the deployment has no batch proposer', () => {
    assistantStoreMock.assistantWorkflowAvailable.mockImplementation(batchUnavailable);
    const target = renderComponent(AssistantComposer, { session: null });
    const select = target.querySelector('select[aria-label="Assistant workflow"]');
    const batch = Array.from(select.options).find((option) => option.value === 'batch');
    expect(batch.disabled).toBe(true);
    expect(batch.textContent).toBe('Batch plan (not available)');
    expect(target.querySelector('.workflow-unavailable')?.textContent).toContain('Batch plans are not available on this deployment');
  });

  it('continues a batch session in iterative when batch is unavailable instead of sending a refused turn', async () => {
    assistantStoreMock.assistantWorkflowAvailable.mockImplementation(batchUnavailable);
    const target = renderComponent(AssistantComposer, { session: { sessionId: 'was-batch', executionVersion: 2, authoritative: true,
      workflow: 'batch', phase: 'completed', currentRunId: 'r1', uncertainEffects: 0 } });
    const select = target.querySelector('select[aria-label="Assistant workflow"]');
    expect(select.options[0].textContent).toBe('Iterative');
    expect(textOf(target)).toContain('new turns in this session use Iterative');
    await setTextAreaValue(target, 'next step');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(assistantStoreMock.publishAssistantPrompt).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 'was-batch', workflow: 'iterative' }));
  });

  it('shows a specific message for a workflow_unavailable refusal', async () => {
    assistantStoreMock.publishAssistantPrompt.mockRejectedValueOnce(serviceRejection('workflow_unavailable',
      'workflow_unavailable: the batch workflow is not available on this deployment because it requires assistant.llm_model'));
    const target = renderComponent(AssistantComposer, { session: null });
    await setTextAreaValue(target, 'plan it');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    const error = target.querySelector('.prompt-error')?.textContent || '';
    expect(error).toContain('That workflow is not available on this deployment');
    expect(error).not.toContain('rejected by the assistant service');
  });

  it('shows a specific message for a session_closed refusal', async () => {
    assistantStoreMock.publishAssistantPrompt.mockRejectedValueOnce(serviceRejection('session_closed',
      'assistant session was closed by a session-scope cancellation'));
    const target = renderComponent(AssistantComposer, { session: { sessionId: 's', executionVersion: 2, authoritative: true,
      workflow: 'iterative', phase: 'completed', currentRunId: 'r1' } });
    await setTextAreaValue(target, 'again');
    target.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(target.querySelector('.prompt-error')?.textContent).toBe('This session was closed. Start a new session to continue.');
  });

  it('still offers approve and reject for an existing batch draft when batch is unavailable', async () => {
    assistantStoreMock.assistantWorkflowAvailable.mockImplementation(batchUnavailable);
    assistantStoreMock.activeAssistantSession.mockReturnValue(batchSession());
    const target = renderComponent(AssistantPanel, {});
    expect(target.querySelector('.plan-card')).toBeTruthy();
    const approve = target.querySelector('button.approve');
    expect(approve.disabled).toBe(false);
    expect(target.querySelector('button.reject').disabled).toBe(false);
  });
});
