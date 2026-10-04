import { finalizeEvent, getEventHash, nip44, verifyEvent } from 'nostr-tools';
import { e2eSecretKeyForPubkey, TEST_PUBKEY } from './e2e-keyring.js';

const exposedPages = new WeakSet();

function conversationKey(authorPubkey, peerPubkey) {
  return nip44.v2.utils.getConversationKey(e2eSecretKeyForPubkey(authorPubkey), peerPubkey);
}

/** Node-side crypto bridge. The browser never receives a test private key. */
export async function exposeTestSigner(page) {
  if (exposedPages.has(page)) return;
  exposedPages.add(page);
  await page.exposeFunction('__bahiaE2ESignEvent', (event, pubkey = TEST_PUBKEY) =>
    finalizeEvent({ kind: event.kind, created_at: event.created_at, tags: event.tags, content: event.content },
      e2eSecretKeyForPubkey(pubkey)));
  await page.exposeFunction('__bahiaE2EEncryptNip44', (authorPubkey, recipientPubkey, plaintext) =>
    nip44.v2.encrypt(String(plaintext), conversationKey(authorPubkey, recipientPubkey)));
  await page.exposeFunction('__bahiaE2EDecryptNip44', (recipientPubkey, senderPubkey, ciphertext) =>
    nip44.v2.decrypt(String(ciphertext), conversationKey(recipientPubkey, senderPubkey)));
  await page.exposeFunction('__bahiaE2EVerifyEvent', (event) => verifyEvent(event));
  await page.exposeFunction('__bahiaE2EVerifyGiftWrap', (outer, servicePubkey) => {
    if (!verifyEvent(outer) || ![1059, 21059].includes(outer.kind)) return false;
    try {
      const inner = JSON.parse(nip44.v2.decrypt(outer.content, conversationKey(servicePubkey, outer.pubkey)));
      // ContextVM's wrapper encrypts an already signed kind-25910 event.
      if (inner.kind === 25910) return verifyEvent(inner);
      const seal = inner;
      if (!verifyEvent(seal) || seal.kind !== 13) return false;
      const plaintext = seal.content.startsWith('mock-nip44:')
        ? Buffer.from(seal.content.slice('mock-nip44:'.length), 'base64').toString('utf8')
        : seal.content.startsWith('enc44:')
          ? seal.content.slice('enc44:'.length)
          : nip44.v2.decrypt(seal.content, conversationKey(servicePubkey, seal.pubkey));
      const rumor = JSON.parse(plaintext);
      return getEventHash(rumor) === rumor.id && rumor.pubkey === seal.pubkey;
    } catch {
      return false;
    }
  });
}

/** Install a deterministic NIP-07 signer before application scripts run. */
export async function installTestSigner(page, { pubkey = TEST_PUBKEY, relays = [], nip44Enabled = true } = {}) {
  await exposeTestSigner(page);
  await page.addInitScript(({ pubkey, relays, nip44Enabled }) => {
    window.nostr = {
      getPublicKey: async () => pubkey,
      getRelays: async () => Object.fromEntries(relays.map(relay => [relay, { read: true, write: true }])),
      signEvent: async event => window.__bahiaE2ESignEvent(event, pubkey),
      ...(nip44Enabled ? { nip44: {
        encrypt: async (recipient, plaintext) => window.__bahiaE2EEncryptNip44(pubkey, recipient, plaintext),
        decrypt: async (sender, ciphertext) => window.__bahiaE2EDecryptNip44(pubkey, sender, ciphertext)
      } } : {})
    };
  }, { pubkey, relays, nip44Enabled });
}
