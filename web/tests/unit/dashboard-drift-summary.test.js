import { describe, it, expect } from 'vitest';
import { summarizeDriftCause, shortHash } from '../../src/routes/dashboard-drift-summary.js';

describe('shortHash', () => {
  it('returns empty string for missing hashes', () => {
    expect(shortHash('')).toBe('');
    expect(shortHash(null)).toBe('');
    expect(shortHash(undefined)).toBe('');
  });

  it('leaves short hashes untouched', () => {
    expect(shortHash('abc123')).toBe('abc123');
  });

  it('truncates long hashes with an ellipsis', () => {
    expect(shortHash('0123456789abcdef', 12)).toBe('0123456789ab…');
  });
});

describe('summarizeDriftCause', () => {
  it('reports in_sync states as healthy', () => {
    const result = summarizeDriftCause({ drift_status: 'in_sync' });
    expect(result.reason).toBe('in_sync');
    expect(result.severity).toBe('success');
    expect(result.headline).toBe('In sync');
  });

  it('flags drifted states with no observation', () => {
    const result = summarizeDriftCause({ drift_status: 'drifted', desired_hash: 'abc' });
    expect(result.reason).toBe('no_observation');
    expect(result.severity).toBe('error');
    expect(result.hashesMatch).toBeNull();
  });

  it('detects a desired/observed hash mismatch', () => {
    const result = summarizeDriftCause({
      drift_status: 'drifted',
      desired_hash: 'aaaa',
      observed_hash: 'bbbb',
      current_observation_id: 'obs-1'
    });
    expect(result.reason).toBe('hash_mismatch');
    expect(result.severity).toBe('error');
    expect(result.hashesMatch).toBe(false);
    expect(result.desiredHash).toBe('aaaa');
    expect(result.observedHash).toBe('bbbb');
  });

  it('treats matching hashes on a drifted state as reconcile pending', () => {
    const result = summarizeDriftCause({
      drift_status: 'drifted',
      desired_hash: 'same',
      observed_hash: 'same',
      current_observation_id: 'obs-1'
    });
    expect(result.reason).toBe('reconcile_pending');
    expect(result.severity).toBe('warning');
    expect(result.hashesMatch).toBe(true);
  });

  it('treats an observed hash alone as evidence of an observation', () => {
    const result = summarizeDriftCause({
      drift_status: 'drifted',
      observed_hash: 'obs-hash'
    });
    // desired hash missing -> generic drift, but observation exists
    expect(result.reason).toBe('drift_detected');
  });

  it('falls back to a generic drift message when hashes are unavailable', () => {
    const result = summarizeDriftCause({
      drift_status: 'drifted',
      current_observation_id: 'obs-1'
    });
    expect(result.reason).toBe('drift_detected');
    expect(result.severity).toBe('error');
  });

  it('handles unknown / missing drift status', () => {
    expect(summarizeDriftCause({}).reason).toBe('unknown');
    expect(summarizeDriftCause({ drift_status: 'weird' }).headline).toBe('Drift status: weird');
    expect(summarizeDriftCause({ drift_status: 'weird' }).severity).toBe('default');
  });
});
