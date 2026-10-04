import { describe, expect, it } from 'vitest';
import d79 from '../fixtures/d79-intent-content.json';
import d80 from '../fixtures/d80-intent-content.json';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';
import {
  adoptionScanIntent, artifactBuildResultIntent, artifactSignatureVerifyIntent, buildRequestIntent,
  environmentWorkerPolicyIntent, mlOperationIntent, mlPinIntent, notificationChannelTestIntent,
  relayPolicyIntent, sbomIntent, securityScanIntent, toolApprovalResponseIntent
} from '../../src/lib/nostr/final-ops-intents.js';

const ORG = '018f6a60-0000-7000-8000-000000000001';
const SERVICE = '018f6a60-0000-7000-8000-000000000002';
const ENV = '018f6a60-0000-7000-8000-000000000003';
const ARTIFACT = '018f6a60-0000-7000-8000-000000000004';
const ENDPOINT = '018f6a60-0000-7000-8000-000000000005';
const CHANNEL = '018f6a60-0000-7000-8000-000000000006';
const D80_ID = '018f6a60-0000-7000-8000-000000000007';
const WORKER = 'a'.repeat(64);

function d79Request(row) {
  const id = row.idempotency_key;
  switch (`${row.domain}/${row.op}`) {
    case 'ml/model-import': case 'ml/recipe-apply': case 'ml/recipe-run':
    case 'ml/inference-deploy': case 'ml/inference-approval': case 'ml/inference-rollback':
      return mlOperationIntent(row.op, row.content, ORG, id);
    case 'tool/approval-response': return toolApprovalResponseIntent(row.content, ORG, id);
    case 'build/request': return buildRequestIntent(row.content, ORG, id);
    case 'adoption/scan': return adoptionScanIntent(row.content, ORG, id);
    default: throw new Error(`Missing D79 case ${row.domain}/${row.op}`);
  }
}

function d80Request(row) {
  const content = row.content;
  switch (`${row.domain}/${row.op}`) {
    case 'security/scan-run': return securityScanIntent(content.target, {}, ORG, D80_ID);
    case 'sbom/generate': case 'sbom/import': return sbomIntent(row.op, content, ORG, D80_ID);
    case 'artifact/signature-verify': return artifactSignatureVerifyIntent(ARTIFACT, ORG, D80_ID);
    case 'artifact/register-build-result': return artifactBuildResultIntent(D80_ID, ORG, D80_ID);
    case 'relay/policy-set': return relayPolicyIntent(content, null, null, ORG, D80_ID);
    case 'notification/channel-test': return notificationChannelTestIntent(CHANNEL, ORG, D80_ID);
    case 'environment/worker-policy-apply':
      return environmentWorkerPolicyIntent(ENV, content.policy, content.expected_updated_at, ORG, D80_ID);
    case 'ml/pin': return mlPinIntent(ENDPOINT, ENV, WORKER, content.expected_updated_at, ORG, D80_ID);
    default: throw new Error(`Missing D80 case ${row.domain}/${row.op}`);
  }
}

describe('D79 and D80 web intent content contracts', () => {
  for (const [fixture, build] of [[d79, d79Request], [d80, d80Request]]) {
    for (const row of fixture.intents) {
      it(`${fixture.schema} ${row.domain}/${row.op} ${row.idempotency_key || row.content.intent_id}`, () => {
        const request = build(row);
        expect(request).toMatchObject({ domain: row.domain, op: row.op, coordinate: row.coordinate,
          content: row.content });
        expect(request.content).toEqual(row.content);
        const event = buildIntentEvent({ ...request, pubkey: WORKER });
        expect(event.tags).toContainEqual(['intent_id', request.intentId]);
        expect(event.tags).toContainEqual(['d', row.coordinate]);
        expect(JSON.parse(event.content)).toEqual(row.content);
      });
    }
  }
});
