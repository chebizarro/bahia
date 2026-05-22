import { describe, expect, it } from 'vitest';
import EmptyState from '../../src/lib/components/EmptyState.svelte';
import Table from '../../src/lib/components/Table.svelte';
import {
  ArtifactIcon,
  AudioFileIcon,
  GenericFileIcon,
  ImageFileIcon,
  JsonFileIcon,
  PdfFileIcon,
  ServiceIcon,
  TextFileIcon,
  VideoFileIcon,
  blossomContentTypeIcon
} from '../../src/lib/icons/domain-icons.js';
import { renderComponent, textOf } from './utils/svelte-component-test';

describe('shared icon primitives', () => {
  it('renders icon components without props-helper runtime crashes', () => {
    const target = renderComponent(ServiceIcon, {});
    const svg = target.querySelector('svg');

    expect(svg).not.toBeNull();
    expect(svg?.getAttribute('width')).toBe('24');
    expect(svg?.getAttribute('height')).toBe('24');
    expect(svg?.getAttribute('aria-hidden')).toBe('true');
  });

  it('accepts explicit icon props without relying on spread/rest props', () => {
    const target = renderComponent(ServiceIcon, { size: 18, strokeWidth: 1.75, className: 'custom-icon', ariaHidden: 'true' });
    const svg = target.querySelector('svg');

    expect(svg?.getAttribute('width')).toBe('18');
    expect(svg?.getAttribute('height')).toBe('18');
    expect(svg?.getAttribute('stroke-width')).toBe('1.75');
    expect(svg?.getAttribute('class')).toContain('custom-icon');
  });

  it('renders EmptyState iconComponent as a decorative SVG', () => {
    const target = renderComponent(EmptyState, {
      title: 'No services yet',
      message: 'Create a service to get started',
      iconComponent: ServiceIcon
    });

    expect(textOf(target)).toContain('No services yet');
    expect(textOf(target)).toContain('Create a service to get started');
    expect(target.querySelector('.icon[aria-hidden="true"] svg')).not.toBeNull();
  });

  it('keeps EmptyState legacy string icon fallback', () => {
    const target = renderComponent(EmptyState, {
      title: 'Legacy empty state',
      icon: 'LEGACY_ICON'
    });

    expect(textOf(target)).toContain('LEGACY_ICON');
    expect(target.querySelector('.icon svg')).toBeNull();
  });

  it('renders Table icon/text columns without using the HTML render path', () => {
    const target = renderComponent(Table, {
      columns: [
        {
          key: 'name',
          label: 'Name',
          icon: (row) => (row.kind === 'artifact' ? ArtifactIcon : ServiceIcon),
          text: (row) => row.name
        }
      ],
      data: [
        { name: 'api-service', kind: 'service' },
        { name: 'worker-image', kind: 'artifact' }
      ]
    });

    const text = textOf(target);
    expect(text).toContain('api-service');
    expect(text).toContain('worker-image');
    expect(target.querySelectorAll('td .cell-icon[aria-hidden="true"] svg')).toHaveLength(2);
    expect(target.querySelectorAll('.cell-with-icon')).toHaveLength(2);
  });
});

describe('Blossom MIME icon mapping', () => {
  it.each([
    ['image/png', ImageFileIcon],
    ['video/mp4', VideoFileIcon],
    ['audio/mpeg', AudioFileIcon],
    ['application/json', JsonFileIcon],
    ['application/activity+json', JsonFileIcon],
    ['text/plain', TextFileIcon],
    ['application/pdf', PdfFileIcon],
    ['application/octet-stream', GenericFileIcon],
    [undefined, GenericFileIcon]
  ])('maps %s to the expected icon component', (contentType, expectedIcon) => {
    expect(blossomContentTypeIcon(contentType)).toBe(expectedIcon);
  });
});
