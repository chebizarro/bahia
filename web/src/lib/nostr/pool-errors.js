export class NostrIncompleteEOSEError extends Error {
  constructor(reason, { partialEvents = [], relaySummary = [], message = '' } = {}) {
    super(message || `Nostr query did not receive complete EOSE history: ${reason}`);
    this.name = 'NostrIncompleteEOSEError';
    this.reason = reason;
    this.partialEvents = partialEvents;
    this.relaySummary = relaySummary;
  }
}
