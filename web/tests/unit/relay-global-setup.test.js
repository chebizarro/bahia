import { access } from 'node:fs/promises';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { execFileMock } = vi.hoisted(() => ({ execFileMock: vi.fn() }));
vi.mock('node:child_process', async importOriginal => {
  const actual = await importOriginal();
  const execFile = (...args) => execFileMock(...args);
  return { ...actual, execFile, default: { ...actual.default, execFile } };
});
import prepareRelayArtifact from '../e2e/global-setup.js';

const previous = process.env.BAHIA_TEST_RELAY_BIN;
afterEach(() => {
  if (previous === undefined) delete process.env.BAHIA_TEST_RELAY_BIN;
  else process.env.BAHIA_TEST_RELAY_BIN = previous;
  vi.clearAllMocks();
});

describe('relay artifact suite prerequisite', () => {
  it('does not publish an artifact until compilation completes and removes it at teardown', async () => {
    delete process.env.BAHIA_TEST_RELAY_BIN;
    let complete;
    let signalBuild;
    const buildStarted = new Promise(resolve => { signalBuild = resolve; });
    execFileMock.mockImplementation((_command, _args, _options, callback) => { complete = callback; signalBuild(); });
    const preparation = prepareRelayArtifact();
    await buildStarted;
    expect(execFileMock).toHaveBeenCalledOnce();
    expect(process.env.BAHIA_TEST_RELAY_BIN).toBeUndefined();
    const [command, args, options] = execFileMock.mock.calls[0];
    expect(command).toBe('go');
    expect(args).toEqual(['build', '-o', expect.any(String), './cmd/bahia-test-relay']);
    expect(options).toMatchObject({ timeout: 120_000 });
    complete(null, '', '');
    const teardown = await preparation;
    expect(process.env.BAHIA_TEST_RELAY_BIN).toBe(args[2]);
    await access(path.dirname(args[2]));
    await teardown();
    await expect(access(path.dirname(args[2]))).rejects.toThrow();
    expect(process.env.BAHIA_TEST_RELAY_BIN).toBeUndefined();
  });

  it('fails with compiler diagnostics before any relay readiness wait and cleans its directory', async () => {
    delete process.env.BAHIA_TEST_RELAY_BIN;
    execFileMock.mockImplementation((_command, _args, _options, callback) => {
      callback(Object.assign(new Error('compiler exited'), { stderr: 'fixture compiler diagnostic' }));
    });
    await expect(prepareRelayArtifact()).rejects.toThrow('fixture compiler diagnostic');
    const binary = execFileMock.mock.calls[0][1][2];
    await expect(access(path.dirname(binary))).rejects.toThrow();
    expect(process.env.BAHIA_TEST_RELAY_BIN).toBeUndefined();
  });
});
