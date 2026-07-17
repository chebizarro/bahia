# Bahia Remediation Wave — 2026-07-17

Parallel file-disjoint buckets. Each agent OWNS its directory subtree, adds/strengthens tests for its own issues, closes each `bd` issue as completed, and commits incrementally per logical group. Do NOT `git push` (orchestrator pushes). Never `git add -A` — stage only files in your subtree. `bd` DB (.beads/) is committed by the orchestrator.

Source evidence: docs/reviews/audit-{adapters,core,web,tests}-2026-07-16.md

## Bucket A — Rollout / reconcile / soulfactory  (owns: internal/rollout, internal/reconcile, internal/soulfactory)
- [ ] bahia-cg3vb (P1) rollout traffic-shift & blue/green are no-ops reported as success
- [ ] bahia-s4y11 (P1) rollout auto-rollback swallows errors, never restores prior artifact
- [ ] bahia-8hpg0 (P1) soulfactory marks steps complete despite failed sub-actions
- [ ] bahia-3elai (P3) rollout health-observer errors ignored by fast-fail thresholds

## Bucket B — Config / cmd hardening  (owns: internal/config, cmd/**)
- [ ] bahia-1ghi1 (P1) fail closed on insecure server/auth/db defaults
- [ ] bahia-67w1m (P3) relay/server defaults embed third-party relays & plaintext dev endpoints
- [ ] bahia-fzvm3 (P3) hardcoded OCI SA hash, anon-pull CIDR, provisioning relay (config side only; leave soulfactory provisioner code to Bucket A)
- [ ] bahia-xuav6 (P3) CLI accepts raw Nostr private keys as flags

## Bucket C — Adapters I: runtime/registry/dns/sbom/telemetry/signing-nostr + backend mock  (owns: internal/adapters/{runtime,registry,dns,sbom,telemetry,signing,nostr}, internal/backends/{factory,filesystem_mock})
- [ ] bahia-3ka2n (P2) Docker PullImage ignores in-stream errors
- [ ] bahia-4a4jt (P2) K8s deploy reports success after failed env/label/rollout
- [ ] bahia-nh63w (P2) OCI maps 401 to "image does not exist"
- [ ] bahia-16usc (P2) SBOM attestation unsigned
- [ ] bahia-cia8i (P3) SBOM parser accepts invalid docs as empty success
- [ ] bahia-yol96 (P3) SBOM storage trusts Blossom result, nil-panic risk
- [ ] bahia-s3qdz (P2) telemetry ignores OTLP; Shutdown no-op
- [ ] bahia-f8e27 (P3) FIPS DNS silently drops unsupported records
- [ ] bahia-5gjs1 (P3) filesystem DNS persists snapshot, no apply/reload
- [ ] bahia-8r9hf (P3) dnsmasq no rollback on reload failure
- [ ] bahia-mv3dh (P2) Nostr VerifySignatures silent no-op
- [ ] bahia-is2kz (P2) filesystem_mock selectable as production backend, no guard
- [ ] bahia-qzwfj (P2) registry factory tests assert only non-nil
- [ ] bahia-kok8m (P3) decoder-completeness test only checks one error string

## Bucket D — Adapters II: llm/blossom/signet/loom/cashu/agentmemory/secrets + nexus/pulp  (owns: internal/adapters/{llm,blossom,signet,loom,cashu,agentmemory,secrets}, internal/backends/{nexus,pulp,packagebackend})
- [ ] bahia-a5stv (P2) LLM gateway fabricates Synced status & config hash
- [ ] bahia-v8hrg (P2) LLM avatar accepts nil/empty provider output
- [ ] bahia-bypyx (P2) LLM soul generator JSON fallback fabricates privileged soul
- [ ] bahia-nzsze (P2) SSRF + unbounded downloads in LLM & Blossom avatar fetches
- [ ] bahia-iu3mr (P2) LLM external provisioner metadata-only
- [ ] bahia-0mx9w (P2) Blossom status/upload confirmation
- [ ] bahia-hlhv5 (P2) Loom dispatch/cancel success on zero relays
- [ ] bahia-h3x92 (P2) Signet logs full bunker URI incl secret
- [ ] bahia-673s5 (P3) Signet auth-header marshal failures swallowed
- [ ] bahia-987az (P3) Signet connect/sign deadlines
- [ ] bahia-ubspz (P3) Signet agent listing authoritative/cache-only
- [ ] bahia-940z7 (P2) Cashu core payment ops non-functional
- [ ] bahia-qio6f (P2) agent-memory drops identity metadata, in-proc task IDs
- [ ] bahia-ylg8g (P2) secrets Encryptor: no key validation, unsalted SHA-256 AES key
- [ ] bahia-1erjw (P2) Pulp fabricates href, malformed = success
- [ ] bahia-ln07n (P2) Nexus hardcoded storage policy, 409 = success
- [ ] bahia-cgnkb (P2) Nexus/Pulp drift/auth/TLS readiness
- [ ] bahia-3w3mb (P3) NIP-44 empty-plaintext test skips on any error (if in secrets)

## Bucket E — Repository/events/auth/notifications/nostrmigration/backup/controlplane responders  (owns: internal/repository, internal/events, internal/auth, internal/notifications, internal/nostrmigration, internal/controlplane, internal/service coordinators, backup coordinators)
- [ ] bahia-0otyn (P2) pg_worker: conflict on zero-row stale advert
- [ ] bahia-f0or0 (P2) pg_secret: optimistic concurrency, zero-row detection
- [ ] bahia-j651y (P2) adopted runtime identity writes tenant-safe & atomic
- [ ] bahia-75oly (P2) pg_hiveci projection writes atomic
- [ ] bahia-fjsgr (P2) pg_sbom projection writes atomic
- [ ] bahia-udtj1 (P2) tenant invite membership lookup scoped by org
- [ ] bahia-yg971 (P3) payment metadata marshal error coerced to {}
- [ ] bahia-l3s1p (P3) event bus handler failures observable/retryable
- [ ] bahia-phnr4 (P2) NIP-98 replay process-local; body unbound
- [ ] bahia-7lzih (P2) notification dispatch swallows errors; zero-relay = sent
- [ ] bahia-sq20c (P2) destructive backup restore re-runnable after checkpoint failure
- [ ] bahia-kc4p8 (P2) nostrmigration single batch, no pagination, content untranslated
- [ ] bahia-dyn1k (P2) control-plane responders return nil without publishing
- [ ] bahia-y5tng (P2) coordinators return nil when mandatory deps missing
- [ ] bahia-y157d (P2) continuity/heartbeat definition events accepted with no operator authz
- [ ] bahia-amfso (P2) LLM promotion check-then-act race, no route lock

## Bucket F — Web frontend  (owns: web/** including web/tests)
- [ ] bahia-ly4dv (P1) fake CI enrichment reports canned data as successful load
- [ ] bahia-nh89d (P1) stale/unverified persisted NIP-07 treated as authenticated
- [ ] bahia-pnme5 (P2) backendAuthenticated/NIP-98 ready set without verified round-trip
- [ ] bahia-kfqp1 (P2) NIP-46 connect doesn't bind pubkey, silent NIP-07 fallback
- [ ] bahia-6o1tj (P2) route RBAC empty; authz from mutable browser globals in prod
- [ ] bahia-hznpt (P2) security rescan failures swallowed to console
- [ ] bahia-ue1j8 (P2) repository fetch rejection converted to stale "success"
- [ ] bahia-jcllm (P3) dead components from abandoned rewrite
- [ ] bahia-f3mlo (P3) silent catch blocks in cache/event projection
- [ ] bahia-6wegq (P3) ensureRepositoryConnection() awaited no-op
- [ ] bahia-w9s9x (P3) isDashboardLoaded only checks <body> exists

## Deferred — needs live infra / enhancement (NOT in this wave)
- bahia-4djt4 live btc-01 relay verification (needs live relay)
- bahia-1vjl7 Docker BuildKit CI w/ gitauth secret (needs CI infra)
- bahia-vq3bb LAN Gemma endpoint validation (needs LAN endpoint)
- bahia-lsxe8 Soul Factory memory seed alignment
- bahia-jzph centralize CAS tag keys (debt)
- bahia-n5tfn extend Compose SDK execution mode (enhancement)
- Tests/pstf cross-cutting: bahia-5xs2j, bahia-zh0xw, bahia-egms2, bahia-ulmrn, bahia-948ac, bahia-ac0pf (test/e2e-agent, pstf docs)

## Final step
Completeness review of all issues closed during this production-readiness push (verify fixes are real, not fake-closed).
