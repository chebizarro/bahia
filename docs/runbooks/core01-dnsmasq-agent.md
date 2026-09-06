# Runbook: centralized internal DNS and HTTPS guards for core-01/edge-01

This runbook deploys `bahia-dns-agent` on **core-01 (192.168.40.1)**, the LAN
resolver running dnsmasq, and moves both split-DNS authority and the edge-01
LAN HTTPS vhost under Bahia's centralized ownership. Bahia's internal
`sharegap.net` projection uses the relay-backed `dnsmasq_agent` backend, while
the signed route plan carries both external Cloudflare and internal nginx
outputs. Bahia (on edge-01) and the DNS agent (on core-01) communicate only
over Nostr relays via ContextVM kind `25910` JSON-RPC (schema
`bahia.dnsagent.v1`); no SSH, REST, or port-forwarding between the hosts is
required.

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
      authoritative: true
      allow_empty_authoritative: false
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

internal_routing:
  enabled: true
  provider: nginx
  include_dir: /etc/nginx/conf.d
  file_prefix: bahia-
  test_command: [nginx, -t]
  reload_command: [nginx, -s, reload]
  command_env: []
  cert_file: /etc/letsencrypt/live/astillero.sharegap.net/fullchain.pem
  key_file: /etc/letsencrypt/live/astillero.sharegap.net/privkey.pem
  zones:
    - sharegap.net
```

`authoritative: true` makes Bahia manage `local=/sharegap.net/` in the zone include. This prevents unanswered query types such as AAAA, HTTPS, and SVCB from being forwarded to public DNS and leaking public Cloudflare/ECH records into the split-DNS path. With the default `allow_empty_authoritative: false`, Bahia refuses to replace a non-empty authoritative include with an empty projected record set, preserving the last listed records during transient projection loss. Set the option to `true` only for an intentional empty authoritative-zone teardown.

`internal_routing.enabled: true` makes the same signed route plan drive the
Bahia-owned nginx vhost on edge-01 after Cloudflare succeeds. The certificate
and key must already exist and be readable. Bahia writes only
`bahia-<hostname>.conf`, validates with `nginx -t`, reloads nginx, and restores
the previous owned file automatically if validation or reload fails.

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
   Astillero record and `local=/sharegap.net/` authority guard. Verify the
   Bahia-managed include appeared and that all **manual** names still resolve
   exactly as in the baseline:

   ```sh
   ssh core-01 cat /etc/dnsmasq.d/bahia-sharegap-net.conf
   dig @192.168.40.1 astillero.sharegap.net   # now answered from the Bahia include
   dig @192.168.40.1 <each-other-manual-name>.sharegap.net
   ```

   While both the manual line and the Bahia include define
   `astillero.sharegap.net`, dnsmasq serves the union; confirm the answer is
   `192.168.40.104`, the value configured for `edge-01-docker` in
   `dns.projection.host_overrides`.

3. Explicitly enable `authoritative: true`, reload Bahia, and verify
   `local=/sharegap.net/` appears in `bahia-sharegap-net.conf`. Only after both
   Bahia-managed resolution of `astillero.sharegap.net` and that managed guard
   are verified, delete **only** the one `astillero` record and any manual
   `local=/sharegap.net/` guard from `sharegap-splitdns.conf` (leave every other
   manual record). The next Bahia reconcile repairs a missing managed guard;
   reload dnsmasq after editing the manual file and re-verify:

   ```sh
   ssh core-01   # remove only the astillero record and manual local=/sharegap.net/ guard
   ssh core-01 systemctl reload dnsmasq
   dig @192.168.40.1 astillero.sharegap.net
   ```

## 6. Migration: move the edge-01 nginx vhost to Bahia — [operator]

Perform this after the DNS guard is healthy; it is independently reversible.

### Containerized nginx

If edge-01 nginx is a container on the same Docker daemon that Bahia uses, configure the exact container name in both commands:

```yaml
test_command: [docker, exec, nginx, nginx, -t]
reload_command: [docker, exec, nginx, nginx, -s, reload]
```

For nginx on a remote daemon, either specify the daemon in each argv—for example `["docker", "--host", "tcp://edge-01:2375", "exec", "nginx", "nginx", "-t"]`—or keep `docker exec` and set `command_env: ["DOCKER_HOST=tcp://edge-01:2375"]`. The Bahia image ships `docker-cli`, and the provided Compose deployment mounts `/var/run/docker.sock`; without `--host` or `DOCKER_HOST`, Docker therefore addresses Bahia's own host daemon. Confirm the target nginx container exists on that daemon before attachment.

Bahia logs the effective test/reload argv and only the configured environment keys, never environment values. These values are part of the reviewed internal-routing configuration hash. A config change after review makes the plan stale and apply returns `internal routing configuration changed after review`; re-attach the route and review the new plan before applying it.

1. Capture the current manual nginx source and behavior before enabling the
   guard:

   ```sh
   nginx -T > /root/nginx-before-bahia.txt
   curl --resolve astillero.sharegap.net:443:192.168.40.104 -fsS https://astillero.sharegap.net/health
   ```

2. Ensure the configured certificate and key exist, then enable
   `internal_routing` from step 4 and reload Bahia. Submit or re-approve the
   Astillero signed route attachment with its internal output enabled. Verify
   `/etc/nginx/conf.d/bahia-astillero.sharegap.net.conf` exists, `nginx -t`
   succeeds, and the forced-LAN HTTPS request still succeeds. The composite
   applies Cloudflare first and nginx second; a failed nginx activation restores
   nginx and then compensates Cloudflare.

3. Only after the Bahia-owned vhost is active, remove the old hand-applied
   Astillero nginx vhost, run `nginx -t`, reload nginx, and repeat the forced-LAN
   request. Do not remove unrelated manual vhosts.

## 7. Rollback — [operator]

The two guards can be rolled back independently.

### DNS authority rollback

To return internal DNS to fully manual management:

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

### Internal HTTPS rollback

1. Restore the saved manual Astillero vhost, validate it with `nginx -t`, and
   keep it ready without reloading.
2. Submit/approve the signed route attachment with `--internal=false`. Bahia
   removes its owned vhost and validates/reloads nginx; then reload the restored
   manual configuration and verify the forced-LAN HTTPS request.
3. Disable `internal_routing`, reload Bahia, and confirm the manual vhost is the
   only active `server_name astillero.sharegap.net` definition. Do not simply
   disable the backend first: that would leave the last Bahia-owned include on
   disk with no backend available to remove it.

A failed nginx validation or reload during normal apply restores the exact prior
Bahia-owned file and active configuration automatically. If the later nginx
stage fails after Cloudflare applied, composite compensation restores Cloudflare
as well.

## 8. Ongoing verification — [operator]

- `dig @192.168.40.1 astillero.sharegap.net` — traceable to Bahia state: the
  answer must match the address in `bahia-sharegap-net.conf`, whose content is
  exactly the reconciler's projected record set.
- `curl -fsS http://<bahia>/health` — the `dns-reconciler` runner must be
  listed and healthy.
- Agent-side: `dns-agent/health` reports the last applied serial; with
  `--health-addr` set, `curl http://127.0.0.1:<port>/healthz` exposes the same
  process-local status.
