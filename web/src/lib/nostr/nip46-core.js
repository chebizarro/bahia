import { isValidHexPubkey } from './nostr-hex.js';

function isWebsocketRelay(url) {
  return typeof url === 'string' && /^wss?:\/\//i.test(url);
}

export function nip44Provider(provider) {
  const crypto = provider?.nip44;
  if (crypto && typeof crypto.encrypt === 'function' && typeof crypto.decrypt === 'function') {
    return crypto;
  }
  return null;
}

export function nip44Blocker(provider) {
  if (nip44Provider(provider)) return null;
  return 'NIP-46 provider does not expose a NIP-44 encrypt/decrypt API to the web app; private Bahia transport requires provider.nip44.encrypt/decrypt or a NIP-07 signer with NIP-44 support';
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
  if (typeof uri !== 'string' || !uri.trim()) throw new Error('nostrconnect URI is required');

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

  return { uri: normalized, signerPubkey, relays, secret, metadata };
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
