# Signer-First Operator Rehearsal Artifact Bundle

- Policy issue: `bahia-rn20`
- Execution artifact issue: `bahia-noxg`
- Execution date (UTC): 2026-05-04
- Release commit under rehearsal: `afb06407c45d4ac307d4168fefc516e41835548f`
- Environment: local Docker + relay simulation
- Compose project: `bahia-rehearsal`
- Bahia server URL: `http://127.0.0.1:18081`
- Relay URL: `ws://127.0.0.1:13334/relay`
- Web URL: `http://127.0.0.1:13001`
- Signer mode: local keyer
- Authorized operator pubkey: `4f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aa`

## Scope covered

1. Unauthorized public signer-first operator scan fails closed.
2. Raw-target operator scan is rejected without explicit compatibility fallback.
3. Authorized signer-first scan succeeds across two endpoint refs (`local-a`, `local-b`).
4. Signer-first import succeeds for an explicitly selected Docker workload.
5. Signer-first `restart`, `stop`, and `deploy` succeed against the imported workload.
6. Relay request/status/result evidence is captured from subscriptions rather than HTTP polling assumptions.

## Imported workload and resulting IDs

- Target ref: `local-a`
- Container name: `bahia-rehearsal-nginx`
- Imported service name: `bahia-rehearsal-nginx`
- Imported service ID: `765f99b7-e2f4-4f8f-a88b-1db4328cbefb`
- Environment ID: `22f7d96b-b18f-41ac-a620-227a46c4b9ce`
- Build ID: `314b3283-8c52-43ec-af80-8a32d10af377`
- Artifact ID: `c71d2439-48c3-4b89-9dfe-5dc4d0dee72e`
- Import status: `created`

## Runtime action outcomes

- Restart observation: `b9874a48-e1bd-4363-9c6e-99ff7750640d` @ `2026-05-04T08:44:35.269095717Z` (`healthy`)
- Stop observation: `795a0f96-2449-4451-a61b-e39645bab446` @ `2026-05-04T08:44:35.857958592Z` (`stopped`)
- Deploy observation: `1ee2466e-2dcd-4766-9e62-7b18077ff426` @ `2026-05-04T08:44:36.366460843Z` (`healthy`)
- Restart kept the same container ID: `c8698ab4949db569e7c172e93c103957f811625f1d1a0747bb2007cb8108a5ad`
- Deploy replaced the container with: `44c60d58bb51cc91e615311e128564d83dcb4474255a94de66d8d4ef0306c6d0`

## Evidence files

- `config.redacted.yaml` — redacted rehearsal config used by server and relay
- `docker-compose.rehearsal.override.yml` — local port/config override used for the rehearsal stack
- `docker-compose-up.txt` / `docker-compose-ps.txt` — stack startup and health evidence
- `health.json` / `system-info.json` / `relay-info.json` — server and relay capability captures
- `operator-auth-inspect.txt` — signer identity used by the operator CLI
- `unauthorized-scan.*` — non-operator rejection evidence
- `raw-target-rejection.*` — explicit raw-target rejection evidence
- `authorized-scan.json` — positive multi-endpoint signer-first scan result
- `candidate-inspect-before.json` / `candidate-container-id.txt` — pre-import workload capture
- `import.json` / `import-ids.env` — signer-first import result and IDs
- `restart.json` / `stop.json` / `deploy.json` — signer-first direct-runtime results
- `container-state-before-restart.json` / `container-state-after-*.json` — runtime state transitions proving action effects
- `relay-events.json` / `event-summary.md` / `relay-events.count.txt` — captured request/status/result event evidence
- `bahia-relay-logs.txt` — relay-side log evidence during the rehearsal
- `database-entities.tsv` — imported entity and observation ledger

## Notes

- No private keys or secrets are stored in this bundle.
- Both endpoint refs map to the same local Docker socket for rehearsal only. This proves endpoint-ref routing without requiring two physical hosts.
- Compose takeover was not in scope for this rehearsal.
- `features.encrypted_nostr_requests` is `false` in `system-info.json`; this rehearsal covers the public signer-first operator path for adoption/import/direct-runtime, not encrypted browser flows.
