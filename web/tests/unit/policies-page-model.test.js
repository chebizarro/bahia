import { describe, expect, it } from 'vitest';
import { policyEvaluationHistory, policyEvaluationLabel } from '../../src/routes/policies/page-model.js';

describe('policy evaluation event history', () => {
  it('extracts the selected policy result from durable operation events', () => {
    const history = policyEvaluationHistory([
      {
        id: 'evaluation-1',
        status: 'completed',
        updated_at: '2026-08-08T04:00:00Z',
        result_event: {
          content: {
            results: [
              { policy_id: 'policy-1', passed: false, enforcement: 'block', violations: [{ rule: 'require_sbom' }] },
              { policy_id: 'policy-2', passed: true }
            ]
          }
        }
      }
    ], 'policy-1');

    expect(history).toHaveLength(1);
    expect(history[0].policy_result.enforcement).toBe('block');
    expect(policyEvaluationLabel(history[0])).toBe('Fail');
  });

  it('retains directly tagged in-progress policy events', () => {
    const history = policyEvaluationHistory([
      { id: 'evaluation-2', entity_refs: { policy_id: 'policy-1' }, status: 'processing' }
    ], 'policy-1');

    expect(policyEvaluationLabel(history[0])).toBe('processing');
  });
});
