import { mintEntityId } from '../entity-id.js';

/** One user action owns one ledger key, including its bounded duplicate retry. */
export async function requestWithIdempotency(options, execute) {
  const requestId = options.requestId || mintEntityId();
  const request = { ...options, requestId };
  try {
    return await execute(request);
  } catch (error) {
    if (Number(error?.code) !== -32011) throw error;
    return execute(request);
  }
}
