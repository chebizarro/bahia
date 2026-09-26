import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  ASSISTANT_KINDS,
  assertAssistantRequestAccepted,
  canonicalAssistantJson,
  classifyAssistantRequestError,
  computeAssistantBatchApprovalHash,
  describeAssistantRequestError,
  parseAssistantArgumentObjectText,
  parseAssistantSessionEvent
} from '../../../src/lib/nostr/assistant.js';
import { extractContextVMResult } from '../../../src/lib/nostr/encrypted-controlplane-utils.js';

const fixture = JSON.parse(readFileSync(resolve(process.cwd(), '../testdata/assistant/batch_approval_hash_vectors.json'), 'utf8'));

describe('assistant v2 batch approval hash contract', () => {
  for (const vector of fixture.vectors) {
    it(`matches exact RFC 8785 bytes and SHA-256 for ${vector.name}`, async () => {
      const input = vector.input;
      const result = await computeAssistantBatchApprovalHash({
        sessionId: input.session_id, runId: input.run_id, proposalId: input.proposal_id,
        revision: input.revision, scope: input.scope, plan: input.plan
      });
      expect(result.canonical).toBe(vector.canonical);
      expect(result.hash).toBe(vector.sha256);
    });
  }

  it('rejects private scope arguments and malformed argument JSON rather than silently hashing a different plan', async () => {
    const baseline = fixture.vectors[0].input;
    await expect(computeAssistantBatchApprovalHash({ sessionId: baseline.session_id, runId: baseline.run_id,
      proposalId: baseline.proposal_id, revision: 1,
      scope: { allowed_tools: null, arguments: { secret: 'not-public' } }, plan: baseline.plan })).rejects.toThrow('Public command-scope commitment');
    expect(() => parseAssistantArgumentObjectText('{"nested":{"x":1,"x":2}}')).toThrow('Duplicate JSON key');
    expect(() => parseAssistantArgumentObjectText('{"x":')).toThrow();
    expect(() => parseAssistantArgumentObjectText('[1]')).toThrow('JSON object');
    expect(() => canonicalAssistantJson({ bad: '\uD800' })).toThrow('Invalid Unicode');
  });
});

describe('assistant session version parsing', () => {
  function projection(schema, content) {
    return { id: 'e'.repeat(64), kind: ASSISTANT_KINDS.SESSION, pubkey: 'f'.repeat(64), created_at: 10,
      tags: [['schema', schema], ['session', 'session-1'], ['d', `${schema}:session-1`]], content: JSON.stringify(content) };
  }
  it('reads v1 as history without v2 approval authority', () => {
    const parsed = parseAssistantSessionEvent(projection('bahia.assistant-session.v1', {
      state: 'awaiting_approval', last_plan_hash: 'legacy', current_plan: { steps: [] }
    }));
    expect(parsed.executionVersion).toBe(1);
    expect(parsed.proposal).toBeNull();
    expect(parsed.pendingApprovals).toEqual([]);
  });
  it('reads public v2 commitment and refuses private scope arguments', () => {
    const content = { session_id: 'session-1', execution_version: 2, workflow: 'batch', current_run_id: 'run-1',
      execution_revision: 3, phase: 'awaiting_approval', scope: { allowed_tools: [], arguments_digest: 'a'.repeat(64) },
      proposal: { proposal_id: 'p1', revision: 1, hash: 'hash', plan: { steps: [] } }, pending_approvals: ['p1'] };
    const parsed = parseAssistantSessionEvent(projection('bahia.assistant-session.v2', content));
    expect(parsed).toMatchObject({ executionVersion: 2, workflow: 'batch', currentRunId: 'run-1',
      scope: { allowed_tools: [], arguments_digest: 'a'.repeat(64) }, pendingApprovals: ['p1'] });
    expect(parseAssistantSessionEvent(projection('bahia.assistant-session.v2', {
      ...content, scope: { allowed_tools: null, arguments: { private: true } }
    }))).toBeNull();
  });
});

describe('assistant request outcome classification', () => {
  function thrown(fn) {
    try { fn(); } catch (err) { return err; }
    throw new Error('expected throw');
  }
  const rejected = (result) => thrown(() => assertAssistantRequestAccepted({ result }));

  it('keeps JSON-RPC error codes so a service verdict is distinguishable from a transport failure', () => {
    const error = thrown(() => extractContextVMResult({ jsonrpc: '2.0', id: 'req-1',
      error: { code: -32000, message: 'proposal_changed_requires_review', data: { reason: 'proposal_changed_requires_review' } } }, 'evt', 'req-1'));
    expect(error).toMatchObject({ code: -32000, data: { reason: 'proposal_changed_requires_review' } });
    expect(classifyAssistantRequestError(error).kind).toBe('stale');
    const denied = thrown(() => extractContextVMResult({ jsonrpc: '2.0', id: 'req-1', error: { code: -32001, message: 'permission denied' } }, 'evt', 'req-1'));
    expect(classifyAssistantRequestError(denied).kind).toBe('rejected');
  });

  it('turns failed-status handler results into request rejections and passes accepted results through', () => {
    for (const step of ['stale_approval', 'plan_hash_mismatch', 'invalid_state', 'approval_contract_upgrade_required', 'proposal_changed_requires_review']) {
      expect(classifyAssistantRequestError(rejected({ status: 'failed', step, error: 'x' })).kind).toBe('stale');
    }
    for (const step of ['run_in_progress', 'unauthorized_participant', 'validation_error']) {
      expect(classifyAssistantRequestError(rejected({ status: 'failed', step, error: 'x' })).kind).toBe('rejected');
    }
    for (const status of ['accepted', 'completed', 'awaiting_approval', 'executing', 'blocked', undefined]) {
      const response = { result: { status } };
      expect(assertAssistantRequestAccepted(response)).toBe(response);
    }
  });

  it('reports interruptions as outcome unknown and never as failure', () => {
    for (const message of ['ContextVM request timed out after 120000ms waiting for result', 'Encrypted controlplane disconnected',
      'ContextVM result subscription auth closure: wss://relay', 'ContextVM request aborted']) {
      const described = describeAssistantRequestError(new Error(message));
      expect(described).toMatchObject({ kind: 'unknown', message: `Request outcome unknown / reconnecting: ${message}` });
      expect(described.message).not.toMatch(/\bfailed\b/i);
    }
  });
});
