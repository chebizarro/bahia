import { KINDS } from './kinds.js';
import { queryOrPartial } from './subscriptions.js';

export function parseRepositoryEvent(event) {
  if (!event || !event.id || !event.pubkey || !Array.isArray(event.tags)) {
    return null;
  }

  const repo = {
    id: event.id,
    pubkey: event.pubkey,
    created_at: event.created_at,
    identifier: '',
    name: '',
    description: '',
    webUrls: [],
    cloneUrls: [],
    relayUrls: [],
    earliestUniqueCommitId: '',
    maintainers: []
  };

  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.length < 2) continue;

    switch (tag[0]) {
      case 'd':
        repo.identifier = tag[1] || '';
        break;
      case 'name':
        repo.name = tag[1] || '';
        break;
      case 'description':
        repo.description = tag[1] || '';
        break;
      case 'web':
        repo.webUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'clone':
        repo.cloneUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'relays':
        repo.relayUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'r':
        repo.earliestUniqueCommitId = tag[1] || '';
        break;
      case 'maintainers':
        repo.maintainers.push(...tag.slice(1).filter(Boolean));
        break;
    }
  }

  if (!repo.identifier) {
    return null;
  }

  repo.repoCoordinate = `${KINDS.REPOSITORY}:${repo.pubkey}:${repo.identifier}`;
  repo.primaryUrl = repo.cloneUrls[0] || repo.webUrls[0] || '';
  repo.displayName = repo.name || repo.identifier || repo.primaryUrl;
  repo.searchText = [
    repo.identifier,
    repo.name,
    repo.description,
    repo.primaryUrl,
    repo.repoCoordinate,
    ...repo.cloneUrls,
    ...repo.webUrls,
    ...repo.relayUrls,
    ...repo.maintainers
  ].join(' ').toLowerCase();

  return repo;
}

export async function fetchRepositories({ authors = null, limit = 200, since = null } = {}) {
  const filter = {
    kinds: [KINDS.REPOSITORY],
    limit
  };

  if (Array.isArray(authors) && authors.length > 0) {
    filter.authors = authors;
  }

  if (typeof since === 'number') {
    filter.since = since;
  }

  const events = await queryOrPartial([filter], { scope: 'repositories' });
  const deduped = new Map();

  for (const event of events) {
    const parsed = parseRepositoryEvent(event);
    if (!parsed) continue;

    const existing = deduped.get(parsed.repoCoordinate);
    if (!existing || parsed.created_at >= existing.created_at) {
      deduped.set(parsed.repoCoordinate, parsed);
    }
  }

  return Array.from(deduped.values());
}
