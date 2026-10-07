# DNS agent operations

`bahia-dns-agent` applies Bahia-owned dnsmasq include files on a resolver host.
It consumes service-authored DNS desired state from relays, persists the last
applied serial, validates the candidate config, atomically replaces only its
owned files and runs the configured reload command.

## Wire surfaces

The agent runs both of its registered surfaces:

- a scoped REQ for service-authored kind `30900`, `#t=dns-zone-sync`; stored
  events are processed through EOSE and the subscription remains live;
- ContextVM methods `dns-agent/health`, `dns-agent/list` and
  `dns-agent/sync`, authorized to the configured Bahia service pubkey.

Set `--require-encryption` in production so ContextVM requests must arrive in a
`1059` or `21059` envelope. The zone-sync record is a public, sanitized desired
state family and is still signature/author checked before apply.

Every 30 seconds the agent publishes expiring kind-`30315` health with
`d=dns-agent`, `t=dns-agent-health` and capability `zone-subscribe:1`. A local
HTTP listener may expose `/healthz` for process supervision.

## Install

Build a static target binary:

```bash
make dist-bahia-dns-agent
install -m 0755 dist/bahia-dns-agent-linux-amd64 /usr/local/bin/bahia-dns-agent
install -d -m 0700 /etc/bahia /var/lib/bahia/dns-agent
install -m 0600 /secure/input/dns-agent.key /etc/bahia/dns-agent.key
```

The key file contains the agent's `nsec` or 64-hex private key. The
`--authorized-pubkey` value is the Bahia service pubkey, not the agent key.

Example:

```bash
/usr/local/bin/bahia-dns-agent \
  --private-key-file /etc/bahia/dns-agent.key \
  --relays wss://bahia.example/relay \
  --authorized-pubkey <bahia-service-pubkey> \
  --include-dir /etc/dnsmasq.d \
  --file-prefix bahia- \
  --allowed-zones internal.example \
  --pre-reload-check 'dnsmasq --test' \
  --reload-command 'systemctl reload dnsmasq' \
  --state-file /var/lib/bahia/dns-agent/state.json \
  --store-path /var/lib/bahia/dns-agent/events.bolt \
  --health-addr 127.0.0.1:9080 \
  --require-encryption
```

The include directory, reload command, state path and allowed zones are
required operational choices. On OpenWrt use the provided procd file and an
explicit `/etc/init.d/dnsmasq reload`; its `/tmp/dnsmasq.d` is rebuilt from
relay state after reboot. Packaged systemd/procd examples are under
[`deploy/dns-agent/`](../deploy/dns-agent/README.md).

## Bahia configuration

Configure a DNS backend of type `agent` with the agent pubkey, relay set and
allowed zones. The daemon publishes the latest zone state to
`dns-zone-sync`; the agent refuses a record from any other author or outside
its zone allowlist.

The agent pubkey must also be admitted where it reads protected ContextVM
traffic. The public zone-sync subscription itself does not require protected
read access.

## Verification

```bash
curl -fsS http://127.0.0.1:9080/healthz
journalctl -u bahia-dns-agent --since '10 minutes ago'
dnsmasq --test
dig @127.0.0.1 host.internal.example
```

Also verify on the relay:

1. the service-authored `30900` `dns-zone-sync` record has the expected zone
   and serial;
2. the agent reaches EOSE and keeps the REQ open;
3. the agent-authored `30315` health record is fresh and its NIP-40
   `expiration` is in the future;
4. the state file records the applied serial;
5. a replay of the same serial is idempotent and a lower serial is refused.

## Failure handling

- **No zone updates:** verify relay reachability, service author, `#t`, allowed
  zones and the agent's local event-store cursor. A `CLOSED` subscription is
  re-established by the process supervisor loop.
- **Candidate rejected:** run the exact `--pre-reload-check` locally and fix
  the record or dnsmasq environment. The existing include remains live.
- **Reload failed:** restore the last known-good include and rerun the reload
  command; do not advance the state serial until apply succeeds.
- **Health missing:** check the agent key, relay `OK` and clock. Health events
  expire after twice the publish interval.
- **ContextVM request rejected:** confirm the service pubkey and gift-wrap
  requirement; do not disable encryption to mask an authorization error.
