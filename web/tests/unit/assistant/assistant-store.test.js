import { describe, it, expect, beforeEach, vi } from 'vitest';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) }
}));

const controlplaneMock = vi.hoisted(() => ({
  controlplaneConnection: { servicePubkey: 'b'.repeat(64) },
  bootstrapControlplane: vi.fn()
}));

const encryptedControlplaneMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn()
}));

const nostrMock = vi.hoisted(() => {
  function store(initial) {
    let value = initial;
    const subscribers = new Set();
    return {
      subscribe(fn) {
        subscribers.add(fn);
        fn(value);
        return () => subscribers.delete(fn);
      },
      set(next) {
        value = next;
        for (const fn of subscribers) fn(value);
      }
    };
  }

  return {
    connected: store(true),
    subscribeWithRecovery: vi.fn()
  };
});

vi.mock('../../../src/lib/stores/auth.js', () => authMock);
vi.mock('../../../src/lib/stores/controlplane.svelte.js', () => controlplaneMock);
vi.mock('../../../src/lib/nostr/encrypted-controlplane.js', () => encryptedControlplaneMock);
vi.mock('../../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../../src/lib/nostr/client.js');
  return { ...actual, nostr: nostrMock };
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function event({ id, kind, pubkey, created_at, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

describe('assistant store', () => {
  let store;
  let ASSISTANT_KINDS;
  let liveHandlers;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    liveHandlers = null;
    authMock.authState.status = 'authenticated';
    authMock.authState.pubkey = 'a'.repeat(64);
    controlplaneMock.controlplaneConnection.servicePubkey = 'b'.repeat(64);
    controlplaneMock.bootstrapControlplane.mockResolvedValue({ ok: true });
    encryptedControlplaneMock.requestEncryptedResult.mockReset();
    globalThis.localStorage?.clear?.();
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValue({
      result: { session_id: 'assistant-session-1', status: 'completed', summary: 'Assistant completed.' },
      requestEventId: 'request-event-1'
    });
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      liveHandlers = handlers;
      // Simulate empty bootstrap: deliver EOSE immediately
      Promise.resolve().then(() => handlers?.onEose?.());
      return vi.fn();
    });

    ({ ASSISTANT_KINDS } = await import('../../../src/lib/nostr/client.js'));
    store = await import('../../../src/lib/stores/assistant.svelte.js');
    store.resetAssistantStore();
  });

  function v2Event({ sessionId = 'v2-session', id = 'v2-projection', createdAt = 100,
    workflow = 'batch', phase = 'awaiting_approval', revision = 1, proposal = null,
    pendingApprovals = [], uncertainEffects = 0, submittedEffects = 0, runId = 'run-1', scope = { allowed_tools: null },
    closed = undefined, closedAt = undefined } = {}) {
    return event({ id, kind: ASSISTANT_KINDS.SESSION,
      pubkey: controlplaneMock.controlplaneConnection.servicePubkey, created_at: createdAt,
      tags: [['d', `bahia.assistant-session.v2:${sessionId}`], ['schema', 'bahia.assistant-session.v2'],
        ['session', sessionId], ['p', authMock.authState.pubkey, '', 'operator']],
      content: { execution_version: 2, session_id: sessionId, state: phase, workflow,
        current_run_id: runId, execution_revision: revision, phase, scope, proposal,
        pending_approvals: pendingApprovals, submitted_effects: submittedEffects, uncertain_effects: uncertainEffects,
        ...(closed === undefined ? {} : { closed }), ...(closedAt === undefined ? {} : { closed_at: closedAt }) } });
  }

  async function emitV2(options = {}) {
    if (!liveHandlers) await store.bootstrapAssistant({ force: true });
    return liveHandlers.onEvent(v2Event(options));
  }

  function statusEvent({ id, sessionId, createdAt = 150, content }) {
    return event({ id, kind: ASSISTANT_KINDS.STATUS, pubkey: controlplaneMock.controlplaneConnection.servicePubkey,
      created_at: createdAt, tags: [['schema', 'bahia.assistant-status.v1'], ['session', sessionId]],
      content: { session_id: sessionId, ...content } });
  }

  function sessionById(sessionId) {
    return store.assistantSessions.find((item) => item.sessionId === sessionId);
  }

  async function batchFixture(sessionId, steps) {
    const { computeAssistantBatchApprovalHash } = await import('../../../src/lib/nostr/client.js');
    const plan = { summary: 'Batch', needs_clarification: false, risk_level: 'low', steps };
    const scope = { allowed_tools: steps.map((step) => step.tool_name), arguments_digest: 'c'.repeat(64) };
    const base = await computeAssistantBatchApprovalHash({ sessionId, runId: 'run-1', proposalId: 'proposal-1', revision: 1, scope, plan });
    await emitV2({ sessionId, proposal: { proposal_id: 'proposal-1', revision: 1, hash: base.hash, plan },
      pendingApprovals: ['proposal-1'], scope });
    return { plan, scope, base, computeAssistantBatchApprovalHash };
  }

  it('publishes revision-bound reordered, pruned and edited batches with the public scope digest only', async () => {
    const { plan, scope, base, computeAssistantBatchApprovalHash } = await batchFixture('batch-1', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } },
      { step_id: 's2', title: 'Second', description: '', tool_name: 'tool.beta', tool_args: { b: true } },
      { step_id: 's3', title: 'Third', description: '', tool_name: 'tool.gamma', tool_args: { c: 'x' } }
    ]);
    // reorder s2 before s1, remove s3, edit s1 arguments; carry a stale preview that must not be hashed
    const edited = { ...plan, steps: [plan.steps[1], { ...plan.steps[0], tool_args: {}, args_preview: { a: 1 } }] };
    await store.publishAssistantApproval({ sessionId: 'batch-1', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve', modifiedPlan: edited });
    const call = encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0];
    expect(call.operation).toBe('assistant/approval');
    expect(call.payload).toMatchObject({ contract_version: 2, workflow: 'batch', session_id: 'batch-1', run_id: 'run-1',
      proposal_id: 'proposal-1', base_revision: 1, base_plan_hash: base.hash, approved_revision: 2, decision: 'approve' });
    expect(call.payload.modified_plan.steps).toEqual([
      { step_id: 's2', title: 'Second', description: '', tool_name: 'tool.beta', tool_args: { b: true } },
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: {} }
    ]);
    const approved = await computeAssistantBatchApprovalHash({ sessionId: 'batch-1', runId: 'run-1',
      proposalId: 'proposal-1', revision: 2, scope, plan: call.payload.modified_plan });
    expect(call.payload.approved_plan_hash).toBe(approved.hash);
    expect(call.payload.approved_plan_hash).not.toBe(base.hash);
    expect(call.payload.request_id).toEqual(expect.any(String));
    expect(JSON.stringify(call.payload)).not.toContain('arguments":');
    // no optimistic consumption: canonical state still lists the proposal
    expect(sessionById('batch-1').pendingApprovals).toEqual(['proposal-1']);
  });

  it('binds an unchanged approval or any rejection to the base revision and hash', async () => {
    const { plan, base } = await batchFixture('batch-2', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } }]);
    await store.publishAssistantApproval({ sessionId: 'batch-2', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve' });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0].payload).toMatchObject({
      approved_revision: 1, approved_plan_hash: base.hash });
    await store.publishAssistantApproval({ sessionId: 'batch-2', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'reject', modifiedPlan: { ...plan, steps: [] } });
    const reject = encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0].payload;
    expect(reject).toMatchObject({ decision: 'reject', approved_revision: 1, approved_plan_hash: base.hash });
    expect(reject).not.toHaveProperty('modified_plan');
  });

  it('refuses to send an approval when the public projection does not reproduce its hash', async () => {
    const { plan } = await batchFixture('batch-3', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } }]);
    await emitV2({ sessionId: 'batch-3', id: 'batch-3-bad', createdAt: 101, proposal: { proposal_id: 'proposal-1', revision: 1,
      hash: 'd'.repeat(64), plan }, pendingApprovals: ['proposal-1'], scope: { allowed_tools: ['tool.alpha'] } });
    encryptedControlplaneMock.requestEncryptedResult.mockClear();
    const error = await store.publishAssistantApproval({ sessionId: 'batch-3', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: 'd'.repeat(64), decision: 'approve' }).catch((err) => err);
    const { classifyAssistantRequestError } = await import('../../../src/lib/nostr/client.js');
    expect(classifyAssistantRequestError(error)).toMatchObject({ kind: 'stale' });
    expect(error.code).toBe('proposal_hash_mismatch');
    expect(encryptedControlplaneMock.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('classifies a service stale verdict as stale and a transport interruption as unknown without touching canonical state', async () => {
    const { base } = await batchFixture('batch-4', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } }]);
    const { classifyAssistantRequestError } = await import('../../../src/lib/nostr/client.js');
    const approve = () => store.publishAssistantApproval({ sessionId: 'batch-4', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve' }).catch((err) => err);
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({ requestEventId: 'r', result: {
      session_id: 'batch-4', status: 'failed', step: 'stale_approval', error: 'base revision superseded' } });
    expect(classifyAssistantRequestError(await approve())).toMatchObject({ kind: 'stale' });
    encryptedControlplaneMock.requestEncryptedResult.mockRejectedValueOnce(new Error('Encrypted controlplane disconnected'));
    expect(classifyAssistantRequestError(await approve())).toMatchObject({ kind: 'unknown' });
    const session = sessionById('batch-4');
    expect(session).toMatchObject({ phase: 'awaiting_approval', pendingApprovals: ['proposal-1'] });
    expect(session.transcript.some((item) => item.failed || item.status === 'failed')).toBe(false);
    expect(session.transcript.some((item) => item.status === 'request_acknowledged')).toBe(false);
  });

  it('uses NIP-01 event order rather than execution revision, while v2 outranks v1 history', async () => {
    await store.bootstrapAssistant({ force: true });
    liveHandlers.onEvent(event({ id: 'legacy-later', kind: ASSISTANT_KINDS.SESSION,
      pubkey: controlplaneMock.controlplaneConnection.servicePubkey, created_at: 300,
      tags: [['schema', 'bahia.assistant-session.v1'], ['session', 'order-1'], ['p', authMock.authState.pubkey, '', 'operator']],
      content: { state: 'awaiting_approval', last_plan_hash: 'legacy' } }));
    await emitV2({ sessionId: 'order-1', id: 'f'.repeat(64), createdAt: 100, revision: 99, phase: 'blocked' });
    await emitV2({ sessionId: 'order-1', id: 'a'.repeat(64), createdAt: 100, revision: 1, phase: 'executing' });
    await emitV2({ sessionId: 'order-1', id: 'z'.repeat(64), createdAt: 100, revision: 200, phase: 'completed' });
    const session = store.assistantSessions.find((entry) => entry.sessionId === 'order-1');
    expect(session).toMatchObject({ executionVersion: 2, phase: 'executing', executionRevision: 1 });
    liveHandlers.onEvent(event({ id: 'legacy-newer', kind: ASSISTANT_KINDS.SESSION,
      pubkey: controlplaneMock.controlplaneConnection.servicePubkey, created_at: 400,
      tags: [['schema', 'bahia.assistant-session.v1'], ['session', 'order-1']],
      content: { state: 'awaiting_approval', last_plan_hash: 'legacy' } }));
    expect(store.assistantSessions.find((entry) => entry.sessionId === 'order-1').phase).toBe('executing');
  });

  it('imports v1 cache only as history and does not cache private metadata', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const legacyKey = `bahia_assistant_transcript:bahia_assistant_transcript_v1:${operator}:${service}`;
    localStorage.setItem(legacyKey, JSON.stringify({ schema: 'bahia_assistant_transcript_v1',
      operatorPubkey: operator, servicePubkey: service, activeSessionId: 'old',
      sessions: [{ sessionId: 'old', state: 'awaiting_approval', lastPlanHash: 'legacy',
        metadata: { command_scope: { arguments: { secret: 'DO-NOT-CACHE' } } } }], transcript: [] }));
    await store.bootstrapAssistant({ force: true });
    const session = store.assistantSessions[0];
    expect(session).toMatchObject({ sessionId: 'old', executionVersion: 1, authoritative: false, pendingActions: [] });
    await expect(store.publishAssistantApproval({ sessionId: 'old', decision: 'approve' })).rejects.toThrow('Current v2 run required');
    await expect(store.publishAssistantPrompt({ prompt: 'continue', sessionId: 'old' })).rejects.toThrow('read-only');
    const newKey = `bahia_assistant_transcript:bahia_assistant_transcript_v2:${operator}:${service}`;
    expect(localStorage.getItem(newKey)).not.toContain('DO-NOT-CACHE');
    expect(JSON.parse(localStorage.getItem(newKey)).sessions[0]).toMatchObject({ sessionId: 'old', executionVersion: 1 });
    expect(localStorage.getItem(legacyKey)).toBeNull();

    // A v2 projection of the same session overrides the imported v1 view.
    await emitV2({ sessionId: 'old', workflow: 'iterative', phase: 'executing', pendingApprovals: [] });
    expect(sessionById('old')).toMatchObject({ executionVersion: 2, authoritative: true, workflow: 'iterative', phase: 'executing' });
  });

  it('keeps a restored v2 cache display-only until the relay re-delivers the projection', async () => {
    const { base } = await batchFixture('cached-1', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } }]);
    const projection = v2Event({ sessionId: 'cached-1', proposal: sessionById('cached-1').proposal,
      pendingApprovals: ['proposal-1'], scope: sessionById('cached-1').scope });
    expect(sessionById('cached-1').authoritative).toBe(true);

    store.resetAssistantStore();
    let handlers = null;
    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, h) => { handlers = h; return vi.fn(); });
    await store.bootstrapAssistant({ force: true });
    const cached = sessionById('cached-1');
    expect(cached).toMatchObject({ executionVersion: 2, authoritative: false, currentRunId: 'run-1', pendingApprovals: ['proposal-1'] });
    await expect(store.publishAssistantApproval({ sessionId: 'cached-1', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve' })).rejects.toThrow('Current v2 run required');
    await expect(store.publishAssistantCancellation({ sessionId: 'cached-1', runId: 'run-1' })).rejects.toThrow('Current v2 run required');

    // An older projection for the coordinate cannot displace the cached newer one (NIP-01).
    expect(handlers.onEvent(v2Event({ sessionId: 'cached-1', id: 'older', createdAt: 50, phase: 'proposing' }))).toBe(false);
    expect(sessionById('cached-1').authoritative).toBe(false);
    // The same event re-delivered by the relay restores authority.
    expect(handlers.onEvent(projection)).toBe(true);
    expect(sessionById('cached-1')).toMatchObject({ authoritative: true, phase: 'awaiting_approval' });
    await store.publishAssistantApproval({ sessionId: 'cached-1', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve' });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0].operation).toBe('assistant/approval');
  });

  it('derives pending actions from the current run projection, whatever order details arrive in', async () => {
    await store.bootstrapAssistant({ force: true });
    // details arrive before the projection, plus a row from an older run with the same action id
    liveHandlers.onEvent(statusEvent({ id: 'detail-run-1', sessionId: 'iter-1', createdAt: 90, content: {
      status: 'awaiting_approval', phase: 'approval_required', run_id: 'run-1', action_id: 'action-1',
      tool_name: 'tool.current', approval_prompt: 'Current approval', args_preview: { zone: 'a' } } }));
    liveHandlers.onEvent(statusEvent({ id: 'detail-run-0', sessionId: 'iter-1', createdAt: 80, content: {
      status: 'awaiting_approval', phase: 'approval_required', run_id: 'run-0', action_id: 'action-0', tool_name: 'tool.old' } }));
    expect(sessionById('iter-1').pendingActions).toEqual([]);
    await emitV2({ sessionId: 'iter-1', workflow: 'iterative', phase: 'awaiting_approval', pendingApprovals: ['action-1'] });
    expect(sessionById('iter-1').pendingActions).toEqual([expect.objectContaining({ actionId: 'action-1', runId: 'run-1',
      sessionId: 'iter-1', toolName: 'tool.current', approvalPrompt: 'Current approval', argsPreview: { zone: 'a' } })]);
    // a newer run re-using the action id does not inherit the previous run's description
    await emitV2({ sessionId: 'iter-1', id: 'iter-1-run-2', createdAt: 200, runId: 'run-2', workflow: 'iterative',
      phase: 'awaiting_approval', pendingApprovals: ['action-1'] });
    expect(sessionById('iter-1').pendingActions).toEqual([{ actionId: 'action-1', runId: 'run-2', sessionId: 'iter-1' }]);
    // a stale approval_required row can never create authority on its own
    await emitV2({ sessionId: 'iter-1', id: 'iter-1-done', createdAt: 300, runId: 'run-2', workflow: 'iterative', phase: 'completed' });
    liveHandlers.onEvent(statusEvent({ id: 'late-detail', sessionId: 'iter-1', createdAt: 310, content: {
      status: 'awaiting_approval', phase: 'approval_required', run_id: 'run-2', action_id: 'action-1' } }));
    expect(sessionById('iter-1').pendingActions).toEqual([]);
    await expect(store.publishAssistantActionDecision({ sessionId: 'iter-1', runId: 'run-2', actionId: 'action-1',
      decision: 'approve' })).rejects.toThrow('Action changed');
  });

  it('blocks overlapping turns and workflow changes with unresolved effects', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    await emitV2({ sessionId: 'overlap-1', workflow: 'batch', phase: 'executing' });
    await expect(store.publishAssistantPrompt({ prompt: 'again', sessionId: 'overlap-1' })).rejects.toThrow('already active');
    await emitV2({ sessionId: 'overlap-1', id: 'overlap-done', createdAt: 150, workflow: 'batch', phase: 'completed', uncertainEffects: 1 });
    await expect(store.publishAssistantPrompt({ prompt: 'again', sessionId: 'overlap-1', workflow: 'iterative' }))
      .rejects.toThrow('Workflow change requires a finished run with no unresolved effects');
    await emitV2({ sessionId: 'overlap-1', id: 'overlap-clean', createdAt: 160, workflow: 'batch', phase: 'completed' });
    await store.publishAssistantPrompt({ prompt: 'switch', sessionId: 'overlap-1', workflow: 'iterative' });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0]).toMatchObject({
      operation: 'assistant/prompt', payload: { contract_version: 2, session_id: 'overlap-1', workflow: 'iterative', prompt: 'switch' } });
  });

  it('records a service-refused prompt as a request rejection, never as an execution failure', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({ requestEventId: 'req', result: {
      session_id: 'refused-1', status: 'failed', step: 'run_in_progress', error: 'a run is already active' } });
    await expect(store.publishAssistantPrompt({ prompt: 'go', sessionId: 'refused-1' })).rejects.toThrow('run_in_progress');
    const transcript = sessionById('refused-1').transcript;
    expect(transcript).toContainEqual(expect.objectContaining({ type: 'result', status: 'request_rejected',
      summary: 'Assistant service rejected the request', error: 'run_in_progress: a run is already active' }));
    expect(transcript.some((item) => item.status === 'outcome_unknown' || item.failed || item.status === 'request_acknowledged')).toBe(false);
    expect(Object.keys(store.pendingAssistantRequests)).toHaveLength(0);
  });

  it('renders a projected closed session as closed and refuses new turns locally', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    await emitV2({ sessionId: 'closed-1', workflow: 'iterative', phase: 'completed', closed: true, closedAt: '2026-09-26T11:00:00Z' });
    expect(sessionById('closed-1')).toMatchObject({ closed: true, closedAt: '2026-09-26T11:00:00Z' });
    encryptedControlplaneMock.requestEncryptedResult.mockClear();
    await expect(store.publishAssistantPrompt({ prompt: 'again', sessionId: 'closed-1' })).rejects.toThrow('session was closed');
    expect(encryptedControlplaneMock.requestEncryptedResult).not.toHaveBeenCalled();
    // Open sessions and projections from backends without the field stay open.
    await emitV2({ sessionId: 'open-1', id: 'open-1-projection', workflow: 'iterative', phase: 'completed' });
    expect(sessionById('open-1').closed).toBe(false);
  });

  it('treats a session_closed refusal as closing the session for backends without the projection field', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    await emitV2({ sessionId: 'legacy-close', workflow: 'iterative', phase: 'completed' });
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({ requestEventId: 'req', result: {
      session_id: 'legacy-close', status: 'failed', step: 'session_closed',
      summary: 'assistant session was closed by a session-scope cancellation', error: 'assistant session was closed by a session-scope cancellation' } });
    await expect(store.publishAssistantPrompt({ prompt: 'go', sessionId: 'legacy-close' })).rejects.toThrow('session_closed');
    const session = sessionById('legacy-close');
    expect(session.closed).toBe(true);
    expect(session.transcript).toContainEqual(expect.objectContaining({ type: 'result', status: 'request_rejected',
      summary: 'This session was closed. Start a new session to continue.' }));
    // A later projection that predates the field does not reopen the session.
    await emitV2({ sessionId: 'legacy-close', id: 'legacy-close-2', createdAt: 150, workflow: 'iterative', phase: 'completed' });
    expect(sessionById('legacy-close').closed).toBe(true);
    encryptedControlplaneMock.requestEncryptedResult.mockClear();
    await expect(store.publishAssistantPrompt({ prompt: 'again', sessionId: 'legacy-close' })).rejects.toThrow('session was closed');
    expect(encryptedControlplaneMock.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('offers only the workflows system discovery advertises, and every workflow when it says nothing', async () => {
    const { discoveryState } = await import('../../../src/lib/stores/discovery.svelte.js');
    discoveryState.info = null;
    expect(store.assistantAvailableWorkflows()).toEqual(['batch', 'iterative']);
    discoveryState.info = { assistant: { enabled: true, available_workflows: ['iterative'], default_workflow: 'iterative' } };
    expect(store.assistantWorkflowAvailable('batch')).toBe(false);
    expect(store.assistantWorkflowAvailable('iterative')).toBe(true);
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    encryptedControlplaneMock.requestEncryptedResult.mockClear();
    await expect(store.publishAssistantPrompt({ prompt: 'plan it', sessionId: 'wf-1', workflow: 'batch' })).rejects.toThrow('batch workflow is not available');
    expect(encryptedControlplaneMock.requestEncryptedResult).not.toHaveBeenCalled();
    discoveryState.info = null;
  });

  it('learns an unavailable workflow from a workflow_unavailable refusal and says why', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({ requestEventId: 'req', result: {
      session_id: 'wf-2', status: 'failed', step: 'workflow_unavailable',
      error: 'workflow_unavailable: the batch workflow is not available on this deployment because it requires assistant.llm_model' } });
    await expect(store.publishAssistantPrompt({ prompt: 'plan it', sessionId: 'wf-2', workflow: 'batch' })).rejects.toThrow('workflow_unavailable');
    expect(store.assistantWorkflowAvailable('batch')).toBe(false);
    expect(store.assistantWorkflowAvailable('iterative')).toBe(true);
    const transcript = sessionById('wf-2').transcript;
    expect(transcript).toContainEqual(expect.objectContaining({ type: 'result', status: 'request_rejected',
      summary: expect.stringContaining('not available on this deployment') }));
    expect(transcript.some((item) => item.summary === 'Assistant service rejected the request')).toBe(false);
    store.resetAssistantStore();
    expect(store.assistantWorkflowAvailable('batch')).toBe(true);
  });

  it('still approves and rejects an existing batch draft when the batch workflow is unavailable', async () => {
    const { discoveryState } = await import('../../../src/lib/stores/discovery.svelte.js');
    discoveryState.info = { assistant: { enabled: true, available_workflows: ['iterative'] } };
    const { base } = await batchFixture('batch-unavailable', [
      { step_id: 's1', title: 'First', description: '', tool_name: 'tool.alpha', tool_args: { a: 1 } }]);
    await store.publishAssistantApproval({ sessionId: 'batch-unavailable', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'approve' });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0]).toMatchObject({
      operation: 'assistant/approval', payload: { workflow: 'batch', decision: 'approve' } });
    await store.publishAssistantApproval({ sessionId: 'batch-unavailable', runId: 'run-1', proposalId: 'proposal-1',
      baseRevision: 1, basePlanHash: base.hash, decision: 'reject' });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0].payload).toMatchObject({ decision: 'reject' });
    discoveryState.info = null;
  });

  it('never records private command-scope arguments from transcript metadata', async () => {
    await store.bootstrapAssistant({ force: true });
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    liveHandlers.onEvent(event({ id: 'transcript-private', kind: ASSISTANT_KINDS.TRANSCRIPT, pubkey: service, created_at: 140,
      tags: [['schema', 'bahia.assistant-transcript.v1'], ['session', 'private-1'], ['domain', 'assistant']],
      content: { session_id: 'private-1', seq: 1, message: { role: 'assistant', text: 'ok' },
        metadata: { phase: 'x', command_scope: { arguments: { token: 'SECRET-ARG' } }, scope: { allowed_tools: null, arguments: { token: 'SECRET-ARG' } } } } }));
    const item = sessionById('private-1').transcript[0];
    expect(JSON.stringify(item)).not.toContain('SECRET-ARG');
    expect(item.metadata.scope).toEqual({ allowed_tools: null });
    const cacheKey = `bahia_assistant_transcript:bahia_assistant_transcript_v2:${authMock.authState.pubkey}:${service}`;
    expect(localStorage.getItem(cacheKey)).not.toContain('SECRET-ARG');
  });

  it('cancels a hashless run while its prompt RPC remains pending, without inventing failure', async () => {
    const pending = deferred();
    encryptedControlplaneMock.requestEncryptedResult.mockReturnValueOnce(pending.promise);
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    store.assistantConnection.servicePubkey = controlplaneMock.controlplaneConnection.servicePubkey;
    const prompt = store.publishAssistantPrompt({ prompt: 'Investigate', sessionId: 'cancel-1', workflow: 'iterative' });
    await emitV2({ sessionId: 'cancel-1', workflow: 'iterative', phase: 'proposing', pendingApprovals: [] });
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({ result: { status: 'accepted' } });
    await store.publishAssistantCancellation({ sessionId: 'cancel-1', runId: 'run-1', scope: 'run' });
    const cancel = encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0];
    expect(cancel.operation).toBe('assistant/cancel');
    expect(cancel.payload).toEqual({ contract_version: 2, session_id: 'cancel-1', run_id: 'run-1', scope: 'run' });
    expect(Object.keys(store.pendingAssistantRequests)).toHaveLength(1);
    pending.reject(new Error('ContextVM connection interrupted'));
    await expect(prompt).rejects.toThrow('ContextVM connection interrupted');
    expect(store.assistantSessions.find((item) => item.sessionId === 'cancel-1').transcript)
      .toEqual(expect.arrayContaining([expect.objectContaining({ status: 'outcome_unknown' })]));
  });

  it('reconciliation submits only an exact downstream request-event reference', async () => {
    await emitV2({ sessionId: 'uncertain-1', phase: 'blocked', uncertainEffects: 1 });
    await expect(store.publishAssistantReconciliation({ sessionId: 'uncertain-1', runId: 'run-1',
      workId: 'work-1', requestEventId: 'not-an-event' })).rejects.toThrow('64-character');
    await store.publishAssistantReconciliation({ sessionId: 'uncertain-1', runId: 'run-1',
      workId: 'work-1', requestEventId: 'a'.repeat(64) });
    expect(encryptedControlplaneMock.requestEncryptedResult.mock.calls.at(-1)[0]).toMatchObject({
      operation: 'assistant/reconcile', payload: { contract_version: 2, session_id: 'uncertain-1',
        run_id: 'run-1', work_id: 'work-1', request_event_id: 'a'.repeat(64) }
    });
  });

  it('exposes subscription recovery health', async () => {
    await store.bootstrapAssistant({ force: true });

    liveHandlers.onHealth({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });

    expect(store.assistantConnection).toMatchObject({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });
  });

  it('bootstraps session state from 30900/30315 relay backfill events', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-session-1';
    const requestId = 'prompt-1';
    const planHash = 'plan-hash-1';
    const plan = {
      summary: 'Deploy chat route',
      risk_level: 'medium',
      steps: [
        { step_id: 'step-1', title: 'Deploy route', tool_name: 'llm.deploy', args_preview: { route_id: 'route-1' } }
      ]
    };

    const bootstrapEvents = [
      event({
        id: 'session-event',
        kind: ASSISTANT_KINDS.SESSION,
        pubkey: service,
        created_at: 100,
        tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['schema', 'bahia.assistant-session.v1'], ['session', sessionId], ['p', operator, '', 'operator'], ['agent', 'assistant-agent'], ['status', 'awaiting_approval']],
        content: {
          state: 'awaiting_approval',
          operator_pubkey: operator,
          assistant_id: 'assistant-agent',
          current_request_id: requestId,
          last_plan_hash: planHash,
          current_plan: plan,
          transcript_summary: 'Deploy chat route'
        }
      }),
      event({
        id: 'status-planned',
        kind: ASSISTANT_KINDS.STATUS,
        pubkey: service,
        created_at: 110,
        tags: [['d', `bahia.assistant-status.v1:${sessionId}:planned`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['e', requestId, '', 'reply'], ['status', 'planned'], ['plan-hash', planHash]],
        content: { session_id: sessionId, status: 'planned', message: 'Plan ready', plan, plan_hash: planHash }
      }),
      event({
        id: 'status-blocked',
        kind: ASSISTANT_KINDS.STATUS,
        pubkey: service,
        created_at: 120,
        tags: [['d', `bahia.assistant-status.v1:${sessionId}:blocked`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['e', requestId, '', 'reply'], ['status', 'blocked'], ['downstream-request', 'downstream-1']],
        content: { session_id: sessionId, status: 'blocked', message: 'relay closed' }
      })
    ];
    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => {
        for (const evt of bootstrapEvents) handlers?.onEvent?.(evt);
        handlers?.onEose?.();
      });
      return vi.fn();
    });

    const result = await store.bootstrapAssistant({ force: true });

    expect(result.ok).toBe(true);
    expect(store.assistantSessions).toHaveLength(1);
    expect(store.assistantUi.activeSessionId).toBe(sessionId);
    const session = store.assistantSessions[0];
    expect(session.state).toBe('awaiting_approval');
    expect(session.lastPlanHash).toBe(planHash);
    expect(session.currentPlan).toEqual(plan);
    expect(session.transcript.map((item) => item.id)).toEqual(['status-planned', 'status-blocked']);
    expect(session.transcript[1]).toMatchObject({ type: 'status', status: 'blocked', message: 'relay closed' });
  });

  it('applies a live 30315 status event to the active session transcript', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-live-session';

    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => {
        handlers?.onEvent?.(event({
          id: 'session-event',
          kind: ASSISTANT_KINDS.SESSION,
          pubkey: service,
          created_at: 100,
          tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['schema', 'bahia.assistant-session.v1'], ['session', sessionId], ['p', operator, '', 'operator'], ['status', 'executing']],
          content: { state: 'executing', operator_pubkey: operator, transcript_summary: 'Live session' }
        }));
        handlers?.onEose?.();
      });
      return vi.fn();
    });

    await store.bootstrapAssistant({ force: true });
    expect(store.assistantSessions[0].transcript).toHaveLength(0);

    expect(store.assistantUi.panelOpen).toBe(false);
    expect(store.assistantUi.hasUnread).toBe(false);

    liveHandlers.onEvent(event({
      id: 'status-executing',
      kind: ASSISTANT_KINDS.STATUS,
      pubkey: service,
      created_at: 130,
      tags: [['d', `bahia.assistant-status.v1:${sessionId}:executing`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['status', 'executing'], ['downstream-request', 'downstream-live']],
      content: { session_id: sessionId, status: 'executing', message: 'Deploying route' }
    }));

    expect(store.assistantUi.hasUnread).toBe(true);
    store.openAssistantPanel();
    expect(store.assistantUi.panelOpen).toBe(true);
    expect(store.assistantUi.hasUnread).toBe(false);

    const session = store.assistantSessions[0];
    expect(session.transcript).toHaveLength(1);
    expect(session.transcript[0]).toMatchObject({
      id: 'status-executing',
      type: 'status',
      status: 'executing',
      message: 'Deploying route',
      downstreamRequestId: 'downstream-live'
    });
  });

  it('parses agentic status phases, pending action approvals, and 30316 transcript events', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-agentic-session';

    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => {
        handlers?.onEvent?.(event({
          id: 'session-agentic',
          kind: ASSISTANT_KINDS.SESSION,
          pubkey: service,
          created_at: 100,
          tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['schema', 'bahia.assistant-session.v1'], ['session', sessionId], ['p', operator, '', 'operator'], ['status', 'executing']],
          content: { state: 'executing', operator_pubkey: operator, metadata: { agent_loop: { state: 'running' } } }
        }));
        handlers?.onEose?.();
      });
      return vi.fn();
    });

    await store.bootstrapAssistant({ force: true });

    liveHandlers.onEvent(event({
      id: 'status-tool-submitted',
      kind: ASSISTANT_KINDS.STATUS,
      pubkey: service,
      created_at: 120,
      tags: [['d', `bahia.assistant-status.v1:${sessionId}:tool-submitted`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['status', 'executing']],
      content: {
        session_id: sessionId,
        status: 'executing',
        phase: 'tool_submitted',
        message: 'async tool submitted; waiting for downstream result',
        tool_call_id: 'tool-call-1',
        tool_name: 'bahia_assistant_dns_zone_create',
        downstream_request: 'downstream-1',
        args_preview: { zone: 'prod.example.com' }
      }
    }));
    liveHandlers.onEvent(event({
      id: 'status-approval-required',
      kind: ASSISTANT_KINDS.STATUS,
      pubkey: service,
      created_at: 130,
      tags: [['d', `bahia.assistant-status.v1:${sessionId}:approval`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['status', 'awaiting_approval']],
      content: {
        session_id: sessionId,
        status: 'awaiting_approval',
        phase: 'approval_required',
        action_id: 'action-rollback-1',
        tool_call_id: 'tool-call-2',
        tool_name: 'bahia_assistant_llm_rollback',
        approval_prompt: 'Rollback production requires approval',
        permission: { risk: 'high' }
      }
    }));
    liveHandlers.onEvent(event({
      id: 'transcript-assistant',
      kind: ASSISTANT_KINDS.TRANSCRIPT,
      pubkey: service,
      created_at: 140,
      tags: [['d', `bahia.assistant-transcript.v1:${sessionId}:00000000000000000001`], ['domain', 'assistant'], ['schema', 'bahia.assistant-transcript.v1'], ['session', sessionId], ['turn', 'turn-1'], ['role', 'assistant'], ['seq', '1'], ['p', operator, '', 'operator']],
      content: {
        session_id: sessionId,
        turn_id: 'turn-1',
        seq: 1,
        message: { role: 'assistant', content: [{ type: 'text', text: 'DNS records are healthy.' }] },
        metadata: { phase: 'assistant_model_response' }
      }
    }));

    const session = store.assistantSessions.find((item) => item.sessionId === sessionId);
    expect(session.pendingActions).toEqual([]); // v1 status is history, never approval authority
    expect(session.transcript).toEqual(expect.arrayContaining([
      expect.objectContaining({
        id: 'status-tool-submitted',
        type: 'status',
        phase: 'tool_submitted',
        toolCallId: 'tool-call-1',
        toolName: 'bahia_assistant_dns_zone_create',
        downstreamRequestId: 'downstream-1',
        argsPreview: { zone: 'prod.example.com' }
      }),
      expect.objectContaining({
        id: 'transcript-assistant',
        type: 'transcript',
        role: 'assistant',
        sequence: 1,
        phase: 'assistant_model_response',
        text: 'DNS records are healthy.'
      })
    ]));
  });

  it('publishes run-bound v2 action decisions and waits for canonical consumption', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    await store.bootstrapAssistant({ force: true });
    liveHandlers.onEvent(event({ id: 'v2-action', kind: ASSISTANT_KINDS.SESSION, pubkey: service,
      created_at: 200, tags: [['d', 'bahia.assistant-session.v2:assistant-action-session'],
        ['schema', 'bahia.assistant-session.v2'], ['session', 'assistant-action-session'], ['p', operator, '', 'operator']],
      content: { session_id: 'assistant-action-session', execution_version: 2, state: 'awaiting_approval', workflow: 'iterative', current_run_id: 'run-1',
        phase: 'awaiting_approval', scope: { allowed_tools: null }, pending_approvals: ['action-1'] } }));
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { session_id: 'assistant-action-session', status: 'executing', action_id: 'action-1' },
      requestEventId: 'approval-request-1'
    });
    await store.publishAssistantActionDecision({ sessionId: 'assistant-action-session', runId: 'run-1',
      actionId: 'action-1', decision: 'approve', reason: 'safe rollback window' });
    expect(encryptedControlplaneMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'assistant/approval', payload: expect.objectContaining({ contract_version: 2,
        session_id: 'assistant-action-session', run_id: 'run-1', workflow: 'iterative',
        action_id: 'action-1', decision: 'approve', reason: 'safe rollback window' })
    }));
    const session = store.assistantSessions.find((item) => item.sessionId === 'assistant-action-session');
    expect(session.pendingActions.map((action) => action.actionId)).toEqual(['action-1']);
    const consumedAccepted = liveHandlers.onEvent(event({ id: 'v2-action-consumed', kind: ASSISTANT_KINDS.SESSION, pubkey: service,
      created_at: 201, tags: [['d', 'bahia.assistant-session.v2:assistant-action-session'],
        ['schema', 'bahia.assistant-session.v2'], ['session', 'assistant-action-session'], ['p', operator, '', 'operator']],
      content: { session_id: 'assistant-action-session', execution_version: 2, state: 'executing', workflow: 'iterative', current_run_id: 'run-1',
        phase: 'executing', scope: { allowed_tools: null }, pending_approvals: [] } }));
    expect(consumedAccepted).toBe(true);
    expect(store.assistantSessions.find((item) => item.sessionId === 'assistant-action-session').pendingActions).toEqual([]);
  });

  it('rejects cancel decisions through assistant/approval', async () => {
    await expect(store.publishAssistantApproval({ decision: 'cancel' })).rejects.toThrow('Decision must be approve or reject');
    expect(encryptedControlplaneMock.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('tracks prompt pending state and records timeout detail when the assistant result does not arrive', async () => {
    const pending = deferred();
    encryptedControlplaneMock.requestEncryptedResult.mockReturnValueOnce(pending.promise);
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    store.assistantConnection.servicePubkey = controlplaneMock.controlplaneConnection.servicePubkey;

    const promptPromise = store.publishAssistantPrompt({
      prompt: 'Deploy api',
      sessionId: 'assistant-timeout-session',
      routeContext: { route: '/services' },
      selectedRefs: ['service:api']
    });

    let session = store.assistantSessions.find((item) => item.sessionId === 'assistant-timeout-session');
    expect(session).toBeTruthy();
    expect(Object.keys(store.pendingAssistantRequests)).toHaveLength(1);
    expect(session.transcript.some((item) => item.type === 'prompt' && item.prompt === 'Deploy api')).toBe(true);
    expect(session.transcript.some((item) => item.pending && item.status === 'planning')).toBe(true);

    pending.reject(new Error('ContextVM request timed out after 120000ms waiting for result'));
    await expect(promptPromise).rejects.toThrow('ContextVM request timed out after 120000ms waiting for result');

    session = store.assistantSessions.find((item) => item.sessionId === 'assistant-timeout-session');
    expect(Object.keys(store.pendingAssistantRequests)).toHaveLength(0);
    expect(session.transcript.some((item) => item.pending)).toBe(false);
    expect(session.transcript).toContainEqual(expect.objectContaining({
      type: 'result',
      status: 'outcome_unknown',
      summary: 'Request outcome unknown / reconnecting',
      error: 'ContextVM request timed out after 120000ms waiting for result'
    }));
  });

  it('restores cached assistant sessions and transcript across reloads', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-cached-session';

    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => {
        handlers?.onEvent?.(event({
          id: 'session-cached',
          kind: ASSISTANT_KINDS.SESSION,
          pubkey: service,
          created_at: 100,
          tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['schema', 'bahia.assistant-session.v1'], ['session', sessionId], ['p', operator, '', 'operator'], ['status', 'executing']],
          content: { state: 'executing', operator_pubkey: operator, transcript_summary: 'Cached session' }
        }));
        handlers?.onEvent?.(event({
          id: 'status-cached',
          kind: ASSISTANT_KINDS.STATUS,
          pubkey: service,
          created_at: 110,
          tags: [['d', `bahia.assistant-status.v1:${sessionId}:executing`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['status', 'executing']],
          content: { session_id: sessionId, status: 'executing', message: 'Cached transcript survives reload' }
        }));
        handlers?.onEose?.();
      });
      return vi.fn();
    });

    await store.bootstrapAssistant({ force: true });
    expect(store.assistantSessions[0].transcript).toHaveLength(1);

    const cacheKey = `bahia_assistant_transcript:bahia_assistant_transcript_v2:${operator}:${service}`;
    expect(globalThis.localStorage.getItem(cacheKey)).toContain('Cached transcript survives reload');

    store.resetAssistantStore();
    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => handlers?.onEose?.());
      return vi.fn();
    });

    await store.bootstrapAssistant({ force: true });

    expect(store.assistantUi.activeSessionId).toBe(sessionId);
    expect(store.assistantSessions).toHaveLength(1);
    expect(store.assistantSessions[0]).toMatchObject({
      sessionId,
      state: 'executing',
      transcriptSummary: 'Cached session'
    });
    expect(store.assistantSessions[0].transcript).toHaveLength(1);
    expect(store.assistantSessions[0].transcript[0]).toMatchObject({
      id: 'status-cached',
      type: 'status',
      status: 'executing',
      message: 'Cached transcript survives reload'
    });
  });

  it('does not replay historical streaming chunks during bootstrap', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-stale-stream-session';

    nostrMock.subscribeWithRecovery.mockImplementationOnce((_filters, handlers) => {
      liveHandlers = handlers;
      Promise.resolve().then(() => {
        handlers?.onEvent?.(event({
          id: 'session-event',
          kind: ASSISTANT_KINDS.SESSION,
          pubkey: service,
          created_at: 100,
          tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['schema', 'bahia.assistant-session.v1'], ['session', sessionId], ['p', operator, '', 'operator'], ['status', 'idle']],
          content: { state: 'idle', operator_pubkey: operator, transcript_summary: 'Stale stream session' }
        }));
        handlers?.onEvent?.(event({
          id: 'historical-stream-1',
          kind: ASSISTANT_KINDS.STATUS,
          pubkey: service,
          created_at: 110,
          tags: [['d', `bahia.assistant-status.v1:${sessionId}:stream-1`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['e', 'request-1', '', 'reply'], ['status', 'planning'], ['streaming', 'true']],
          content: { session_id: sessionId, status: 'planning', streaming: true, chunk: '{"summary":' }
        }));
        handlers?.onEvent?.(event({
          id: 'historical-stream-2',
          kind: ASSISTANT_KINDS.STATUS,
          pubkey: service,
          created_at: 111,
          tags: [['d', `bahia.assistant-status.v1:${sessionId}:stream-2`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['e', 'request-1', '', 'reply'], ['status', 'planning'], ['streaming', 'true']],
          content: { session_id: sessionId, status: 'planning', streaming: true, chunk: '"partial"}' }
        }));
        handlers?.onEose?.();
      });
      return vi.fn();
    });

    await store.bootstrapAssistant({ force: true });

    expect(store.assistantSessions).toHaveLength(1);
    expect(store.assistantSessions[0].transcript).toHaveLength(0);

    liveHandlers.onEvent(event({
      id: 'live-stream',
      kind: ASSISTANT_KINDS.STATUS,
      pubkey: service,
      created_at: 130,
      tags: [['d', `bahia.assistant-status.v1:${sessionId}:stream-live`], ['schema', 'bahia.assistant-status.v1'], ['session', sessionId], ['e', 'request-2', '', 'reply'], ['status', 'planning'], ['streaming', 'true']],
      content: { session_id: sessionId, status: 'planning', streaming: true, chunk: 'Planning live response' }
    }));

    expect(store.assistantSessions[0].transcript).toHaveLength(1);
    expect(store.assistantSessions[0].transcript[0]).toMatchObject({
      id: 'stream:request-2',
      streaming: true,
      streamingContent: 'Planning live response'
    });
  });
});
