# HITL decisions — BAHIA_ROUTE_CANARY_NOSTR_PROJECTION (bahia-dblkq)

Decisions taken within the delegated brief. Each one is recorded so a human can revisit it.

## 1. `domain=route` vocabulary

- cascadia-nips NIP-CAS-0002 lists canonical `domain` values and states that new domains may be added without amending the NIP (`registry/tags.yaml` canonical_values is advisory). `route` is not a registered domain and is not used elsewhere in Bahia; LLM routes use `domain=llm`.
- The Cascadia `route` **tag** is registered as an LLM/API route identifier. The managed hostname is therefore carried in a `hostname` tag, and no `route` tag is emitted.
- No cascadia-nips change is required. Registering `route` in the NIP-CAS-0002 canonical domain table upstream is optional.

## 2. Fleet-health status mapping

- Open outage → `unhealthy`, whatever the latest classification. The outage stays declared until the success threshold is met.
- Failing below the open threshold → `degraded`. The failure is real but not yet a declared outage.
- Warnings (`tls_expiring`, `health_path_not_discriminating`) → `degraded`. The route serves traffic, so it is not `unhealthy`, but it needs attention, so it is not `healthy`.
- `route_ok` → `healthy`; unrecognized classification → `unknown`.

## 3. Route audits are lineage in fleet-health

The precedent runtime audits (no `d`) collapse into one signer-wide entity per domain. For `route`, that would add a phantom route to the gauge, so route `4903` facts are classified as lineage and ignored. Other domains are unchanged.

## 4. Subscriber observer fix (D1)

Fixing self-echo suppression was required for the acceptance criterion ("the Prometheus gauge counts them") to hold in a running Bahia. The fix is additive: a new `WithObserver` option, and fleet-health telemetry moved from `WithHandler` to it. Side-effect handler gating is unchanged.

## 5. Projector gated on Nostr publishing

The projector is constructed only when `nostr.publish_enabled` is set and a service private key is configured, matching the stale-run detector. With publishing disabled, route outages remain visible through the REST API and in-process events.
