// Cross-slice wire contract: org/rekey is a request, not a canonical org update.
const ORG_ID = '018f6a60-0000-7000-8000-000000000001';
const INTENT_ID = '018f6a60-0000-7000-8000-000000000081';

export function rekeyIntentStatusFixture({ requesterPubkey = 'a'.repeat(64),
  servicePubkey = 'b'.repeat(64), reason = 'Membership changed',
  keyVersion = 'v2', recordsRepublished = 7 } = {}) {
  return {
    request: { domain: 'org', op: 'rekey', coordinate: ORG_ID, orgId: ORG_ID,
      schema: 'bahia.intent.org.v1', intentId: INTENT_ID,
      content: { org_id: ORG_ID, reason } },
    intentTags: [['d', ORG_ID], ['domain', 'org'], ['schema', 'bahia.intent.org.v1'],
      ['t', 'bahia-intent'], ['t', 'org'], ['op', 'rekey'], ['org', ORG_ID], ['intent_id', INTENT_ID]],
    intentContent: { org_id: ORG_ID, reason },
    statusTags: [['d', `intent-status:${requesterPubkey}:${ORG_ID}`], ['domain', 'intent'],
      ['status', 'accepted'], ['t', 'intent-status'], ['p', requesterPubkey], ['intent_id', INTENT_ID]],
    statusContent: { status: 'accepted', intent_id: INTENT_ID, coordinate: ORG_ID, reason: '',
      data: { key_version: keyVersion, records_republished: recordsRepublished } },
    servicePubkey
  };
}
