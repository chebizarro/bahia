# Soul Factory First-OK Publication Verification

Production evidence on 2026-07-23 showed Marjam provisioning requests `1f4fdcb6…` and `e60b617f…` reached the Bahia sidecar and reactor. Each workflow stopped immediately after logging step 1, before a `6950` appeared. The relay bus published sequentially to `ws://relay:3334/relay`, `wss://relay.sharegap.net`, and `ws://192.168.40.104:3337`; a non-responsive secondary publish prevented the already-accepted sidecar result from completing.

The relay bus now starts all publishes concurrently, completes after the first verified `OK accepted=true`, and cancels remaining attempts. It still collects every rejection/error when no relay accepts the event.

Focused verification:

```text
go test ./internal/soulfactory -run '^TestRelayBusPublish' -count=1 -v
```

Result: PASS, five tests.

The full `internal/soulfactory` suite has one unrelated existing failure:
`TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`.
