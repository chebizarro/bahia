export {
  CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND,
  CONTEXTVM_GIFT_WRAP_KIND,
  CONTEXTVM_MESSAGE_KIND,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_REQUEST_ROUTING_TAG,
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_RESULT_KIND
} from './encrypted-controlplane-constants.js';
export { EncryptedControlplaneTransport } from './encrypted-controlplane-transport.js';
export {
  encryptedRelayUrlsFromSystemInfo,
  encryptedRequestsAvailable,
  servicePubkeyFromSystemInfo
} from './encrypted-controlplane-utils.js';
import { EncryptedControlplaneTransport } from './encrypted-controlplane-transport.js';

export function createEncryptedControlplaneTransport(options = {}) {
  return new EncryptedControlplaneTransport(options);
}

export async function buildEncryptedRequestEvent(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  return transport.buildEncryptedRequestEvent(options);
}

export async function publishEncryptedRequest(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  try {
    const event = options?.event || await transport.buildEncryptedRequestEvent(options);
    return await transport.publishEncryptedRequest(event);
  } finally {
    transport.disconnect();
  }
}

export async function awaitEncryptedResult(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  try {
    await transport.connect();
    return await transport.awaitEncryptedResult(options);
  } finally {
    transport.disconnect();
  }
}

export async function requestEncryptedResult(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  try {
    return await transport.requestEncryptedResult(options);
  } finally {
    transport.disconnect();
  }
}
