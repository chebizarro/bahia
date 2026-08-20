# Bahia / OpenClaw provisioning deployment map

Task: **bahia-openclaw-rollout-conformance-20260819**

## Evidence boundary

This inventory is derived only from repository files and the authoritative task specification. No host, relay, Signet, or OpenClaw endpoint was contacted. Values not represented in the repository are marked **Track B discovery required**. Secret values, bunker URIs, client keys, tokens, and private-key paths are intentionally omitted.

Repository evidence: .github/workflows/deploy-edge.yml; docker-compose.yml; docker-compose.deploy.yml; docker-compose.edge-soulfactory.yml; docs/openclaw-soulfactory-control-wrapper.md; docs/soul-factory-sidecar-runbook.md; docs/soulfactory-runtime-control.md; internal/app/soulfactory.go; internal/soulfactory/openclawcontrol/runtime_orchestrator.go; internal/soulfactory/signet_enrollment.go; internal/soulfactory/saga/.

## Repository-observed shape

| Area | Repository-observed contract | Ownership / preservation rule |
| --- | --- | --- |
| Bahia | Edge workflow builds Bahia and web at an exact GitHub SHA, resolves local images to sha256 IDs, updates host Compose, and gates readiness, relay NIP-11, web, relay-policy continuity, and Marjam gallery presence. | Release-owned containers may be replaced. Durable runs and valid identities are not rollback targets. |
| Relay | Default Compose has a dedicated relay with persistent relaydata; the edge override lists local, public, and additional relays. Exact deployed policy/routes require Track B inspection. | Back up history before policy/image changes. Disconnect is recovery/backfill input, never terminal success. |
| Signet | Bahia constructs containerized signetctl administration and protected enrollment/client-key state. Enrollment applies exact-client policy, verifies NIP-46, persists the durable reference, then removes one-time handoff. | Identity/key custody belongs to Signet. Rollback removes only a failed run's owned client material, never a valid identity. |
| Automated souls | per-agent-compose is the only live provisioning mode. Each soul gets deterministic names, separate config/agent/workspace mounts, ownership labels, limits, immutable image, pinned source, exact account binding, and plugin allowlist. | Only matching saga-created resources are compensable; adopted/pre-existing resources are never deleted. |
| Shared gateways | existing-container is dry-run compatibility only. The current Nostr plugin exposes one top-level identity per gateway. | Shared incumbent gateways are protected from external-soul mutation. |
| Images | docker-compose.deploy.yml requires digest-shaped Bahia/web inputs. The workflow resolves local image IDs. Historical mutable recovery tags are not rollout pins. | Canary and production use the same recorded OCI digests. |
| Volumes | Default Compose declares pgdata and relaydata. Per-soul Compose binds separate config, agent, workspace, and protected secret files. | Back up database, relay, saga, Signet, and per-soul state without plaintext secrets. |
| Build identity | Integrated baseline is f88ec8fa418485670803c3ee72e2dd10d7de601e. | Metrics/logs expose instance and build identity. |

## Incumbent mapping

### Marjam

Repository evidence treats Marjam as the expected incumbent gallery soul and documents shared/single-identity gateway behavior. Its exact container, Signet policy, public identity, route, mounts, and image digest are not fully represented in source.

Automated mapping:

1. Classify gateway, route, workspace, and identity as pre_existing.
2. Do not run soulfactory.provision against its shared gateway.
3. Preserve reachability at every canary and rollback gate.
4. Any identity, policy, route, volume, or gateway change requires separate review.
5. Retain the edge gallery gate as the non-secret reachability check.

### SNR

Task context records that SNR was manually added to the Marjam single-identity gateway without a dedicated Nostr account, Signet policy, subscription, route, or verified DM path. The repository has no canonical host inventory for SNR.

Automated mapping:

1. Discover and sanitize current public identity/runtime lineage in Track B.
2. Reconcile into managed inventory as adopted or pre_existing.
3. Do not recreate, rotate, export, or replace its key during adoption.
4. Do not claim running until the independent encrypted DM-to-LLM-to-Signet-signed-reply gate succeeds.
5. A dedicated-gateway migration retains the old path for rollback and leaves identity custody unchanged.

## Track B discovery worksheet

Capture only public references or one-way hashes:

| Entity | Required facts | Evidence form |
| --- | --- | --- |
| Marjam and SNR | public identity, account/agent binding, route IDs, relay set, runtime/container | signed public events, redacted inspection, one-way refs |
| Shared gateways | owner, host role, image digest, source commit, labels, policy | sanitized Compose/inspect |
| Signet | instance/build, public bunker identity, client public keys, policy revision | sanitized signetctl; no URI secrets |
| Relays | NIP-11, NIP-42, read/write/control policy, EOSE/backfill | sanitized protocol transcript |
| Volumes | purpose, owner UID/GID, backup ID, restore result | path class and backup digest |
| Images | repository, immutable OCI digest, SBOM/provenance | release record |

Review Track B output for credential patterns before attachment.
