import { test, expect } from '@playwright/test';
import { attachRuntimeErrorGuards } from './helpers-console.js';
import { installE2EMocks, TEST_PUBKEY } from './helpers.js';

const PUBLIC_RELAY = 'ws://relay.test.local';
const SERVICE_PUBKEY = '68680737c76dabb801cb2204f57dbe4e4579e4f710cd67dc1b4227592c81e9b5';

const systemInfo = {
  nostr: {
    browser_relays: [PUBLIC_RELAY],
    service_relays: [PUBLIC_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    encrypted_nostr_requests: true,
    legacy_sse: false
  }
};

async function installAssistantAgenticHarness(page) {
  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    systemInfo,
    nostrEvents: []
  });

  await page.addInitScript(({ servicePubkey, operatorPubkey }) => {
    const KIND_CONTEXTVM = 25910;
    const KIND_SESSION = 30900;
    const KIND_STATUS = 30315;
    const KIND_TRANSCRIPT = 30316;

    function decodeRequest(event) {
      const content = String(event.content || '');
      if (!content.startsWith('mock-nip44:') && !content.startsWith('enc44:') && event.kind !== KIND_CONTEXTVM) return null;
      const plaintext = content.startsWith('mock-nip44:')
        ? decodeURIComponent(escape(atob(content.replace(/^mock-nip44:/, ''))))
        : content.replace(/^enc44:/, '');
      const envelope = JSON.parse(plaintext || '{}');
      const params = { ...(envelope.params || envelope.payload || {}) };
      delete params._meta;
      return { envelope, operation: envelope.method || envelope.operation || '', payload: params };
    }

    function latestAssistantSessionId() {
      for (const [key, value] of Object.entries(localStorage)) {
        if (!key.startsWith('bahia_assistant_transcript:')) continue;
        try {
          const cached = JSON.parse(value || '{}');
          if (cached?.activeSessionId) return cached.activeSessionId;
          const sessionId = cached?.sessions?.[0]?.sessionId;
          if (sessionId) return sessionId;
        } catch {}
      }
      return `assistant-e2e-${Date.now()}`;
    }

    function requestFromGiftWrap(event) {
      const sessionId = latestAssistantSessionId();
      const prompt = window.__BAHIA_E2E_ASSISTANT_NEXT_PROMPT || '';
      if (prompt) {
        window.__BAHIA_E2E_ASSISTANT_NEXT_PROMPT = '';
        return {
          envelope: { id: event.id, method: 'assistant/prompt' },
          operation: 'assistant/prompt',
          payload: { session_id: sessionId, turn_id: `turn-${Date.now()}`, prompt }
        };
      }
      const decision = window.__BAHIA_E2E_NEXT_ACTION_DECISION || { decision: 'approve', reason: '' };
      window.__BAHIA_E2E_NEXT_ACTION_DECISION = null;
      return {
        envelope: { id: event.id, method: 'assistant/approval' },
        operation: 'assistant/approval',
        payload: { session_id: sessionId, action_id: 'action-rollback-1', decision: decision.decision || 'approve', reason: decision.reason || '' }
      };
    }

    function encodeResponse(requestEvent, envelope, result) {
      const payload = requestEvent.kind === 1059
        ? { request_event_id: requestEvent.id, ...result }
        : { jsonrpc: '2.0', id: envelope.id || requestEvent.id, result };
      const plaintext = JSON.stringify(payload);
      return `mock-nip44:${btoa(unescape(encodeURIComponent(plaintext)))}`;
    }

    function push(event) {
      window.__bahiaPushNostrEvent?.(event);
    }

    function assistantEvent({ id, kind, sessionId, status = '', tags = [], content = {} }) {
      const now = Math.floor(Date.now() / 1000);
      return {
        id,
        kind,
        pubkey: servicePubkey,
        created_at: now,
        tags: [
          ['domain', 'assistant'],
          ['schema', kind === KIND_SESSION ? 'bahia.assistant-session.v1' : kind === KIND_STATUS ? 'bahia.assistant-status.v1' : 'bahia.assistant-transcript.v1'],
          ['session', sessionId],
          ['p', operatorPubkey, '', 'operator'],
          ...(status ? [['status', status]] : []),
          ...tags
        ],
        content: JSON.stringify(content),
        sig: '0'.repeat(128)
      };
    }

    function publishSession(sessionId, state) {
      push(assistantEvent({
        id: `session-${sessionId}-${state}-${Date.now()}`,
        kind: KIND_SESSION,
        sessionId,
        status: state,
        tags: [['d', `bahia.assistant-session.v1:${sessionId}`], ['agent', 'bahia-assistant']],
        content: {
          schema: 'bahia.assistant-session.v1',
          session_id: sessionId,
          state,
          operator_pubkey: operatorPubkey,
          participants: [operatorPubkey],
          assistant_id: 'bahia-assistant',
          assistant_pubkey: servicePubkey,
          metadata: { agent_loop: { state } }
        }
      }));
    }

    function publishStatus(sessionId, status, fields) {
      push(assistantEvent({
        id: `status-${sessionId}-${fields.phase || status}-${Date.now()}-${Math.random()}`,
        kind: KIND_STATUS,
        sessionId,
        status,
        tags: [['d', `bahia.assistant-status.v1:${sessionId}:${fields.phase || status}:${Date.now()}`], ['agent', 'bahia-assistant']],
        content: {
          schema: 'bahia.assistant-status.v1',
          session_id: sessionId,
          status,
          ...fields
        }
      }));
    }

    function publishTranscript(sessionId, turnId, text, phase = 'assistant_model_response') {
      push(assistantEvent({
        id: `transcript-${sessionId}-${Date.now()}-${Math.random()}`,
        kind: KIND_TRANSCRIPT,
        sessionId,
        tags: [['d', `bahia.assistant-transcript.v1:${sessionId}:00000000000000000001:${turnId}`], ['turn', turnId], ['role', 'assistant'], ['seq', '1']],
        content: {
          schema: 'bahia.assistant-transcript.v1',
          session_id: sessionId,
          turn_id: turnId,
          seq: 1,
          message: { role: 'assistant', content: [{ type: 'text', text }] },
          metadata: { phase }
        }
      }));
    }

    function publishContextVMResult(requestEvent, envelope, result) {
      push({
        id: `assistant-result-${requestEvent.id}-${Date.now()}`,
        kind: requestEvent.kind,
        pubkey: servicePubkey,
        created_at: Math.floor(Date.now() / 1000),
        tags: [['e', requestEvent.id], ['p', operatorPubkey], ['encrypted', 'contextvm-jsonrpc-v1'], ['method', envelope.method || '']],
        content: encodeResponse(requestEvent, envelope, result),
        sig: '0'.repeat(128)
      });
    }

    function completeReadOnly(sessionId, turnId) {
      publishSession(sessionId, 'executing');
      publishStatus(sessionId, 'planning', { phase: 'loop_started', message: 'Agentic loop started' });
      publishStatus(sessionId, 'executing', { phase: 'model_requested', message: 'Reading DNS state' });
      publishTranscript(sessionId, turnId, 'Read-only DNS answer: DNS records are healthy.');
      publishStatus(sessionId, 'completed', { phase: 'loop_completed', message: 'Read-only DNS answer: DNS records are healthy.' });
      publishSession(sessionId, 'completed');
      return { session_id: sessionId, status: 'completed', summary: 'Read-only DNS answer: DNS records are healthy.', completed: true };
    }

    function completeLowRisk(sessionId) {
      publishSession(sessionId, 'executing');
      publishStatus(sessionId, 'executing', {
        phase: 'tool_call_requested',
        message: 'Preparing low-risk audited DNS mutation',
        tool_call_id: 'tool-call-low-risk',
        tool_name: 'bahia_assistant_dns_zone_create',
        args_preview: { zone: 'preview.example.com' }
      });
      publishStatus(sessionId, 'executing', {
        phase: 'tool_submitted',
        message: 'Low-risk audited mutation submitted; awaiting Nostr result',
        tool_call_id: 'tool-call-low-risk',
        tool_name: 'bahia_assistant_dns_zone_create',
        downstream_request: 'downstream-low-risk-1'
      });
      publishStatus(sessionId, 'executing', {
        phase: 'tool_observed',
        message: 'Nostr result observed for low-risk mutation',
        tool_call_id: 'tool-call-low-risk',
        tool_name: 'bahia_assistant_dns_zone_create',
        downstream_request: 'downstream-low-risk-1'
      });
      publishStatus(sessionId, 'completed', { phase: 'loop_completed', message: 'Low-risk audited mutation completed.' });
      publishSession(sessionId, 'completed');
      return { session_id: sessionId, status: 'completed', summary: 'Low-risk audited mutation completed.', completed: true };
    }

    function requireRollbackApproval(sessionId) {
      publishSession(sessionId, 'awaiting_approval');
      publishStatus(sessionId, 'awaiting_approval', {
        phase: 'approval_required',
        message: 'High-risk rollback requires operator approval',
        approval_prompt: 'High-risk rollback requires operator approval',
        action_id: 'action-rollback-1',
        tool_call_id: 'tool-call-rollback-1',
        tool_name: 'bahia_assistant_llm_rollback',
        args_preview: { route_id: 'llm-route-prod', environment_id: 'prod' },
        permission: { risk: 'high', decision: 'ask' }
      });
      return { session_id: sessionId, status: 'awaiting_approval', suspended: true, action_id: 'action-rollback-1', summary: 'High-risk rollback requires operator approval' };
    }

    function blockedThenRecovered(sessionId) {
      publishSession(sessionId, 'executing');
      publishStatus(sessionId, 'executing', {
        phase: 'tool_submitted',
        message: 'Waiting for downstream Nostr result',
        tool_call_id: 'tool-call-recovery-1',
        tool_name: 'bahia_assistant_dns_zone_create',
        downstream_request: 'downstream-recovery-1'
      });
      publishStatus(sessionId, 'blocked', {
        phase: 'tool_observation_blocked',
        message: 'relay closed before terminal result',
        error: 'relay closed before terminal result',
        tool_call_id: 'tool-call-recovery-1',
        tool_name: 'bahia_assistant_dns_zone_create',
        downstream_request: 'downstream-recovery-1'
      });
      publishSession(sessionId, 'blocked');
      setTimeout(() => {
        publishSession(sessionId, 'executing');
        publishStatus(sessionId, 'executing', {
          phase: 'tool_observed',
          message: 'Recovery resumed and observed the downstream result',
          tool_call_id: 'tool-call-recovery-1',
          tool_name: 'bahia_assistant_dns_zone_create',
          downstream_request: 'downstream-recovery-1'
        });
        publishStatus(sessionId, 'completed', { phase: 'loop_completed', message: 'Recovery resumed and completed.' });
        publishSession(sessionId, 'completed');
      }, 100);
      return { session_id: sessionId, status: 'blocked', phase: 'tool_observation_blocked', summary: 'relay closed before terminal result' };
    }

    function handlePrompt(payload) {
      const sessionId = payload.session_id;
      const turnId = payload.turn_id || 'turn-1';
      const prompt = String(payload.prompt || '').toLowerCase();
      if (prompt.includes('low-risk')) return completeLowRisk(sessionId);
      if (prompt.includes('rollback')) return requireRollbackApproval(sessionId);
      if (prompt.includes('relay close')) return blockedThenRecovered(sessionId);
      return completeReadOnly(sessionId, turnId);
    }

    function handleApproval(payload) {
      window.__BAHIA_E2E_ASSISTANT_APPROVALS.push(payload);
      const sessionId = payload.session_id;
      publishSession(sessionId, 'executing');
      publishStatus(sessionId, 'executing', {
        phase: 'tool_submitted',
        message: 'Approved rollback action submitted',
        action_id: payload.action_id,
        tool_call_id: 'tool-call-rollback-1',
        tool_name: 'bahia_assistant_llm_rollback',
        downstream_request: 'downstream-rollback-1'
      });
      publishStatus(sessionId, 'executing', {
        phase: 'tool_observed',
        message: 'Approved rollback Nostr result observed',
        action_id: payload.action_id,
        tool_call_id: 'tool-call-rollback-1',
        tool_name: 'bahia_assistant_llm_rollback',
        downstream_request: 'downstream-rollback-1'
      });
      publishStatus(sessionId, 'completed', { phase: 'loop_completed', message: 'High-risk rollback executed after approval.', action_id: payload.action_id });
      publishSession(sessionId, 'completed');
      return { session_id: sessionId, status: 'completed', action_id: payload.action_id, decision: payload.decision, summary: 'High-risk rollback executed after approval.' };
    }

    window.__BAHIA_E2E_ASSISTANT_APPROVALS = [];
    window.__BAHIA_E2E_ASSISTANT_PROMPTS = [];
    const originalSend = window.WebSocket.prototype.send;
    window.WebSocket.prototype.send = function assistantAgenticSend(data) {
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }
      if (Array.isArray(message) && message[0] === 'EVENT' && (message[1]?.kind === KIND_CONTEXTVM || message[1]?.kind === 1059)) {
        const requestEvent = message[1];
        const decoded = decodeRequest(requestEvent) || requestFromGiftWrap(requestEvent);
        if (decoded.operation === 'assistant/prompt' || decoded.operation === 'assistant/approval') {
          const sent = originalSend.call(this, data);
          const result = decoded.operation === 'assistant/prompt'
            ? handlePrompt(decoded.payload)
            : handleApproval(decoded.payload);
          if (decoded.operation === 'assistant/prompt') window.__BAHIA_E2E_ASSISTANT_PROMPTS.push(decoded.payload);
          setTimeout(() => publishContextVMResult(requestEvent, decoded.envelope, result), 0);
          return sent;
        }
      }
      return originalSend.call(this, data);
    };
  }, { servicePubkey: SERVICE_PUBKEY, operatorPubkey: TEST_PUBKEY });
}

async function openAssistant(page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Open assistant chat' }).click();
  const panel = page.locator('[role="dialog"]');
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('live');
  return panel;
}

async function submitAssistantPrompt(page, prompt) {
  await page.evaluate((value) => { window.__BAHIA_E2E_ASSISTANT_NEXT_PROMPT = value; }, prompt);
  const textarea = page.getByPlaceholder('Ask the Bahia assistant…');
  await textarea.fill(prompt);
  await textarea.press('Enter');
}

test.describe('assistant agentic frontend enablement', () => {
  test.beforeEach(async ({ page }) => {
    await installAssistantAgenticHarness(page);
  });

  test('read-only question completes without approval', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);
    const panel = await openAssistant(page);

    await submitAssistantPrompt(page, 'read-only DNS question');

    await expect(panel).toContainText('loop_completed');
    await expect(panel).toContainText('Read-only DNS answer: DNS records are healthy.');
    await expect(panel).not.toContainText('Action approval required');
    await assertNoRuntimeErrors();
  });

  test('low-risk mutation auto-runs in audited mode and awaits the Nostr result', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);
    const panel = await openAssistant(page);

    await submitAssistantPrompt(page, 'low-risk audited DNS mutation');

    await expect(panel).toContainText('tool_submitted');
    await expect(panel).toContainText('downstream-low-risk-1');
    await expect(panel).toContainText('tool_observed');
    await expect(panel).toContainText('Low-risk audited mutation completed.');
    await expect(panel).not.toContainText('Action approval required');
    await assertNoRuntimeErrors();
  });

  test('high-risk rollback surfaces approval_required and executes only after approval', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);
    const panel = await openAssistant(page);

    await submitAssistantPrompt(page, 'high-risk rollback production route');

    await expect(panel).toContainText('approval_required');
    await expect(panel).toContainText('High-risk rollback requires operator approval');
    await expect(panel).toContainText('bahia_assistant_llm_rollback');
    await expect(panel).not.toContainText('High-risk rollback executed after approval.');

    await page.evaluate(() => {
      window.__BAHIA_E2E_NEXT_ACTION_DECISION = { decision: 'approve', reason: 'approved in E2E' };
      document.querySelector('button.approve')?.click();
    });

    await expect(panel).toContainText('High-risk rollback executed after approval.');
    const approvals = await page.evaluate(() => window.__BAHIA_E2E_ASSISTANT_APPROVALS);
    expect(approvals).toEqual([expect.objectContaining({
      session_id: expect.any(String),
      action_id: 'action-rollback-1',
      decision: 'approve',
      reason: 'approved in E2E'
    })]);
    await assertNoRuntimeErrors();
  });

  test('relay close blocks the turn and recovery resumes from later relay events', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);
    const panel = await openAssistant(page);

    await submitAssistantPrompt(page, 'relay close during async tool observation');

    await expect(panel).toContainText('tool_observation_blocked');
    await expect(panel).toContainText('relay closed before terminal result');
    await expect(panel).toContainText('Recovery resumed and observed the downstream result');
    await expect(panel).toContainText('Recovery resumed and completed.');
    await assertNoRuntimeErrors();
  });
});
