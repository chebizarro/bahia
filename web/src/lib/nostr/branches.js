/**
 * Branch detection utilities using @nostr-git/core
 * Fetches repo state events (NIP-34 kind 30618) to extract branch information
 */

import { KINDS, nostr } from './client.js';
import { createReadModelMetadataTracker, uniqueRelays } from './pool-utils.js';

const REPO_STATE_KIND = KINDS.REPOSITORY_STATE;

/**
 * Parse branches from a repo state event's tags
 * @param {Object} event - The repo state event
 * @returns {{ branches: string[], defaultBranch: string | null }}
 */
function parseRepoStateBranches(event) {
  if (!event?.tags || !Array.isArray(event.tags)) {
    return { branches: [], defaultBranch: null };
  }

  const branches = [];
  let head = null;

  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.length < 2) continue;

    const tagName = tag[0];

    // Extract branches from refs/heads/* tags
    if (tagName.startsWith('refs/heads/')) {
      const branchName = tagName.replace('refs/heads/', '');
      if (branchName && !branches.includes(branchName)) {
        branches.push(branchName);
      }
    }

    // Extract HEAD reference
    if (tagName === 'HEAD' && tag[1]) {
      head = tag[1];
    }
  }

  // Parse default branch from HEAD (format: "ref: refs/heads/main")
  let defaultBranch = null;
  if (head) {
    const match = head.match(/^ref:\s*refs\/heads\/(.+)$/);
    if (match) {
      defaultBranch = match[1];
    }
  }

  // Fallback: prefer main, then master, then first branch
  if (!defaultBranch && branches.length > 0) {
    if (branches.includes('main')) {
      defaultBranch = 'main';
    } else if (branches.includes('master')) {
      defaultBranch = 'master';
    } else {
      defaultBranch = branches[0];
    }
  }

  // Sort branches: default first, then alphabetically
  branches.sort((a, b) => {
    if (a === defaultBranch) return -1;
    if (b === defaultBranch) return 1;
    return a.localeCompare(b);
  });

  return { branches, defaultBranch };
}

/**
 * Parse repository coordinate to extract pubkey and identifier
 * Format: "30617:pubkey:identifier"
 * @param {string} repoCoordinate
 * @returns {{ pubkey: string, identifier: string } | null}
 */
function parseRepoCoordinate(repoCoordinate) {
  if (!repoCoordinate || typeof repoCoordinate !== 'string') {
    return null;
  }

  const parts = repoCoordinate.split(':');
  if (parts.length !== 3) {
    return null;
  }

  const [kind, pubkey, identifier] = parts;
  if (kind !== String(KINDS.REPOSITORY) || !pubkey || !identifier) {
    return null;
  }

  return { pubkey, identifier };
}

/**
 * Fetch branches for a NIP-34 repository
 * @param {string} repoCoordinate - Repository coordinate (30617:pubkey:identifier)
 * @param {Object} options
 * @param {number} options.timeout - Incomplete-query timeout in ms (default: 5000)
 * @param {string[]} options.relayUrls - Repository relay hints from the NIP-34 30617 relays tag
 * @returns {Promise<{ branches: string[], defaultBranch: string | null, error: string | null, complete: boolean, degraded: Object | null, relaySummary: Array }>}
 */
export async function fetchRepoBranches(repoCoordinate, { timeout = 5000, relayUrls = null } = {}) {
  const parsed = parseRepoCoordinate(repoCoordinate);
  if (!parsed) {
    return {
      branches: [],
      defaultBranch: null,
      error: 'Invalid repository coordinate',
      complete: false,
      degraded: null,
      relaySummary: []
    };
  }

  const { pubkey, identifier } = parsed;
  const repositoryRelays = Array.isArray(relayUrls) ? uniqueRelays(relayUrls) : null;
  const fallbackRelays = typeof nostr.getConnectedRelays === 'function'
    ? nostr.getConnectedRelays()
    : (typeof nostr.getRelays === 'function' ? nostr.getRelays() : []);
  const expectedRelays = repositoryRelays?.length > 0 ? repositoryRelays : fallbackRelays;
  const missingRepositoryRelayFallback = Array.isArray(relayUrls) && repositoryRelays.length === 0
    ? {
        incomplete: false,
        reason: 'missing_repository_relays',
        message: 'NIP-34 repository announcement did not include relays tag values; queried global Bahia relays as a degraded fallback.',
        relaySummary: [],
        partialEventCount: 0
      }
    : null;

  return new Promise((resolve) => {
    const events = [];
    const tracker = createReadModelMetadataTracker({
      relays: expectedRelays,
      partialEventCount: () => events.length
    });
    let timer = null;
    let settled = false;

    const latestBranches = () => {
      const latestEvent = events.reduce((latest, event) => {
        if (!latest || event.created_at > latest.created_at) return event;
        return latest;
      }, null);
      return latestEvent ? parseRepoStateBranches(latestEvent) : { branches: [], defaultBranch: null };
    };

    const settle = (metadataOptions = {}, error = null) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      const metadata = tracker.metadata({ degraded: missingRepositoryRelayFallback, ...metadataOptions });
      resolve({ ...latestBranches(), error, ...metadata });
    };

    if (timeout > 0) {
      timer = setTimeout(() => {
        const timeoutError = events.length === 0 ? 'Timed out waiting for repo state EOSE' : null;
        settle({
          forceIncomplete: true,
          reason: 'timeout-before-eose',
          message: 'Timed out before every expected relay reached EOSE; returning degraded branch history.'
        }, timeoutError);
      }, timeout);
    }

    const filters = [{
      kinds: [REPO_STATE_KIND],
      authors: [pubkey],
      '#d': [identifier]
    }];

    const subscribeArgs = [filters, {
      onEvent: (event, relay) => {
        tracker.markEvent(event, relay);
        events.push(event);
      },
      onEose: (relay) => {
        tracker.markEose(relay);
        if (tracker.isComplete()) settle();
      },
      onClosed: (reason, relay, meta) => {
        tracker.markClosed(reason, relay, meta);
        if (tracker.isTerminal()) {
          settle({}, events.length === 0 ? (reason || 'Subscription closed before EOSE') : null);
        }
      },
      onAuth: (challenge, relay) => {
        tracker.markAuth(challenge, relay);
      }
    }];

    if (repositoryRelays?.length > 0) {
      nostr.subscribeWithRecoveryOnRelays(repositoryRelays, ...subscribeArgs);
    } else {
      nostr.subscribeWithRecovery(...subscribeArgs);
    }
  });
}

/**
 * Check if a repository selection is a NIP-34 Nostr repository
 * @param {Object} selection - Repository selection object
 * @returns {boolean}
 */
export function isNostrRepository(selection) {
  return selection?.source === 'nip34' && !!selection?.repoCoordinate;
}
