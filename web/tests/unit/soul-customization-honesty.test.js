import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

function source(path) {
  return readFileSync(resolve(process.cwd(), path), 'utf8');
}

const newSoulSource = source('src/routes/souls/new/+page.svelte');
const soulDetailSource = source('src/routes/souls/[id]/+page.svelte');
const soulEditSource = source('src/routes/souls/[id]/edit/+page.svelte');
const avatarSource = source('src/lib/components/souls/AvatarStudio.svelte');
const voiceSource = source('src/lib/components/souls/VoiceStudio.svelte');
const memorySource = source('src/lib/components/MemoryConfig.svelte');

describe('Soul Factory customization honesty contracts', () => {
  it('serializes only an explicit agent model and presents fleet/runtime inheritance', () => {
    expect(newSoulSource).toContain("let runtimeModel = $state('');");
    expect(newSoulSource).toContain("...(runtimeModel.trim() ? { model: runtimeModel.trim() } : {})");
    expect(newSoulSource).toContain('placeholder={runtimeModelPlaceholder}');
    expect(newSoulSource).toContain('fleetConfigStore.subscribe()');
    expect(newSoulSource).toContain('agents add --model');
  });

  it('labels tool and approval policy as draft intent rather than OpenClaw enforcement', () => {
    for (const pageSource of [newSoulSource, soulDetailSource, soulEditSource]) {
      expect(pageSource).toContain('tools, MCP, or plugin enforcement');
    }
    expect(newSoulSource).toContain('Saved to draft — runtime enforcement varies.');
    expect(soulEditSource).toContain('Saved to draft — runtime enforcement varies.');
  });

  it('never creates a browser-local avatar URL and gates unavailable actions', () => {
    expect(avatarSource).not.toContain('URL.createObjectURL');
    expect(avatarSource).not.toContain('type="file"');
    expect(avatarSource).toContain('hasInteractiveGenerator');
    expect(avatarSource).toContain('web app has no durable Blossom upload endpoint');
    expect(avatarSource).toContain('placeholder="blossom:… or https://…"');
  });

  it('requires a real sample dispatcher or deployed soul for voice playback', () => {
    expect(voiceSource).toContain("typeof onPlay === 'function' || Boolean(soul)");
    expect(voiceSource).not.toContain('Voice draft updated.');
    expect(voiceSource).toContain('Sample playback is available after deployment');
    expect(soulDetailSource).toContain('showAdvanced={true} {soul} />');
  });

  it('keeps pre-deployment reindex disabled and distinguishes publish from execution', () => {
    expect(memorySource).toContain('const canReindex = $derived(Boolean(soul)');
    expect(memorySource).toContain('Reindex is available only for a deployed soul.');
    expect(memorySource).toContain('Relay acceptance confirms publication only');
  });
});
