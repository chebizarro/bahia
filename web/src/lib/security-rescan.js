import { toast } from '$lib/components/toast.js';
import { rescanSecurityTarget } from '$lib/stores/security.svelte.js';

export async function runSecurityRescan(
  targetKeyHash,
  { rescan = rescanSecurityTarget, notifications = toast } = {}
) {
  if (!targetKeyHash) {
    const error = new Error('Security rescan target is required');
    notifications.error(error.message);
    return { ok: false, error };
  }

  try {
    const result = await rescan(targetKeyHash);
    notifications.success('Security rescan accepted');
    return { ok: true, result };
  } catch (error) {
    const normalized = error instanceof Error ? error : new Error(String(error));
    notifications.error(`Security rescan failed: ${normalized.message}`);
    return { ok: false, error: normalized };
  }
}
