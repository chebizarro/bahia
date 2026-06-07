import { detectNip07, waitForNip07 } from './nip07-detection.js';
import { isValidHexPubkey } from './nostr-hex.js';

let nip07CryptoQueue = Promise.resolve();

function requireNip44Provider() {
  const { available, reason } = detectNip07();
  if (!available) throw new Error(`NIP-07 extension not available: ${reason}`);

  const provider = window.nostr?.nip44;
  if (!provider || typeof provider.encrypt !== 'function' || typeof provider.decrypt !== 'function') {
    throw new Error('NIP-07 signer does not expose NIP-44 encrypt/decrypt support');
  }
  return provider;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function isTransientBridgeError(error) {
  const message = String(error?.message || error || '').toLowerCase();
  return message.includes('could not establish connection')
    || message.includes('receiving end does not exist')
    || message.includes('extension context invalidated')
    || message.includes('message port closed');
}

async function runQueuedNip44Operation(operation) {
  const previous = nip07CryptoQueue.catch(() => {});
  const next = previous.then(operation);
  const queueEntry = next.catch(() => {}).finally(() => {
    if (nip07CryptoQueue === queueEntry) nip07CryptoQueue = Promise.resolve();
  });
  nip07CryptoQueue = queueEntry;
  return next;
}

async function withNip44ProviderRetry(action) {
  const retryDelays = [0, 150, 400];
  let lastError = null;

  for (let attempt = 0; attempt < retryDelays.length; attempt += 1) {
    if (retryDelays[attempt] > 0) {
      await delay(retryDelays[attempt]);
      await waitForNip07({ timeoutMs: retryDelays[attempt] + 150 });
    }

    const provider = requireNip44Provider();
    try {
      return await action(provider);
    } catch (error) {
      lastError = error;
      if (!isTransientBridgeError(error) || attempt === retryDelays.length - 1) throw error;
    }
  }

  throw lastError || new Error('Unknown NIP-44 provider failure');
}

function ensureCryptoPubkey(pubkey, role) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`Invalid ${role} pubkey for NIP-44 encryption`);
  }
}

export async function encryptNip44(recipientPubkey, plaintext) {
  ensureCryptoPubkey(recipientPubkey, 'recipient');
  if (typeof plaintext !== 'string') throw new Error('NIP-44 plaintext must be a string');

  return runQueuedNip44Operation(async () => {
    try {
      return await withNip44ProviderRetry((provider) => provider.encrypt(recipientPubkey, plaintext));
    } catch (error) {
      throw new Error(`Failed to encrypt with NIP-44: ${error.message}`);
    }
  });
}

export async function decryptNip44(senderPubkey, ciphertext) {
  ensureCryptoPubkey(senderPubkey, 'sender');
  if (typeof ciphertext !== 'string' || ciphertext.length === 0) {
    throw new Error('NIP-44 ciphertext must be a non-empty string');
  }

  return runQueuedNip44Operation(async () => {
    try {
      return await withNip44ProviderRetry((provider) => provider.decrypt(senderPubkey, ciphertext));
    } catch (error) {
      throw new Error(`Failed to decrypt with NIP-44: ${error.message}`);
    }
  });
}
