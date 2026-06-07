import { authState, signWithAuth, updateAuthProfile } from '$lib/stores/auth.js';
import { createNostrPoolClient } from './pool.js';
import { normalizeRelayUrl, uniqueRelays } from './pool-utils.js';

export const NOSTR_PROFILE_KIND = 0;

const PROFILE_FIELD_LIMITS = {
  name: 64,
  display_name: 80,
  about: 500,
  picture: 2048,
  banner: 2048,
  website: 2048,
  nip05: 254,
  lud16: 254
};

const PROFILE_FIELDS = Object.keys(PROFILE_FIELD_LIMITS);

function trimField(value) {
  return String(value || '').trim();
}

function isHttpUrl(value) {
  if (!value) return true;
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:';
  } catch {
    return false;
  }
}

function isNip05(value) {
  if (!value) return true;
  return /^[^@\s]+@([a-z0-9-]+\.)+[a-z]{2,}$/i.test(value);
}

function profileFieldValue(input, field) {
  if (field === 'display_name') {
    return trimField(input?.display_name ?? input?.displayName);
  }
  return trimField(input?.[field]);
}

export function validateProfileMetadata(input = {}) {
  const errors = {};
  const metadata = {};

  for (const field of PROFILE_FIELDS) {
    const value = profileFieldValue(input, field);
    if (!value) continue;

    const limit = PROFILE_FIELD_LIMITS[field];
    if (value.length > limit) {
      errors[field] = `${profileFieldLabel(field)} must be ${limit} characters or fewer.`;
      continue;
    }

    if (['picture', 'banner', 'website'].includes(field) && !isHttpUrl(value)) {
      errors[field] = `${profileFieldLabel(field)} must be a valid http:// or https:// URL.`;
      continue;
    }

    if ((field === 'nip05' || field === 'lud16') && !isNip05(value)) {
      errors[field] = `${profileFieldLabel(field)} must use name@example.com format.`;
      continue;
    }

    metadata[field] = value;
  }

  return {
    valid: Object.keys(errors).length === 0,
    errors,
    metadata
  };
}

function profileFieldLabel(field) {
  return {
    name: 'Name',
    display_name: 'Display name',
    about: 'About',
    picture: 'Picture URL',
    banner: 'Banner URL',
    website: 'Website',
    nip05: 'NIP-05 identifier',
    lud16: 'Lightning address'
  }[field] || field;
}

export function profileFormFromMetadata(profile = {}) {
  return {
    name: trimField(profile?.name),
    display_name: trimField(profile?.display_name ?? profile?.displayName),
    about: trimField(profile?.about),
    picture: trimField(profile?.picture),
    banner: trimField(profile?.banner),
    website: trimField(profile?.website),
    nip05: trimField(profile?.nip05),
    lud16: trimField(profile?.lud16)
  };
}

function relayEntries(relays = {}) {
  if (Array.isArray(relays)) {
    return relays.map((relay) => [relay, { read: true, write: true }]);
  }
  return Object.entries(relays || {});
}

export function profileWriteRelayUrls(relays = {}) {
  return uniqueRelays(
    relayEntries(relays)
      .filter(([url, config]) => typeof url === 'string' && /^wss?:\/\//i.test(url) && config?.write !== false)
      .map(([url]) => normalizeRelayUrl(url))
  );
}

function publishAccepted(result) {
  return result?.sent === true && result?.accepted === true;
}

function rejectionReason(rejectedRelays) {
  return rejectedRelays.map((result) => {
    const relay = result?.relay || 'relay';
    const message = result?.message || 'OK false without reason';
    return `${relay}: ${message}`;
  }).join('; ');
}

export function buildUnsignedProfileEvent(metadata, { pubkey = authState.pubkey, created_at = Math.floor(Date.now() / 1000) } = {}) {
  if (!pubkey) throw new Error('Authenticated Nostr pubkey is required to publish profile metadata');
  return {
    kind: NOSTR_PROFILE_KIND,
    pubkey,
    created_at,
    tags: [],
    content: JSON.stringify(metadata || {})
  };
}

export async function publishProfileMetadata(input = {}, options = {}) {
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    throw new Error('Not authenticated - please login first');
  }

  const validation = validateProfileMetadata(input);
  if (!validation.valid) {
    const error = new Error('Profile metadata validation failed');
    error.validation = validation;
    throw error;
  }

  const relays = options.relays || profileWriteRelayUrls(authState.relays);
  if (relays.length === 0) {
    throw new Error('No writable Nostr relays are available for profile publishing');
  }

  const unsignedEvent = buildUnsignedProfileEvent(validation.metadata, {
    pubkey: authState.pubkey,
    created_at: options.created_at ?? Math.floor((options.now?.() ?? Date.now()) / 1000)
  });
  const event = await signWithAuth(unsignedEvent);
  if (!event?.id) throw new Error('Cannot publish unsigned Nostr profile event');

  const client = (options.clientFactory || createNostrPoolClient)({ relays, saveRelayConfig: () => {} });
  try {
    const summary = await client.connect(relays, { force: true });
    if (!summary?.connected) {
      throw new Error('No writable Nostr relay connection was established for profile publishing');
    }

    const ok = await client.publish(event);
    const acceptedRelays = ok.filter(publishAccepted);
    const rejectedRelays = ok.filter((result) => !publishAccepted(result));

    if (acceptedRelays.length === 0) {
      const reason = rejectionReason(rejectedRelays) || 'no relay returned OK accepted=true';
      throw new Error(`Nostr profile publish rejected: ${reason}`);
    }

    const profile = updateAuthProfile(validation.metadata);
    return {
      event,
      ok,
      acceptedRelays,
      rejectedRelays,
      profile
    };
  } finally {
    client.disconnect?.();
  }
}
