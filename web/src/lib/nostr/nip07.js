// NIP-07 browser extension compatibility surface.
// Implementation lives in focused modules so this public import path remains stable.

export { detectNip07, waitForNip07, watchNip07Availability } from './nip07-detection.js';
export { decryptNip44, encryptNip44 } from './nip07-crypto.js';
export { getCapabilities, getNip07Signer, getPublicKey, getRelays, signEvent } from './nip07-signer.js';
