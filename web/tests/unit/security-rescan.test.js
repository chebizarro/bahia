import { describe, expect, it, vi } from 'vitest';
import { runSecurityRescan } from '../../src/lib/security-rescan.js';

function notifications() {
  return { success: vi.fn(), error: vi.fn() };
}

describe('security rescan outcomes', () => {
  it('confirms an accepted rescan to the operator', async () => {
    const notifier = notifications();
    const accepted = { status: 'accepted', run_id: 'run-1' };
    const rescan = vi.fn().mockResolvedValue(accepted);

    await expect(runSecurityRescan('target-1', { rescan, notifications: notifier })).resolves.toEqual({
      ok: true,
      result: accepted
    });
    expect(rescan).toHaveBeenCalledWith('target-1');
    expect(notifier.success).toHaveBeenCalledWith('Security rescan accepted');
    expect(notifier.error).not.toHaveBeenCalled();
  });

  it('returns an explicit failure and surfaces it to the operator', async () => {
    const notifier = notifications();
    const rescan = vi.fn().mockRejectedValue(new Error('scanner offline'));

    const outcome = await runSecurityRescan('target-1', { rescan, notifications: notifier });

    expect(outcome.ok).toBe(false);
    expect(outcome.error).toEqual(expect.objectContaining({ message: 'scanner offline' }));
    expect(notifier.error).toHaveBeenCalledWith('Security rescan failed: scanner offline');
    expect(notifier.success).not.toHaveBeenCalled();
  });
});
