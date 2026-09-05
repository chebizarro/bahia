# Runbook: bahia-dns-agent on core-01 (LAN dnsmasq authority)

This runbook deploys `bahia-dns-agent` on **core-01 (192.168.40.1)**, the LAN
resolver running dnsmasq, and switches Bahia's internal `sharegap.net`
projection for `edge-01-production` from local-filesystem dnsmasq management to
the relay-backed `dnsmasq_agent` backend. Bahia (on edge-01) and the agent (on
core-01) communicate only over Nostr relays via ContextVM kind `25910`
JSON-RPC (schema `bahia.dnsagent.v1`); no SSH, REST, or port-forwarding
between the hosts is required.

Reference material:

- `cmd/bahia-dns-agent/README.md` — flags, env fallbacks, key handling, the
  include-file ownership guarantee, and build targets.
- `deploy/dns-agent/README.md` — systemd, OpenWrt/procd, and generic
  supervision examples (`bahia-dns-agent.service`, `bahia-dns-agent.init`).
- `docs/user-guide/guides/managed-dns-and-https-routes.md` — the end-to-end
  Astillero flow this backend slots into.

**Operator/Track B markers.** Every step tagged **[operator]** requires human
action on real hosts (core-01 or edge-01) and cannot be executed or verified by
repo-side automation. Steps tagged **[automated]** are proven by the in-repo
end-to-end tests (`internal/adapters/dns/dnsmasq_agent_e2e_test.go`).

## 1. Build and install the agent binary — [operator]

On a build host with Go:

```sh
make dist-bahia-dns-agent   # linux/amd64 + linux/arm64 static binaries
```

Copy the binary matching core-01's architecture:

```sh
scp dist/bahia-dns-agent-linux-amd64 core-01:/tmp/
ssh core-01 install -m 755 /tmp/bahia-dns-agent-linux-amd64 /usr/local/bin/bahia-dns-agent
```

### Portability matrix

| Init system | Example | Status |
|---|---|---|
| systemd (Ubuntu/Debian) | `deploy/dns-agent/bahia-dns-agent.service` | Supported |
| procd (OpenWrt) | `deploy/dns-agent/bahia-dns-agent.init` | Supported (tmpfs `conf-dir` note in `deploy/dns-agent/README.md`) |
| generic / none (BSD, rc.local, runit) | foreground invocation in `deploy/dns-agent/README.md` | Supported — the agent self-supervises its relay subscription |

| Architecture | Target | Status |
|---|---|---|
| amd64 | `linux/amd64`, `CGO_ENABLED=0` static | Supported |
| arm64 | `linux/arm64`, `CGO_ENABLED=0` static | Supported |
| mips (32-bit softfloat) | `linux/mips` | **Not currently buildable** — `modernc.org/sqlite` libc has no 32-bit MIPS port; tracked as beads issue `bahia-1m1ef` |

## 2. Generate the agent keypair — [operator]

Keys are accepted **only from a file** (`--private-key-file`). Never pass the
key as an argument, environment value, or paste it into notes/tickets — the
raw-key env var is rejected by the agent by design.

On core-01:

```sh
umask 077
mkdir -p /etc/bahia
nak key generate > /etc/bahia/dns-agent.key
chmod 600 /etc/bahia/dns-agent.key
nak key public "$(cat /etc/bahia/dns-agent.key)"   # note this pubkey for step 4
```

Record the printed **agent pubkey** (hex). You will also need the **Bahia
service pubkey** (hex) from the edge-01 Bahia deployment — it becomes the
agent's `--authorized-pubkey` so only Bahia-signed requests are served.

## 3. Configure and start the agent on core-01 — [operator]

Agent invocation (systemd `ExecStart` or equivalent; see
`deploy/dns-agent/README.md` for full unit/init examples):

```sh
/usr/local/bin/bahia-dns-agent \
  --private-key-file /etc/bahia/dns-agent.key \
  --relays wss://relay.sharegap.net \
  --authorized-pubkey <BAHIA_SERVICE_PUBKEY_HEX> \
  --include-dir /etc/dnsmasq.d \
  --allowed-zones sharegap.net \
  --reload-command "systemctl reload dnsmasq" \
  --state-file /var/lib/bahia-dns-agent/state.json \
  --require-encryption
```

Reload command guidance per init system — set `--reload-command` explicitly;
automatic detection works but an explicit command is unambiguous:

| core-01 init system | `--reload-command` |
|---|---|
| systemd | `systemctl reload dnsmasq` |
| SysV / service wrapper | `service dnsmasq reload` |
| OpenWrt / init.d | `/etc/init.d/dnsmasq reload` |
| none (raw dnsmasq) | `killall -HUP dnsmasq` |

The agent writes **only** files named `bahia-<zone>.conf` (for `sharegap.net`:
`/etc/dnsmasq.d/bahia-sharegap-net.conf`). It never reads, writes, or deletes
any other file in `/etc/dnsmasq.d` — in particular the existing manual
`sharegap-splitdns.conf` is out of its ownership boundary.

Verify startup:

```sh
journalctl -u bahia-dns-agent -f     # or the init system's log path
```

## 4. Configure Bahia (edge-01) to use the agent backend — [operator]

In the Bahia server configuration, replace (or add alongside) the DNS backend
for the internal zone with type `dnsmasq_agent`:

```yaml
dns:
  enabled: true
  default_ttl: 300
  reconcile_interval: 1m
  zones:
    - name: sharegap.net
      visibility: internal
      backend: core01-dnsmasq
      ttl: 300
  backends:
    core01-dnsmasq:
      type: dnsmasq_agent
      agent_pubkey: <AGENT_PUBKEY_HEX_FROM_STEP_2>
      agent_relays:
        - wss://relay.sharegap.net
      agent_encrypted: true
      agent_timeout: 30s
  projection:
    services: true
    environment_zones:
      edge-01-production: sharegap.net
    host_overrides:
      edge-01-docker: 192.168.40.104
```

`host_overrides` is mandatory for this rollout: map `edge-01-docker` to the
edge-01 LAN address (`192.168.40.104`), and map each other Bahia-managed
deployment-unit endpoint alias to its concrete LAN IP or fully qualified DNS
name. Docker observations identify the runtime by endpoint alias, so without
this mapping Astillero would project as
`CNAME astillero.sharegap.net -> edge-01-docker`; the bare target is not
resolvable and returns `NXDOMAIN`. Bahia now skips such unsafe records, so an
omitted mapping leaves Astillero without a managed record rather than creating
a useless one.

`agent_relays` defaults to the control-plane relays when omitted. Send
`SIGHUP` to the Bahia server; DNS changes apply through whole-application
reconstruction, and startup fails closed if the agent health check
(`dns-agent/health`) does not succeed over the relay.

## 5. Migration: move the astillero record to Bahia — [operator]

The existing manual records in `/etc/dnsmasq.d/sharegap-splitdns.conf` **stay
untouched** — the agent manages a distinct file
(`bahia-sharegap-net.conf`), so both coexist. The automated equivalent of this
guarantee is proven by
`TestDnsmasqAgentBackendPreservesForeignIncludeFiles` (foreign file is
byte-identical after repeated syncs).

1. **Before** enabling the backend, capture baseline resolution for every
   existing manual name:

   ```sh
   dig @192.168.40.1 astillero.sharegap.net
   dig @192.168.40.1 <each-other-manual-name>.sharegap.net
   ```

2. Enable the backend (step 4) and let the `dns-reconciler` project the
   Astillero record. Verify the Bahia-managed include appeared and that all
   **manual** names still resolve exactly as in the baseline:

   ```sh
   ssh core-01 cat /etc/dnsmasq.d/bahia-sharegap-net.conf
   dig @192.168.40.1 astillero.sharegap.net   # now answered from the Bahia include
   dig @192.168.40.1 <each-other-manual-name>.sharegap.net
   ```

   While both the manual line and the Bahia include define
   `astillero.sharegap.net`, dnsmasq serves the union; confirm the answer is
   `192.168.40.104`, the value configured for `edge-01-docker` in
   `dns.projection.host_overrides`.

3. Only after Bahia-managed resolution of `astillero.sharegap.net` is
   verified, delete **only** the one `astillero` line from
   `sharegap-splitdns.conf` (leave every other manual record), then reload
   dnsmasq and re-verify:

   ```sh
   ssh core-01   # edit /etc/dnsmasq.d/sharegap-splitdns.conf, remove only the astillero line
   ssh core-01 systemctl reload dnsmasq
   dig @192.168.40.1 astillero.sharegap.net
   ```

## 6. Rollback — [operator]

To return to fully manual management:

1. Stop the agent, or remove/disable the Bahia include:

   ```sh
   ssh core-01 systemctl stop bahia-dns-agent
   ssh core-01 rm -f /etc/dnsmasq.d/bahia-sharegap-net.conf
   ```

   (Also disable the `dnsmasq_agent` backend / zone in Bahia's config and
   `SIGHUP` Bahia, otherwise the reconciler will report the zone unhealthy.)

2. Restore the manual `astillero` line in
   `/etc/dnsmasq.d/sharegap-splitdns.conf`.

3. Reload dnsmasq and verify:

   ```sh
   ssh core-01 systemctl reload dnsmasq
   dig @192.168.40.1 astillero.sharegap.net
   ```

Note that transient failure rollback is automatic and narrower: a failed
dnsmasq reload after a sync rolls the Bahia include back to its previous bytes
on the agent side and surfaces the error to Bahia
(`TestDnsmasqAgentBackendReloadFailureRollsBackAndRecovers`); this section is
only for deliberately abandoning Bahia management.

## 7. Ongoing verification — [operator]

- `dig @192.168.40.1 astillero.sharegap.net` — traceable to Bahia state: the
  answer must match the address in `bahia-sharegap-net.conf`, whose content is
  exactly the reconciler's projected record set.
- `curl -fsS http://<bahia>/health` — the `dns-reconciler` runner must be
  listed and healthy.
- Agent-side: `dns-agent/health` reports the last applied serial; with
  `--health-addr` set, `curl http://127.0.0.1:<port>/healthz` exposes the same
  process-local status.
