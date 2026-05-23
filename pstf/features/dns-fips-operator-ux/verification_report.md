# Verification Report: dns-fips-operator-ux

## Evidence gathered before edits

- `bd prime`, `bd show bahia-ky76`, and `bd update bahia-ky76 --claim` were run before editing.
- Child PSTF reports cite completed DNS/FIPS behavior:
  - `bahia-5l4f`: web DNS command client builds signed Nostr DNS requests, verifies relay OK outcomes, subscribes to status, awaits terminal result events, and avoids REST writes.
  - `bahia-0jh6`: FIPS mesh browser store uses EOSE-aware Nostr read-model bootstrap/live subscriptions with replaceable dedupe, tombstones, CLOSED handling, and no REST FIPS state endpoint.
  - `bahia-i9v3`: MCP exposes FIPS mesh tools/resources from mesh DNS projection records and adds no REST endpoints.
  - `bahia-xere`: DNS dashboard read paths use scoped Nostr subscriptions for DNS read models and remove REST refresh/dashboard substrate behavior.
  - `bahia-66d4`: resolver uses shared EOSE/CLOSED-aware subscriptions, NIP-11 metadata fetch, and inbound event validation.
  - `bahia-mou1`: DNS web control panels dispatch through Nostr DNS command APIs and render signer readiness, OK summaries, progress status, terminal result details, and errors.
  - `bahia-wprt`: DNS/operator UI exposes FIPS mesh visibility through the EOSE-aware Nostr FIPS mesh store without fake state, polling, or REST endpoints.
- Repository evidence confirmed current DNS control-plane backend handlers in `internal/controlplane/dns_handlers.go`, DNS request subscription wiring in `internal/controlplane/reactor.go`, and stale docs in `docs/control-planes.md` / `docs/event-spec.md` that still described DNS requests as reserved-only.

## Documentation changes

- Updated `docs/control-planes.md` to describe the current DNS/FIPS operator UX for humans and agents:
  - Web DNS/FIPS reads come from Nostr read models/subscriptions.
  - DNS writes use signed 594x Nostr control-plane requests with OK/status/result tracking.
  - MCP exposes DNS/FIPS discovery and action surfaces for agent operators.
  - REST is compatibility/query-only for this UX, not the dashboard substrate or DNS write plane.
- Updated `docs/event-spec.md` to remove the stale reserved-only claim for DNS operator commands and describe active DNS request/status/result roles.
- Updated `docs/designs/fips-bahia-integration.md` with the implemented operator UX status: FIPS mesh state is observed through Nostr read models and worker/DNS projection, not fake state.

## Verification commands

- `python3 -m json.tool pstf/features/dns-fips-operator-ux/feature_spec.json >/tmp/feature.json && python3 -m json.tool pstf/features/dns-fips-operator-ux/acceptance_criteria.json >/tmp/ac.json && python3 -m json.tool pstf/features/dns-fips-operator-ux/test_matrix.json >/tmp/tm.json && python3 -m json.tool pstf/features/dns-fips-operator-ux/defects.json >/tmp/defects.json` — passed.
- `go test ./internal/controlplane ./internal/mcp ./pkg/discovery` — passed (`internal/controlplane` 0.321s; `internal/mcp` cached; `pkg/discovery` cached).
- `cd web && npm run test:unit -- --run tests/unit/dns-controlplane.test.js tests/unit/dns-store-commands.test.js tests/unit/dns-store-subscriptions.test.js tests/unit/dns-page-model.test.js tests/unit/fips-mesh-store.test.js tests/unit/fips-mesh-panel.test.js` — passed, 6 files / 31 tests.
- `grep -RInE "router\.(POST|PUT|PATCH|DELETE)|HandleFunc\([^)]*dns|/dns" internal/api cmd || true` — no matches, confirming no REST DNS write route patterns in backend API routing paths.
- `grep -RInE "fetch\([^)]*(POST|PUT|PATCH|DELETE)|method:\s*['\"](POST|PUT|PATCH|DELETE)['\"]" web/src/routes/dns web/src/lib/stores/dns.svelte.js web/src/lib/nostr/dns-controlplane.js || true` — no matches, confirming no fetch-based DNS/FIPS dashboard mutations in the checked web paths.
- `grep -RInE "api/v1.*dns|/api/.*dns" web/src/routes/dns web/src/lib/stores/dns.svelte.js web/src/lib/nostr/dns-controlplane.js || true` — no matches, confirming the dashboard paths are not calling REST DNS catalog endpoints.

## Result

The DNS/FIPS operator UX rollup satisfies the acceptance criteria. The docs now match repository behavior: browser DNS/FIPS reads use Nostr read models/subscriptions, browser DNS writes use signed Nostr DNS control-plane events with relay OK/status/result handling, MCP exposes DNS/FIPS discovery/action surfaces for agents, and REST is not the DNS/FIPS dashboard substrate or write plane.

## Remaining defects

None observed in this docs/PSTF rollup scope. No follow-up Beads issue was created because verification did not identify a remaining real gap in the touched scope.
