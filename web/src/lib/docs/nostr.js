/**
 * Nostr-based documentation fetching from the BahiaEventStore with link resolution.
 *
 * Reads documentation topics from the relay as NIP-23 long-form content
 * (kind 30023) events tagged with "bahia-docs". Persisted event-store
 * history renders offline; EOSE marks relay catch-up.
 */
import { boot, getEventStore } from '$lib/nostr/boot.js';
import { KINDS } from '$lib/nostr/kinds.js';
import { nostr } from '$lib/nostr/subscriptions.js';
import { dedupeReplaceableEvents } from '$lib/nostr/replaceable.js';
import { getDTag, getTagValue, getTagValues } from '$lib/nostr/tags.js';
import { createReadModelMetadataTracker } from '$lib/nostr/read-model-metadata.js';
import { getBootstrapSeed } from '$lib/stores/discovery.svelte.js';

/**
 * Fetch docs from the event store, then subscribe for historical catch-up.
 * @param {Object} [options]
 * @param {boolean} [options.bypassCache=false] Wait for relay EOSE even if the event store has a snapshot.
 * @returns {Promise<{events: Array, complete: boolean, degraded: Object|null, relaySummary: Array}>} Deduplicated events with EOSE metadata.
 */
async function fetchDocsEvents({ bypassCache = false } = {}) {
  const publishers = getBootstrapSeed()?.service_pubkeys || [];
  if (!publishers.length) throw new Error('Documentation publisher not configured');
  await boot();
  const filter = { kinds: [KINDS.LONG_FORM_CONTENT], '#t': ['bahia-docs'], authors: publishers };
  const trusted = new Set(publishers);
  const isTrustedDoc = (event) => event?.kind === KINDS.LONG_FORM_CONTENT && trusted.has(event.pubkey)
    && event.tags?.some((tag) => tag[0] === 't' && tag[1] === 'bahia-docs');
  const stored = (getEventStore()?.query(filter) || []).filter(isTrustedDoc);
  if (stored.length && !bypassCache) {
    return { events: dedupeReplaceableEvents(stored), complete: false,
      degraded: { incomplete: true, reason: 'local-event-store', partialEventCount: stored.length }, relaySummary: [] };
  }

  const result = await new Promise((resolve) => {
    const collected = [...stored];
    const expectedRelays = typeof nostr.getConnectedRelays === 'function'
      ? nostr.getConnectedRelays()
      : (typeof nostr.getRelays === 'function' ? nostr.getRelays() : []);
    const tracker = createReadModelMetadataTracker({
      relays: expectedRelays,
      partialEventCount: () => collected.length
    });
    let settled = false;
    let stop = null;

    const settle = (metadataOptions = {}) => {
      if (settled) return;
      settled = true;
      stop?.();
      const deduped = dedupeReplaceableEvents(collected);
      resolve({ events: deduped, ...tracker.metadata(metadataOptions) });
    };

    stop = nostr.subscribeWithRecovery([filter], {
      onEvent: (event, relay) => {
        if (!isTrustedDoc(event)) return;
        tracker.markEvent(event, relay);
        collected.push(event);
      },
      onEose: (relay) => {
        tracker.markEose(relay);
        if (tracker.isComplete()) settle();
      },
      onClosed: (reason, relay, meta) => {
        tracker.markClosed(reason, relay, meta);
        if (tracker.isTerminal()) settle();
      },
      onAuth: (challenge, relay) => {
        tracker.markAuth(challenge, relay);
      }
    });
    // Some injected clients report EOSE synchronously while subscribing.
    if (settled) stop?.();
  });

  return result;
}

/**
 * Fetch the documentation catalog from the relay.
 *
 * Returns a structure with topics grouped for display:
 *   { topics: Topic[], groups: Group[], count: number }
 *
 * @param {Object} [options]
 * @param {boolean} [options.bypassCache=false] - Require relay EOSE rather than local history
 * @returns {Promise<{topics: Array, groups: Array, count: number}>}
 */
export async function fetchDocsCatalog({ bypassCache = false } = {}) {
  const result = await fetchDocsEvents({ bypassCache });
  const topics = result.events.map(parseDocTopic).filter(Boolean);

  // Sort deterministically by topic slug.
  topics.sort((a, b) => a.topic.localeCompare(b.topic));

  return {
    topics,
    groups: groupDocsCatalog(topics),
    count: topics.length,
    complete: result.complete,
    degraded: result.degraded,
    relaySummary: result.relaySummary
  };
}

/**
 * Fetch a single documentation topic from the relay.
 *
 * Returns the document with resolved cross-document links:
 *   { metadata: Topic, markdown: string, links: DocumentLink[] }
 *
 * @param {string} topic - Topic slug (d-tag value)
 * @param {Object} [options]
 * @param {boolean} [options.bypassCache=false] - Require relay EOSE rather than local history
 * @returns {Promise<{metadata: Object, markdown: string, links: Array}|null>}
 */
export async function fetchDoc(topic, { bypassCache = false } = {}) {
  // Fetch all docs events (leverages cache) so we have the catalog for link resolution.
  const result = await fetchDocsEvents({ bypassCache });
  const allEvents = result.events;

  const event = allEvents.find((e) => getDTag(e) === topic);
  if (!event) return null;

  const metadata = parseDocTopic(event);
  if (!metadata) return null;

  // Build catalog for link resolution.
  const catalog = allEvents.map(parseDocTopic).filter(Boolean);
  const links = resolveDocumentLinks(event.content || '', catalog);

  return {
    metadata,
    markdown: event.content || '',
    links,
    complete: result.complete,
    degraded: result.degraded,
    relaySummary: result.relaySummary
  };
}

// --- Link resolution ---

const MARKDOWN_LINK_PATTERN = /!?\[[^\]\n]+\]\(([^)\s]+)(?:\s+['"][^)]*['"])?\)/g;

/**
 * Convert a relative markdown path to a topic slug.
 * Mirrors the server-side TopicFromPath logic:
 *   strip extension, replace / with -, trim parts.
 *
 * @param {string} relPath - e.g. "features/services.md"
 * @returns {string} e.g. "features-services"
 */
function topicFromPath(relPath) {
  // Strip extension
  const dotIdx = relPath.lastIndexOf('.');
  const withoutExt = dotIdx > 0 ? relPath.slice(0, dotIdx) : relPath;
  return withoutExt
    .split('/')
    .map((part) => part.trim())
    .filter(Boolean)
    .join('-');
}

/**
 * Check if a link href is an internal markdown reference.
 * @param {string} href
 * @returns {boolean}
 */
function isInternalMarkdownHref(href) {
  const value = String(href || '').trim();
  if (!value || value.startsWith('#')) return false;
  if (value.startsWith('/') || value.startsWith('//')) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(value)) return false;
  const pathOnly = value.split(/[?#]/, 1)[0];
  return pathOnly.toLowerCase().endsWith('.md');
}

/**
 * Check if a link href is an external URL.
 * @param {string} href
 * @returns {boolean}
 */
function isExternalHref(href) {
  return /^(https?:|mailto:)/i.test(href) || href.startsWith('//');
}

/**
 * Resolve all markdown links in a document against the known catalog.
 *
 * @param {string} markdown - Raw markdown content
 * @param {Array} catalog - Array of topic metadata objects
 * @returns {Array} Resolved link objects for the renderer
 */
function resolveDocumentLinks(markdown, catalog) {
  if (!markdown || !catalog?.length) return [];

  const catalogSet = new Map(catalog.map((t) => [t.topic, t]));
  const seen = new Set();
  const links = [];

  for (const match of markdown.matchAll(MARKDOWN_LINK_PATTERN)) {
    const rawHref = (match[1] || '').trim();
    if (!rawHref || seen.has(rawHref)) continue;
    seen.add(rawHref);

    // External links
    if (isExternalHref(rawHref)) {
      links.push({ original: rawHref, href: rawHref, external: true, status: 'resolved' });
      continue;
    }

    // Internal markdown links
    if (isInternalMarkdownHref(rawHref)) {
      // Strip query/fragment for topic resolution
      const pathOnly = rawHref.split(/[?#]/, 1)[0];
      // Normalize: remove leading ./ or nested ../
      const cleaned = pathOnly.replace(/^(\.\/)+/, '');
      const candidateTopic = topicFromPath(cleaned);

      const found = catalogSet.get(candidateTopic);
      if (found) {
        let href = `/docs/${candidateTopic}`;
        // Preserve fragment
        const hashIdx = rawHref.indexOf('#');
        if (hashIdx >= 0) href += rawHref.slice(hashIdx);
        links.push({ original: rawHref, href, topic: candidateTopic, external: false, status: 'resolved' });
      } else {
        links.push({ original: rawHref, status: 'not_found', error: `Topic "${candidateTopic}" not found in catalog` });
      }
      continue;
    }

    // Fragment-only or non-markdown links — skip, renderer handles them natively.
  }

  return links;
}

// --- Parsing helpers ---

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
 * Group topics into catalog sections.
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
