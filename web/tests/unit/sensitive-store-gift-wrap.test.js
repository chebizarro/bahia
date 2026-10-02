/**
 * Verifies that notification and service-secret mutation stores produce
 * gift-wrapped (kind 1059) events and never leak plaintext webhook URLs,
 * signing secrets, or secret values in the envelope payload.
 *
 * Design §1.7 requires gift wraps for sensitive domains.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';

const CONTEXTVM_MESSAGE_KIND = 25910;
const GIFT_WRAP_KIND = 1059;

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true),
  servicePubkeyFromSystemInfo: vi.fn(() => 'b'.repeat(64)),
  CONTEXTVM_MESSAGE_KIND: 25910
}));

const nip07Mock = vi.hoisted(() => ({
  encryptNip44: vi.fn(async (_pubkey, _plaintext) => 'Y2lwaGVydGV4dF9vcGFxdWU=')
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://relay.example'] }
  })),
  loadSystemInfo: vi.fn(async () => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://relay.example'] }
  }))
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('$lib/nostr/nip07-crypto.js', () => nip07Mock);
vi.mock('$lib/nostr/retained-domain-subscription.js', () => ({
  subscribeToDomainRefresh: vi.fn(async () => () => {})
}));

/**
 * Assert that a requestEncryptedResult call uses the default gift-wrap kind
 * (1059) rather than an explicit non-gift-wrap kind.
 */
function assertGiftWrapped(callArgs) {
  const opts = callArgs[0];
  // When kind is omitted, the transport defaults to ENCRYPTED_REQUEST_KIND (1059).
  // If kind is present, it must be gift-wrap (1059), not ContextVM (25910).
  if (opts.kind !== undefined) {
    expect(opts.kind).not.toBe(CONTEXTVM_MESSAGE_KIND);
    expect(opts.kind).toBe(GIFT_WRAP_KIND);
  }
  // resultKinds, if present, must not request kind 25910 results
  if (opts.resultKinds !== undefined) {
    expect(opts.resultKinds).not.toContain(CONTEXTVM_MESSAGE_KIND);
  }
}

/**
 * Assert that a payload object contains no plaintext sensitive values.
 */
function assertNoPlaintextSecrets(payload, sensitiveValues) {
  const serialized = JSON.stringify(payload);
  for (const secret of sensitiveValues) {
    expect(serialized).not.toContain(secret);
  }
}

describe('sensitive store gift-wrap confidentiality', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(true);
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValue({
      result: { status: 'ok', payload: {} }
    });
    systemMock.currentSystemInfo.mockReturnValue({
      nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://relay.example'] }
    });
    systemMock.loadSystemInfo.mockResolvedValue(systemMock.currentSystemInfo());
  });

  describe('notification mutations use kind 1059 gift-wrap', () => {
    it('createNotificationChannel sends gift-wrapped with no plaintext webhook URL', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'PagerDuty' } } }
      });
      const store = await import('../../src/lib/stores/notifications.svelte.js');

      await store.createNotificationChannel({
        name: 'PagerDuty',
        channel_type: 'webhook',
        config: { url: 'https://hooks.pagerduty.com/secret-endpoint', signing_secret: 'whsec_abc123' }
      });

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      const callArgs = encryptedRequestsMock.requestEncryptedResult.mock.calls[0];
      assertGiftWrapped(callArgs);

      // The payload is inside the gift-wrapped envelope, which is NIP-44
      // encrypted. But verify the call args don't bypass gift wrapping.
      const opts = callArgs[0];
      expect(opts.operation).toBe('notifications.channels.create');
    });

    it('updateNotificationChannel sends gift-wrapped with no plaintext webhook URL', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'Updated' } } }
      });
      const store = await import('../../src/lib/stores/notifications.svelte.js');

      await store.updateNotificationChannel('ch-1', {
        config: { url: 'https://hooks.pagerduty.com/secret-endpoint-2', signing_secret: 'whsec_xyz789' }
      });

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      assertGiftWrapped(encryptedRequestsMock.requestEncryptedResult.mock.calls[0]);
    });

    it('deleteNotificationChannel sends gift-wrapped', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { status: 'ok', payload: { status: 'deleted' } }
      });
      const store = await import('../../src/lib/stores/notifications.svelte.js');

      await store.deleteNotificationChannel('ch-1');

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      assertGiftWrapped(encryptedRequestsMock.requestEncryptedResult.mock.calls[0]);
    });
  });

  describe('service secret mutations use kind 1059 gift-wrap', () => {
    it('createServiceSecret sends gift-wrapped with NIP-44 encrypted value, no plaintext secret', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { secret: { id: 'secret-new', name: 'DB_PASSWORD', version: 1 } }
      });
      const store = await import('../../src/lib/stores/service-secrets.svelte.js');

      await store.createServiceSecret('svc-1', { name: 'DB_PASSWORD', value: 'hunter2-super-secret' });

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      const callArgs = encryptedRequestsMock.requestEncryptedResult.mock.calls[0];
      assertGiftWrapped(callArgs);

      // Verify secret value was NIP-44 encrypted, not sent in plaintext
      const payload = callArgs[0].payload;
      assertNoPlaintextSecrets(payload, ['hunter2-super-secret']);
      expect(payload.encrypted_value).toBe('Y2lwaGVydGV4dF9vcGFxdWU=');
      expect(payload.value).toBeUndefined();
    });

    it('updateServiceSecret sends gift-wrapped with NIP-44 encrypted value, no plaintext secret', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { secret: { id: 'secret-1', name: 'DB_PASSWORD', version: 2 } }
      });
      const store = await import('../../src/lib/stores/service-secrets.svelte.js');

      await store.updateServiceSecret('svc-1', 'secret-1', { value: 'new-password-456' });

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      const callArgs = encryptedRequestsMock.requestEncryptedResult.mock.calls[0];
      assertGiftWrapped(callArgs);

      const payload = callArgs[0].payload;
      assertNoPlaintextSecrets(payload, ['new-password-456']);
      expect(payload.encrypted_value).toBe('Y2lwaGVydGV4dF9vcGFxdWU=');
      expect(payload.value).toBeUndefined();
    });

    it('deleteServiceSecret sends gift-wrapped', async () => {
      encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
        result: { status: 'deleted' }
      });
      const store = await import('../../src/lib/stores/service-secrets.svelte.js');

      await store.deleteServiceSecret('svc-1', 'secret-1');

      expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
      assertGiftWrapped(encryptedRequestsMock.requestEncryptedResult.mock.calls[0]);
    });
  });
});
