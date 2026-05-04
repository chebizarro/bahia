import { describe, it, expect } from 'vitest';
import {
  activityData,
  buildCreateRoutePayload,
  buildDeployPayload,
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

const KINDS = {
  BAHIA_LLM_ROUTE_CREATE_RESULT: 7971,
  BAHIA_LLM_RELEASE_REGISTER_RESULT: 7972,
  BAHIA_LLM_DEPLOYMENT_STATUS: 6973,
  BAHIA_LLM_DEPLOYMENT_RESULT: 7973
};

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
  });

  it('derives activity history, releases, pending approvals, and route state rows', () => {
    const events = [
      {
        kind: 6973,
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
        nostr_event: { tags: [['intent', 'llm-intent-1'], ['route', 'llm-route-1'], ['environment', 'env-prod'], ['release', 'llm-release-1'], ['step', 'accepted']] }
      },
      {
        kind: 7972,
        time: '2026-05-04T12:00:00.000Z',
        data: { route_id: 'llm-route-1', release_id: 'llm-release-1', version: 'v1', status: 'success' },
        nostr_event: { tags: [['route', 'llm-route-1'], ['release', 'llm-release-1'], ['status', 'success']] }
      },
      {
        kind: 9999,
        time: '2026-05-04T12:10:00.000Z',
        data: { ignored: true },
        nostr_event: { tags: [] }
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
    const history = buildLLMEventHistory(events, llmKinds);
    expect(history).toHaveLength(2);
    expect(history[0].kind).toBe(6973);
    expect(activityData(history[0]).step).toBe('accepted');

    const releases = buildRecentReleases(history.filter((event) => event.kind === KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT), getTagValue);
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
    expect(kindLabel(KINDS, 7973)).toBe('Deploy result');
    expect(kindLabel(KINDS, 1234)).toBe('Kind 1234');
  });
});
