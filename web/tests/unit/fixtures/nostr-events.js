import { createMockEvent } from '../utils/test-helpers.js';

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
