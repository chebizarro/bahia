import { execSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

const packageJson = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'));
const repoRoot = fileURLToPath(new URL('..', import.meta.url));

function gitCommit() {
  try {
    return execSync('git rev-parse HEAD', { cwd: repoRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
  } catch {
    return 'dev';
  }
}

const baseVersion = process.env.PUBLIC_BAHIA_WEB_BASE_VERSION || process.env.VITE_BAHIA_WEB_BASE_VERSION || packageJson.version || '0.1.0';
const gitCommitHash = process.env.PUBLIC_BAHIA_GIT_COMMIT || process.env.VITE_BAHIA_GIT_COMMIT || process.env.GIT_COMMIT || gitCommit();
const webVersion = process.env.PUBLIC_BAHIA_WEB_VERSION || process.env.VITE_BAHIA_WEB_VERSION || `${baseVersion}-${gitCommitHash}`;

export default defineConfig({
  plugins: [sveltekit()],
  define: {
    __BAHIA_WEB_BASE_VERSION__: JSON.stringify(baseVersion),
    __BAHIA_WEB_COMMIT__: JSON.stringify(gitCommitHash),
    __BAHIA_WEB_VERSION__: JSON.stringify(webVersion)
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080'
    }
  }
});
