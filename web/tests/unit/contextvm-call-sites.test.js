import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const sourceRoot = join(process.cwd(), 'src');
const directCall = /\b(?:requestEncryptedResult|publishEncryptedRequest)\s*\(/;
const transportImport = /from\s+['"][^'"]*encrypted-controlplane(?:-transport)?\.js['"]|import\s*\(\s*['"][^'"]*encrypted-controlplane(?:-transport)?\.js['"]\s*\)/;

function isTransportCallSite(path) {
  const source = readFileSync(path, 'utf8');
  const relativePath = relative(sourceRoot, path);
  if (relativePath === 'lib/stores/auth.svelte.js') {
    expect(source.match(/encrypted-controlplane\.js/g)).toHaveLength(1);
    expect(source).toMatch(/import\(['"]\$lib\/nostr\/encrypted-controlplane\.js['"]\)\.then\(\(\{ disconnectEncryptedControlplane \}\)/);
    return directCall.test(source);
  }
  return directCall.test(source) || transportImport.test(source);
}

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(?:js|svelte)$/.test(entry.name) ? [path] : [];
  });
}

describe('remaining ContextVM call sites', () => {
  it('allows only the interactive exceptions and shared transport', () => {
    const actual = sourceFiles(sourceRoot).filter(isTransportCallSite)
      .map(path => relative(sourceRoot, path)).sort();
    expect(actual).toEqual([
      'lib/components/assistant/AssistantExecutionReconciliation.svelte',
      'lib/nostr/encrypted-controlplane-transport.js',
      'lib/nostr/encrypted-controlplane.js',
      'lib/stores/assistant.svelte.js',
      'lib/stores/deployment-run-logs.svelte.js',
      'lib/stores/service-secrets.svelte.js'
    ]);
    expect(readFileSync(join(sourceRoot, 'lib/nostr/assistant.js'), 'utf8')).toMatch(/await request\(\{ \.\.\.built, signal, timeoutMs \}\)/);
    const secrets = readFileSync(join(sourceRoot, 'lib/stores/service-secrets.svelte.js'), 'utf8');
    expect(secrets).toMatch(/reveal:\s*['"]services\.secrets\.reveal['"]/);
    expect([...secrets.matchAll(/encryptedSecretRequest\(/g)]).toHaveLength(2);
  });

  it('does not send migrated operations through a ContextVM call site', () => {
    const callSites = sourceFiles(sourceRoot).filter(isTransportCallSite);
    const migratedOperations = /['"](?:artifact\/(?:register|import-observed|signature-verify|register-build-result)|adoption\/(?:import|scan)|dns\/drift-remediate|deployment\/(?:preview|route-attach)|policy\/evaluate|service\/deploy-preview|build\/request|security\/(?:scan|rescan)|sbom\/(?:generate|import)|relay\/policy-set|notification\/channel-test|environment\/worker-policy-apply|ml\/(?:model-import|inference-deploy|pin))['"]/;
    for (const path of callSites) {
      expect(readFileSync(path, 'utf8'), relative(sourceRoot, path)).not.toMatch(migratedOperations);
    }
  });
});
