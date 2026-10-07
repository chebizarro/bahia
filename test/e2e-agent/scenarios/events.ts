/**
 * Nostr sidecar control-plane verification scenarios.
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

function splitList(value: string | undefined): string[] {
  return (value || '').split(',').map((item) => item.trim()).filter(Boolean);
}

export const sidecarRelayDiscovery: Scenario = {
  name: 'Discover Nostr Sidecar Relay',
  description: 'Verify explicit Nostr bootstrap seeds are present for sidecar read-model discovery',
  tags: ['events', 'nostr', 'sidecar', 'smoke'],

  async run(_drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();

    try {
      const discoverStart = Date.now();
      const relays = splitList(process.env.BAHIA_BOOTSTRAP_RELAYS || process.env.BAHIA_NOSTR_RELAYS);
      const servicePubkeys = splitList(process.env.BAHIA_SERVICE_PUBKEYS || process.env.BAHIA_SERVICE_PUBKEY);
      if (relays.length === 0) {
        throw new Error('no explicit Nostr bootstrap relays configured; set BAHIA_BOOTSTRAP_RELAYS or BAHIA_NOSTR_RELAYS');
      }
      if (servicePubkeys.length === 0) {
        throw new Error('no trusted Bahia service pubkey configured; set BAHIA_SERVICE_PUBKEYS or BAHIA_SERVICE_PUBKEY');
      }
      steps.push(step('Read explicit Nostr bootstrap seed', 'passed', Date.now() - discoverStart));
      steps.push(step('Validate explicit relay and service-pubkey requirements', 'passed', Date.now() - discoverStart));

      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { relays, servicePubkeys },
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
