import { createMockEvent } from '../utils/test-helpers.js';

// Numeric kinds in this generic fixture are incidental test data, not NIP-90 semantics.
export const SAMPLE_NOSTR_EVENTS = [
  createMockEvent({
    id: 'evt-auth',
    kind: 7000,
    content: JSON.stringify({ type: 'auth.success' }),
    tags: [['t', 'auth']]
  }),
  createMockEvent({
    id: 'evt-task',
    kind: 7001,
    content: JSON.stringify({ type: 'task.complete', taskId: 't-123' }),
    tags: [['t', 't-123']]
  })
];
