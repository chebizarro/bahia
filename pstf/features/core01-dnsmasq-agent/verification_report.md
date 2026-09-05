# Verification Report: core01-dnsmasq-agent

Nostrig task: `bahia-core01-dnsmasq-backend-20260905` · Beads: `bahia-sekvq` (WS5, this artifact set) building on the sibling implementation beads for engine/protocol/agent, backend, transport, and config wiring.

## Evidence

- Added `internal/adapters/dns/dnsmasq_agent_e2e_test.go` (package `dns_test`): five end-to-end tests wiring `DnsmasqAgentBackend` to the DNS agent service core in-process. The shim between them serializes every request/response through the exact ContextVM JSON-RPC wire JSON (`cascontextvm.Request`/`Response` byte round trips), so backend↔agent wire compatibility is proven without relays:
  - `TestDnsmasqAgentBackendEndToEndAstilleroFlow` — SyncZone of `astillero.sharegap.net -> 192.168.40.104` writes exactly `# Managed by Bahia...\n# Zone: sharegap.net\naddress=/astillero.sharegap.net/192.168.40.104\n` into `bahia-sharegap-net.conf` (t.TempDir, stub reload runner, one reload); ListRecords round-trips the record; `dns-agent/health` reports the applied serial.
  - `TestDnsmasqAgentBackendPreservesForeignIncludeFiles` — a pre-existing `sharegap-splitdns.conf` is byte-identical after an initial sync and a subsequent sync with changed records.
  - `TestDnsmasqAgentBackendReloadFailureRollsBackAndRecovers` — failing reload runner surfaces an error from `SyncZone`, prior include bytes are preserved, the serial does not advance, and the next successful sync recovers and advances it.
  - `TestDnsmasqAgentBackendAllowlistRejectionSurfaces` — a disallowed zone is rejected with `not allowed by agent allowlist` through both SyncZone and ListRecords, and no `bahia-*.conf` is created.
  - `TestDNSReconcilerConvergesRemoteAgentInclude` — `reconcile.DNSReconciler.ReconcileOnce` with the dnsmasq_agent backend behind `dns.NewStaticResolver` (bridged like `internal/app/app.go`'s `dnsResolverBridge`) and a projector emitting the Astillero observation converges the remote include file; a second reconcile is a no-op (file unchanged, still one reload).
- Added `docs/runbooks/core01-dnsmasq-agent.md`: core-01 deployment referencing `deploy/dns-agent/*`, file-only key generation, agent config (relay `wss://relay.sharegap.net`, authorized Bahia service pubkey placeholder, include dir `/etc/dnsmasq.d`, allowlist `sharegap.net`, explicit reload command per init system), Bahia-side `dns:` exemplar with `type: dnsmasq_agent` + `agent_pubkey` placeholder + `edge-01-production -> sharegap.net`, migration steps that leave `sharegap-splitdns.conf` untouched and delete only the astillero line after verified Bahia-managed resolution, rollback, and the portability matrix (systemd/procd/generic; amd64/arm64; mips blocked by `bahia-1m1ef`). Operator vs automated steps are tagged explicitly.
- Updated `docs/user-guide/features/dns.md` and `docs/user-guide/guides/managed-dns-and-https-routes.md` with the `dnsmasq_agent` backend for the remote-resolver (edge-01/core-01) topology, including a config exemplar and runbook link. `docs/user-guide/index.md` does not index runbooks, so it was not changed.

## Commands Run

- `go test ./internal/adapters/dns/ -run 'TestDnsmasqAgentBackend|TestDNSReconcilerConverges' -count=1` — passing (all five tests).
- `go build ./...` — passing.
- `go vet ./...` — passing.
- `go test ./... -count=1` — passing.

## Nostr Review

- The e2e path exercises the approved ContextVM exception exactly: JSON-RPC 2.0 kind-25910 request/response shapes with schema `bahia.dnsagent.v1` are the only wire format crossed; no polling loops, sleeps, or ad hoc RPC wrappers were added.
- The shim constructs the agent-side `ContextVMRequest` with a `1059` gift-wrap outer-event kind, matching the encrypted-envelope contract the agent enforces under `--require-encryption`.
- Durable truth stays with the resolver-side serial state and include file; the ContextVM response is treated as acknowledgment plus schema-validated result, mirroring `internal/adapters/dns/dnsmasq_agent.go` production behavior.

## Operator Path (Track B)

Live items intentionally not claimed by automation, mapped in `test_matrix.json` as `operator-path`: deploying the agent on core-01 (192.168.40.1), `dig @192.168.40.1` traceability and before/after verification of all manual `sharegap-splitdns.conf` names, deletion of the single manual astillero line, and rollback drills. All are scripted in `docs/runbooks/core01-dnsmasq-agent.md`.
