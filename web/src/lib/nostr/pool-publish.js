import { messageFromError, publishSentBeforeFailure } from './pool-utils.js';

export async function publishFromPool(client, event, options = {}) {
  if (!event?.id) throw new Error('Cannot publish event without id');
  const relays = client.getConnectedRelays();
  if (relays.length === 0) return [];

  const publishPromises = client.pool.publish(relays, event, options);
  return Promise.all(publishPromises.map((promise, index) => {
    const relay = relays[index];
    return Promise.resolve(promise)
      .then((message = '') => ({ relay, sent: true, accepted: true, message: String(message || '') }))
      .catch((error) => {
        const message = messageFromError(error);
        return { relay, sent: publishSentBeforeFailure(message), accepted: false, message };
      });
  }));
}
