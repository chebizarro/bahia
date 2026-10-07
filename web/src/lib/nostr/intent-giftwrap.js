import { finalizeEvent, generateSecretKey, getPublicKey, nip44 } from 'nostr-tools';

export const SENSITIVE_INTENT_DOMAINS = new Set(['org', 'secret', 'notification', 'relay']);

export function sensitiveIntentBlocker(capabilities) {
  if (capabilities?.nip44 === true) return null;
  return capabilities?.nip44Blocker || 'This signer does not support NIP-44 encryption. Sensitive mutations require a NIP-44-capable NIP-07 or NIP-46 signer.';
}

function randomizedTime(now) {
  const random = crypto.getRandomValues(new Uint32Array(1))[0];
  return now - 60 * (random % 600);
}

/** NIP-59: signed operator seal containing the unsigned rumor, then ephemeral gift wrap.*/
export async function giftWrapIntent(inner, servicePubkey, signer, { now = Math.floor(Date.now() / 1000) } = {}) {
  if (!SENSITIVE_INTENT_DOMAINS.has(inner?.tags?.find(tag => tag[0] === 'domain')?.[1])) {
    throw new Error('Only sensitive intents may be gift-wrapped');
  }
  if (!/^[0-9a-f]{64}$/i.test(servicePubkey || '')) throw new Error('A valid service pubkey is required');
  if (!signer?.encryptNip44 || !signer?.signEvent || !signer?.getPublicKey) {
    throw new Error('Sensitive mutations require a NIP-44-capable signer');
  }
  const operatorPubkey = await signer.getPublicKey();
  if (inner.pubkey !== operatorPubkey || inner.kind !== 30900 || !inner.id || !inner.sig) {
    throw new Error('Gift wrap requires an operator-signed 30900 intent');
  }
  const rumor = { ...inner, sig: '' };
  const seal = await signer.signEvent({
    kind: 13, created_at: randomizedTime(now), tags: [],
    content: await signer.encryptNip44(servicePubkey, JSON.stringify(rumor))
  });
  if (seal?.kind !== 13 || seal?.pubkey !== operatorPubkey || !seal.id || !seal.sig) {
    throw new Error('Signer returned an invalid NIP-59 seal');
  }
  const ephemeralKey = generateSecretKey();
  const ephemeralPubkey = getPublicKey(ephemeralKey);
  const conversationKey = nip44.v2.utils.getConversationKey(ephemeralKey, servicePubkey);
  const wrap = finalizeEvent({
    kind: 1059, created_at: randomizedTime(now), tags: [['p', servicePubkey]],
    content: nip44.v2.encrypt(JSON.stringify(seal), conversationKey)
  }, ephemeralKey);
  if (wrap.pubkey !== ephemeralPubkey || wrap.pubkey === operatorPubkey) throw new Error('Invalid ephemeral gift-wrap key');
  return wrap;
}
