export const ENCRYPTED_REQUEST_ROUTING_TAG = 'encrypted';
// Historical Nostr routing discriminator. Progress ack support is negotiated
// through discovery control_plane.wire_version, not by changing this tag.
export const ENCRYPTED_REQUEST_WIRE_VERSION = 'contextvm-jsonrpc-v1';
export const CONTEXTVM_MESSAGE_KIND = 25910;
export const CONTEXTVM_GIFT_WRAP_KIND = 1059;
export const CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND = 21059;
export const ENCRYPTED_REQUEST_KIND = CONTEXTVM_GIFT_WRAP_KIND;
export const ENCRYPTED_RESULT_KIND = CONTEXTVM_GIFT_WRAP_KIND;
