import { describe, expect, it } from 'vitest';
import Table from '../../src/lib/components/Table.svelte';
import { ArtifactIcon, ServiceIcon } from '../../src/lib/icons/domain-icons.js';
import { renderComponent, textOf, tick } from './utils/svelte-component-test';

describe('table icon regression', () => {
  it('renders direct icon components in table cells without invoking them as row callbacks', async () => {
    const target = renderComponent(Table, {
      columns: [
        { key: 'name', label: 'Name', icon: ServiceIcon, text: (row) => row.name }
      ],
      data: [{ name: 'bahia-web' }]
    });

    await tick();
    expect(textOf(target)).toContain('bahia-web');
    expect(target.querySelector('svg')).toBeTruthy();
  });

  it('still supports row-driven icon resolvers that return a component', async () => {
    const target = renderComponent(Table, {
      columns: [
        { key: 'artifact', label: 'Artifact', icon: (row) => (row.hasArtifact ? ArtifactIcon : null), text: (row) => row.artifact }
      ],
      data: [{ artifact: 'sha256:abc', hasArtifact: true }]
    });

    await tick();
    expect(textOf(target)).toContain('sha256:abc');
    expect(target.querySelector('svg')).toBeTruthy();
  });
});
