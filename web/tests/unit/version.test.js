import { describe, expect, it } from 'vitest';
import * as versionModule from '../../src/lib/version.js';

const { buildInformationRows, webComponentVersion } = versionModule;

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

  it('never derives deployed state from discovery or build metadata', () => {
    expect(versionModule.observedDeploymentRows).toBeUndefined();
    const rows = buildInformationRows({
      observed_deployments: [{ service_name: 'legacy discovery row', observed_host: 'tcp://10.0.0.5:2376' }],
      versions: { components: [{ id: 'relay', name: 'Bahia relay', version: '0.1.0-static' }] }
    });
    expect(rows.map((row) => row.name)).toEqual(['Bahia web app', 'Bahia relay']);
    expect(JSON.stringify(rows)).not.toContain('10.0.0.5');
  });
});
