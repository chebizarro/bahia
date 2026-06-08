/**
 * Nostr-based documentation fetching.
 *
 * Reads documentation topics from the relay as NIP-23 long-form content
 * (kind 30023) events tagged with "bahia-docs". This replaces the REST API
 * docs endpoints with a fully nostr-native documentation pipeline.
 */
import { KINDS } from '$lib/nostr/kinds.js';
import { queryOrPartial, readModelEvents } from '$lib/nostr/subscriptions.js';
import { dedupeReplaceableEvents } from '$lib/nostr/replaceable.js';
import { getDTag, getTagValue, getTagValues } from '$lib/nostr/tags.js';

/**
 * Fetch the documentation catalog from the relay.
 *
 * Returns a structure compatible with the REST API DocsCatalogResponse:
 *   { topics: Topic[], groups: Group[], count: number }
 *
 * @param {Object} [options]
 * @param {string} [options.servicePubkey] - Filter by service pubkey (optional)
 * @param {number} [options.timeoutMs=10000] - Query timeout
 * @returns {Promise<{topics: Array, groups: Array, count: number}>}
 */
export async function fetchDocsCatalog({ servicePubkey = null, timeoutMs = 10000 } = {}) {
  const filter = {
    kinds: [KINDS.LONG_FORM_CONTENT],
    '#t': ['bahia-docs']
  };
  if (servicePubkey) {
    filter.authors = [servicePubkey];
  }

  const result = await queryOrPartial([filter], {
    scope: 'docs-catalog',
    timeoutMs
  });

  const events = dedupeReplaceableEvents(readModelEvents(result));
  const topics = events.map(parseDocTopic).filter(Boolean);

  // Sort deterministically by topic slug.
  topics.sort((a, b) => a.topic.localeCompare(b.topic));

  return {
    topics,
    groups: groupDocsCatalog(topics),
    count: topics.length
  };
}

/**
 * Fetch a single documentation topic from the relay.
 *
 * Returns a structure compatible with the REST API DocsDocumentResponse:
 *   { metadata: Topic, markdown: string, links: [] }
 *
 * @param {string} topic - Topic slug (d-tag value)
 * @param {Object} [options]
 * @param {string} [options.servicePubkey] - Filter by service pubkey (optional)
 * @param {number} [options.timeoutMs=10000] - Query timeout
 * @returns {Promise<{metadata: Object, markdown: string, links: Array}|null>}
 */
export async function fetchDoc(topic, { servicePubkey = null, timeoutMs = 10000 } = {}) {
  const filter = {
    kinds: [KINDS.LONG_FORM_CONTENT],
    '#d': [topic],
    '#t': ['bahia-docs']
  };
  if (servicePubkey) {
    filter.authors = [servicePubkey];
  }

  const result = await queryOrPartial([filter], {
    scope: 'docs-read',
    timeoutMs
  });

  const events = dedupeReplaceableEvents(readModelEvents(result));
  if (events.length === 0) return null;

  const event = events[0];
  const metadata = parseDocTopic(event);
  if (!metadata) return null;

  return {
    metadata,
    markdown: event.content || '',
    links: [] // Link resolution is not available from relay events; render raw links.
  };
}

/**
 * Parse a NIP-23 event into a docs topic metadata object.
 * @param {Object} event - Nostr event
 * @returns {Object|null} Topic metadata or null if invalid
 */
function parseDocTopic(event) {
  if (!event || event.kind !== KINDS.LONG_FORM_CONTENT) return null;

  const topic = getDTag(event);
  if (!topic) return null;

  const title = getTagValue(event, 'title', topic);
  const categories = getTagValues(event, 't').filter((t) => t !== 'bahia-docs');
  const category = categories[0] || 'guide';

  return {
    topic,
    title,
    category,
    sourcePath: '', // Not available from relay events.
    href: `/docs/${topic}`
  };
}

/**
 * Group topics into catalog sections matching the REST API format.
 * @param {Array} topics
 * @returns {Array} Groups with category, label, and topics.
 */
function groupDocsCatalog(topics) {
  const groups = [
    { category: 'guide', label: 'Getting Started & Guides', topics: [] },
    { category: 'feature', label: 'Feature Guides', topics: [] },
    { category: 'reference', label: 'Integration & Reference', topics: [] }
  ];

  const byCategory = new Map(groups.map((g) => [g.category, g]));

  for (const topic of topics) {
    const group = byCategory.get(topic.category);
    if (group) {
      group.topics.push(topic);
    } else {
      // Unknown category — add to guides.
      byCategory.get('guide')?.topics.push(topic);
    }
  }

  return groups;
}
