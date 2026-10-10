# Service identity: one injected Keyer

Bahia's service identity is a single `nostr.Keyer` (fiatjaf.com/nostr:
`SignEvent`, `GetPublicKey`, NIP-44 `Encrypt`/`Decrypt`). Startup builds it
once in `internal/app/service_keyer.go` (`newServiceKeyer`), which opens the
signer selected by `nostr.signer` through `servicesigner.Open` (see
[service-signer.md](service-signer.md)), and injects it into every consumer
that signs, answers NIP-42 AUTH or NIP-44s as the service. No consumer takes a
hex key. In a remote signer mode (NIP-46 bunker, NIP-55L) there is no raw
private key in the process. The signer session lives until shutdown, after
every signing component has stopped.

Local mode uses `nostrutil.LocalKeyer`: an in-process key signer that also
exposes the optional `nostrutil.ServiceKeyMaterialHolder` capability (the
configured key text, verbatim) and binary NIP-44. Remote keyers do not hold
key material.

## Raw-key derivations fail closed

Some features hash or HKDF the nsec itself, which no signer can serve. They
ask the injected Keyer for key material through
`nostrutil.RequireServiceKeyMaterial`; without it they return
`nostrutil.ErrServiceKeyMaterialRequired` (wrapped with the feature name) on
every use and log the blocker at startup. They never skip silently and never
fake a key. Data migration belongs to bahia-cd0wr.4.x.

## Guard

`internal/archtest/service_key_reads_test.go` fails on any non-test read of
`config.NostrConfig.PrivateKey` outside the seam and an exact allowlist, and
on allowlist entries that no longer match a read.

## Classification

(a) signs or NIP-44s through the injected Keyer; (b) raw-key derivation,
fails with the sentinel without local key material; (c) operator tool or
separate identity that keeps a local key by design.

| Consumer | Class | Now |
|---|---|---|
| `internal/servicesigner/servicesigner.go` `Open`, called only by `internal/app/service_keyer.go` `newServiceKeyer` | seam | the only daemon read of `nostr.private_key` |
| `internal/app/app.go:172` relay pools (NIP-42 AUTH) | a | `WithAuthSigner(serviceKeyer)`; `RelayPool.WithPrivateKey` deleted |
| `internal/adapters/nostr/publisher.go:949` publishers | a | `WithPublisherSigner`; enabled only with a signer |
| `internal/adapters/nostr/projector.go:1493` projector (+ `backup_run_admission`, `dns_canonical_publisher`, `legacy_ock_migration`, `org_refounding`, `projector_warmstart`, `control_state_dedupe`) | a | `WithProjectorSigner(keyer, pubkey)`; pubkey pinned, never re-derived |
| `internal/adapters/loom/client.go:154` Loom job client | a | `NewClient(cfg, keyer, …)`; 5100 signed and job secrets NIP-44'd via Keyer; `WithJobSigner` and hex `ProjectCanonical*` deleted |
| `internal/controlplane/encrypted_transport.go:184` encrypted responder | a | `NewEncryptedResponder(pub, keyer, log)`; decrypt/encrypt/sign via Keyer |
| `internal/controlplane/reactor.go:380` reactor | a | `Config.PrivateKey` deleted; AUTH via its signer |
| `internal/notifications/nostr_dm.go:26` DM sender | a | encrypts and signs via Keyer (previously encrypted with the raw key, signed with the signer) |
| `internal/app/app.go:849` status projector | a | instance id = service pubkey (it was the nsec; bahia-h9n7f) |
| `internal/app/app.go` assistant identity, authorized pubkeys, route canary, stale-run, docs gates | a | use `servicePubkey` / `serviceKeyer != nil` |
| `internal/adapters/nostr/relayadmin/client.go:332` NIP-86 admin | a (admin identity) | `Config.Signer`; app builds it from the resolved admin secret |
| `internal/adapters/blossom` list/upload auth | a (Blossom identity) | `Config.Signer`; app builds it from `blossom.private_key` |
| `pkg/discovery/resolver.go:112`, `pkg/client/intent_publisher.go:105` | a | `WithAuthSigner` |
| `internal/adapters/secrets/nip44.go:38` secret store HKDF AES | b (4.7) | `NewServiceEncryptor(keyer)`; every op returns the sentinel without material |
| `internal/app/service_keyer.go:41` legacy O1 org-state key | b | blocked decryptor returns the sentinel |
| `internal/adapters/nostr/confidential_dedupe.go:51` state_hash HMAC | b | `publishCanonicalFirst` returns the sentinel |
| `internal/app/app.go:4979` assistant transcript key, `assistant_wrapped_keys.go:68` | b (4.8) | wraps the sentinel; signature fixed by the factory slice |
| `cmd/bahia-policy-census`, `cmd/bahia-migrate` (`f74a_import`, `nostr`), `cmd/cli` (`config_fabric`, confidential reads, `soulfactory`), `cmd/bahia-dns-agent`, `cmd/openclaw-soulfactory-sidecar`, `cmd/bahia-test-relay`, `internal/nostrmigration` | c | offline/operator tools or separate identities; AUTH via a local signer |
| `internal/relaysidecar/policy.go:42`, `server.go:132,189` | pending (bahia-cd0wr.3.5) | separate relay process; sign/pubkey-only, needs the factory in `cmd/relay` |
| `internal/adapters/signet`, `internal/soulfactory/signet_enrollment.go` | n/a | NIP-46 *client* transport keys, not the service key |
