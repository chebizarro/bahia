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
    subscribe: vi.fn()
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
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValue({
      result: { session_id: 'assistant-session-1', status: 'completed', summary: 'Assistant completed.' },
      requestEventId: 'request-event-1'
    });
    nostrMock.subscribe.mockImplementation((_filters, handlers) => {
      liveHandlers = handlers;
      // Simulate empty bootstrap: deliver EOSE immediately
      Promise.resolve().then(() => handlers?.onEose?.());
      return vi.fn();
    });

    ({ ASSISTANT_KINDS } = await import('../../../src/lib/nostr/client.js'));
    store = await import('../../../src/lib/stores/assistant.svelte.js');
    store.resetAssistantStore();
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
    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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

    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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
      status: 'failed',
      summary: 'assistant planning failed',
      error: 'ContextVM request timed out after 120000ms waiting for result'
    }));
  });

  it('does not replay historical streaming chunks during bootstrap', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-stale-stream-session';

    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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
