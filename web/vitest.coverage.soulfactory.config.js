import { defineConfig, mergeConfig } from 'vitest/config';
import base from './vitest.config.js';

export default mergeConfig(base, defineConfig({
  test: {
    coverage: {
      enabled: true,
      provider: 'v8',
      all: true,
      reporter: ['json-summary', 'text'],
      reportsDirectory: '../pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage/web',
      include: [
        'src/lib/auth/route-access.js',
        'src/lib/components/nav-model.js',
        'src/lib/stores/souls.svelte.js',
        'src/routes/souls/page-model.js'
      ]
    }
  }
}));
