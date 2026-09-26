import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  ASSISTANT_KINDS,
  canonicalAssistantJson,
  computeAssistantBatchApprovalHash,
  parseAssistantArgumentObjectText,
  parseAssistantSessionEvent
} from '../../../src/lib/nostr/assistant.js';

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
