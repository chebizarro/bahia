import { defineConfig, mergeConfig } from 'vitest/config';
import base from './vitest.config.js';

export default mergeConfig(base, defineConfig({
  test: {
    coverage: {
      enabled: true,
      provider: 'v8',
      all: true,
      reporter: ['json-summary', 'text'],
      reportsDirectory: '../coverage/web/llm',
      include: [
        'src/lib/auth/route-access.js',
        'src/lib/components/nav-model.js',
        'src/routes/llm/page-model.js'
      ]
    }
  }
}));
