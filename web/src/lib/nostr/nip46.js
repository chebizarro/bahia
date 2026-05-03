// NIP-46 (Nostr Connect) browser helper utilities
// Supports nostrconnect:// URI parsing, provider detection, and session connect/disconnect flows

function isValidHexPubkey(str) {
  return typeof str === 'string' && /^[0-9a-fA-F]{64}$/.test(str);
}

function isWebsocketRelay(url) {
  return typeof url === 'string' && /^wss?:\/\//i.test(url);
}

function nip44Provider(provider) {
  const crypto = provider?.nip44;
  if (crypto && typeof crypto.encrypt === 'function' && typeof crypto.decrypt === 'function') {
    return crypto;
  }
  return null;
}

function nip44Blocker(provider) {
  if (nip44Provider(provider)) return null;
  return 'NIP-46 provider does not expose a NIP-44 encrypt/decrypt API to the web app; private Bahia transport requires provider.nip44.encrypt/decrypt or a NIP-07 signer with NIP-44 support';
}

function ensureCryptoPubkey(pubkey, role) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`Invalid ${role} pubkey for NIP-44 encryption`);
  }
}

export function detectNip46() {
  if (typeof window === 'undefined') {
    return { available: false, provider: null, reason: 'not_browser' };
  }

  const provider = window?.nostr?.nip46;
  if (!provider) {
    return { available: false, provider: null, reason: 'missing_nip46_provider' };
  }

  return { available: true, provider, reason: null };
}

export function parseNostrConnectUri(uri) {
  if (typeof uri !== 'string' || !uri.trim()) {
    throw new Error('nostrconnect URI is required');
  }

  const normalized = uri.trim();
  if (!normalized.toLowerCase().startsWith('nostrconnect://')) {
    throw new Error('URI must start with nostrconnect://');
  }

  const body = normalized.slice('nostrconnect://'.length);
  const [signerPubkeyRaw, query = ''] = body.split('?', 2);
  const signerPubkey = decodeURIComponent((signerPubkeyRaw || '').trim());

  if (!isValidHexPubkey(signerPubkey)) {
    throw new Error('nostrconnect URI must include a valid signer pubkey');
  }

  const params = new URLSearchParams(query);
  const relays = params.getAll('relay').map((relay) => relay.trim()).filter(isWebsocketRelay);
  if (relays.length === 0) {
    throw new Error('nostrconnect URI must include at least one relay query parameter');
  }

  const secret = (params.get('secret') || '').trim();
  const metadataRaw = params.get('metadata');

  let metadata = null;
  if (metadataRaw) {
    try {
      metadata = JSON.parse(metadataRaw);
    } catch {
      metadata = null;
    }
  }

  return {
    uri: normalized,
    signerPubkey,
    relays,
    secret,
    metadata
  };
}

export function getCapabilities() {
  const { available, provider } = detectNip46();
  if (!available) {
    return {
      connect: false,
      disconnect: false,
      getPublicKey: false,
      signEvent: false,
      getRelays: false,
      nip44: false,
      nip44Blocker: 'NIP-46 provider is not available'
    };
  }

  const hasNip44 = Boolean(nip44Provider(provider));
  return {
    connect: typeof provider.connect === 'function' || typeof provider.bunkerConnect === 'function' || typeof provider.enable === 'function',
    disconnect: typeof provider.disconnect === 'function',
    getPublicKey: typeof provider.getPublicKey === 'function',
    signEvent: typeof provider.signEvent === 'function',
    getRelays: typeof provider.getRelays === 'function',
    nip44: hasNip44,
    nip44Blocker: hasNip44 ? null : nip44Blocker(provider)
  };
}

function getProviderSigner(provider) {
  if (provider && typeof provider.signEvent === 'function') {
    return provider;
  }
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
  if (!available) {
    throw new Error(`NIP-46 provider not available: ${reason}`);
  }

  const session = typeof uriOrSession === 'string' ? parseNostrConnectUri(uriOrSession) : uriOrSession;
  if (!session?.uri) {
    throw new Error('Valid NIP-46 session data is required');
  }

  await invokeConnect(provider, session);

  let pubkey = null;
  if (typeof provider.getPublicKey === 'function') {
    pubkey = await provider.getPublicKey();
  } else if (typeof window !== 'undefined' && window?.nostr && typeof window.nostr.getPublicKey === 'function') {
    pubkey = await window.nostr.getPublicKey();
  }

  if (!isValidHexPubkey(pubkey)) {
    throw new Error('NIP-46 signer did not provide a valid public key');
  }

  let relays = {};
  if (typeof provider.getRelays === 'function') {
    relays = (await provider.getRelays()) || {};
  } else {
    relays = Object.fromEntries(session.relays.map((relay) => [relay, { read: true, write: true }]));
  }

  return {
    ...session,
    pubkey,
    relays,
    connectedAt: new Date().toISOString()
  };
}

export async function disconnectNip46() {
  const { available, provider } = detectNip46();
  if (!available) return;

  if (typeof provider.disconnect === 'function') {
    await provider.disconnect();
  }
}

export async function signEvent(event) {
  const signer = getNip46Signer();
  if (!event || typeof event !== 'object') {
    throw new Error('Invalid event object');
  }
  return signer.signEvent(event);
}

export async function encryptNip44(recipientPubkey, plaintext) {
  ensureCryptoPubkey(recipientPubkey, 'recipient');
  if (typeof plaintext !== 'string') {
    throw new Error('NIP-44 plaintext must be a string');
  }

  const { available, provider, reason } = detectNip46();
  if (!available) {
    throw new Error(`NIP-46 provider not available: ${reason}`);
  }
  const crypto = nip44Provider(provider);
  if (!crypto) {
    throw new Error(nip44Blocker(provider));
  }
  try {
    return await crypto.encrypt(recipientPubkey, plaintext);
  } catch (error) {
    throw new Error(`Failed to encrypt with NIP-44 via NIP-46 provider: ${error.message}`);
  }
}

export async function decryptNip44(senderPubkey, ciphertext) {
  ensureCryptoPubkey(senderPubkey, 'sender');
  if (typeof ciphertext !== 'string' || ciphertext.length === 0) {
    throw new Error('NIP-44 ciphertext must be a non-empty string');
  }

  const { available, provider, reason } = detectNip46();
  if (!available) {
    throw new Error(`NIP-46 provider not available: ${reason}`);
  }
  const crypto = nip44Provider(provider);
  if (!crypto) {
    throw new Error(nip44Blocker(provider));
  }
  try {
    return await crypto.decrypt(senderPubkey, ciphertext);
  } catch (error) {
    throw new Error(`Failed to decrypt with NIP-44 via NIP-46 provider: ${error.message}`);
  }
}

/**
 * Resolve a signer-shaped NIP-46 contract
 * @returns {{getPublicKey: Function, signEvent: Function, getRelays: Function, disconnect: Function}}
 */
export function getNip46Signer() {
  const { available, provider, reason } = detectNip46();
  if (!available) {
    throw new Error(`NIP-46 provider not available: ${reason}`);
  }

  const signer = getProviderSigner(provider);
  if (!signer) {
    throw new Error('NIP-46 signer API is unavailable');
  }

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
      if (typeof provider.disconnect === 'function') {
        await provider.disconnect();
      }
    }
  };
}
