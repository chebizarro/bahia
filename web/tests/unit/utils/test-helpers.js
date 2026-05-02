export function createMockEvent({
  id = 'evt-1',
  kind = 1,
  pubkey = 'test-pubkey',
  content = '{}',
  created_at = Math.floor(Date.now() / 1000),
  tags = []
} = {}) {
  return {
    id,
    kind,
    pubkey,
    content,
    created_at,
    tags,
    sig: 'test-signature'
  };
}
