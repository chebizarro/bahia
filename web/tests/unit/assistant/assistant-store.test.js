import { describe, it, expect, beforeEach, vi } from 'vitest';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) }
}));

const controlplaneMock = vi.hoisted(() => ({
  controlplaneConnection: { servicePubkey: 'b'.repeat(64) },
  bootstrapControlplane: vi.fn()
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
    queryUntilEose: vi.fn(),
    subscribe: vi.fn()
  };
});

vi.mock('../../../src/lib/stores/auth.js', () => authMock);
vi.mock('../../../src/lib/stores/controlplane.svelte.js', () => controlplaneMock);
vi.mock('../../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../../src/lib/nostr/client.js');
  return { ...actual, nostr: nostrMock };
});

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
    nostrMock.queryUntilEose.mockResolvedValue([]);
    nostrMock.subscribe.mockImplementation((_filters, handlers) => {
      liveHandlers = handlers;
      return vi.fn();
    });

    ({ ASSISTANT_KINDS } = await import('../../../src/lib/nostr/client.js'));
    store = await import('../../../src/lib/stores/assistant.svelte.js');
    store.resetAssistantStore();
  });

  it('bootstraps session state from 31990/38422/38423 relay backfill events', async () => {
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

    nostrMock.queryUntilEose.mockResolvedValueOnce([
      event({
        id: 'session-event',
        kind: ASSISTANT_KINDS.SESSION,
        pubkey: service,
        created_at: 100,
        tags: [['d', sessionId], ['session', sessionId], ['p', operator, '', 'operator'], ['agent', 'assistant-agent'], ['status', 'awaiting_approval']],
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
        tags: [['session', sessionId], ['e', requestId, '', 'reply'], ['status', 'planned'], ['plan-hash', planHash]],
        content: { session_id: sessionId, status: 'planned', message: 'Plan ready', plan, plan_hash: planHash }
      }),
      event({
        id: 'result-blocked',
        kind: ASSISTANT_KINDS.RESULT,
        pubkey: service,
        created_at: 120,
        tags: [['session', sessionId], ['e', requestId, '', 'reply'], ['status', 'blocked'], ['downstream-request', 'downstream-1']],
        content: { session_id: sessionId, status: 'blocked', error: 'relay closed' }
      })
    ]);

    const result = await store.bootstrapAssistant({ force: true });

    expect(result.ok).toBe(true);
    expect(store.assistantSessions).toHaveLength(1);
    expect(store.assistantUi.activeSessionId).toBe(sessionId);
    const session = store.assistantSessions[0];
    expect(session.state).toBe('awaiting_approval');
    expect(session.lastPlanHash).toBe(planHash);
    expect(session.currentPlan).toEqual(plan);
    expect(session.transcript.map((item) => item.id)).toEqual(['status-planned', 'result-blocked']);
    expect(session.transcript[1]).toMatchObject({ type: 'result', blocked: true, error: 'relay closed' });
  });

  it('applies a live 38422 status event to the active session transcript', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-live-session';

    nostrMock.queryUntilEose.mockResolvedValueOnce([
      event({
        id: 'session-event',
        kind: ASSISTANT_KINDS.SESSION,
        pubkey: service,
        created_at: 100,
        tags: [['d', sessionId], ['session', sessionId], ['p', operator, '', 'operator'], ['status', 'executing']],
        content: { state: 'executing', operator_pubkey: operator, transcript_summary: 'Live session' }
      })
    ]);

    await store.bootstrapAssistant({ force: true });
    expect(store.assistantSessions[0].transcript).toHaveLength(0);

    expect(store.assistantUi.panelOpen).toBe(false);
    expect(store.assistantUi.hasUnread).toBe(false);

    liveHandlers.onEvent(event({
      id: 'status-executing',
      kind: ASSISTANT_KINDS.STATUS,
      pubkey: service,
      created_at: 130,
      tags: [['session', sessionId], ['status', 'executing'], ['downstream-request', 'downstream-live']],
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
});
