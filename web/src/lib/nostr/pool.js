import { PoolBackedClient } from './pool-client.js';

export function createNostrPoolClient(options = {}) {
  return new PoolBackedClient(options);
}
