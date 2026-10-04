import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const sourceRoot = join(process.cwd(), 'src');
const directCall = /\b(?:requestEncryptedResult|publishCommand|publishEncryptedRequest)\s*\(/;

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(?:js|svelte)$/.test(entry.name) ? [path] : [];
  });
}

describe('remaining ContextVM call sites', () => {
  it('keeps the current transport and unmigrated operations explicit', () => {
    const actual = sourceFiles(sourceRoot).filter(path => directCall.test(readFileSync(path, 'utf8')))
      .map(path => relative(sourceRoot, path)).sort();
    expect(actual).toEqual([
      'lib/nostr/encrypted-controlplane-transport.js',
      'lib/nostr/encrypted-controlplane.js',
      'lib/nostr/relay-settings-controlplane.js',
      'lib/stores/arcana-build.js',
      'lib/stores/artifact-signatures.svelte.js',
      'lib/stores/assistant.svelte.js',
      'lib/stores/deployment-run-logs.svelte.js',
      'lib/stores/notifications.svelte.js',
      'lib/stores/public-controlplane.svelte.js',
      'lib/stores/security.svelte.js',
      'lib/stores/service-secrets.svelte.js',
      'routes/environments/[id]/+page.svelte',
      'routes/ml/+page.svelte'
    ]);
  });

  it('does not send migrated operations through a ContextVM call site', () => {
    const callSites = sourceFiles(sourceRoot).filter(path => directCall.test(readFileSync(path, 'utf8')));
    const migratedOperations = /['"](?:artifact\/register|artifact\/import-observed|adoption\/import|dns\/drift-remediate|deployment\/preview|deployment\/route-attach|policy\/evaluate|service\/deploy-preview)['"]/;
    for (const path of callSites) {
      expect(readFileSync(path, 'utf8'), relative(sourceRoot, path)).not.toMatch(migratedOperations);
    }
  });
});
