import { describe, it, expect, vi } from 'vitest';
import AssistantPlanApproval from '../../../src/lib/components/assistant/AssistantPlanApproval.svelte';
import AssistantTurn from '../../../src/lib/components/assistant/AssistantTurn.svelte';
import { renderComponent, textOf } from '../utils/svelte-component-test';

vi.mock('../../../src/lib/stores/assistant.svelte.js', () => ({
  publishAssistantApproval: vi.fn(),
  downstreamRequestsForTurn: (item) => item?.downstreamRequestId ? [item.downstreamRequestId] : []
}));

describe('assistant components', () => {
  it('renders plan approval steps with tool names, args previews, and decisions', () => {
    const target = renderComponent(AssistantPlanApproval, {
      sessionId: 'assistant-session-1',
      planHash: 'plan-hash-1',
      plan: {
        summary: 'Deploy the chat route',
        risk_level: 'medium',
        steps: [
          {
            step_id: 'step-1',
            title: 'Deploy LLM route',
            description: 'Roll the route out to staging.',
            tool_name: 'llm.deploy',
            args_preview: { route_id: 'route-1', environment_id: 'staging' }
          },
          {
            step_id: 'step-2',
            title: 'Approve deployment',
            tool_name: 'llm.approve',
            tool_args: { deployment_id: 'deploy-1' }
          }
        ]
      }
    });

    const text = textOf(target);
    expect(text).toContain('Plan review');
    expect(text).toContain('Deploy the chat route');
    expect(text).toContain('medium');
    expect(text).toContain('Deploy LLM route');
    expect(text).toContain('llm.deploy');
    expect(text).toContain('route-1');
    expect(text).toContain('staging');
    expect(text).toContain('Approve deployment');
    expect(text).toContain('llm.approve');
    expect(text).toContain('deploy-1');
    expect(target.querySelector('button.approve')?.textContent).toBe('Approve');
    expect(target.querySelector('button.reject')?.textContent).toBe('Reject');
  });

  it('shows blocked visual state for a relay-closed assistant turn', () => {
    const target = renderComponent(AssistantTurn, {
      operatorPubkey: 'a'.repeat(64),
      session: { sessionId: 'assistant-session-1', state: 'blocked', lastPlanHash: '' },
      item: {
        id: 'result-blocked',
        type: 'result',
        pubkey: 'b'.repeat(64),
        createdAt: 100,
        status: 'blocked',
        blocked: true,
        error: 'relay closed before terminal result',
        downstreamRequestId: 'downstream-1'
      }
    });

    const text = textOf(target);
    expect(text).toContain('Assistant');
    expect(text).toContain('blocked');
    expect(text).toContain('relay closed before terminal result');
    expect(text).toContain('downstream-1');
    expect(target.querySelector('.badge.blocked')?.textContent).toBe('blocked');
  });
});
