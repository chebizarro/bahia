# bahia-dns-agent deployment examples

`bahia-dns-agent` is a portable, static (`CGO_ENABLED=0`) binary for LAN
resolver hosts. It assumes no init system: it supervises its own relay
subscription internally (capped exponential backoff with jitter, reset after a
healthy period) and shuts down cleanly on SIGINT/SIGTERM. The examples here
only need to restart the *process* if it exits.

See `cmd/bahia-dns-agent/README.md` for flags, key handling, and the
include-file ownership guarantee.

## systemd (Ubuntu/Debian)

Use [`bahia-dns-agent.service`](bahia-dns-agent.service). Edit the
`ExecStart` arguments, then:

```sh
install -m 755 bahia-dns-agent-linux-amd64 /usr/local/bin/bahia-dns-agent
install -m 600 dns-agent.key /etc/bahia/dns-agent.key
cp bahia-dns-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now bahia-dns-agent
```

For a release built by the canonical Bahia Dockerfile, extract the DNS agent
from the exact provenance-labelled image rather than rebuilding on the resolver
host:

```sh
container=$(docker create <immutable-bahia-image-digest>)
docker cp "$container:/usr/local/bin/bahia-dns-agent" ./bahia-dns-agent
docker rm "$container"
install -m 0755 ./bahia-dns-agent /usr/local/bin/bahia-dns-agent.next
/usr/local/bin/bahia-dns-agent.next --version
```

After the guarded systemd replacement, `GET http://127.0.0.1:8953/healthz`
must report the same full `commit` embedded in the source image. A missing,
`dev`, short, or mismatched commit fails deployment. Preserve the previous
binary until relay reconnection and one encrypted DNS request/response pass.

The agent's publications (health status, DNS requests) cross the same
process-wide outbound admission controller as the Bahia server, with the same
bounded defaults: per-lane and aggregate event budgets, a per-relay wire
budget, a shared rate-limit circuit breaker, and duplicate suppression.
`healthz` reports the controller's content-free counters under
`outbound_admission`. For the emergency kill switch, point
`BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE` at a root-controlled local file and
write `stop` into it; the agent rejects every new publication before relay
I/O while that content is present, and an unreadable configured file fails
closed. See `docs/runbooks/nostr-outbound-admission.md`.

## OpenWrt (procd)

Use [`bahia-dns-agent.init`](bahia-dns-agent.init). OpenWrt's default dnsmasq
config already reads `conf-dir=/tmp/dnsmasq.d`; because `/tmp` is tmpfs the
include files vanish on reboot, but the durable state file under `/etc/bahia`
keeps serial guarantees and the next `dns-agent/sync` regenerates the include.
Pass `--reload-command "/etc/init.d/dnsmasq reload"` explicitly — automatic
strategy detection works, but an explicit command is unambiguous on embedded
hosts.

## BSD / generic supervision

No supervisor is strictly required: run the binary in the foreground from
`rc.local`, daemontools, runit, supervisord, or tmux for testing. Two rules:

1. Restart the process if it exits non-zero (fatal config errors exit 1 and
   should page a human rather than flap).
2. Deliver SIGTERM for shutdown; the agent finishes in-flight handler work and
   exits 0.

Example `daemon(8)`-style FreeBSD invocation:

```sh
daemon -r -P /var/run/bahia-dns-agent.pid \
  /usr/local/sbin/bahia-dns-agent \
  --private-key-file /usr/local/etc/bahia/dns-agent.key \
  --relays wss://relay.example.net \
  --authorized-pubkey <bahia-service-pubkey-hex> \
  --include-dir /usr/local/etc/dnsmasq.d \
  --allowed-zones example.internal \
  --reload-command "service dnsmasq reload" \
  --state-file /var/db/bahia-dns-agent/state.json \
  --require-encryption
```
