import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';
import { FLEET_INTENT_ORG_ID } from '../../src/lib/nostr/intent-client.svelte.js';
import {
  deploymentIntentRequest, runtimeIntentRequest, llmLifecycleIntentRequest,
  backupRestoreApprovalIntentRequest, dnsIntentRequest, mlIntentRequest, workerIntentRequest
} from '../../src/lib/nostr/domain-intents.js';

const fixture = name => JSON.parse(readFileSync(join(process.cwd(), 'tests', 'fixtures', name), 'utf8'));
const d69 = fixture('deployment-intents.json');
const d70 = fixture('d70-intent-content.json').intents;
const tag = (event, name) => event.tags.find(item => item[0] === name)?.[1];
const worker = content => ({ pubkey: content.worker_pubkey, labels: content.labels,
  scheduling_state: content.scheduling_state });

function fromD69(domain, op, content, orgId, intentId) {
  if (domain === 'deployment') return deploymentIntentRequest(op, content, orgId, intentId);
  if (domain === 'runtime') return runtimeIntentRequest(op, content, orgId, intentId);
  if (domain === 'llm') return llmLifecycleIntentRequest(op, content, orgId, intentId);
  if (domain === 'backup') return backupRestoreApprovalIntentRequest(content.restore_id, content.decision, content.message, orgId, intentId);
  throw new Error(`Unexpected D69 domain ${domain}`);
}

function fromD70({ domain, op, content, coordinate }) {
  const current = op.endsWith('-update') || op.endsWith('-delete')
    ? { ...content, updated_at: content.expected_updated_at,
      ...(domain === 'ml' && op === 'model-delete' ? { slug: coordinate.slice('model:'.length) } : {}) } : null;
  if (domain === 'dns') return dnsIntentRequest(op, content, FLEET_INTENT_ORG_ID, current);
  if (domain === 'ml') return mlIntentRequest(op, content, FLEET_INTENT_ORG_ID,
    current);
  if (domain === 'worker') return workerIntentRequest(op, worker(content), FLEET_INTENT_ORG_ID,
    { reason: content.reason, labels: content.labels, cleanupMode: content.cleanup_mode });
  throw new Error(`Unexpected D70 domain ${domain}`);
}

describe('D69 deployment, runtime, LLM and backup wire fixtures', () => {
  for (const expected of d69) {
    const domain = tag(expected, 'domain');
    const op = tag(expected, 'op');
    it(`${domain}/${op}: signed request content and required tags equal Go fixture`, () => {
      const content = JSON.parse(expected.content);
      const request = fromD69(domain, op, content, tag(expected, 'org'), tag(expected, 'intent_id'));
      const actual = buildIntentEvent({ ...request, createdAt: expected.created_at });
      expect(request.coordinate).toBe(tag(expected, 'd'));
      expect(JSON.parse(actual.content)).toEqual(content);
      for (const expectedTag of expected.tags) expect(actual.tags).toContainEqual(expectedTag);
      expect(actual.tags).toContainEqual(['t', domain]);
    });
  }
});

describe('D70 DNS, ML and worker handler content fixtures', () => {
  const knownUnmigrated = [];
  const implemented = d70.filter(expected => {
    try {
      fromD70(expected);
      return true;
    } catch (error) {
      if (/^Unsupported (DNS|ML|worker) intent op: /.test(error.message)) return false;
      throw error;
    }
  });

  it('tracks the exact daemon fixture operations not yet implemented by web intent builders', () => {
    const implementedOps = new Set(implemented.map(({ domain, op }) => `${domain}/${op}`));
    expect(d70.filter(({ domain, op }) => !implementedOps.has(`${domain}/${op}`))
      .map(({ domain, op }) => `${domain}/${op}`).sort()).toEqual(knownUnmigrated);
  });

  for (const expected of implemented) {
    it(`${expected.domain}/${expected.op}: signed request equals Go fixture`, () => {
      const request = fromD70(expected);
      const actual = buildIntentEvent({ ...request, createdAt: 1790985600 });
      expect(request.coordinate).toBe(expected.coordinate);
      expect(actual.content).toBe(JSON.stringify(expected.content));
      expect(JSON.parse(actual.content)).toEqual(expected.content);
      expect(actual.tags).toContainEqual(['domain', expected.domain]);
      expect(actual.tags).toContainEqual(['op', expected.op]);
      expect(actual.tags).toContainEqual(['d', expected.coordinate]);
      expect(actual.tags).toContainEqual(['org', FLEET_INTENT_ORG_ID]);
    });
  }

  it('allows identity-changing ML updates and uses the old model identity for deletes', () => {
    const current = { id: '00000000-0000-4000-8000-000000000003', slug: 'sample', updated_at: '2026-10-03T00:00:00Z' };
    expect(mlIntentRequest('model-update', { ...current, slug: 'different' }, FLEET_INTENT_ORG_ID, current).coordinate)
      .toBe('model:different');
    expect(mlIntentRequest('model-delete', { id: current.id }, FLEET_INTENT_ORG_ID, current))
      .toMatchObject({ coordinate: 'model:sample', content: { id: current.id, expected_updated_at: current.updated_at } });
  });
});
