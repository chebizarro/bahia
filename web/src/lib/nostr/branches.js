/**
 * Branch detection utilities using @nostr-git/core
 * Fetches repo state events (NIP-34 kind 30618) to extract branch information
 */

import { nostr, KINDS } from './client.js';

// Repo state event kind (NIP-34)
const REPO_STATE_KIND = 30618;

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
 * @param {number} options.timeout - Query timeout in ms (default: 5000)
 * @returns {Promise<{ branches: string[], defaultBranch: string | null, error: string | null }>}
 */
export async function fetchRepoBranches(repoCoordinate, { timeout = 5000 } = {}) {
  const parsed = parseRepoCoordinate(repoCoordinate);
  if (!parsed) {
    return {
      branches: [],
      defaultBranch: null,
      error: 'Invalid repository coordinate'
    };
  }

  const { pubkey, identifier } = parsed;

  try {
    // Query for repo state events (kind 30618) with matching author and d-tag
    const events = await nostr.query([{
      kinds: [REPO_STATE_KIND],
      authors: [pubkey],
      '#d': [identifier]
    }], timeout);

    if (!events || events.length === 0) {
      // No state event found - repo may not have state published yet
      return {
        branches: [],
        defaultBranch: null,
        error: null // Not an error, just no state available
      };
    }

    // Use the most recent state event
    const latestEvent = events.reduce((latest, event) => {
      if (!latest || event.created_at > latest.created_at) {
        return event;
      }
      return latest;
    }, null);

    const { branches, defaultBranch } = parseRepoStateBranches(latestEvent);

    return {
      branches,
      defaultBranch,
      error: null
    };
  } catch (err) {
    console.error('[branches] Failed to fetch repo state:', err);
    return {
      branches: [],
      defaultBranch: null,
      error: err?.message || 'Failed to fetch branches'
    };
  }
}

/**
 * Check if a repository selection is a NIP-34 Nostr repository
 * @param {Object} selection - Repository selection object
 * @returns {boolean}
 */
export function isNostrRepository(selection) {
  return selection?.source === 'nip34' && !!selection?.repoCoordinate;
}
