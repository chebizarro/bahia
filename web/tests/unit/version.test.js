import { describe, expect, it } from 'vitest';
import { componentVersionRows, webComponentVersion } from '../../src/lib/version.js';

describe('component version helpers', () => {
  it('exposes the web app as its own packaged frontend component', () => {
    expect(webComponentVersion).toMatchObject({
      id: 'web',
      name: 'Bahia web app',
      kind: 'frontend',
      packaged_as: 'web/Dockerfile'
    });
    expect(webComponentVersion.version).toMatch(/^0\.1\.0-/);
  });

  it('renders only the web row when backend discovery has no versions field', () => {
    const rows = componentVersionRows({ features: { relay_read_models: true } });

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: 'web', kind: 'frontend' });
  });

  it('combines web and backend discovery component versions for settings display', () => {
    const rows = componentVersionRows({
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
});
