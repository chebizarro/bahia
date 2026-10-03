import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { createCoreQuery, contentId } from './core-query.js';

export const environments = $state([]);
const query = createCoreQuery({ topic: CP_STATE_TOPICS.ENVIRONMENT_REGISTRY, target: environments, identity: event => contentId(event, 'id') });
export function initEnvironmentStoreBinding() { query.bind(getEventStore(), getServicePubkey()); }
export function teardownEnvironmentStoreBinding() { query.unbind(); }
export function resetEnvironments() { query.reset(); }
export function refreshEnvironments() { query.flush(); }
