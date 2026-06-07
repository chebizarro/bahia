import { isValidHexPubkey } from './nostr-hex.js';
import { detectNip46, nip44Blocker, nip44Provider } from './nip46-core.js';

function ensureCryptoPubkey(pubkey, role) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`Invalid ${role} pubkey for NIP-44 encryption`);
  }
}

export async function encryptNip44(recipientPubkey, plaintext) {
  ensureCryptoPubkey(recipientPubkey, 'recipient');
  if (typeof plaintext !== 'string') throw new Error('NIP-44 plaintext must be a string');

  const { available, provider, reason } = detectNip46();
  if (!available) throw new Error(`NIP-46 provider not available: ${reason}`);

  const crypto = nip44Provider(provider);
  if (!crypto) throw new Error(nip44Blocker(provider));

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
  if (!available) throw new Error(`NIP-46 provider not available: ${reason}`);

  const crypto = nip44Provider(provider);
  if (!crypto) throw new Error(nip44Blocker(provider));

  try {
    return await crypto.decrypt(senderPubkey, ciphertext);
  } catch (error) {
    throw new Error(`Failed to decrypt with NIP-44 via NIP-46 provider: ${error.message}`);
  }
}
