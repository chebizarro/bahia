import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import {
  applyControlplaneEvent,
  readModelFilters,
  resetEventRouting
} from '../../src/lib/stores/controlplane/events.svelte.js';
import { controlplaneConnection } from '../../src/lib/stores/controlplane/connection.svelte.js';
import {
  operations,
  operationsForDomain,
  operationsForEntity,
  refreshOperations,
  resetOperations
} from '../../src/lib/stores/collections/operations.svelte.js';
import {
  DEPLOYMENT_RESULT,
  DEPLOYMENT_STATUS,
  HIVE_CI_WORKFLOW_RESULT,
  HIVE_CI_WORKFLOW_RUN
} from '../../src/lib/nostr/kinds.gen.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const OTHER_PUBKEY = 'c'.repeat(64);
const REQUEST_ID = '1'.repeat(64);
const STATUS_ID = '2'.repeat(64);
const RESULT_ID = '3'.repeat(64);
const INTENT_ID = 'intent-123';
const SERVICE_ID = 'service-123';
const ENVIRONMENT_ID = 'environment-123';

const STATUS_KINDS = [
  6941,
  6950,
  6961,
  6962,
  6963,
  6973,
  6976,
  6978,
  6981,
  6982,
  6983,
  6984,
  6991,
  6997
];

const RESULT_KINDS = [
  7941,
  7942,
  7943,
  7944,
  7945,
  7950,
  7961,
  7962,
  7963,
  7964,
  7965,
  7966,
  7971,
  7972,
  7973,
  7974,
  7975,
  7976,
  7977,
  7978,
  7979,
  7991,
  7992,
  7997
];

function relayEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content),
    sig: '0'.repeat(128)
  };
}

const deploymentStatus = relayEvent({
  id: STATUS_ID,
  kind: DEPLOYMENT_STATUS,
  created_at: 1770000010,
  tags: [
    ['e', REQUEST_ID, '', 'reply'],
    ['domain', 'deployment'],
    ['status', 'running'],
    ['step', 'rollout'],
    ['intent', INTENT_ID],
    ['service', SERVICE_ID],
    ['environment', ENVIRONMENT_ID]
  ],
  content: {
    request_event_id: REQUEST_ID,
    status: 'running',
    step: 'rollout',
    message: 'Rolling out deployment'
  }
});

const deploymentResult = relayEvent({
  id: RESULT_ID,
  kind: DEPLOYMENT_RESULT,
  created_at: 1770000020,
  tags: [
    ['e', REQUEST_ID, '', 'reply'],
    ['domain', 'deployment'],
    ['status', 'success'],
    ['intent', INTENT_ID],
    ['service', SERVICE_ID],
    ['environment', ENVIRONMENT_ID],
    ['artifact', 'artifact-123']
  ],
  content: {
    request_event_id: REQUEST_ID,
    intent_id: INTENT_ID,
    service_id: SERVICE_ID,
    environment_id: ENVIRONMENT_ID,
    artifact_id: 'artifact-123',
    status: 'succeeded',
    message: 'Deployment completed'
  }
});

beforeEach(() => {
  resetEventRouting();
  resetOperations();
  controlplaneConnection.servicePubkey = SERVICE_PUBKEY;
});

afterEach(() => {
  controlplaneConnection.servicePubkey = '';
});

describe('operational event subscriptions', () => {
  it('subscribes to every specified 69xx, 79xx, and Hive-CI kind', () => {
    const filters = readModelFilters();
    const subscribedKinds = new Set(filters.flatMap((filter) => filter.kinds || []));

    for (const kind of [...STATUS_KINDS, ...RESULT_KINDS, HIVE_CI_WORKFLOW_RUN, HIVE_CI_WORKFLOW_RESULT]) {
      expect(subscribedKinds.has(kind), `missing subscription kind ${kind}`).toBe(true);
    }
  });

  it('uses canonical-author filtering for Bahia operations but not external Hive-CI events', () => {
    const filters = readModelFilters();
    const deploymentFilter = filters.find((filter) => filter.kinds?.includes(DEPLOYMENT_STATUS));
    const hiveFilter = filters.find((filter) => filter.kinds?.includes(HIVE_CI_WORKFLOW_RUN));

    expect(deploymentFilter).toMatchObject({ authors: [SERVICE_PUBKEY] });
    expect(hiveFilter?.authors).toBeUndefined();

    expect(applyControlplaneEvent(relayEvent({
      ...deploymentStatus,
      id: '4'.repeat(64),
      pubkey: OTHER_PUBKEY
    }))).toBe(false);
    expect(operations).toHaveLength(0);
  });
});

describe('generic operations projection', () => {
  it('merges a realistic status -> result sequence by request e-tag and domain entity', () => {
    expect(applyControlplaneEvent(deploymentStatus)).toBe(true);
    expect(applyControlplaneEvent(deploymentResult)).toBe(true);
    refreshOperations();

    expect(operations).toHaveLength(1);
    expect(operations[0]).toMatchObject({
      id: REQUEST_ID,
      request_event_id: REQUEST_ID,
      domain: 'deployment',
      entity_type: 'intent_id',
      entity_id: INTENT_ID,
      status_event_id: STATUS_ID,
      result_event_id: RESULT_ID,
      status: 'success',
      success: true,
      terminal: true,
      intent_id: INTENT_ID,
      service_id: SERVICE_ID,
      environment_id: ENVIRONMENT_ID,
      artifact_id: 'artifact-123',
      entity_refs: {
        intent_id: INTENT_ID,
        service_id: SERVICE_ID,
        environment_id: ENVIRONMENT_ID,
        artifact_id: 'artifact-123'
      }
    });
    expect(operationsForDomain(operations, 'deployment')).toHaveLength(1);
    expect(operationsForEntity(operations, 'service', SERVICE_ID)).toHaveLength(1);
  });

  it('does not let a late non-terminal status regress a terminal result', () => {
    expect(applyControlplaneEvent(deploymentResult)).toBe(true);
    expect(applyControlplaneEvent(deploymentStatus)).toBe(true);
    refreshOperations();

    expect(operations).toHaveLength(1);
    expect(operations[0].status).toBe('success');
    expect(operations[0].message).toBe('Deployment completed');
    expect(operations[0].result_event_id).toBe(RESULT_ID);
  });

  it('projects out-of-order Hive-CI result and run events onto one operation', () => {
    const runId = '5'.repeat(64);
    const hiveResult = relayEvent({
      id: '6'.repeat(64),
      kind: HIVE_CI_WORKFLOW_RESULT,
      pubkey: OTHER_PUBKEY,
      created_at: 1770000040,
      tags: [
        ['e', runId],
        ['status', 'success'],
        ['exit_code', '0'],
        ['duration', '42'],
        ['log_url', 'https://ci.example/run.log']
      ],
      content: {
        image_repo: 'registry.example/app',
        image_tag: 'abc123'
      }
    });
    const hiveRun = relayEvent({
      id: runId,
      kind: HIVE_CI_WORKFLOW_RUN,
      pubkey: OTHER_PUBKEY,
      created_at: 1770000030,
      tags: [
        ['a', '30617:owner:repo'],
        ['commit', 'abc123'],
        ['branch', 'main'],
        ['workflow', '.hive/workflows/build.yaml'],
        ['triggered-by', 'push'],
        ['publisher', OTHER_PUBKEY]
      ]
    });

    expect(applyControlplaneEvent(hiveResult)).toBe(true);
    expect(applyControlplaneEvent(hiveRun)).toBe(true);
    refreshOperations();

    expect(operations).toHaveLength(1);
    expect(operations[0]).toMatchObject({
      request_event_id: runId,
      run_event_id: runId,
      result_event_id: hiveResult.id,
      domain: 'hive-ci',
      repo_coordinate: '30617:owner:repo',
      commit_sha: 'abc123',
      status: 'success',
      terminal: true
    });
  });

  it('does not let an older terminal status or result replace a newer result', () => {
    const olderFailedStatus = relayEvent({
      id: '8'.repeat(64),
      kind: DEPLOYMENT_STATUS,
      created_at: 1770000005,
      tags: [
        ['e', REQUEST_ID],
        ['status', 'failed'],
        ['intent', INTENT_ID]
      ],
      content: {
        status: 'failed',
        message: 'stale failure'
      }
    });
    const olderFailedResult = relayEvent({
      id: '9'.repeat(64),
      kind: DEPLOYMENT_RESULT,
      created_at: 1770000006,
      tags: [
        ['e', REQUEST_ID],
        ['status', 'failed'],
        ['intent', INTENT_ID]
      ],
      content: {
        status: 'failed',
        message: 'stale result'
      }
    });

    expect(applyControlplaneEvent(deploymentResult)).toBe(true);
    expect(applyControlplaneEvent(olderFailedStatus)).toBe(true);
    expect(applyControlplaneEvent(olderFailedResult)).toBe(true);
    refreshOperations();

    expect(operations[0].status).toBe('success');
    expect(operations[0].message).toBe('Deployment completed');
    expect(operations[0].result_event_id).toBe(RESULT_ID);
    expect(operations[0].updated_at).toBe(new Date(deploymentResult.created_at * 1000).toISOString());
  });

  it('isolates untrusted external results and ignores payload attempts to forge projection fields', () => {
    const externalCollision = relayEvent({
      id: 'a'.repeat(64),
      kind: HIVE_CI_WORKFLOW_RESULT,
      pubkey: OTHER_PUBKEY,
      created_at: 1770000030,
      tags: [
        ['e', REQUEST_ID],
        ['status', 'failure'],
        ['exit_code', '1'],
        ['duration', '1'],
        ['log_url', 'https://evil.example/log']
      ],
      content: {
        domain: 'deployment',
        terminal: false,
        result_event_id: 'forged-result',
        request_event_id: 'forged-request'
      }
    });

    expect(applyControlplaneEvent(deploymentResult)).toBe(true);
    expect(applyControlplaneEvent(externalCollision)).toBe(true);
    refreshOperations();

    expect(operations).toHaveLength(1);
    expect(operations[0]).toMatchObject({
      source: 'bahia',
      request_event_id: REQUEST_ID,
      result_event_id: RESULT_ID,
      status: 'success',
      terminal: true
    });
  });

  it('rejects operational status/result events without an e-tag correlation id', () => {
    const uncorrelated = relayEvent({
      id: '7'.repeat(64),
      kind: DEPLOYMENT_STATUS,
      tags: [['status', 'running']]
    });

    expect(applyControlplaneEvent(uncorrelated)).toBe(false);
    refreshOperations();
    expect(operations).toHaveLength(0);
  });
});
