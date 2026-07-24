import { describe, it, expect } from 'vitest';
import {
  activityData,
  buildCreateRoutePayload,
  buildDeployPayload,
  buildRollbackPayload,
  buildEnvironmentOptions,
  buildLLMActivityKinds,
  buildLLMEventHistory,
  buildPendingApprovals,
  buildRecentReleases,
  buildReleaseOptions,
  buildReleasePayload,
  buildRouteOptions,
  buildRouteStateRows,
  kindLabel,
  routeName,
  environmentName
} from '../../src/routes/llm/page-model.js';

const KINDS = {};

function getTagValue(event, name) {
  const tags = event?.tags || [];
  return tags.find((tag) => Array.isArray(tag) && tag[0] === name)?.[1] || '';
}

describe('llm page model helpers', () => {
  it('builds signer-first route, release, and deploy payloads', () => {
    expect(buildCreateRoutePayload({
      name: 'chat-stage',
      description: 'Stage route',
      public_model: 'bahia/chat-stage',
      path: ''
    })).toEqual({
      name: 'chat-stage',
      description: 'Stage route',
      gateway_config: {
        public_model: 'bahia/chat-stage',
        path: undefined
      }
    });

    expect(buildReleasePayload({
      route_id: 'llm-route-1',
      version: 'v2',
      model_ref: 'hf://meta-llama/Llama-3',
      model_source: 'huggingface',
      backend_mode: 'external',
      external_base_url: 'https://llm.example.com'
    })).toEqual({
      route_id: 'llm-route-1',
      version: 'v2',
      model_ref: 'hf://meta-llama/Llama-3',
      model_source: 'huggingface',
      backend_preferences: ['external_api'],
      external_backend: { base_url: 'https://llm.example.com' }
    });

    expect(buildReleasePayload({
      route_id: 'llm-route-1',
      version: 'v3',
      model_ref: 'oci://bahia/vllm',
      model_source: 'oci',
      backend_mode: 'runtime',
      runtime_image: 'vllm/vllm-openai:latest',
      runtime_container_port: '8000',
      runtime_host_port: '18000',
      runtime_health_path: '/healthz'
    })).toEqual({
      route_id: 'llm-route-1',
      version: 'v3',
      model_ref: 'oci://bahia/vllm',
      model_source: 'oci',
      backend_preferences: ['vllm'],
      runtime_backend: {
        image: 'vllm/vllm-openai:latest',
        container_port: 8000,
        host_port: 18000,
        health_path: '/healthz'
      }
    });

    expect(buildDeployPayload({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      release_id: 'llm-release-1',
      requested_by: ''
    }, 'f'.repeat(64))).toEqual({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      release_id: 'llm-release-1',
      requested_by: 'f'.repeat(64)
    });

    expect(buildRollbackPayload({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      requested_by: ''
    }, 'f'.repeat(64))).toEqual({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      requested_by: 'f'.repeat(64)
    });
  });

  it('uses secret UUID references for gateway and health Authorization headers', () => {
    expect(buildCreateRoutePayload({
      name: 'paid-chat',
      description: '',
      public_model: 'bahia/paid-chat',
      path: '',
      authorization_secret_ref: 'route-secret-uuid'
    }).gateway_config.header_secret_refs).toEqual({ Authorization: 'route-secret-uuid' });

    expect(buildReleasePayload({
      route_id: 'llm-route-1',
      version: 'v1',
      model_ref: 'external://routstr',
      model_source: 'external',
      backend_mode: 'external',
      external_base_url: 'http://routstr:8080',
      external_health_url: 'http://routstr:8080/healthz',
      health_authorization_secret_ref: 'health-secret-uuid'
    }).external_backend).toEqual({
      base_url: 'http://routstr:8080',
      health_url: 'http://routstr:8080/healthz',
      health_header_secret_refs: { Authorization: 'health-secret-uuid' }
    });
  });

  it('derives activity history, releases, pending approvals, and route state rows', () => {
    const events = [
      {
        kind: 30315,
        time: '2026-05-04T12:05:00.000Z',
        data: {
          intent_id: 'llm-intent-1',
          route_id: 'llm-route-1',
          environment_id: 'env-prod',
          release_id: 'llm-release-1',
          requested_by: 'f'.repeat(64),
          status: 'processing',
          step: 'accepted',
          message: 'accepted'
        },
        nostr_event: { tags: [['domain', 'llm'], ['schema', 'bahia.status.llm.v1'], ['intent', 'llm-intent-1'], ['route', 'llm-route-1'], ['environment', 'env-prod'], ['release', 'llm-release-1'], ['step', 'accepted']] }
      },
      {
        kind: 4903,
        time: '2026-05-04T12:00:00.000Z',
        data: { route_id: 'llm-route-1', release_id: 'llm-release-1', version: 'v1', status: 'success' },
        nostr_event: { tags: [['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'release-register'], ['route', 'llm-route-1'], ['release', 'llm-release-1'], ['status', 'success']] }
      },
      {
        kind: 4903,
        time: '2026-05-04T12:10:00.000Z',
        data: { ignored: true, domain: 'service', schema: 'bahia.audit.v1' },
        nostr_event: { tags: [['domain', 'service'], ['schema', 'bahia.audit.v1']] }
      }
    ];
    const llmRoutes = [{ id: 'llm-route-1', route_id: 'llm-route-1', name: 'chat-prod' }];
    const environments = [{ id: 'env-prod', name: 'production' }];
    const llmRouteStates = [{
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      desired_release_id: 'llm-release-1',
      desired_intent_id: 'llm-intent-1',
      active_run_id: null,
      drift_status: 'deploying',
      gateway_status: 'pending'
    }];

    const llmKinds = buildLLMActivityKinds(KINDS);
    const history = buildLLMEventHistory(events, llmKinds, getTagValue);
    expect(history).toHaveLength(2);
    expect(history[0].kind).toBe(30315);
    expect(activityData(history[0]).step).toBe('accepted');

    const releases = buildRecentReleases(history, getTagValue);
    expect(releases).toEqual([{ id: 'llm-release-1', route_id: 'llm-route-1', version: 'v1', created_at: '2026-05-04T12:00:00.000Z', status: 'success' }]);

    const rows = buildRouteStateRows(llmRouteStates, llmRoutes, environments, releases);
    expect(rows).toEqual([expect.objectContaining({
      route_name: 'chat-prod',
      environment_name: 'production',
      desired_release_label: 'v1',
      desired_intent_label: 'llm-intent-1',
      active_run_label: '-'
    })]);

    const approvals = buildPendingApprovals(history, llmRouteStates, llmRoutes, environments, releases, KINDS, getTagValue);
    expect(approvals).toEqual([expect.objectContaining({
      intent_id: 'llm-intent-1',
      route_name: 'chat-prod',
      environment_name: 'production',
      release_label: 'v1'
    })]);
  });

  it('filters release options and labels activity kinds', () => {
    const releases = [
      { id: 'rel-1', route_id: 'llm-route-1', version: 'v1' },
      { id: 'rel-2', route_id: 'llm-route-2', version: 'v2' }
    ];
    const routes = [
      { id: 'llm-route-1', name: 'chat-prod' },
      { route_id: 'llm-route-2', name: 'chat-stage' }
    ];
    const environments = [{ id: 'env-prod', name: 'production' }];

    expect(buildRouteOptions(routes)).toEqual([
      { value: 'llm-route-1', label: 'chat-prod' },
      { value: 'llm-route-2', label: 'chat-stage' }
    ]);
    expect(buildEnvironmentOptions(environments)).toEqual([{ value: 'env-prod', label: 'production' }]);
    expect(buildReleaseOptions(releases, 'llm-route-1')).toEqual([{ id: 'rel-1', route_id: 'llm-route-1', version: 'v1' }]);
    expect(routeName(routes, 'llm-route-2')).toBe('chat-stage');
    expect(environmentName(environments, 'env-prod')).toBe('production');
    expect(kindLabel({ data: { schema: 'bahia.result.llm.v1', operation: 'deploy' }, nostr_event: { tags: [] } }, getTagValue)).toBe('Deploy result');
    expect(kindLabel({ data: {}, nostr_event: { tags: [] } }, getTagValue)).toBe('Nostr activity');
  });
});
