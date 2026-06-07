// NIP-46 (Nostr Connect) browser helper compatibility surface.
// Focused implementation modules preserve this public import path.

export { detectNip46, getCapabilities, parseNostrConnectUri } from './nip46-core.js';
export { decryptNip44, encryptNip44 } from './nip46-crypto.js';
export { connectNip46, disconnectNip46, getNip46Signer, signEvent } from './nip46-session.js';
