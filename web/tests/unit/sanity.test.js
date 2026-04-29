import { describe, it, expect } from 'vitest';

describe('Sanity Tests', () => {
  it('should pass a basic assertion', () => {
    expect(true).toBe(true);
  });

  it('should perform arithmetic correctly', () => {
    expect(2 + 2).toBe(4);
  });

  it('should have access to browser globals', () => {
    expect(typeof localStorage).toBe('object');
    expect(typeof fetch).toBe('function');
  });
});
