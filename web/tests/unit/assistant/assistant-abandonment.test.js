import { describe, it, expect, beforeEach, vi } from 'vitest';
import AssistantExecutionReconciliation from '../../../src/lib/components/assistant/AssistantExecutionReconciliation.svelte';
import {
  ASSISTANT_ABANDONMENT_ATTESTATION,
  ASSISTANT_REQUEST_ERROR_KINDS,
  buildAssistantAbandonmentRequest,
  classifyAssistantRequestError,
  publishAssistantAbandonment
} from '../../../src/lib/nostr/assistant.js';
import { renderComponent, textOf, tick } from '../utils/svelte-component-test';

const transport = vi.hoisted(() => ({ requestEncryptedResult: vi.fn() }));
vi.mock('../../../src/lib/nostr/encrypted-controlplane.js', () => transport);
vi.mock('../../../src/lib/stores/assistant.svelte.js', () => ({ publishAssistantReconciliation: vi.fn() }));

function uncertainSession(overrides = {}) {
  return { sessionId: 's1', currentRunId: 'r1', executionVersion: 2, authoritative: true, workflow: 'batch',
    phase: 'cancelling', uncertainEffects: 1, pendingApprovals: [], participants: [], ...overrides };
}

async function flush() {
  await tick();
  await Promise.resolve();
  await tick();
}

async function fill(target, selector, value) {
  const field = target.querySelector(selector);
  field.value = value;
  field.dispatchEvent(new Event('input', { bubbles: true }));
  await tick();
}

async function attest(target) {
  const box = target.querySelector('input[type="checkbox"]');
  box.checked = true;
  box.dispatchEvent(new Event('change', { bubbles: true }));
  await tick();
}

describe('assistant abandonment request', () => {
  it('builds an attested abandon resolution on the reconcile surface', () => {
    const built = buildAssistantAbandonmentRequest({ session: uncertainSession(), workId: ' r1:one ', reason: ' history pruned ', attested: true });
    expect(built).toEqual({
      operation: 'assistant/reconcile',
      payload: { contract_version: 2, session_id: 's1', run_id: 'r1', work_id: 'r1:one', resolution: 'abandon',
        reason: 'history pruned', attestation: ASSISTANT_ABANDONMENT_ATTESTATION },
      tags: [['session', 's1'], ['run', 'r1']]
    });
    expect(built.payload).not.toHaveProperty('request_event_id');
  });

  it('refuses locally without a reason, an attestation, a current run or uncertain work', () => {
    const cases = [
      [{ session: uncertainSession(), workId: 'w', reason: '  ', attested: true }, ASSISTANT_REQUEST_ERROR_KINDS.INVALID],
      [{ session: uncertainSession(), workId: 'w', reason: 'r', attested: false }, ASSISTANT_REQUEST_ERROR_KINDS.INVALID],
      [{ session: uncertainSession(), workId: 'w', reason: 'r', attested: 'yes' }, ASSISTANT_REQUEST_ERROR_KINDS.INVALID],
      [{ session: uncertainSession({ authoritative: false }), workId: 'w', reason: 'r', attested: true }, ASSISTANT_REQUEST_ERROR_KINDS.STALE],
      [{ session: uncertainSession({ uncertainEffects: 0 }), workId: 'w', reason: 'r', attested: true }, ASSISTANT_REQUEST_ERROR_KINDS.STALE]
    ];
    for (const [input, kind] of cases) {
      expect(() => buildAssistantAbandonmentRequest(input)).toThrow();
      try { buildAssistantAbandonmentRequest(input); } catch (err) { expect(err.assistantErrorKind).toBe(kind); }
    }
  });

  it('surfaces the service refusal code as a rejection, never as success', async () => {
    const request = vi.fn().mockResolvedValue({ result: { status: 'failed', step: 'abandonment_refused', error: 'work item is succeeded; only uncertain work can be abandoned' } });
    const err = await publishAssistantAbandonment({ request, session: uncertainSession(), workId: 'w', reason: 'r', attested: true }).catch((e) => e);
    expect(err.code).toBe('abandonment_refused');
    expect(classifyAssistantRequestError(err).kind).toBe(ASSISTANT_REQUEST_ERROR_KINDS.REJECTED);
    expect(request).toHaveBeenCalledTimes(1);
  });

  it('sends nothing when the local checks fail', async () => {
    const request = vi.fn();
    await expect(publishAssistantAbandonment({ request, session: uncertainSession(), workId: 'w', reason: 'r', attested: false })).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
  });
});

describe('AssistantExecutionReconciliation abandonment', () => {
  beforeEach(() => {
    transport.requestEncryptedResult.mockReset();
  });

  it('enables abandonment only with a work ID, a reason and the attestation, and sends the attested request', async () => {
    transport.requestEncryptedResult.mockResolvedValue({ result: { status: 'accepted', step: 'abandoned' } });
    const target = renderComponent(AssistantExecutionReconciliation, { session: uncertainSession() });
    const button = () => target.querySelector('button.abandon');
    expect(button().disabled).toBe(true);
    await fill(target, 'input[aria-label="Uncertain work ID to abandon"]', 'r1:one');
    await fill(target, 'textarea', 'downstream history pruned');
    expect(button().disabled).toBe(true);
    await attest(target);
    expect(button().disabled).toBe(false);

    target.querySelector('form[aria-label="Abandon uncertain work"]').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(transport.requestEncryptedResult).toHaveBeenCalledTimes(1);
    const sent = transport.requestEncryptedResult.mock.calls[0][0];
    expect(sent.operation).toBe('assistant/reconcile');
    expect(sent.payload).toMatchObject({ session_id: 's1', run_id: 'r1', work_id: 'r1:one', resolution: 'abandon',
      reason: 'downstream history pruned', attestation: ASSISTANT_ABANDONMENT_ATTESTATION });
    expect(textOf(target)).toContain('outcome of this work remains unknown');
    expect(textOf(target)).not.toMatch(/complete[d]? successfully|marked complete/i);
  });

  it('shows a service refusal as a rejection', async () => {
    transport.requestEncryptedResult.mockResolvedValue({ result: { status: 'failed', step: 'abandonment_refused', error: 'only uncertain work can be abandoned' } });
    const target = renderComponent(AssistantExecutionReconciliation, { session: uncertainSession() });
    await fill(target, 'input[aria-label="Uncertain work ID to abandon"]', 'r1:one');
    await fill(target, 'textarea', 'tidy up');
    await attest(target);
    target.querySelector('form[aria-label="Abandon uncertain work"]').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await flush();
    expect(textOf(target)).toContain('Abandonment rejected by the assistant service');
    expect(textOf(target)).toContain('abandonment_refused');
  });
});
