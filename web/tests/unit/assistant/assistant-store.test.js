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
    globalThis.localStorage?.clear?.();
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

  it('parses agentic status phases, pending action approvals, and 30316 transcript events', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-agentic-session';

    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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
    expect(session.pendingActions).toEqual([expect.objectContaining({
      actionId: 'action-rollback-1',
      toolCallId: 'tool-call-2',
      toolName: 'bahia_assistant_llm_rollback',
      approvalPrompt: 'Rollback production requires approval',
      permission: { risk: 'high' }
    })]);
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

  it('publishes action-level assistant approval decisions with action_id and reason', async () => {
    store.assistantConnection.operatorPubkey = authMock.authState.pubkey;
    store.assistantConnection.servicePubkey = controlplaneMock.controlplaneConnection.servicePubkey;
    encryptedControlplaneMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { session_id: 'assistant-action-session', status: 'executing', action_id: 'action-1', decision: 'approve' },
      requestEventId: 'approval-request-1'
    });

    await store.publishAssistantActionDecision({
      sessionId: 'assistant-action-session',
      actionId: 'action-1',
      decision: 'approve',
      reason: 'safe rollback window'
    });

    expect(encryptedControlplaneMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'assistant/approval',
      payload: {
        session_id: 'assistant-action-session',
        action_id: 'action-1',
        decision: 'approve',
        reason: 'safe rollback window'
      },
      tags: [['session', 'assistant-action-session'], ['action', 'action-1'], ['decision', 'approve']],
      signal: undefined,
      timeoutMs: 180000
    });
    const session = store.assistantSessions.find((item) => item.sessionId === 'assistant-action-session');
    expect(session.transcript).toEqual(expect.arrayContaining([
      expect.objectContaining({ type: 'approval', actionId: 'action-1', decision: 'approve', message: 'safe rollback window' })
    ]));
  });

  it('rejects cancel decisions for action-level assistant approvals', async () => {
    await expect(store.publishAssistantApproval({
      sessionId: 'assistant-action-session',
      actionId: 'action-1',
      decision: 'cancel'
    })).rejects.toThrow('action decision must be approve or reject');
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
      status: 'failed',
      summary: 'assistant planning failed',
      error: 'ContextVM request timed out after 120000ms waiting for result'
    }));
  });

  it('restores cached assistant sessions and transcript across reloads', async () => {
    const operator = authMock.authState.pubkey;
    const service = controlplaneMock.controlplaneConnection.servicePubkey;
    const sessionId = 'assistant-cached-session';

    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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

    const cacheKey = `bahia_assistant_transcript:bahia_assistant_transcript_v1:${operator}:${service}`;
    expect(globalThis.localStorage.getItem(cacheKey)).toContain('Cached transcript survives reload');

    store.resetAssistantStore();
    nostrMock.subscribe.mockImplementationOnce((_filters, handlers) => {
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
