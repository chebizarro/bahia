import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const routeSource = (path) => readFileSync(resolve(process.cwd(), path), 'utf8');

const environmentDetailSource = routeSource('src/routes/environments/[id]/+page.svelte');
const backupDetailSource = routeSource('src/routes/backup/[section]/[id]/+page.svelte');
const eventsSource = routeSource('src/routes/events/+page.svelte');

describe('hidden operational UI surfaces', () => {
  it('keeps environment runtime configuration visible when runtime config is empty', () => {
    expect(environmentDetailSource).toContain('Runtime Configuration');
    expect(environmentDetailSource).toContain('No runtime configuration');
    expect(environmentDetailSource).toContain('This environment does not currently have runtime configuration projected.');
    expect(environmentDetailSource).toContain('runtimeConfigEmpty');
    expect(environmentDetailSource).toContain('runtimeConfigIsEmpty');
    expect(environmentDetailSource).not.toContain('environment.runtime_config && Object.keys(environment.runtime_config).length > 0');
  });

  it('keeps repository backend capability visibility when no capabilities are advertised', () => {
    expect(backupDetailSource).toContain('Advertised backend capabilities');
    expect(backupDetailSource).toContain('No backend capabilities advertised');
    expect(backupDetailSource).toContain('This repository record does not advertise backend capability metadata yet.');
    expect(backupDetailSource).toContain("{#if section === 'repositories'}");
    expect(backupDetailSource).not.toContain("section === 'repositories' && capabilities.length > 0");
  });

  it('keeps relay provenance visible when no relays are available', () => {
    expect(eventsSource).toContain('relayProvenance');
    expect(eventsSource).toContain('No relays advertised/configured/connected.');
    expect(eventsSource).toContain('{relayProvenance}');
    expect(eventsSource).not.toContain('{#if controlplaneConnection.relays.length > 0}');
  });
});
