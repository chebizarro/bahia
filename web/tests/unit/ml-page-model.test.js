import { describe, expect, it } from 'vitest';
import { buildDeployPayload, buildPlacementPolicy, previewWorkerEligibility, resolveEndpointForInput } from '../../src/routes/ml/page-model.js';

const onlineWorker = {
  pubkey: 'a'.repeat(64),
  architecture: 'amd64',
  name: 'gpu-west',
  status: 'online',
  scheduling_state: 'active',
  labels: { role: 'inference', track: 'stable' },
  runtime_target: { public_base_url: 'https://worker.invalid' },
  ml_capabilities: {
    runtimes: ['vllm'],
    accelerators: ['gpu_nvidia_cuda']
  },
  accelerators: [{ vendor: 'nvidia', model: 'L40S', count: 1, memory_gb: 48 }]
};

const drainingWorker = {
  ...onlineWorker,
  pubkey: 'b'.repeat(64),
  name: 'gpu-draining',
  scheduling_state: 'draining'
};

describe('Inference placement page model', () => {
  it('builds deploy placement policy with pin, labels, rollout, and selector', () => {
    const form = {
      endpoint: 'endpoint:qwen:prod',
      model_version: 'model-version:qwen:v1',
      runtime_preference: 'vllm',
      accelerator: 'gpu_nvidia_cuda',
      min_vram_gb: '24',
      pinned_worker: onlineWorker.pubkey,
      label_selector: 'role=inference\ntrack=stable',
      worker_selector: '{"architecture":"amd64"}',
      rollout_from_labels: 'track=canary',
      rollout_to_labels: 'track=stable'
    };

    expect(buildPlacementPolicy(form)).toEqual({
      accelerator: 'gpu_nvidia_cuda',
      min_vram_gb: 24,
      pinned_worker: onlineWorker.pubkey,
      label_selector: { role: 'inference', track: 'stable' },
      worker_selector: { architecture: 'amd64' },
      rollout: {
        from_labels: { track: 'canary' },
        to_labels: { track: 'stable' }
      }
    });
    expect(buildDeployPayload(form).placement.pinned_worker).toBe(onlineWorker.pubkey);
  });

  it('previews eligible and rejected workers with operator-visible reasons', () => {
    const preview = previewWorkerEligibility([drainingWorker, onlineWorker], {
      runtime_preference: 'vllm',
      accelerator: 'gpu_nvidia_cuda',
      min_vram_gb: '24',
      pinned_worker: '',
      label_selector: 'role=inference',
      worker_selector: '',
      rollout_from_labels: '',
      rollout_to_labels: ''
    });

    expect(preview.estimated_eligible_count).toBe(1);
    expect(preview.eligible_workers[0]).toMatchObject({ worker_pubkey: onlineWorker.pubkey, eligible: true });
    expect(preview.rejected_workers[0].reason).toContain('scheduling state is draining');
    expect(preview.selected_winner.worker_pubkey).toBe(onlineWorker.pubkey);
  });

  it('applies worker selector and rollout target labels to the preview', () => {
    const preview = previewWorkerEligibility([onlineWorker], {
      runtime_preference: 'vllm',
      accelerator: 'gpu_nvidia_cuda',
      min_vram_gb: '',
      pinned_worker: '',
      label_selector: '',
      worker_selector: '{"architecture":"arm64"}',
      rollout_from_labels: 'track=canary',
      rollout_to_labels: 'track=stable'
    });

    expect(preview.estimated_eligible_count).toBe(0);
    expect(preview.rejected_workers[0].reason).toContain('selector mismatch');
    expect(preview.checked_capabilities.label_selector).toEqual({ track: 'stable' });
  });

  it('resolves existing endpoints only by unique backend-supported id or coordinate', () => {
    const endpoints = [
      { id: 'endpoint-1', name: 'qwen', environment_id: 'prod-env' },
      { id: 'endpoint-2', name: 'qwen', environment_id: 'stage-env' }
    ];

    expect(resolveEndpointForInput(endpoints, 'endpoint-1')).toMatchObject({ id: 'endpoint-1' });
    expect(resolveEndpointForInput(endpoints, 'endpoint:qwen:prod-env')).toMatchObject({ id: 'endpoint-1' });
    expect(resolveEndpointForInput(endpoints, 'qwen')).toBeNull();
  });

  it('rejects all non-pinned workers when a pin is selected', () => {
    const preview = previewWorkerEligibility([drainingWorker, onlineWorker], {
      runtime_preference: 'vllm',
      accelerator: 'gpu_nvidia_cuda',
      min_vram_gb: '',
      pinned_worker: onlineWorker.pubkey,
      label_selector: '',
      worker_selector: '',
      rollout_from_labels: '',
      rollout_to_labels: ''
    });

    expect(preview.eligible_workers).toHaveLength(1);
    expect(preview.rejected_workers.map((candidate) => candidate.worker_pubkey)).toContain(drainingWorker.pubkey);
  });
});
