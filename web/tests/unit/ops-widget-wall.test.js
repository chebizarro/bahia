import { afterEach, describe, expect, it, vi } from 'vitest';
import { DASHBOARD_WIDGET } from '../../src/lib/nostr/kinds.gen.js';
import {
  createOpsWidgetWall,
  getOpsWidgetAllowedPubkeys,
  parseOpsWidgetPublisherAllowlist
} from '../../src/lib/widgets/ops-widget-wall.js';

const PUBKEY = 'ab'.repeat(32);
const OTHER = 'cd'.repeat(32);

function widgetEvent(id, pubkey = PUBKEY, created_at = 100) {
  return { id, pubkey, created_at, kind: DASHBOARD_WIDGET, tags: [['d', 'cpu:host-a:api:5m']], content: '{}' };
}

function harness(initial = [], allowedPubkeys = [PUBKEY]) {
  let events = initial;
  let onRefresh;
  const unregister = vi.fn();
  const store = {
    query: vi.fn(({ kinds, authors }) => events.filter((event) => kinds.includes(event.kind) && authors.includes(event.pubkey)))
  };
  const wall = createOpsWidgetWall({
    allowedPubkeys,
    eventStore: () => store,
    registerRefresh: (callback) => { onRefresh = callback; return unregister; }
  });
  return { wall, store, unregister, setEvents: (next) => { events = next; onRefresh?.(); } };
}

afterEach(() => vi.unstubAllGlobals());

describe('Bahia ops widget store-first wall', () => {
  it('normalizes and validates publisher allowlist entries', () => {
    expect(parseOpsWidgetPublisherAllowlist(` ${PUBKEY.toUpperCase()},invalid,${PUBKEY} `)).toEqual([PUBKEY]);
    expect(parseOpsWidgetPublisherAllowlist([OTHER.toUpperCase(), 'bad'])).toEqual([OTHER]);
  });

  it('takes runtime deployment publisher trust over build-time configuration', () => {
    vi.stubGlobal('window', { __BAHIA_BOOTSTRAP__: { widget_pubkeys: [PUBKEY, 'invalid'] } });
    expect(getOpsWidgetAllowedPubkeys()).toEqual([PUBKEY]);
    window.__BAHIA_BOOTSTRAP__.widget_pubkeys = [];
    expect(getOpsWidgetAllowedPubkeys()).toEqual([]);
  });

  it('queries cached trusted widgets before network and refreshes from the shared store', () => {
    const cached = widgetEvent('1'.repeat(64));
    const untrusted = widgetEvent('2'.repeat(64), OTHER);
    const live = widgetEvent('3'.repeat(64), PUBKEY, 101);
    const { wall, store, unregister, setEvents } = harness([cached, untrusted]);
    const snapshots = [];
    const unsubscribe = wall.subscribe((events) => snapshots.push(events.map((event) => event.id)));

    wall.start();
    expect(store.query).toHaveBeenCalledWith({ kinds: [DASHBOARD_WIDGET], authors: [PUBKEY] });
    expect(snapshots.at(-1)).toEqual([cached.id]);
    setEvents([live, untrusted]); // Boot's frame callback re-queries latest-by-slot state.
    expect(snapshots.at(-1)).toEqual([live.id]);
    wall.stop();
    expect(unregister).toHaveBeenCalledOnce();
    unsubscribe();
  });

  it('fails closed without configured publishers', () => {
    const { wall, store } = harness([widgetEvent('1'.repeat(64))], []);
    let events;
    wall.subscribe((snapshot) => { events = snapshot; });
    wall.start();
    expect(events).toEqual([]);
    expect(store.query).not.toHaveBeenCalled();
    wall.stop();
  });
});
