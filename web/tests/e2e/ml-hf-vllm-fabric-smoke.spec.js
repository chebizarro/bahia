import { createHash } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const WORKER_PUBKEY = 'c'.repeat(64);
const now = Math.floor(Date.now() / 1000);

function nostrEvent({ kind, pubkey = SERVICE_PUBKEY, tags = [], content = {} }) {
  const body = typeof content === 'string' ? content : JSON.stringify(content);
  const event = {
    kind,
    pubkey,
    created_at: now,
    tags,
    content: body,
    sig: '0'.repeat(128)
  };
  event.id = createHash('sha256')
    .update(JSON.stringify([0, event.pubkey, event.created_at, event.kind, event.tags, event.content]))
    .digest('hex');
  return event;
}

const modelId = '11111111-1111-4111-8111-111111111111';
const versionId = '22222222-2222-4222-8222-222222222222';
const endpointId = '33333333-3333-4333-8333-333333333333';
const envId = '44444444-4444-4444-8444-444444444444';
const runId = '55555555-5555-4555-8555-555555555555';

const relaySystemInfo = {
  nostr: {
    browser_relays: ['ws://relay.test.local'],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

const nostrEvents = [
  nostrEvent({
    kind: 31974,
    tags: [['d', 'bahia-system-v1']],
    content: { ...relaySystemInfo, schema: 'bahia.system-discovery.v1' }
  }),
  nostrEvent({
    kind: 30002,
    tags: [['d', 'bahia-browser-v1'], ['relay', 'ws://relay.test.local']],
    content: ''
  }),
  nostrEvent({
    kind: 30002,
    tags: [['d', 'bahia-service-v1'], ['relay', 'ws://relay.test.local']],
    content: ''
  }),
  nostrEvent({
    kind: 31980,
    tags: [['d', 'model:qwen2.5-coder-32b'], ['task', 'chat_completions'], ['license', 'apache-2.0'], ['name', 'Qwen2.5-Coder-32B-Instruct']],
    content: {
      id: modelId,
      slug: 'qwen2.5-coder-32b',
      name: 'Qwen2.5-Coder-32B-Instruct',
      family: 'qwen',
      modalities: ['text'],
      task_kinds: ['chat_completions'],
      license: 'apache-2.0',
      source: { kind: 'huggingface', uri: 'hf://Qwen/Qwen2.5-Coder-32B-Instruct', revision: 'abc123' }
    }
  }),
  nostrEvent({
    kind: 31981,
    tags: [['d', 'model-version:qwen2.5-coder-32b:v1'], ['model', 'model:qwen2.5-coder-32b'], ['version', 'v1'], ['runtime', 'vllm'], ['format', 'safetensors']],
    content: {
      id: versionId,
      model_id: modelId,
      version: 'v1',
      source: { kind: 'huggingface', uri: 'hf://Qwen/Qwen2.5-Coder-32B-Instruct', revision: 'abc123' },
      runtime_requirements: { preferred_runtimes: ['vllm'], required_formats: ['safetensors'], accelerators: ['gpu_nvidia_cuda'], min_vram_gb: 48 }
    }
  }),
  nostrEvent({
    kind: 31985,
    tags: [['d', 'endpoint:qwen-coder:prod'], ['runtime', 'vllm'], ['environment', envId]],
    content: {
      id: endpointId,
      name: 'qwen-coder',
      environment_id: envId,
      task_kinds: ['chat_completions'],
      protocol: 'openai-compatible',
      gateway: { gateway_ref: 'gateway-prod' },
      placement_policy: { accelerator: 'gpu_nvidia_cuda', min_vram_gb: 48 }
    }
  }),
  nostrEvent({
    kind: 31986,
    tags: [['d', 'endpoint-state:qwen-coder:prod'], ['endpoint', endpointId], ['environment', envId], ['runtime', 'vllm'], ['status', 'healthy']],
    content: {
      endpoint_id: endpointId,
      environment_id: envId,
      desired_model_version_id: versionId,
      active_run_id: runId,
      drift_status: 'in_sync',
      gateway_status: 'synced',
      runtime_kind: 'vllm',
      backend_endpoint: 'http://worker.example.com:8000',
      backend_health: 'healthy',
      gateway_target: 'http://worker.example.com:8000',
      status: 'healthy'
    }
  }),
  nostrEvent({
    kind: 31988,
    tags: [['d', 'artifact:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'], ['sha256', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'], ['source', 'huggingface']],
    content: {
      artifact: { uri: 'hf://Qwen/Qwen2.5-Coder-32B-Instruct@abc123/model.safetensors', format: 'safetensors', sha256: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' },
      edges: [{ edge_kind: 'mirror_of', verified: true }, { edge_kind: 'worker_verified', verified: true, run_id: runId }]
    }
  }),
  nostrEvent({
    kind: 31989,
    pubkey: WORKER_PUBKEY,
    tags: [['d', `worker:${WORKER_PUBKEY}:ai-capability`], ['runtime', 'vllm'], ['artifact_format', 'safetensors'], ['task', 'chat_completions'], ['accelerator', 'gpu_nvidia_cuda'], ['vram_gb', '48'], ['status', 'ready']],
    content: { pubkey: WORKER_PUBKEY, name: 'gpu-worker', status: 'ready' }
  })
];

test('ML fabric page renders HF to GPU/vLLM deployed state from Nostr read models', async ({ page }) => {
  await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents });
  await page.route('**/api/v1/ml/**', (route) => route.fulfill({
    status: 202,
    contentType: 'application/json',
    body: JSON.stringify({ message: 'request accepted; observe Nostr read models for completion' })
  }));

  await page.goto('/ml');
  await page.waitForLoadState('networkidle');

  await expect(page.getByRole('heading', { name: /ML Fabric/ })).toBeVisible();
  await expect(page.locator('[data-testid="ml-model-catalog"]')).toContainText('Qwen2.5-Coder-32B-Instruct');
  await expect(page.locator('[data-testid="ml-model-catalog"]')).toContainText('huggingface');
  await expect(page.locator('[data-testid="ml-endpoints"]')).toContainText('qwen-coder');
  await expect(page.locator('[data-testid="ml-endpoints"]')).toContainText('openai-compatible');
  await expect(page.locator('[data-testid="ml-endpoints"]')).toContainText('healthy');

  await page.locator('[data-testid="ml-model-catalog"] tbody tr').first().click();
  await expect(page.locator('[data-testid="ml-model-versions"]')).toContainText('vllm');
  await expect(page.locator('[data-testid="ml-model-versions"]')).toContainText('v1');
});
