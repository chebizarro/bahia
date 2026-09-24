import { beforeEach, describe, expect, it } from 'vitest';
import { replaceableKey, shouldAcceptReplaceableEvent, upsertReplaceableEvent } from '../../src/lib/nostr/replaceable.js';
import { compareProjectionVersions } from '../../src/lib/stores/collections/utils.js';
import { deploymentApplicators, deploymentIntents, states, resetDeployments, refreshDeployments } from '../../src/lib/stores/collections/deployments.svelte.js';

const low = '1'.repeat(64);
const high = 'f'.repeat(64);
const pubkey = 'a'.repeat(64);
function event(id, { created_at = 100, d = 'svc:env', domainTime = '2026-09-23T12:00:00Z', deleted = false } = {}) {
  return { id, kind: 30900, pubkey, created_at, tags: [['d', d]], content: JSON.stringify({
    id: 'intent', service_id: 'svc', environment_id: 'env', updated_at: domainTime, deleted
  }) };
}
function permutations(items) {
  return items.length < 2 ? [items] : items.flatMap((item, i) => permutations(items.filter((_, j) => i !== j)).map((rest) => [item, ...rest]));
}

it('uses lowest ID on a wire timestamp tie and still gives timestamps precedence', () => {
  expect(shouldAcceptReplaceableEvent(event(high), event(low))).toBe(true);
  expect(shouldAcceptReplaceableEvent(event(low), event(high))).toBe(false);
  expect(shouldAcceptReplaceableEvent(event(low), event(low))).toBe(false);
  expect(shouldAcceptReplaceableEvent(event(low), event(high, { created_at: 101 }))).toBe(true);
  expect(shouldAcceptReplaceableEvent(event(high), event(low, { created_at: 99 }))).toBe(false);
  const version = { domainTime: 1, relayTime: 100 };
  expect(compareProjectionVersions({ ...version, eventId: low }, { ...version, eventId: high })).toBe(1);
  expect(compareProjectionVersions({ ...version, eventId: high }, { ...version, eventId: low })).toBe(-1);
});

it('keys regular replaceable kinds by kind/author and addressable kinds by kind/author/d', () => {
  for (const kind of [0, 3, 10002, 11316]) {
    expect(replaceableKey({ ...event(low), kind })).toBe(`${kind}:${pubkey}`);
  }
  expect(replaceableKey(event(low))).toBe(`30900:${pubkey}:svc:env`);
  expect(replaceableKey({ ...event(low), kind: 30315 })).toBe(`30315:${pubkey}:svc:env`);
});

it.each([false, true])('retains the lowest-ID tombstone or live event in either arrival order (deleted=%s)', (deleted) => {
  const winner = event(low, { deleted });
  const loser = event(high, { deleted: !deleted });
  for (const order of [[winner, loser], [loser, winner]]) {
    const map = new Map();
    order.forEach((e) => upsertReplaceableEvent(map, e));
    expect([...map.values()]).toEqual([winner]);
    expect(upsertReplaceableEvent(map, loser).accepted).toBe(false);
  }
});

for (const [name, rows] of [['serviceState', states], ['intent', deploymentIntents]]) {
  describe(`${name}: coordinate winners precede domain-time merging`, () => {
    beforeEach(resetDeployments);
    for (const deleted of [false, true]) {
      it(`converges in every delivery order even when the losing event has a newer domain clock (deleted=${deleted})`, () => {
        const winner = event(low, { deleted });
        const loser = event(high, { domainTime: '2026-09-23T13:00:00Z', deleted: !deleted });
        for (const order of [[winner, loser], [loser, winner]]) {
          resetDeployments();
          const map = new Map();
          order.forEach((e) => deploymentApplicators[name](e, map));
          refreshDeployments();
          expect(rows.map((row) => row.nostr_event_id)).toEqual(deleted ? [] : [low]);
          expect([...map.values()]).toEqual([winner]);
          expect(deploymentApplicators[name](loser, map)).toBe(false);
        }
      });
    }
    it('recomputes the logical winner if a NIP-01 winner displaces a candidate with a newer domain clock', () => {
      const winner = event(low);
      const loser = event(high, { domainTime: '2026-09-23T14:00:00Z' });
      const legacy = event('8'.repeat(64), { d: 'legacy', domainTime: '2026-09-23T13:00:00Z' });
      for (const order of permutations([winner, loser, legacy])) {
        resetDeployments();
        const map = new Map();
        order.forEach((e) => deploymentApplicators[name](e, map));
        refreshDeployments();
        expect(rows.map((row) => row.nostr_event_id)).toEqual([legacy.id]);
        expect(map.get(replaceableKey(winner))).toEqual(winner);
      }
    });
  });
}
