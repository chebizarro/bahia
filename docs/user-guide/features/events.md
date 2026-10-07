# Events

**Events** (`/events`) is a live inspection view of the Nostr events behind Bahia's control plane.

## What it loads

The browser's store-first subscriptions feed the page: the service's canonical `30900` records (up to 1,000 per topic set), the last seven days of `30315` status events (100) and `4903` audit facts (500), SBOM references and availability lists, deletions, worker advertisements (`10100`), and — when configured — trusted ops widgets (`30318`). Subscriptions stay open after EOSE and resume with a one-second overlap after a disconnect; duplicate event ids are suppressed.

The relay indicator shows how many relays have caught up. A connected relay is transport evidence only; every event is still checked for signature, author, and tags before it is accepted.

## Filtering and inspection

Filter by category — **All Events**, **Deployments**, **Services**, **LLM Routes**, **Policies**, **SBOM**, **Artifacts** — and choose 25, 50, or 100 rows per page. Columns show time, event type, and entity id; selecting a row opens the full JSON (kind, author, tags, content, timestamps).

## Uses

- Correlate an intent with its `30315` status, the resulting `30900` record, and `4903` audit facts.
- Confirm an event's author and tags when a page shows unexpected data.
- Distinguish a relay that has not delivered an event from a projection that never happened.

The most recent arrival is not automatically canonical: replaceable-event ordering per coordinate and authorized authors decide what is accepted.

## Related

- [Nostr Integration](../nostr-integration.md)
- [Deployments](deployments.md)
- [Artifacts](artifacts.md)
