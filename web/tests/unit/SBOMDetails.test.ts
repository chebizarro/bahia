import { describe, expect, it } from 'vitest';
import SBOMDetails from '../../src/lib/components/SBOMDetails.svelte';
import { renderComponent, textOf, tick } from './utils/svelte-component-test';

function render(props: Record<string, unknown> = {}) {
  return renderComponent(SBOMDetails, props);
}

describe('SBOMDetails.svelte', () => {
  it('shows a loading state while SBOM data is being fetched', () => {
    const target = render({ loading: true });

    expect(textOf(target)).toContain('Loading SBOM details...');
  });

  it('shows an empty state when no SBOM, attestation, or packages are available', () => {
    const target = render();

    const text = textOf(target);
    expect(text).toContain('No SBOM available');
    expect(text).toContain('This artifact does not have an SBOM or it has not been ingested yet');
  });

  it('renders attestation details, hashes, vulnerability badges, and package counts', () => {
    const target = render({
      sbom: {
        format: 'spdx',
        generator: { id: 'syft', version: '1.2.3' },
        source_url: 'blossom://sboms/example.spdx.json',
        raw_hash: '0123456789abcdef00112233445566778899aabbccddeeff76543210',
        created_at: '2026-05-13T12:00:00Z',
        package_count: 2,
        vulnerability_count: 3,
        critical_count: 1,
        high_count: 2
      },
      packages: [
        { name: 'lodash', version: '4.17.21', ecosystem: 'npm', license: 'MIT' },
        { name: 'openssl', version: '3.2.0', ecosystem: 'apk', license: 'Apache-2.0' }
      ]
    });

    const text = textOf(target);
    expect(text).toContain('Attestation Details');
    expect(text).toContain('SPDX');
    expect(text).toContain('syft@1.2.3');
    expect(text).toContain('Storage Blossom');
    expect(text).toContain('blossom://sboms/example.spdx.json');
    expect(text).toContain('0123456789abcdef...76543210');
    expect(text).toContain('Package Count 2');
    expect(text).toContain('3 total');
    expect(text).toContain('1 critical');
    expect(text).toContain('2 high');
  });

  it('prefers attestation predicate NTIA compliance and marks compliant fields', () => {
    const target = render({
      attestation: {
        predicate: {
          format: 'cyclonedx',
          ntia: {
            isCompliant: true,
            hasSupplierName: true,
            hasComponentName: true,
            hasComponentVersion: true,
            hasUniqueID: true,
            hasRelationship: true,
            hasAuthor: true,
            hasTimestamp: true
          }
        }
      }
    });

    const text = textOf(target);
    expect(text).toContain('NTIA Minimum Elements');
    expect(text).toContain('Compliant');
    expect(text).toContain('Supplier Name');
    expect(text).toContain('Dependency Relationships');
    expect(target.querySelectorAll('.ntia-item.passed')).toHaveLength(7);
    expect(target.querySelectorAll('.ntia-item.failed')).toHaveLength(0);
  });

  it('renders partial NTIA compliance from the SBOM fallback data', () => {
    const target = render({
      sbom: {
        format: 'cyclonedx',
        ntia: {
          isCompliant: false,
          hasSupplierName: true,
          hasComponentName: true,
          hasComponentVersion: false,
          hasUniqueID: true,
          hasRelationship: false,
          hasAuthor: false,
          hasTimestamp: false
        }
      }
    });

    const text = textOf(target);
    expect(text).toContain('3/7 Fields');
    expect(target.querySelectorAll('.ntia-item.passed')).toHaveLength(3);
    expect(target.querySelectorAll('.ntia-item.failed')).toHaveLength(4);
  });

  it('renders package table rows and safely handles missing package fields', async () => {
    const target = render({
      packages: [
        {
          name: 'very-long-package',
          version: '1.0.0',
          ecosystem: 'npm',
          license: 'MIT',
          purl: 'pkg:npm/@acme/very-long-package-name-that-should-be-truncated@1.0.0'
        },
        { name: 'missing-fields' }
      ]
    });

    await tick();

    const text = textOf(target);
    expect(text).toContain('Packages (2)');
    expect(text).toContain('Package Name');
    expect(text).toContain('very-long-package');
    expect(text).toContain('pkg:npm/@acme/very-long-package-name-tha...');
    expect(text).toContain('missing-fields');
    expect(text).toContain('-');
  });

  it('handles sparse SBOM objects without optional metadata', () => {
    const target = render({ sbom: {} });

    const text = textOf(target);
    expect(text).toContain('Attestation Details');
    expect(text).toContain('Format Unknown');
    expect(text).toContain('Package Count 0');
  });
});
