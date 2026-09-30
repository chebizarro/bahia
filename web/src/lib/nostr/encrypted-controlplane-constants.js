export const ENCRYPTED_REQUEST_ROUTING_TAG = 'encrypted';
// Historical Nostr routing discriminator. Progress ack support is negotiated
// through discovery control_plane.wire_version, not by changing this tag.
export const ENCRYPTED_REQUEST_WIRE_VERSION = 'contextvm-jsonrpc-v1';
export const CONTEXTVM_MESSAGE_KIND = 25910;
export const CONTEXTVM_GIFT_WRAP_KIND = 1059;
export const CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND = 21059;
export const ENCRYPTED_REQUEST_KIND = CONTEXTVM_GIFT_WRAP_KIND;
export const ENCRYPTED_RESULT_KIND = CONTEXTVM_GIFT_WRAP_KIND;
// Bahia's relay closes a websocket whose frame exceeds this many bytes (NIP-11
// limitation.max_message_length), so an oversized EVENT never gets an OK.
export const CONTEXTVM_MAX_RELAY_MESSAGE_BYTES = 512000;
// Largest event content Bahia's relay stores (NIP-11 max_content_length). A
// larger stored 1059 wrap is refused; it is sent as an ephemeral 21059 wrap.
export const STORED_EVENT_MAX_CONTENT_BYTES = 65535;
// NIP-44 v2 encrypts at most this many plaintext bytes.
export const NIP44_MAX_PLAINTEXT_BYTES = 65535;
