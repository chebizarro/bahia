export {
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_RESULT_KIND,
  encryptedRelayUrlsFromSystemInfo,
  servicePubkeyFromSystemInfo,
  encryptedRequestsAvailable,
  EncryptedControlplaneTransport,
  createEncryptedControlplaneTransport,
  buildEncryptedRequestEvent,
  publishEncryptedRequest,
  awaitEncryptedResult,
  requestEncryptedResult
} from './encrypted-controlplane.js';

import {
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_RESULT_KIND,
  encryptedRelayUrlsFromSystemInfo,
  servicePubkeyFromSystemInfo,
  encryptedRequestsAvailable,
  EncryptedControlplaneTransport,
  createEncryptedControlplaneTransport,
  buildEncryptedRequestEvent,
  publishEncryptedRequest,
  awaitEncryptedResult,
  requestEncryptedResult
} from './encrypted-controlplane.js';

export const PRIVATE_TRANSPORT_VERSION = ENCRYPTED_REQUEST_WIRE_VERSION;
export const PRIVATE_REQUEST_KIND = ENCRYPTED_REQUEST_KIND;
export const PRIVATE_RESULT_KIND = ENCRYPTED_RESULT_KIND;

export const privateRelayUrlsFromSystemInfo = encryptedRelayUrlsFromSystemInfo;
export const privateServicePubkeyFromSystemInfo = servicePubkeyFromSystemInfo;
export const privateTransportAvailable = encryptedRequestsAvailable;

export class PrivateControlplaneTransport extends EncryptedControlplaneTransport {
  buildPrivateRequestEvent(options) {
    return this.buildEncryptedRequestEvent(options);
  }

  publishPrivateRequest(event) {
    return this.publishEncryptedRequest(event);
  }

  awaitPrivateResult(options) {
    return this.awaitEncryptedResult(options);
  }

  requestPrivateResult(options) {
    return this.requestEncryptedResult(options);
  }
}

export function createPrivateControlplaneTransport(options = {}) {
  return new PrivateControlplaneTransport(options);
}

export async function buildPrivateRequestEvent(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  return transport.buildPrivateRequestEvent(options);
}

export async function publishPrivateRequest(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  const event = options?.event || await transport.buildPrivateRequestEvent(options);
  return transport.publishPrivateRequest(event);
}

export async function awaitPrivateResult(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  await transport.connect();
  return transport.awaitPrivateResult(options);
}

export async function requestPrivateResult(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  return transport.requestPrivateResult(options);
}
