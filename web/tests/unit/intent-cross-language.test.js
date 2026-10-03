import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';

const fixtureDir = join(process.cwd(), 'tests', 'fixtures');
const orgId = '3b45458b-2724-4dda-9fc6-66f12249660d';
const intentId = '018f1fae-7b91-7bea-81d6-0669758de945';

describe('Go/web intent fixture', () => {
  it('pins byte-identical unsigned tags and content on both sides', () => {
    const event = buildIntentEvent({ domain: 'service', op: 'update', coordinate: 'service:record-1',
      orgId, intentId, createdAt: 1727740800, currentRecord: { content: '{"updated_at":42}' },
      content: { id: 'record-1', name: 'api', org_id: orgId, config: { a: 2, z: 1 } } });
    const go = JSON.parse(readFileSync(join(fixtureDir, 'intent-go.json'), 'utf8'));
    expect(event).toEqual(go);
    const webPath = join(fixtureDir, 'intent-web.json');
    const bytes = `${JSON.stringify(event, null, 2)}\n`;
    if (process.env.BAHIA_REGEN_FIXTURE === '1') writeFileSync(webPath, bytes);
    expect(readFileSync(webPath, 'utf8')).toBe(bytes);
  });
});
