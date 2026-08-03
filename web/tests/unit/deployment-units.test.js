import { describe, expect, it } from 'vitest';
import {
  buildDeploymentUnitSetUpdate,
  deploymentTargetIssue,
  deploymentUnitErrorMessage,
  deploymentUnitWriteShape,
  validateDeploymentUnitForm
} from '../../src/lib/deployment-units.js';

const form = {
  key: 'max',
  display_name: 'Max Compose',
  runtime_type: 'compose',
  endpoint_ref: 'max',
  compose_dir: '/srv/bahia/compose/gastown',
  ownership_mode: 'bahia_managed',
  reconcile_mode: 'approval_required',
  execution_mode: 'sdk'
};

function environment(overrides = {}) {
  return {
    id: 'env-1',
    name: 'production',
    updated_at: '2026-08-02T08:00:00Z',
    protected: false,
    targeting: {
      default_unit_key: 'default',
      secret_scope_mode: 'unit',
      default_reconcile_mode: 'observe_only'
    },
    deployment_units: [{
      id: '',
      key: 'default',
      runtime_type: 'compose',
      endpoint_ref: 'local',
      compose_dir: '/srv/default',
      ownership_mode: 'bahia_managed',
      reconcile_mode: 'observe_only',
      implicit: true
    }],
    ...overrides
  };
}

describe('deployment unit contract helpers', () => {
  it('creates the first explicit unit as a revision-guarded complete set', () => {
    const payload = buildDeploymentUnitSetUpdate(environment(), { form });
    expect(payload).toEqual({
      expected_updated_at: '2026-08-02T08:00:00Z',
      targeting: {
        default_unit_key: 'max',
        secret_scope_mode: 'unit',
        default_reconcile_mode: 'observe_only'
      },
      deployment_units: [{
        key: 'max',
        display_name: 'Max Compose',
        runtime_type: 'compose',
        endpoint_ref: 'max',
        compose_dir: '/srv/bahia/compose/gastown',
        ownership_mode: 'bahia_managed',
        reconcile_mode: 'approval_required',
        runtime_config: { execution_mode: 'sdk' }
      }]
    });
  });

  it('edits one unit while preserving and deterministically sorting the complete set', () => {
    const env = environment({
      targeting: { default_unit_key: 'zeta', secret_scope_mode: 'environment', default_reconcile_mode: 'approval_required' },
      deployment_units: [
        {
          id: 'unit-z',
          environment_id: 'env-1',
          key: 'zeta',
          runtime_type: 'compose',
          endpoint_ref: 'old',
          compose_dir: '/srv/zeta',
          ownership_mode: 'bahia_managed',
          reconcile_mode: 'observe_only',
          runtime_config: { execution_mode: 'cli', preserved: true },
          implicit: false,
          created_at: 'ignored'
        },
        {
          id: 'unit-a',
          key: 'alpha',
          display_name: 'Alpha',
          runtime_type: 'compose',
          endpoint_ref: 'alpha',
          compose_dir: '/srv/alpha',
          ownership_mode: 'bahia_managed',
          reconcile_mode: 'observe_only',
          network_profile: { zone: 'west' },
          implicit: false
        }
      ]
    });

    const payload = buildDeploymentUnitSetUpdate(env, {
      originalKey: 'zeta',
      form: { ...form, key: 'max' }
    });

    expect(payload.targeting.default_unit_key).toBe('max');
    expect(payload.deployment_units.map((unit) => unit.key)).toEqual(['alpha', 'max']);
    expect(payload.deployment_units[0].network_profile).toEqual({ zone: 'west' });
    expect(payload.deployment_units[1].runtime_config).toEqual({ execution_mode: 'sdk', preserved: true });
    expect(payload.deployment_units[1]).not.toHaveProperty('id');
    expect(payload.deployment_units[1]).not.toHaveProperty('environment_id');
    expect(payload.deployment_units[1]).not.toHaveProperty('implicit');
  });

  it('rejects duplicate keys, missing revisions, mixed runtimes, and missing ownership', () => {
    const explicit = {
      key: 'max',
      runtime_type: 'compose',
      endpoint_ref: 'max',
      compose_dir: '/srv/max',
      ownership_mode: 'bahia_managed',
      reconcile_mode: 'observe_only'
    };
    expect(() => buildDeploymentUnitSetUpdate(environment({ deployment_units: [explicit] }), { form }))
      .toThrow('already exists');
    expect(() => buildDeploymentUnitSetUpdate(environment({ updated_at: '' }), { form }))
      .toThrow('revision is unavailable');
    expect(() => buildDeploymentUnitSetUpdate(environment({ deployment_units: [{ ...explicit, key: 'docker', runtime_type: 'docker' }] }), { form }))
      .toThrow('mixed-runtime');
    expect(() => buildDeploymentUnitSetUpdate(environment({ deployment_units: [{ ...explicit, key: 'adopted', ownership_mode: 'adopted' }] }), { form }))
      .toThrow('Bahia-managed ownership');
  });

  it('validates endpoint aliases, paths, fixed ownership, and protected safeguards', () => {
    expect(validateDeploymentUnitForm({ ...form, endpoint_ref: 'tcp://docker:2376' }).error)
      .toContain('URLs and credentials');
    expect(validateDeploymentUnitForm({ ...form, compose_dir: '../compose' }).error)
      .toContain('absolute server path');
    expect(validateDeploymentUnitForm({ ...form, ownership_mode: 'adopted' }).error)
      .toContain('Bahia-managed ownership');
    expect(validateDeploymentUnitForm({ ...form, reconcile_mode: 'auto_apply' }, { protectedEnvironment: true }).error)
      .toContain('Protected environments');
  });

  it('fails deploy targeting clearly for ambiguity, runtime conflict, ownership, endpoint, and missing IDs', () => {
    const baseUnit = {
      id: 'unit-max',
      key: 'max',
      runtime_type: 'compose',
      endpoint_ref: 'max',
      compose_dir: '/srv/max',
      ownership_mode: 'bahia_managed',
      reconcile_mode: 'approval_required'
    };
    const multi = environment({ deployment_units: [baseUnit, { ...baseUnit, id: 'unit-east', key: 'east', endpoint_ref: 'east' }] });
    expect(deploymentTargetIssue({ runtime_type: 'compose' }, multi)).toContain('Select an explicit');
    expect(deploymentTargetIssue({ runtime_type: 'docker' }, multi, 'unit-max')).toContain('conflicts');
    expect(deploymentTargetIssue({ runtime_type: 'compose' }, environment({ deployment_units: [{ ...baseUnit, ownership_mode: '' }] }), 'unit-max')).toContain('Bahia-managed');
    expect(deploymentTargetIssue({ runtime_type: 'compose' }, environment({ deployment_units: [{ ...baseUnit, endpoint_ref: '' }] }), 'unit-max')).toContain('endpoint alias');
    expect(deploymentTargetIssue({ runtime_type: 'compose' }, environment({ deployment_units: [{ ...baseUnit, id: '' }] }))).toContain('durable ID');
  });

  it('maps backend endpoint and revision failures without exposing structured data', () => {
    expect(deploymentUnitErrorMessage({ code: -32009, message: 'conflict', data: { secret: 'hidden' } })).toContain('changed');
    expect(deploymentUnitErrorMessage(new Error('endpoint_ref unknown'))).toContain('endpoint alias');
    expect(deploymentUnitWriteShape({ ...form, runtime_config: { execution_mode: 'sdk' } })).not.toHaveProperty('execution_mode');
  });
});
