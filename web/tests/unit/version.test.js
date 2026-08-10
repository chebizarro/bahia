import { describe, expect, it } from 'vitest';
import {
  buildInformationRows,
  observedDeploymentRows,
  webComponentVersion
} from '../../src/lib/version.js';

describe('version helpers', () => {
  it('exposes the web app as compile-time build information', () => {
    expect(webComponentVersion).toMatchObject({
      id: 'web',
      name: 'Bahia web app',
      kind: 'frontend',
      packaged_as: 'web/Dockerfile'
    });
    expect(webComponentVersion.version).toMatch(/^0\.1\.0-/);
  });

  it('keeps build information separate when backend discovery has no versions field', () => {
    const rows = buildInformationRows({ features: { relay_read_models: true } });

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: 'web', kind: 'frontend' });
  });

  it('combines frontend and backend compile-time metadata as build information', () => {
    const rows = buildInformationRows({
      versions: {
        backend: '0.1.0-abcdef',
        components: [
          { id: 'backend', name: 'Bahia backend', kind: 'backend', packaged_as: 'cmd/server', version: '0.1.0-abcdef', base: '0.1.0', commit: 'abcdef' },
          { id: 'relay', name: 'Bahia relay', kind: 'service', packaged_as: 'cmd/relay', version: '0.1.0-abcdef', base: '0.1.0', commit: 'abcdef' }
        ]
      }
    });

    expect(rows.map((row) => row.id)).toEqual(['web', 'backend', 'relay']);
    expect(rows.find((row) => row.id === 'backend')).toMatchObject({
      name: 'Bahia backend',
      version: '0.1.0-abcdef',
      packaged_as: 'cmd/server'
    });
  });

  it('maps and deterministically sorts authoritative observed deployments', () => {
    const rows = observedDeploymentRows({
      observed_deployments: [
        {
          service_id: 'service-relay',
          service_name: 'Bahia relay',
          environment_id: 'env-prod',
          environment_name: 'production',
          runtime_type: 'compose',
          observed_image_repo: 'ghcr.io/openagentsinc/bahia-relay',
          observed_image_digest: 'sha256:relay',
          health_status: 'healthy',
          drift_status: 'drifted',
          observed_host: 'relay-01',
          observed_at: '2026-08-08T12:01:00Z'
        },
        {
          service_id: 'service-backend',
          service_name: 'Bahia backend',
          environment_id: 'env-prod',
          environment_name: 'production',
          runtime_target: 'docker',
          observed_version: '0.2.0-backend',
          observed_image_repo: 'ghcr.io/openagentsinc/bahia',
          observed_image_digest: 'sha256:backend',
          health_status: 'healthy',
          drift_status: 'in_sync',
          observed_host: 'backend-01',
          observed_at: '2026-08-08T12:00:00Z'
        }
      ]
    });

    expect(rows.map((row) => row.name)).toEqual(['Bahia backend', 'Bahia relay']);
    expect(rows[0]).toMatchObject({
      environment: 'production',
      kind: 'docker',
      version: '0.2.0-backend',
      image: 'ghcr.io/openagentsinc/bahia@sha256:backend',
      host: 'backend-01',
      health: 'healthy',
      drift: 'in_sync'
    });
    expect(rows[1]).toMatchObject({
      version: 'sha256:relay',
      image: 'ghcr.io/openagentsinc/bahia-relay@sha256:relay'
    });
  });

  it('does not infer observed deployments from static build metadata', () => {
    const rows = observedDeploymentRows({
      versions: {
        components: [{ id: 'relay', name: 'Bahia relay', version: '0.1.0-static' }]
      }
    });

    expect(rows).toEqual([]);
  });
});
