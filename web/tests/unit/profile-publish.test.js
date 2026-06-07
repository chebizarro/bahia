import { describe, it, expect, beforeEach, vi } from 'vitest';

const authMock = vi.hoisted(() => ({
  authState: {
    status: 'authenticated',
    pubkey: 'a'.repeat(64),
    relays: {
      'wss://write.example': { read: true, write: true },
      'wss://read.example': { read: true, write: false }
    }
  },
  signWithAuth: vi.fn(),
  updateAuthProfile: vi.fn((profile) => profile)
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('../../src/lib/stores/auth.js', () => authMock);

function createClient({ connectSummary = { total: 1, connected: 1, failed: 0 }, publishResults = [] } = {}) {
  return {
    connect: vi.fn(async () => connectSummary),
    publish: vi.fn(async () => publishResults),
    disconnect: vi.fn()
  };
}

describe('Nostr profile publisher', () => {
  let profile;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    authMock.authState.status = 'authenticated';
    authMock.authState.pubkey = 'a'.repeat(64);
    authMock.authState.relays = {
      'wss://write.example': { read: true, write: true },
      'wss://read.example': { read: true, write: false }
    };
    authMock.signWithAuth.mockImplementation(async (event) => ({
      ...event,
      id: 'kind0-event-id',
      sig: 'signature'
    }));
    authMock.updateAuthProfile.mockImplementation((metadata) => metadata);
    profile = await import('../../src/lib/nostr/profile.js');
  });

  it('validates and normalizes editable kind-0 profile fields', () => {
    const valid = profile.validateProfileMetadata({
      name: ' alice ',
      displayName: 'Alice Example',
      about: 'Operator',
      picture: 'https://example.com/avatar.png',
      banner: 'https://example.com/banner.png',
      website: 'https://example.com',
      nip05: 'alice@example.com',
      lud16: 'tips@example.com'
    });

    expect(valid).toEqual({
      valid: true,
      errors: {},
      metadata: {
        name: 'alice',
        display_name: 'Alice Example',
        about: 'Operator',
        picture: 'https://example.com/avatar.png',
        banner: 'https://example.com/banner.png',
        website: 'https://example.com',
        nip05: 'alice@example.com',
        lud16: 'tips@example.com'
      }
    });

    const invalid = profile.validateProfileMetadata({
      name: 'x'.repeat(65),
      picture: 'notaurl',
      nip05: 'not-an-identifier'
    });

    expect(invalid.valid).toBe(false);
    expect(invalid.errors).toMatchObject({
      name: expect.stringContaining('64'),
      picture: expect.stringContaining('URL'),
      nip05: expect.stringContaining('name@example.com')
    });
  });

  it('signs and publishes kind-0 metadata through the authenticated signer and requires relay OK accepted outcomes', async () => {
    const client = createClient({
      publishResults: [
        { relay: 'wss://write.example/', sent: true, accepted: true, message: 'stored' },
        { relay: 'wss://other.example', sent: true, accepted: false, message: 'rate limited' }
      ]
    });
    const clientFactory = vi.fn(() => client);

    const result = await profile.publishProfileMetadata({
      name: 'alice',
      display_name: 'Alice Example',
      about: 'Maintainer'
    }, {
      now: () => 1_714_521_600_000,
      clientFactory
    });

    expect(clientFactory).toHaveBeenCalledWith({ relays: ['wss://write.example/'], saveRelayConfig: expect.any(Function) });
    expect(client.connect).toHaveBeenCalledWith(['wss://write.example/'], { force: true });
    expect(authMock.signWithAuth).toHaveBeenCalledWith({
      kind: 0,
      pubkey: authMock.authState.pubkey,
      created_at: 1714521600,
      tags: [],
      content: JSON.stringify({ name: 'alice', display_name: 'Alice Example', about: 'Maintainer' })
    });
    expect(client.publish).toHaveBeenCalledWith(expect.objectContaining({ id: 'kind0-event-id', kind: 0 }));
    expect(result.acceptedRelays).toEqual([{ relay: 'wss://write.example/', sent: true, accepted: true, message: 'stored' }]);
    expect(result.rejectedRelays).toEqual([{ relay: 'wss://other.example', sent: true, accepted: false, message: 'rate limited' }]);
    expect(authMock.updateAuthProfile).toHaveBeenCalledWith({ name: 'alice', display_name: 'Alice Example', about: 'Maintainer' });
    expect(client.disconnect).toHaveBeenCalledTimes(1);
  });

  it('rejects validation failures before signing or publishing', async () => {
    await expect(profile.publishProfileMetadata({ website: 'not a url' }, { clientFactory: vi.fn() }))
      .rejects.toMatchObject({ message: 'Profile metadata validation failed' });

    expect(authMock.signWithAuth).not.toHaveBeenCalled();
  });

  it('fails when every relay rejects the kind-0 publish OK outcome', async () => {
    const client = createClient({
      publishResults: [
        { relay: 'wss://write.example/', sent: true, accepted: false, message: 'auth-required: sign challenge' }
      ]
    });

    await expect(profile.publishProfileMetadata({ name: 'alice' }, { clientFactory: () => client }))
      .rejects.toThrow('auth-required');
    expect(authMock.updateAuthProfile).not.toHaveBeenCalled();
    expect(client.disconnect).toHaveBeenCalledTimes(1);
  });

  it('fails without publishing when no writable relay connection can be established', async () => {
    const client = createClient({ connectSummary: { total: 1, connected: 0, failed: 1 } });

    await expect(profile.publishProfileMetadata({ name: 'alice' }, { clientFactory: () => client }))
      .rejects.toThrow('No writable Nostr relay connection was established');
    expect(client.publish).not.toHaveBeenCalled();
    expect(client.disconnect).toHaveBeenCalledTimes(1);
  });
});
