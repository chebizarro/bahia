import { execFile } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');

// Compilation is a suite prerequisite, not part of a relay's ready/health
// deadline. Every worker launches the same built artifact, not a go-run wrapper.
export default async function prepareRelayArtifact() {
  const directory = await mkdtemp(path.join(tmpdir(), 'bahia-playwright-relay-'));
  const binary = path.join(directory, process.platform === 'win32' ? 'bahia-test-relay.exe' : 'bahia-test-relay');
  const previous = process.env.BAHIA_TEST_RELAY_BIN;
  try {
    await promisify(execFile)('go', ['build', '-o', binary, './cmd/bahia-test-relay'], {
      cwd: repoRoot, env: process.env, timeout: 120_000, maxBuffer: 4 * 1024 * 1024
    });
  } catch (error) {
    await rm(directory, { recursive: true, force: true });
    throw new Error(`Bahia test relay build failed before browser startup:\n${error.stderr || error.message}`, { cause: error });
  }
  process.env.BAHIA_TEST_RELAY_BIN = binary;
  return async () => {
    if (previous === undefined) delete process.env.BAHIA_TEST_RELAY_BIN;
    else process.env.BAHIA_TEST_RELAY_BIN = previous;
    await rm(directory, { recursive: true, force: true });
  };
}
