import { describe, expect, it } from 'vitest';
import fixture from '../fixtures/d70-intent-content.json';

describe('Go DNS and ML intent content fixtures', () => {
  it('commits one full-content shape for every D72 mutation', () => {
    expect(fixture.schema).toBe('bahia.intent-fixtures.d72.v1');
    expect(fixture.intents).toHaveLength(31);
    const byOp = new Map(fixture.intents.map(row => [`${row.domain}/${row.op}`, row]));
    expect(byOp.size).toBe(31);
    for (const op of ['zone-update', 'zone-delete', 'endpoint-create', 'endpoint-update', 'endpoint-delete',
      'backend-create', 'backend-update', 'backend-delete', 'policy-update', 'policy-delete']) {
      const row = byOp.get(`dns/${op}`);
      expect(row).toBeDefined();
      expect(row.coordinate).toMatch(/^(zone:|endpoint:|dnsbackend:|dnspolicy:)/);
      if (op.endsWith('update') || op.endsWith('delete')) {
        expect(row.content.expected_updated_at).toMatch(/^2026-10-03T00:00:00Z$/);
      }
    }
    for (const op of ['model-delete', 'version-delete', 'endpoint-delete']) {
      const row = byOp.get(`ml/${op}`);
      expect(row.content.id).toMatch(/^[0-9a-f-]{36}$/);
      expect(row.content.expected_updated_at).toBe('2026-10-03T00:00:00Z');
    }
    expect(byOp.get('ml/model-update').content.slug).toBe('sample-renamed');
    expect(byOp.get('ml/version-update').content.version).toBe('v2');
    expect(byOp.get('ml/endpoint-update').content.name).toBe('inference-renamed');
  });
});
