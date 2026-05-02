/**
 * Nostr sidecar control-plane verification scenarios.
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

type SystemInfo = {
  features?: Record<string, boolean>;
  nostr?: { browser_relays?: string[] };
};

type APIEnvelope = {
  data?: SystemInfo;
};

async function fetchSystemInfo(apiUrl: string): Promise<SystemInfo> {
  const response = await fetch(`${apiUrl}/api/v1/system/info`, {
    signal: AbortSignal.timeout(2000),
  });
  if (!response.ok) {
    throw new Error(`system info failed: ${response.status}`);
  }
  const payload = (await response.json()) as APIEnvelope | SystemInfo;
  if ('data' in payload && payload.data) {
    return payload.data;
  }
  return payload as SystemInfo;
}

export const sidecarRelayDiscovery: Scenario = {
  name: 'Discover Nostr Sidecar Relay',
  description: 'Verify /system/info advertises sidecar read models and no legacy SSE/JWT/agent HTTP surfaces',
  tags: ['events', 'nostr', 'sidecar', 'api', 'smoke'],

  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;

    try {
      const discoverStart = Date.now();
      const info = await fetchSystemInfo(apiUrl);
      steps.push(step('Fetch system info', 'passed', Date.now() - discoverStart));

      const features = info.features || {};
      if (features.legacy_sse || features.legacy_jwt_exchange || features.legacy_agent_http || features.nostr_auth_exchange) {
        throw new Error(`legacy control-plane feature still advertised: ${JSON.stringify(features)}`);
      }
      if (!features.relay_sidecar || !features.relay_read_models) {
        throw new Error('sidecar relay/read-model features are not advertised');
      }

      const relays = info.nostr?.browser_relays || [];
      if (!Array.isArray(relays) || relays.length === 0) {
        throw new Error('no browser relay endpoints advertised');
      }
      steps.push(step('Validate sidecar-first feature flags', 'passed', Date.now() - discoverStart));

      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { relays },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

export const eventScenarios: Scenario[] = [
  sidecarRelayDiscovery,
];
