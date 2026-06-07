import { detectNip07 } from './nip07-detection.js';
import { decryptNip44, encryptNip44 } from './nip07-crypto.js';
import { isValidHexPubkey } from './nostr-hex.js';

export async function getPublicKey() {
  const { available, reason } = detectNip07();
  if (!available) throw new Error(`NIP-07 extension not available: ${reason}`);

  try {
    const pubkey = await window.nostr.getPublicKey();
    if (!isValidHexPubkey(pubkey)) {
      throw new Error('Invalid public key format returned by extension');
    }
    return pubkey;
  } catch (error) {
    throw new Error(`Failed to get public key: ${error.message}`);
  }
}

export async function getRelays() {
  const { available, reason } = detectNip07();
  if (!available) throw new Error(`NIP-07 extension not available: ${reason}`);

  try {
    if (typeof window.nostr.getRelays !== 'function') return {};
    const relays = await window.nostr.getRelays();
    return relays || {};
  } catch (error) {
    console.warn('Failed to get relays from NIP-07:', error);
    return {};
  }
}

export async function signEvent(event) {
  const { available, reason } = detectNip07();
  if (!available) throw new Error(`NIP-07 extension not available: ${reason}`);
  if (!event || typeof event !== 'object') throw new Error('Invalid event object');

  try {
    return await window.nostr.signEvent(event);
  } catch (error) {
    throw new Error(`Failed to sign event: ${error.message}`);
  }
}

export function getCapabilities() {
  const { available } = detectNip07();
  if (!available) {
    return { getPublicKey: false, signEvent: false, getRelays: false, nip04: false, nip44: false };
  }

  const nostr = window.nostr;
  return {
    getPublicKey: typeof nostr.getPublicKey === 'function',
    signEvent: typeof nostr.signEvent === 'function',
    getRelays: typeof nostr.getRelays === 'function',
    nip04: typeof nostr.nip04 === 'object'
      && typeof nostr.nip04.encrypt === 'function'
      && typeof nostr.nip04.decrypt === 'function',
    nip44: typeof nostr.nip44 === 'object'
      && typeof nostr.nip44.encrypt === 'function'
      && typeof nostr.nip44.decrypt === 'function'
  };
}

export function getNip07Signer() {
  return { getPublicKey, signEvent, getRelays, encryptNip44, decryptNip44 };
}
