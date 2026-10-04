import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';
import { artifactRegisterIntent, observedArtifactImportIntent, adoptionImportIntent,
  dnsDriftRemediateIntent, deploymentPreviewIntent, deploymentRouteAttachIntent,
  policyEvaluateIntent } from '../../src/lib/nostr/last-ops-intents.js';

const orgId = '018f6a60-0000-7000-8000-000000000001';
const fixture = JSON.parse(readFileSync(join(process.cwd(), 'tests/fixtures/d76-intent-content.json'), 'utf8'));
const builders = [artifactRegisterIntent, observedArtifactImportIntent, adoptionImportIntent,
  dnsDriftRemediateIntent, deploymentPreviewIntent, deploymentRouteAttachIntent];

describe('last web intent operations', () => {
  for (const [index, shape] of fixture.intents.entries()) {
    it(`${shape.domain}/${shape.op} matches the Go-generated D76 fixture`, () => {
      const { intent_id, ...input } = shape.content;
      const request = builders[index](input, orgId, intent_id);
      expect(request).toEqual({ domain: shape.domain, op: shape.op, coordinate: shape.coordinate,
        orgId, intentId: intent_id, content: shape.content });
      const event = buildIntentEvent({ ...request, pubkey: 'a'.repeat(64), createdAt: 100 });
      expect(JSON.parse(event.content)).toEqual(shape.content);
      expect(event.tags).toContainEqual(['op', shape.op]);
      expect(event.tags).toContainEqual(['d', shape.coordinate]);
    });
  }

  it('evaluates policy at the D77 coordinate with only bounded request fields', () => {
    const artifact = '018f6a60-0000-7000-8000-000000000005';
    const environment = '018f6a60-0000-7000-8000-000000000003';
    const intentId = '018f6a60-0000-7000-8000-000000000012';
    const request = policyEvaluateIntent({ artifact_id: artifact, environment_id: environment,
      service_id: 'ignored' }, orgId, intentId);
    expect(request).toEqual({ domain: 'policy', op: 'evaluate',
      coordinate: `evaluation:${artifact}:${environment}`, orgId, intentId,
      content: { artifact_id: artifact, environment_id: environment, intent_id: intentId } });
    expect(buildIntentEvent({ ...request, pubkey: 'a'.repeat(64), createdAt: 100 }).tags).toContainEqual(['op', 'evaluate']);
  });
});
