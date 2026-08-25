import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const __dirname = dirname(fileURLToPath(import.meta.url));
const settingsSource = readFileSync(
  resolve(__dirname, '../../src/routes/settings/+page.svelte'),
  'utf8'
);

const sectionIndex = (label) => settingsSource.indexOf(label);

describe('settings page section order', () => {
  it('keeps operational configuration and registries before version metadata', () => {
    const operationalSettings = sectionIndex('<!-- Operational Settings Section -->');
    const serverConfiguration = sectionIndex('<!-- Server Configuration Section -->');
    const availableRegistries = sectionIndex('<!-- Available Registries Section -->');
    const versions = sectionIndex('<!-- Version Section -->');

    expect(operationalSettings).toBeGreaterThan(-1);
    expect(serverConfiguration).toBeGreaterThan(-1);
    expect(availableRegistries).toBeGreaterThan(-1);
    expect(versions).toBeGreaterThan(-1);

    expect(operationalSettings).toBeLessThan(versions);
    expect(serverConfiguration).toBeLessThan(versions);
    expect(availableRegistries).toBeLessThan(versions);
  });

  it('keeps documented settings surfaces visible from the settings page', () => {
      expect(settingsSource).toContain("href: '/settings/profile'");
      expect(settingsSource).toContain("href: '/settings/fleet'");
    expect(settingsSource).toContain("href: '/notifications'");
    expect(settingsSource).toContain("href: '/notifications/log'");
    expect(settingsSource).toContain('Container Registry');
    expect(settingsSource).toContain('Blossom Storage');
    expect(settingsSource).toContain('Runtime');
    expect(settingsSource).toContain('Features');
    expect(settingsSource).toContain('Feature discovery');
    expect(settingsSource).toContain('Available Registries');
  });
});
