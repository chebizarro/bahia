import { describe, expect, it } from 'vitest';
import {
  latestPackageOperation,
  packageDriftOutcome,
  packageOperationLabel,
  packageOperationsForRepository
} from '../../src/routes/packages/page-model.js';

describe('packages live operation model', () => {
  const operations = [
    {
      id: 'promote-request',
      domain: 'package',
      operation: 'package/promote',
      repository_id: 'repo-1',
      status: 'completed',
      result_event_kind: 7991,
      updated_at: '2026-08-08T02:00:00Z'
    },
    {
      id: 'drift-request',
      domain: 'package',
      operation: 'package/drift-detect',
      entity_refs: { repository_id: 'repo-1' },
      status: 'drifted',
      result_event_kind: 7992,
      updated_at: '2026-08-08T03:00:00Z'
    },
    {
      id: 'other-request',
      domain: 'package',
      repository_id: 'repo-2',
      status: 'completed',
      updated_at: '2026-08-08T04:00:00Z'
    }
  ];

  it('selects repository outcomes and keeps the newest live event first', () => {
    expect(packageOperationsForRepository(operations, 'repo-1').map((operation) => operation.id))
      .toEqual(['drift-request', 'promote-request']);
    expect(latestPackageOperation(operations, 'repo-1')?.status).toBe('drifted');
  });

  it('surfaces drift events and readable package operation labels', () => {
    expect(packageDriftOutcome(operations, 'repo-1')?.result_event_kind).toBe(7992);
    expect(packageOperationLabel(operations[0])).toBe('promote');
  });
});
