import { describe, expect, it, vi } from 'vitest';
import { requestWithIdempotency } from '../../src/lib/nostr/contextvm-idempotency.js';

describe('ContextVM idempotency', () => {
  it('retries -32011 with the exact same progress token', async () => {
    const execute = vi.fn()
      .mockRejectedValueOnce(Object.assign(new Error('already accepted'), { code: -32011 }))
      .mockResolvedValueOnce({ result: 'replayed' });
    await expect(requestWithIdempotency({ operation: 'assistant/prompt', payload: { text: 'hello' } }, execute))
      .resolves.toEqual({ result: 'replayed' });
    expect(execute).toHaveBeenCalledTimes(2);
    const first = execute.mock.calls[0][0];
    const second = execute.mock.calls[1][0];
    expect(first.requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(second.requestId).toBe(first.requestId);
    expect(first.payload).toEqual(second.payload);
  });

  it('does not retry invalid requests or loop on repeated duplicates', async () => {
    const invalid = vi.fn().mockRejectedValue(Object.assign(new Error('invalid'), { code: -32600 }));
    await expect(requestWithIdempotency({}, invalid)).rejects.toThrow('invalid');
    expect(invalid).toHaveBeenCalledTimes(1);
    const duplicate = vi.fn().mockRejectedValue(Object.assign(new Error('duplicate'), { code: -32011 }));
    await expect(requestWithIdempotency({}, duplicate)).rejects.toThrow('duplicate');
    expect(duplicate).toHaveBeenCalledTimes(2);
    expect(duplicate.mock.calls[0][0].requestId).toBe(duplicate.mock.calls[1][0].requestId);
  });
});
