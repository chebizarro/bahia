# bahia-dns-agent

Portable agent for a LAN resolver host (Ubuntu, OpenWrt, BSD — no systemd
assumption). It receives Bahia-signed desired DNS state over Nostr (ContextVM
kind 25910 JSON-RPC, schema `bahia.dnsagent.v1`) and manages **only**
Bahia-owned dnsmasq include files.

## Include-file ownership guarantee

The agent never touches your dnsmasq configuration. It writes exactly one
include file per allowed zone, named `<file-prefix><zone-with-dashes>.conf`
(default prefix `bahia-`) inside `--include-dir`, each carrying a Bahia
ownership header. Writes are atomic (temp file + rename), applies are guarded
by a monotonic per-zone serial persisted in `--state-file`, and a failed
reload (or failed `--pre-reload-check`) rolls the include file back to its
previous contents before the error is returned to the caller. Files in the
include dir that do not match the prefix are never read, written, or deleted.

## Methods

| Method | Purpose |
|---|---|
| `dns-agent/health` | Status, config echo, last applied serial |
| `dns-agent/list` | Current records + serial for one allowed zone |
| `dns-agent/sync` | Apply desired records for a zone at a serial. Stale serial → error; equal serial → idempotent no-op |

Requests are only accepted from `--authorized-pubkey` (the Bahia service).
With `--require-encryption`, bare kind-25910 requests are rejected; only
CEP-4/NIP-59 `1059`/`21059` envelopes are served.

## Flags / environment

Every flag has a `BAHIA_DNS_AGENT_*` environment fallback; flags win. An
optional JSON config file (`--config` / `BAHIA_DNS_AGENT_CONFIG`) is applied
first, then environment, then flags.

| Flag | Env | Notes |
|---|---|---|
| `--config` | `BAHIA_DNS_AGENT_CONFIG` | Optional JSON config file |
| `--private-key-file` | `BAHIA_DNS_AGENT_PRIVATE_KEY_FILE` | **Required.** File containing the agent nsec or hex key (≤4096 bytes, trimmed) |
| `--relays` | `BAHIA_DNS_AGENT_RELAYS` | **Required.** Comma-separated relay URLs |
| `--authorized-pubkey` | `BAHIA_DNS_AGENT_AUTHORIZED_PUBKEY` | **Required.** Bahia service pubkey (hex) |
| `--include-dir` | `BAHIA_DNS_AGENT_INCLUDE_DIR` | **Required.** dnsmasq conf-dir |
| `--allowed-zones` | `BAHIA_DNS_AGENT_ALLOWED_ZONES` | **Required.** Comma-separated zone allowlist |
| `--file-prefix` | `BAHIA_DNS_AGENT_FILE_PREFIX` | Default `bahia-` |
| `--reload-command` | `BAHIA_DNS_AGENT_RELOAD_COMMAND` | Explicit reload (run via `sh -c`); otherwise auto-detected (systemctl/service/init.d/killall/pkill HUP) |
| `--pre-reload-check` | `BAHIA_DNS_AGENT_PRE_RELOAD_CHECK` | Optional validation command run before reload |
| `--state-file` | `BAHIA_DNS_AGENT_STATE_FILE` | Durable serial state; defaults into the include dir |
| `--require-encryption` | `BAHIA_DNS_AGENT_REQUIRE_ENCRYPTION` | Reject bare kind-25910 requests |
| `--health-addr` | `BAHIA_DNS_AGENT_HEALTH_ADDR` | Optional local HTTP `/healthz` |

## Key handling — file only, never argv or env

Secrets are accepted **only** via `--private-key-file`. The raw-key
environment variable `BAHIA_DNS_AGENT_PRIVATE_KEY` is rejected without being
read, so a key can never leak through `ps`, shell history, or unit files.
Generate a dedicated keypair for each resolver host, e.g. with
[`nak`](https://github.com/fiatjaf/nak):

```sh
umask 077
nak key generate > /etc/bahia/dns-agent.key       # hex; nsec also accepted
nak key public "$(cat /etc/bahia/dns-agent.key)"  # register this with Bahia
chmod 600 /etc/bahia/dns-agent.key
```

The agent authenticates to relays with NIP-42 using this key and signs all
ContextVM responses with it.

## Building

```sh
make build-bahia-dns-agent      # host build, CGO_ENABLED=0
make dist-bahia-dns-agent       # linux/amd64, linux/arm64, linux/mips softfloat
```

Known limitation: the `linux/mips` (GOMIPS=softfloat) target currently fails
because `internal/controlplane` transitively links `modernc.org/sqlite`
(via `internal/adapters/nostr` → `internal/adapters/sbom`), whose libc has no
32-bit MIPS port. Tracked as beads issue `bahia-1m1ef`; amd64 and arm64
static builds are verified.

## Deployment

Examples for systemd, OpenWrt procd, and generic supervision live in
[`deploy/dns-agent/`](../../deploy/dns-agent/README.md). The agent supervises
its own relay subscription (capped exponential backoff + jitter, reset after a
healthy run); external supervisors only need to restart the process itself.
