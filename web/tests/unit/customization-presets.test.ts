import { describe, expect, it } from 'vitest';

import { customizationPresets } from '$lib/data/customization-presets';

const supportedRerankModels = new Set([
  'cohere-rerank-v3',
  'rerank-v3.5',
  'rerank-english-v3.0',
  'rerank-multilingual-v3.0'
]);

describe('Soul Factory customization presets', () => {
  it('only emits rerank models accepted by the backend draft validator', () => {
    for (const preset of customizationPresets) {
      const search = preset.content.memory?.search;
      if (!search?.rerank_model) continue;

      expect(search.rerank, `${preset.id} must enable reranking`).toBe(true);
      expect(supportedRerankModels.has(search.rerank_model), preset.id).toBe(true);
    }
  });
});
