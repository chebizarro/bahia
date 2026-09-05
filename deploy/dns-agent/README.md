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
