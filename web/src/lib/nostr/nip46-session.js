import { isValidHexPubkey } from './nostr-hex.js';
import { detectNip46, parseNostrConnectUri } from './nip46-core.js';
import { decryptNip44, encryptNip44 } from './nip46-crypto.js';

function getProviderSigner(provider) {
  if (provider && typeof provider.signEvent === 'function') return provider;
  if (typeof window !== 'undefined' && window?.nostr && typeof window.nostr.signEvent === 'function') {
    return window.nostr;
  }
  return null;
}

async function invokeConnect(provider, session) {
  const uri = session.uri;
  if (typeof provider.connect === 'function') {
    await provider.connect(uri);
    return;
  }
  if (typeof provider.bunkerConnect === 'function') {
    await provider.bunkerConnect(uri);
    return;
  }
  if (typeof provider.enable === 'function') {
    await provider.enable(uri);
    return;
  }
  throw new Error('NIP-46 provider does not support connect()');
}

export async function connectNip46(uriOrSession) {
  const { available, provider, reason } = detectNip46();
  if (!available) throw new Error(`NIP-46 provider not available: ${reason}`);

  const session = typeof uriOrSession === 'string' ? parseNostrConnectUri(uriOrSession) : uriOrSession;
  if (!session?.uri) throw new Error('Valid NIP-46 session data is required');

  await invokeConnect(provider, session);

  let pubkey = null;
  if (typeof provider.getPublicKey === 'function') {
    pubkey = await provider.getPublicKey();
  } else if (typeof window !== 'undefined' && window?.nostr && typeof window.nostr.getPublicKey === 'function') {
    pubkey = await window.nostr.getPublicKey();
  }

  if (!isValidHexPubkey(pubkey)) throw new Error('NIP-46 signer did not provide a valid public key');

  let relays = {};
  if (typeof provider.getRelays === 'function') {
    relays = (await provider.getRelays()) || {};
  } else {
    relays = Object.fromEntries(session.relays.map((relay) => [relay, { read: true, write: true }]));
  }

  return { ...session, pubkey, relays, connectedAt: new Date().toISOString() };
}

export async function disconnectNip46() {
  const { available, provider } = detectNip46();
  if (!available) return;
  if (typeof provider.disconnect === 'function') await provider.disconnect();
}

export async function signEvent(event) {
  const signer = getNip46Signer();
  if (!event || typeof event !== 'object') throw new Error('Invalid event object');
  return signer.signEvent(event);
}

export function getNip46Signer() {
  const { available, provider, reason } = detectNip46();
  if (!available) throw new Error(`NIP-46 provider not available: ${reason}`);

  const signer = getProviderSigner(provider);
  if (!signer) throw new Error('NIP-46 signer API is unavailable');

  return {
    getPublicKey: async () => {
      if (typeof provider.getPublicKey === 'function') return provider.getPublicKey();
      if (typeof signer.getPublicKey === 'function') return signer.getPublicKey();
      throw new Error('NIP-46 signer getPublicKey() is unavailable');
    },
    signEvent: (event) => signer.signEvent(event),
    encryptNip44,
    decryptNip44,
    getRelays: async () => {
      if (typeof provider.getRelays === 'function') return (await provider.getRelays()) || {};
      return {};
    },
    disconnect: async () => {
      if (typeof provider.disconnect === 'function') await provider.disconnect();
    }
  };
}
