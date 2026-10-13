import { describe, expect, it } from 'vitest';
import { artifactVersionLabel, userFacingSBOMError, userFacingVerifyError } from '../../src/routes/artifacts/[id]/presentation.js';

describe('artifact detail presentation', () => {
  it('never repeats a display name as the version', () => {
    expect(artifactVersionLabel({ name: 'release', version: 'release', image_tag: 'v2' }, 'release')).toBe('v2');
    expect(artifactVersionLabel({ image_tag: 'v2' }, 'v2')).toBe('-');
    expect(artifactVersionLabel({ name: 'release', version: 'v3' }, 'release')).toBe('v3');
  });

  it('does not surface transport or protocol errors to users', () => {
    for (const error of [
      new Error('ContextVM method not found'),
      new Error('NIP-46 signer-first handshake failed'),
      new Error('kind 25910 intent failed')
    ]) {
      expect(userFacingVerifyError(error)).not.toMatch(/ContextVM|NIP-|signer-first|method not found|kind \d+|intent/i);
      expect(userFacingSBOMError(error)).not.toMatch(/ContextVM|NIP-|signer-first|method not found|kind \d+|intent/i);
    }
    expect(userFacingVerifyError(new Error('Registry unreachable'))).toBe('Registry unreachable');
  });
});
