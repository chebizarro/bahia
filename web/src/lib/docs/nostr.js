/**
 * Nostr-based documentation fetching with browser caching and link resolution.
 *
 * Reads documentation topics from the relay as NIP-23 long-form content
 * (kind 30023) events tagged with "bahia-docs". Caches events in localStorage
 * and resolves cross-document markdown links client-side.
 */
import { browser } from '$app/environment';
import { KINDS } from '$lib/nostr/kinds.js';
import { nostr } from '$lib/nostr/subscriptions.js';
import { dedupeReplaceableEvents } from '$lib/nostr/replaceable.js';
import { getDTag, getTagValue, getTagValues } from '$lib/nostr/tags.js';

// --- Cache configuration ---

const DOCS_CACHE_KEY = 'bahia_docs_cache';
const DOCS_CACHE_TTL_MS = 5 * 60 * 1000; // 5 minutes

/**
 * Read cached docs events from localStorage.
 * @returns {{ events: Array, cachedAt: number } | null}
 */
function readCache() {
  if (!browser || typeof localStorage?.getItem !== 'function') return null;
  try {
    const raw = localStorage.getItem(DOCS_CACHE_KEY);
    if (!raw) return null;
    const snapshot = JSON.parse(raw);
    const age = Date.now() - Number(snapshot?.cachedAt || 0);
    if (age > DOCS_CACHE_TTL_MS) return null;
    if (!Array.isArray(snapshot?.events) || snapshot.events.length === 0) return null;
    return snapshot;
  } catch {
    return null;
  }
}

/**
 * Write docs events to localStorage cache.
 * @param {Array} events - Raw nostr event objects
 */
function writeCache(events) {
  if (!browser || typeof localStorage?.setItem !== 'function') return;
  if (!Array.isArray(events) || events.length === 0) return;
  try {
    localStorage.setItem(DOCS_CACHE_KEY, JSON.stringify({
      cachedAt: Date.now(),
      events: events.map((e) => ({
        id: e.id,
        kind: e.kind,
        pubkey: e.pubkey,
        created_at: e.created_at,
        content: e.content,
        tags: e.tags,
        sig: e.sig
      }))
    }));
  } catch {
    // Storage full or unavailable — ignore.
  }
}

/**
 * Fetch all docs events, using cache when fresh.
 * @param {Object} [options]
 * @param {string} [options.servicePubkey]
 * @param {number} [options.timeoutMs=10000]
 * @param {boolean} [options.bypassCache=false]
 * @returns {Promise<Array>} Deduplicated events
 */
async function fetchDocsEvents({ servicePubkey = null, timeoutMs = 10000, bypassCache = false } = {}) {
  if (!bypassCache) {
    const cached = readCache();
    if (cached) return cached.events;
  }

  const filter = {
    kinds: [KINDS.LONG_FORM_CONTENT],
    '#t': ['bahia-docs']
  };
  if (servicePubkey) {
    filter.authors = [servicePubkey];
  }

  const events = await new Promise((resolve, reject) => {
    const collected = [];
    const timer = setTimeout(() => resolve(collected), timeoutMs);

    nostr.subscribe([filter], {
      onEvent: (event) => collected.push(event),
      onEose: () => {
        clearTimeout(timer);
        resolve(collected);
      },
      onClosed: (reason) => {
        clearTimeout(timer);
        if (collected.length > 0) resolve(collected);
        else reject(new Error(`Docs subscription closed: ${reason}`));
      }
    });
  });

  const deduped = dedupeReplaceableEvents(events);
  writeCache(deduped);
  return deduped;
}

/**
 * Fetch the documentation catalog from the relay.
 *
 * Returns a structure with topics grouped for display:
 *   { topics: Topic[], groups: Group[], count: number }
 *
 * @param {Object} [options]
 * @param {string} [options.servicePubkey] - Filter by service pubkey (optional)
 * @param {number} [options.timeoutMs=10000] - Query timeout
 * @param {boolean} [options.bypassCache=false] - Skip localStorage cache
 * @returns {Promise<{topics: Array, groups: Array, count: number}>}
 */
export async function fetchDocsCatalog({ servicePubkey = null, timeoutMs = 10000, bypassCache = false } = {}) {
  const events = await fetchDocsEvents({ servicePubkey, timeoutMs, bypassCache });
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
 * Returns the document with resolved cross-document links:
 *   { metadata: Topic, markdown: string, links: DocumentLink[] }
 *
 * @param {string} topic - Topic slug (d-tag value)
 * @param {Object} [options]
 * @param {string} [options.servicePubkey] - Filter by service pubkey (optional)
 * @param {number} [options.timeoutMs=10000] - Query timeout
 * @param {boolean} [options.bypassCache=false] - Skip localStorage cache
 * @returns {Promise<{metadata: Object, markdown: string, links: Array}|null>}
 */
export async function fetchDoc(topic, { servicePubkey = null, timeoutMs = 10000, bypassCache = false } = {}) {
  // Fetch all docs events (leverages cache) so we have the catalog for link resolution.
  const allEvents = await fetchDocsEvents({ servicePubkey, timeoutMs, bypassCache });

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
    links
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
