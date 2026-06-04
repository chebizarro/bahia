import { describe, expect, it } from 'vitest';
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { basename, dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '../../..');
const matrixPath = resolve(repoRoot, 'pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json');
const routesRoot = resolve(repoRoot, 'web/src/routes');

const matrix = JSON.parse(readFileSync(matrixPath, 'utf8'));
const allowedClasses = new Set(matrix.taxonomy);
const signerFirstClasses = new Set(matrix.signer_first_classes);
const apiRouteImportPattern = /(?:from\s+|import\s*\(\s*)['"]\$lib\/api\/[^'"]+['"]/;
const apiRouteImportScanPattern = /(?:from\s+|import\s*\(\s*)['"](\$lib\/api\/[^'"]+)['"]/g;
const mandatoryEntryIds = [
  'dashboard-read-models',
  'orgs-encrypted-crud-facade',
  'payments-encrypted-history',
  'notifications-encrypted-config-log',
  'services-public-controlplane',
  'environments-public-controlplane',
  'deployments-public-and-encrypted',
  'artifacts-registry-read-model',
  'artifacts-blossom-and-sbom-http',
  'packages-public-controlplane',
  'policies-public-controlplane',
  'workers-read-models-and-controls',
  'ml-rest-to-nostr-ingress',
  'ml-existing-endpoint-pin-signer-first',
  'llm-public-controlplane',
  'dns-public-controlplane',
  'backup-public-controlplane',
  'continuity-nostr-read-models',
  'souls-nostr-with-eose-caveats',
  'events-relay-activity',
  'settings-discovery-and-relay-config',
  'backend-rest-compatibility-routes'
];

function walkRouteFiles(dir = routesRoot) {
  const files = [];
  for (const entry of readdirSync(dir)) {
    const fullPath = resolve(dir, entry);
    const stats = statSync(fullPath);
    if (stats.isDirectory()) {
      files.push(...walkRouteFiles(fullPath));
      continue;
    }
    if (/\.(svelte|js|ts)$/.test(entry)) {
      files.push(fullPath);
    }
  }
  return files;
}

function repoRelative(path) {
  return relative(repoRoot, path).replaceAll('\\', '/');
}

function routeApiImports() {
  return walkRouteFiles()
    .map((file) => {
      const source = readFileSync(file, 'utf8');
      const imports = [...source.matchAll(apiRouteImportScanPattern)].map((match) => match[1]);
      return { file: repoRelative(file), imports: [...new Set(imports)].sort() };
    })
    .filter((entry) => entry.imports.length > 0);
}

function routePageFiles() {
  return walkRouteFiles()
    .map(repoRelative)
    .filter((file) => basename(file).startsWith('+page.'))
    .sort();
}

function routeFileClasses() {
  const byFile = new Map();
  for (const entry of matrix.entries) {
    for (const file of entry.route_files || []) {
      const classes = byFile.get(file) || new Set();
      classes.add(entry.transport_class);
      byFile.set(file, classes);
    }
  }
  return byFile;
}

describe('BAHIA_NOSTR_AUDIT_PARITY route transport matrix', () => {
  it('uses only the shared orchestration taxonomy and covers every audited route/control surface', () => {
    expect(matrix.taxonomy).toEqual([
      'nostr_native',
      'nostr_request_result_facade',
      'rest_to_nostr_bridge',
      'rest_compatibility',
      'http_native'
    ]);
    expect(matrix.signer_first_classes).toEqual(['nostr_native', 'nostr_request_result_facade']);

    const entryIds = matrix.entries.map((entry) => entry.id);
    const ids = new Set(entryIds);
    expect(ids.size, 'matrix entry IDs are unique').toBe(entryIds.length);
    expect(entryIds).toEqual(expect.arrayContaining(mandatoryEntryIds));

    for (const entry of matrix.entries) {
      expect(allowedClasses.has(entry.transport_class), `${entry.id} has a valid transport_class`).toBe(true);
      expect(entry.evidence?.length, `${entry.id} records repository-backed evidence`).toBeGreaterThan(0);
      expect(entry.pstf_status, `${entry.id} records PSTF status`).toBeTruthy();
    }
  });

  it('classifies every live SvelteKit page/loader and only references route files that exist', () => {
    const matrixRouteFiles = new Set(matrix.entries.flatMap((entry) => entry.route_files || []));

    for (const file of matrixRouteFiles) {
      expect(existsSync(resolve(repoRoot, file)), `${file} exists`).toBe(true);
    }

    const missingRoutePages = routePageFiles().filter((file) => !matrixRouteFiles.has(file));
    expect(missingRoutePages).toEqual([]);
  });

  it('keeps REST/API route imports explicit and limited to non-signer-first surfaces', () => {
    const actualImports = routeApiImports();
    const actualImportFiles = actualImports.map((entry) => entry.file).sort();
    const allowlistedFiles = matrix.route_file_rest_import_allowlist.map((entry) => entry.file).sort();

    expect(actualImportFiles).toEqual(allowlistedFiles);

    const actualByFile = new Map(actualImports.map((entry) => [entry.file, entry.imports]));
    for (const allow of matrix.route_file_rest_import_allowlist) {
      expect(allow.allowed_usage, `${allow.file} documents why REST import remains`).toBeTruthy();
      expect(allow.permitted_transport_classes.length, `${allow.file} has permitted classes`).toBeGreaterThan(0);
      expect(actualByFile.get(allow.file), `${allow.file} imports match the matrix allowlist`).toEqual([...allow.allowed_imports].sort());
      for (const transportClass of allow.permitted_transport_classes) {
        expect(allowedClasses.has(transportClass), `${allow.file} references valid class ${transportClass}`).toBe(true);
        expect(signerFirstClasses.has(transportClass), `${allow.file} must not allow REST imports for signer-first classes`).toBe(false);
      }
    }
  });

  it('records completed org and ML domain ingress decisions', () => {
    const orgs = matrix.entries.find((entry) => entry.id === 'orgs-encrypted-crud-facade');
    expect(orgs.transport_class).toBe('nostr_request_result_facade');
    expect(orgs.pstf_status).toBe('decision_recorded_2026-06-02');
    expect(orgs.resolved_by_beads).toEqual(['bahia-sv0j']);
    expect(orgs.evidence.join(' ')).toMatch(/encrypted request\/result facade|25910/);
    expect(orgs.evidence.join(' ')).toMatch(/without public org read-model projection|not nostr_native read models/);

    const ml = matrix.entries.find((entry) => entry.id === 'ml-rest-to-nostr-ingress');
    expect(ml.transport_class).toBe('rest_to_nostr_bridge');
    expect(ml.pstf_status).toBe('decision_recorded_2026-06-02');
    expect(ml.resolved_by_beads).toEqual(['bahia-jxm3']);
    expect(ml.rest_import_policy).toBe('allowed_rest_to_nostr_bridge');
    expect(ml.evidence.join(' ')).toMatch(/HTTP 202 is publish acceptance only/);

    const mlPin = matrix.entries.find((entry) => entry.id === 'ml-existing-endpoint-pin-signer-first');
    expect(mlPin.transport_class).toBe('nostr_native');
    expect(mlPin.route_files).toEqual(['web/src/routes/ml/+page.svelte']);
    expect(mlPin.evidence.join(' ')).toMatch(/publishCommand\(\).*workload\.pin\.request/);

    const orgDocs = readFileSync(resolve(repoRoot, 'docs/user-guide/features/organizations.md'), 'utf8');
    expect(orgDocs).toContain('encrypted request/result facade');
    expect(orgDocs).toContain('durable org state remains repository-backed');
    const mlPage = readFileSync(resolve(repoRoot, 'web/src/routes/ml/+page.svelte'), 'utf8');
    expect(mlPage).toContain('REST-to-Nostr bridge');
    expect(mlPage).toContain('HTTP acceptance is not completion');
  });

  it('fails if a pure signer-first route starts importing REST clients for command ingress', () => {
    const classesByFile = routeFileClasses();

    for (const [file, classes] of classesByFile.entries()) {
      const classList = [...classes];
      const isPureSignerFirst = classList.every((transportClass) => signerFirstClasses.has(transportClass));
      if (!isPureSignerFirst) continue;

      const fullPath = resolve(repoRoot, file);
      if (!existsSync(fullPath)) continue;
      const source = readFileSync(fullPath, 'utf8');
      expect(source, `${file} is classified as ${classList.join(', ')} and must not import REST clients`).not.toMatch(apiRouteImportPattern);
    }
  });
});
