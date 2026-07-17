# Production-Readiness Audit — Adapters, Backends & Binaries

- **Date:** 2026-07-16
- **Repo:** `bahia` (Go)
- **Scope:** `internal/adapters/**`, `internal/backends/**`, `cmd/**`
- **Out of scope (other reviewers):** `internal/api`, `internal/service`, `internal/db`, `web/`
- **Method:** Static read-through + targeted tracing of the most important adapters end-to-end (input → logic → external call → error handling). Load-bearing P1/P2 findings were re-read and verified directly against source; line references are quoted. Parallel read-only probes covered breadth; every cited claim below was spot-checked by the author.
- **Mutations:** None. This was a read-only audit.

## Executive Summary

The adapter layer is, encouragingly, mostly *real*: Kopia, Velero, Syft, cdxgen, CoreDNS (etcd), PowerDNS, OSV, the OCI/GHCR/DockerHub registry clients, the HiveCI subscriber, the runtime factory, and the Loom NIP-44 secret encryption all make genuine external calls with reasonable error handling. `internal/backends/filesystem_mock` does **real** filesystem work (real SHA-256, real index generation) — it is not a canned-response fake — and `cmd/bahia-test-relay`'s hardcoded secrets do **not** leak into any production binary (verified by repo-wide search).

However, there is a recurring and dangerous pattern layered through the codebase: **operations that report success without confirming the external effect actually happened**, and **security verifiers/crypto that are stubs or fail open**. The highest-impact issues:

- **Cosign signature verification performs no cryptography and fails open** on registry errors (supply-chain security).
- **Loom silently drops job secrets** when no worker pubkey is resolved, yet still dispatches the job as success.
- **A backend explicitly named `filesystem_mock`** is a first-class, config- and MCP-selectable production backend with no guard.
- Multiple adapters (**Kubernetes deploy, Docker pull, Pulp, LLM gateway, LLM avatar, Blossom upload**) return success after a failed or unconfirmed external effect.
- **Insecure server defaults** (auth disabled, `0.0.0.0`, hardcoded DB password, `sslmode=disable`) apply when no config file is supplied.

Counts: **3 P1**, **~18 P2**, **~9 P3**.

---

## P1 — Critical

### P1-1. Cosign "verifier" performs no cryptographic verification and fails open on registry errors
- **Category:** security / missing-implementation
- **Files:** `internal/adapters/signing/cosign.go:49-58`, `:63-101`
- **Evidence:**
  ```go
  referrers, err := v.inspector.GetReferrers(ctx, artifact.ImageRepo, artifact.ImageDigest)
  if err != nil {
      v.logger.Warn("failed to fetch referrers for signature check", ...)
      // Don't fail the whole flow — just no signatures found.
      return nil, nil
  }
  ...
  sig := domain.ArtifactSignature{
      ...
      Verified:           false,
      VerificationStatus: domain.SignatureStatusDiscovered,
      Metadata: map[string]any{
          "discovery_only":    true,
          "verification_note": "OCI referrer discovered; cryptographic cosign verification not performed",
      },
  }
  ```
- **Why it's not production-ready:** `CosignVerifier` implements the `SignatureVerifier` interface used for policy enforcement, but it never shells out to `cosign` nor calls a Sigstore library. It only *discovers* referrers by media type and trusts unverified annotations to populate signer identity. Forged, expired, revoked, wrong-subject, or untrusted signatures are never cryptographically rejected. Worse, when `GetReferrers` fails (auth failure, registry outage, timeout, malformed response) the method returns `nil, nil` — indistinguishable from "no signatures found." Any policy that treats a non-error result as "checked" is fail-open. (To its credit, records are marked `Verified:false`/`Discovered`, so a policy that requires `Verified==true` will fail closed — but no code path can ever produce `Verified==true` for cosign, meaning the capability is effectively absent.)
- **Recommended fix:** Perform real cosign/Sigstore verification against the exact image digest, configured trust roots/Fulcio identity constraints, and Rekor/bundle proofs; return an explicit `indeterminate/unavailable` status on registry errors that policy must not treat as "unsigned-but-acceptable"; return the lookup error instead of swallowing it.

### P1-2. Loom silently drops job secrets when no worker pubkey is resolved, then dispatches as success
- **Category:** security / fake-completion
- **File:** `internal/adapters/loom/client.go:196-203`, `:229-231`, `:257-264`, `:278-294`
- **Evidence:**
  ```go
  workerPubkey := job.WorkerPubkey
  if workerPubkey == "" && c.workerRepo != nil {   // only resolves when workerRepo is set
      selected, err := c.selectWorker(ctx, job)
      ...
      workerPubkey = selected
  }
  ...
  // NIP-44 encrypted secret env vars.
  if len(job.Secrets) > 0 && workerPubkey != "" {  // secrets skipped entirely when pubkey empty
      secretTags, err := c.encryptSecrets(job.Secrets, workerPubkey)
      ...
      tags = append(tags, secretTags...)
  }
  ...
  published, err := c.pool.Publish(ctx, ev)  // job still published without the secrets
  ...
  return eventID, nil
  ```
- **Why it's not production-ready:** If `job.Secrets` is non-empty, `WorkerPubkey` is empty, and no `workerRepo` is configured, the deployment job is signed and published **without any of its secrets**, and the caller receives a successful event ID. A deploy that requires (e.g.) a database password or registry credential runs mis-provisioned while the control plane records success. This is a silent, security-relevant data-loss path.
- **Recommended fix:** Fail closed: whenever `len(job.Secrets) > 0` and `workerPubkey == ""`, return an error before building/publishing the event. Validate the resolved worker pubkey up front.

### P1-3. Server runs with insecure defaults (auth disabled, `0.0.0.0`, hardcoded DB password, TLS disabled) when no config is supplied
- **Category:** security / unsafe-hardcoding
- **Files:** `cmd/server/main.go:12-16` (optional `-config`), backed by defaults in `internal/config/config.go:715-729,845-847`
- **Evidence:**
  ```go
  // cmd/server/main.go — config file is optional
  configPath := flag.String("config", "", "path to config YAML file")
  cfg, err := config.Load(*configPath)
  ```
  ```go
  // internal/config/config.go — defaults
  Server: ServerConfig{ Host: "0.0.0.0", Port: 8080 },
  DB: DBConfig{ Host: "localhost", Port: 5432, User: "bahia",
               Password: "bahia", Name: "bahia", SSLMode: "disable" },
  Auth: AuthConfig{ Enabled: false },
  ```
- **Why it's not production-ready:** Launching `cmd/server` with no config file exposes the HTTP API on every interface with authentication disabled, and connects to Postgres with a predictable credential over plaintext. (Root defaults live in `internal/config`, but `cmd/server`/`cmd/relay` are the vector that consumes them, so they are in-scope as a binary-facing risk.)
- **Recommended fix:** Require an explicit production config; fail startup when `Auth.Enabled == false` on a non-loopback listener; remove the DB password default and reject `sslmode=disable` outside an explicit dev mode; default the listener to loopback.

---

## P2 — High

### P2-1. `filesystem_mock` is a first-class, selectable production backend with no guard
- **Category:** incomplete-migration / debt
- **Files:** `internal/backends/factory/factory.go:13,76-79`; `internal/config/config.go:1745-1746,1776`; `internal/domain/package_registry.go:14-22`; `internal/mcp/package_tools.go:35`
- **Evidence:**
  ```go
  // factory.go — production factory, no build tag / dev gate
  case domain.PackageBackendFilesystemMock:
      return filesystem_mock.New(filesystem_mock.Config{RootDir: cfg.RootDir, PublicBaseURL: cfg.PublicBaseURL})
  ```
  ```go
  // mcp/package_tools.go — exposed to operators via MCP tool schema
  "backend_type": map[string]interface{}{"type": "string", "enum": []string{"nexus", "pulp", "filesystem_mock"}},
  ```
  ```go
  // filesystem_mock.go self-description
  // Backend stores package artifacts under a local root. It is intended for tests,
  // development, and deterministic control-plane integration checks only.
  ```
- **Why it's not production-ready:** A component that documents itself as test/dev-only is accepted by config validation, `domain.IsValid`, the production factory, and the operator-facing MCP tool enum. A typo or copy-pasted config routes real package operations to non-durable local storage with no HA. (Note: the backend does real filesystem work — it is not a data-faking mock — so this is a wiring/exposure defect, not fabricated success.)
- **Recommended fix:** Remove it from the production factory and MCP enum; register it only under a build tag or an explicit development-mode gate that fails closed in production.

### P2-2. Pulp fabricates a repository href when creation is not confirmed, and treats malformed responses as success
- **Category:** fake-completion
- **File:** `internal/backends/pulp/pulp.go:87-101`, `:323-343`
- **Evidence:**
  ```go
  _, repoHref, err = b.findRepository(ctx, name)   // discards the exists bool
  ...
  if repoHref == "" {
      repoHref = "/pulp/api/v3/repositories/file/file/" + url.PathEscape(name) + "/"  // invented href
  }
  ...
  return packagebackend.RepositoryObservation{Exists: true, ...}, nil
  ```
  ```go
  data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))  // read error discarded
  _ = json.Unmarshal(data, &payload)                       // decode error discarded
  if task == "" { return nil }                             // no task ⇒ "success"
  ```
- **Why it's not production-ready:** Pulp hrefs are UUID-based (`.../file/file/<uuid>/`), so the fabricated name-based href is almost certainly invalid — yet the repository is reported as created and used to build the distribution. Separately, any 2xx with malformed/unexpected body and no task URL is reported as complete, so create/store/promote/delete can appear done without confirmation.
- **Recommended fix:** Require `exists == true` and a server-provided href (retry lookup with bounded backoff after task completion, then fail); propagate body-read/decode errors and require a valid task URL for async operations.

### P2-3. Nostr `VerifySignatures` is a silent no-op
- **Category:** missing-implementation
- **File:** `internal/adapters/signing/nostr_sign.go:128-137`
- **Evidence:**
  ```go
  func (v *NostrVerifier) VerifySignatures(_ context.Context, _ *domain.Artifact) ([]domain.ArtifactSignature, error) {
      // Nostr signatures are event-driven, not pull-based.
      return nil, nil
  }
  ```
- **Why it's not production-ready:** It implements `SignatureVerifier` but ignores both inputs and returns success with no records. A caller cannot distinguish "verified, none found" from "never checked." (The event-driven `VerifyEvent` path *does* perform real `CheckID`/`VerifySignature`/digest/trust checks — the gap is only the pull-based interface method.)
- **Recommended fix:** Query persisted/subscribed attestations for the artifact and verify each, or return an explicit unsupported/unavailable error so policy fails closed.

### P2-4. SBOM "attestation" is unsigned metadata
- **Category:** fake-completion / missing-implementation
- **File:** `internal/adapters/sbom/attestation.go:64-132`
- **Evidence:**
  ```go
  // BuildAttestation creates an in-toto/SLSA-style attestation for an SBOM.
  attestation := &domain.SBOMAttestation{
      Type: InTotoStatementType,
      ...
      Predicate: domain.SBOMPredicate{ Generator: generator, ... },
  }
  return attestation, nil   // no DSSE envelope, no signature
  ```
- **Why it's not production-ready:** It builds an in-toto/SLSA-shaped JSON object and records a generator, but performs no signing, DSSE envelope creation, or key binding. Anyone can forge or mutate it while keeping the claimed generator identity. Calling it an "attestation" overstates its trust value.
- **Recommended fix:** Produce a signed DSSE/in-toto envelope with an explicitly configured signer; expose verification against a trusted identity before accepting/publishing.

### P2-5. Kubernetes deploy reports success after failing to apply env vars, labels, or rollout
- **Category:** fake-completion
- **File:** `internal/adapters/runtime/kubernetes.go:172-209`
- **Evidence:**
  ```go
  if _, err := k.runCommand(ctx, labelArgs...); err != nil {
      k.logger.Warn("failed to apply bahia label", zap.Error(err))
  }
  ...
  if _, err := k.runCommand(ctx, envArgs...); err != nil {
      k.logger.Warn("failed to set env vars", zap.Error(err))
  }
  ...
  if _, err := k.runCommand(ctx, restartArgs...); err != nil {
      k.logger.Warn("rollout restart failed", zap.Error(err))
  }
  ...
  return nil
  ```
- **Why it's not production-ready:** The workload can end up missing requested environment variables (e.g., DB credentials), missing the `bahia.service` tracking label (breaking later observation/management), or with no rollout — yet `Deploy` returns success.
- **Recommended fix:** Treat env and ownership-label failures as deployment failures (return joined errors); only classify genuinely optional steps as warnings.

### P2-6. Docker `PullImage` ignores in-stream errors and reports success on a failed pull
- **Category:** fake-completion
- **File:** `internal/adapters/runtime/control_client.go:77-102`
- **Evidence:**
  ```go
  if resp.StatusCode != http.StatusOK { return fmt.Errorf("docker pull returned %d", resp.StatusCode) }
  // Drain response body (pull progress).
  buf := make([]byte, 1024)
  for {
      if _, err := resp.Body.Read(buf); err != nil { break }  // any error ⇒ done, incl. non-EOF
  }
  return nil
  ```
- **Why it's not production-ready:** Docker's `/images/create` returns HTTP 200 and streams JSON progress; failures (missing manifest, auth denied) are reported as `{"error":...}` objects *in the stream*, and transport failures surface as non-EOF read errors. Both are discarded, so a failed pull returns success. Minor: `fromImage=%s` is not URL-encoded.
- **Recommended fix:** Decode the JSON progress stream, surface streamed `error`/`errorDetail`, distinguish `io.EOF` from transport failures, and URL-encode `fromImage`.

### P2-7. Registry OCI maps `401 Unauthorized` to "image does not exist"
- **Category:** reliability / security
- **File:** `internal/adapters/registry/oci.go:98-99`
- **Evidence:**
  ```go
  if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
      return &ImageInspection{Exists: false}, nil
  }
  ```
- **Why it's not production-ready:** `401` means expired/incorrect credentials or insufficient access, not absence. Callers making deployment/verification decisions receive a confident `Exists:false`, which can mask an outage or a misconfiguration as "no such image."
- **Recommended fix:** Map only `404` to `Exists:false`; return a typed auth error for `401`/`403`.

### P2-8. Loom dispatch/cancel report success when zero relays accepted the event
- **Category:** fake-completion
- **File:** `internal/adapters/loom/client.go:278-294` (and cancellation ~`:601-615`)
- **Evidence:**
  ```go
  published, err := c.pool.Publish(ctx, ev)
  if err != nil { return "", fmt.Errorf("publishing job request: %w", err) }
  ...
  return eventID, nil   // never checks published == 0
  ```
- **Why it's not production-ready:** `projection.go` correctly rejects `n == 0` ("no relay accepted event"), but the dispatch and cancellation paths do not. If the pool returns no aggregate error while no relay accepted the event, Bahia records submission/cancellation as successful and remembers the worker.
- **Recommended fix:** Treat `published == 0` as an error before recording success.

### P2-9. LLM soul generator's JSON-parse fallback fabricates a valid, write-privileged soul
- **Category:** fake-completion / security
- **File:** `internal/adapters/llm/generator.go:210-241`
- **Evidence:**
  ```go
  if err := json.Unmarshal([]byte(jsonStr), &output); err != nil {
      g.logger.Warn("failed to parse JSON, using fallback", "error", err)
      return g.fallbackParse(response)
  }
  ...
  func (g *SoulGenerator) fallbackParse(response string) (*domain.SoulGeneratorOutput, error) {
      output := &domain.SoulGeneratorOutput{
          SoulMD:       response,               // raw, unvalidated model text
          AllowedKinds: []int{0, 1, 4},
          ToolGrants: []domain.ToolGrant{
              {MCPServer: "agent-memory", Scopes: []string{"read", "write"}},  // auto write grant
          },
          ...
      }
      return output, nil
  }
  ```
- **Why it's not production-ready:** Invalid or adversarial model output silently becomes a "successful" configuration, bypassing the `output.SoulMD == ""` validation that the normal path enforces, and is handed a hard-coded write-capable tool grant — violating least-privilege on the failure path.
- **Recommended fix:** Fail closed on malformed output; never synthesize privileged grants in a parser fallback.

### P2-10. LLM gateway client fabricates `Synced` status and a config hash from empty/inconclusive responses
- **Category:** fake-completion
- **File:** `internal/adapters/llm/gateway_http.go:80-87`, `:202-206`
- **Evidence:**
  ```go
  if obs.Status == "" || obs.Status == domain.GatewayRouteStatusUnknown {
      obs.Status = domain.GatewayRouteStatusSynced
  }
  ...
  if len(strings.TrimSpace(string(body))) == 0 {
      obs.Status = domain.GatewayRouteStatusSynced
      obs.GatewayConfigHash = fallback.ManagedConfigHash()   // locally invented, not observed
      return obs, nil
  }
  ```
- **Why it's not production-ready:** Any empty 2xx response, or one omitting status, is reported as synchronized with a locally-manufactured config hash. Callers cannot distinguish a confirmed apply from an endpoint that did nothing.
- **Recommended fix:** Require an acknowledgement containing the observed route identity/hash, or follow the mutation with a GET and compare; preserve `Unknown` when confirmation is absent.

### P2-11. LLM avatar retrieval allows SSRF and unbounded downloads
- **Category:** security
- **File:** `internal/adapters/llm/avatar.go:522-544`, `:557-577` (direct-URL provider path `:250-264`)
- **Evidence:**
  ```go
  imageURL := apiResp.ImageURL                       // provider-controlled
  imageData, imageContentType, err := p.fetchImage(ctx, imageURL)
  ...
  req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
  imageData, err := io.ReadAll(resp.Body)            // no size limit
  ```
- **Why it's not production-ready:** The generation service can direct Bahia to arbitrary URLs including loopback, private-network, and cloud metadata endpoints; the response body is read without a size cap (memory exhaustion), and redirects use default permissive behavior.
- **Recommended fix:** Restrict downloads to the configured provider origin or an allowlist; reject private/link-local/loopback/metadata destinations and userinfo; cap response size; validate MIME/image data; constrain redirects.

### P2-12. LLM avatar generation accepts nil/empty provider output as completed
- **Category:** fake-completion
- **File:** `internal/adapters/llm/avatar.go:342-367`, `:547-554`
- **Evidence:**
  ```go
  result, err := provider.GenerateAvatar(...)
  ...
  emitAvatarProgress(... Stage: AvatarProgressCompleted, ... Result: result)
  return result, nil
  ```
- **Why it's not production-ready:** A provider returning `(nil, nil)`, or Flux returning HTTP 200 with an empty body, emits a `Completed` event and a nil error despite no avatar existing.
- **Recommended fix:** Before completion, require a non-nil result, non-empty image bytes, an allowed content type, and basic image decode/size validation.

### P2-13. Cashu wallet's core payment operations are non-functional
- **Category:** missing-implementation
- **File:** `internal/adapters/cashu/wallet.go:25-26`, `:172-176`, `:220-224`, `:243-256`
- **Evidence:**
  ```go
  var ErrMintBackedFlowUnsupported = errors.New("cashu mint-backed proof flow not implemented")
  ...
  func (w *Wallet) CreatePaymentToken(...) (string, error) {
      ... // balance/validation checks
      return "", ErrMintBackedFlowUnsupported
  }
  func (w *Wallet) ReceiveToken(...) (int64, string, error) {
      ...
      return 0, "", ErrMintBackedFlowUnsupported
  }
  // AddProofs increments balance with no mint verification:
  w.proofs[mintURL] = append(w.proofs[mintURL], proofs...)
  w.balances[mintURL] += total
  ```
- **Why it's not production-ready:** The two operations that actually move ecash always return an "unsupported" error, so the payment feature cannot function; `selectProofs` is dead code; and `AddProofs` inflates a local balance with no mint verification (that inflated balance is what `CreatePaymentToken` checks before failing). The wallet advertises capabilities (and telemetry counters) it cannot fulfill. (To its credit it returns an explicit error rather than fake success — hence P2, not P1.)
- **Recommended fix:** Implement the NUT mint/melt/swap flows against a real mint, or remove the wallet from any production wiring and clearly mark the payment surface as unavailable.

### P2-14. Telemetry silently ignores configured OTLP export; `Shutdown` is a no-op
- **Category:** observability
- **File:** `internal/adapters/telemetry/telemetry.go:16-21`, `:130-140`, `:166-169`
- **Evidence:**
  ```go
  OTLPEndpoint   string // e.g. "localhost:4317" ...
  OTLPProtocol   string // "grpc" or "http" ...
  ...
  if cfg.OTLPEndpoint != "" {
      logger.Warn("telemetry OTLP endpoint configured but exporters are not implemented; endpoint will be ignored", ...)
  }
  ...
  func (p *Provider) Shutdown(ctx context.Context) error {
      p.logger.Info("telemetry shutdown complete"); return nil  // nothing to flush
  }
  ```
- **Why it's not production-ready:** `Config` exposes `OTLPEndpoint`/`OTLPProtocol` as if OTLP export is supported, but no exporter exists — traces/metrics are only available via the in-process `/metrics` handler. Operators configuring OTLP get silent (warn-only) drop; `Shutdown` flushes nothing.
- **Recommended fix:** Wire real OTLP exporters, or remove the OTLP config surface and document that only the Prometheus `/metrics` endpoint is supported; make `Shutdown` flush real exporters when added.

### P2-15. PowerDNS client uses `http.DefaultClient` (no timeout) and permits cleartext key transport
- **Category:** reliability / security
- **File:** `internal/adapters/dns/powerdns.go:58-60`, `:63-73`, `:159-173`
- **Evidence:**
  ```go
  func NewPowerDNSBackend(cfg PowerDNSConfig) (*PowerDNSBackend, error) {
      return newPowerDNSBackend(cfg, http.DefaultClient)   // no overall timeout
  }
  ...
  request.Header.Set("X-API-Key", b.apiKey)   // any scheme (incl. http) accepted at validation
  ```
- **Why it's not production-ready:** With a context that has no deadline, calls can hang indefinitely, stalling health checks and reconciliation; and the API secret can be sent over plaintext `http`.
- **Recommended fix:** Construct an `http.Client` with request/dial/TLS/response-header timeouts; require `https` by default and allow `http` only under an explicit insecure-dev flag.

### P2-16. DockerHub and GHCR token requests have no client timeout
- **Category:** reliability
- **Files:** `internal/adapters/registry/dockerhub.go:62`, `internal/adapters/registry/ghcr.go:59`
- **Evidence:**
  ```go
  resp, err := http.DefaultClient.Do(req)   // auth path — no timeout
  ```
  (Contrast: the OCI manifest client uses `&http.Client{Timeout: 30 * time.Second}`.)
- **Why it's not production-ready:** Authentication can hang indefinitely when the caller's context lacks a deadline, blocking inspection/deployment.
- **Recommended fix:** Use a bounded `http.Client` for the auth providers, ideally sharing the OCI transport.

### P2-17. Blossom: server failures become "not found", 3xx treated as success, upload fabricates a descriptor
- **Category:** reliability / fake-completion / security
- **File:** `internal/adapters/blossom/download.go:173-185`; `upload.go:134-153`; `client.go:115-120`; `avatar.go:209-220,250-264`
- **Evidence:**
  ```go
  // download.go — existence probe
  return resp.StatusCode == http.StatusOK, nil   // 401/403/429/5xx ⇒ (false, nil)
  ```
  ```go
  // upload.go — invent a descriptor when the body isn't JSON
  if err := json.Unmarshal(body, &bd); err != nil {
      bd = BlobDescriptor{ URL: url[:len(url)-7] + "/" + hash, SHA256: hash, Size: int64(len(data)) }
  }
  return &bd, nil
  ```
  ```go
  // client.go / download.go / list.go / upload.go / avatar.go — 3xx accepted
  if resp.StatusCode >= 400 { return fmt.Errorf("server returned %d", resp.StatusCode) }
  return nil
  ```
- **Why it's not production-ready:** An outage or auth failure during the existence check reads as authoritative absence (upload then proceeds); any non-error response with malformed/empty body becomes a locally-invented "successful" upload with unverified hash/size/URL; and final 3xx responses are treated as success. The direct-avatar path also allows SSRF (only scheme/host are validated).
- **Recommended fix:** Return `(false, nil)` only for `404`; require the documented `2xx` range; validate the upload response's SHA-256/size/URL (or confirm with an authenticated HEAD); apply SSRF protections on avatar fetches.

### P2-18. Signet logs the full bunker URI including its `secret`
- **Category:** security
- **File:** `internal/adapters/signet/client.go:71-76`, `:131`
- **Evidence:**
  ```go
  BunkerURI string // bunker://<pubkey>?relay=...&secret=...
  ...
  c.logger.Info("connecting to Signet bunker", "uri", c.bunkerURI)
  ```
- **Why it's not production-ready:** The NIP-46 connection `secret` (and relay details) can land in production logs.
- **Recommended fix:** Parse and redact `secret` before logging; log only a shortened bunker pubkey and sanitized relay hosts.

### P2-19. Nexus hardcodes storage policy and accepts `409 Conflict` as success without verifying state
- **Category:** unsafe-hardcoding / reliability
- **File:** `internal/adapters/nexus/nexus.go:85-94`, `:99-102`
- **Evidence:**
  ```go
  "storage": map[string]any{
      "blobStoreName":               "default",
      "strictContentTypeValidation": false,
      "writePolicy":                 "allow_once",
  },
  ...
  if resp.StatusCode == http.StatusConflict ||
      (resp.StatusCode >= 200 && resp.StatusCode < 300) {
      return packagebackend.RepositoryObservation{Exists: true, ...}, nil
  }
  ```
- **Why it's not production-ready:** Every repo is forced onto a blob store literally named `default`, with content-type validation disabled and a fixed write policy — this fails on installs without that blob store and can violate retention/security policy. A `409` may reflect an *incompatible* existing config, but is reported as "exists" without reading it.
- **Recommended fix:** Make blob store/validation/write-policy explicit validated config with secure defaults; on conflict, fetch and compare the existing resource, returning an actionable mismatch error.

### P2-20. Secrets `Encryptor`: no key validation, unsalted single-SHA-256 AES key, swallowed pubkey-derivation error
- **Category:** security
- **File:** `internal/adapters/secrets/nip44.go:26-40`, `:133-140`
- **Evidence:**
  ```go
  // any non-empty string is accepted as key material
  hash := sha256.Sum256([]byte(nostrPrivateKey))
  return &Encryptor{ privateKey: nostrPrivateKey, aesKey: hash[:] }, nil
  ...
  func (e *Encryptor) publicKey() string {
      pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(e.privateKey)
      if err != nil { return "" }   // error swallowed ⇒ empty pubkey used downstream
      return pubkey
  }
  ```
- **Why it's not production-ready:** `NewEncryptor` never validates that the input is a real secp256k1 key; the `EncryptionAES256` path derives the AES-256 key via a single unsalted SHA-256 of the raw string (no domain separation, key-domain coupling to the identity key); and `publicKey()` converts an actionable derivation error into `""`, which silently corrupts NIP-44 self-encryption conversation keys. (Mitigating factor: a valid Nostr key is high-entropy, so offline guessing of the AES key is not the primary risk — the validation gap and swallowed error are.)
- **Recommended fix:** Validate the secp256k1 key in the constructor; use a domain-separated HKDF (or a separate KMS-managed data key) for AES; make `publicKey()` return `(string, error)` and propagate failures.

### P2-21. LLM external provisioner is production-wired but provision/deprovision are metadata-only
- **Category:** incomplete-migration
- **File:** `internal/adapters/llm/provisioner_external.go:26-39`, `:75-77`; wiring `internal/app/app.go:554-557`
- **Evidence:**
  ```go
  func (p *ExternalAPIProvisioner) Provision(_ context.Context, req ProvisionCandidateRequest) (*ProvisionCandidateResult, error) {
      ... return &ProvisionCandidateResult{ ... BackendEndpoint: ... }, nil   // no control-plane call
  }
  func (p *ExternalAPIProvisioner) Deprovision(...) error { return nil }       // no-op
  ```
- **Why it's not production-ready:** The `LLMBackendKindExternalAPI` provisioner is registered in the production factory but performs no provider create/delete — it always reports success after local validation. If "external" means registration-only, the lifecycle state overstates what Bahia did (and `provisioner_runtime.go` deprovision similarly returns `nil` when the runtime target is absent).
- **Recommended fix:** Model this explicitly as attach/detach or unmanaged registration and surface that status; otherwise implement and verify provider-specific lifecycle operations.

### P2-22. Agent-memory client drops supplied identity metadata and keeps task IDs only in process memory
- **Category:** incomplete-migration / reliability
- **File:** `internal/adapters/agentmemory/client.go:75-87`, `:26-28,186-203`
- **Evidence:**
  ```go
  func (c *Client) RegisterAgent(ctx context.Context, agentID string, npub string, metadata map[string]interface{}) error {
      ...
      result, err := c.callToolText(ctx, "memory_task_start", map[string]interface{}{
          "agent": agentID, "goal": goal,     // npub and metadata never sent
      })
  }
  ...
  taskIDs map[string]string   // in-memory only; lost on restart ⇒ duplicate task on re-seed
  ```
- **Why it's not production-ready:** Callers can believe `npub`/`metadata` were persisted when only the agent ID and a generated goal were sent; and after a restart, seeding an existing agent starts a *new* task rather than recovering the existing one, risking duplicate memory streams.
- **Recommended fix:** Send `npub`/`metadata` (or drop them from the signature); persist the task-ID mapping or use an idempotent upstream create keyed by agent ID.

---

## P3 — Medium

### P3-1. FIPS DNS backend silently drops unsupported records and can "succeed" with an empty managed section
- **Category:** fake-completion
- **File:** `internal/adapters/dns/fips.go:163-170`, `:207-210`
- **Evidence:**
  ```go
  fqdn, value, ok, err := fipsHostsEntry(zone, record)
  ...
  if !ok { continue }        // unsupported value ⇒ silently skipped
  ...
  if !isFIPSHostsValue(value) { return "", "", false, nil }
  ```
- **Why:** A desired zone whose records all fail `isFIPSHostsValue` produces (and successfully writes) an empty managed section, reporting reconciliation success while applying nothing. (The write/parse order is internally self-consistent — `hostname  value` written, `fields[0]=hostname, fields[1]=value` read — so there is *not* a round-trip bug within Bahia; the risk is only if the file is consumed by a standard `/etc/hosts` resolver, which expects `address hostname`.)
- **Fix:** Reject unsupported records explicitly or return a structured partial-apply result; validate the whole desired set before writing.

### P3-2. Filesystem DNS backend persists a snapshot but performs no DNS apply/reload
- **Category:** fake-completion / missing-implementation
- **File:** `internal/adapters/dns/filesystem.go:102-164`
- **Evidence:** Writes Bahia-specific JSON (`filesystemZonePayload`) via atomic rename and returns `nil` with no server reload/API call/consumer confirmation.
- **Why:** It is storage, not an operational DNS apply backend, yet satisfies the same interface and returns success.
- **Fix:** Classify it as snapshot/test-only, or add a configured renderer/apply/reload path and verify activation before returning success.

### P3-3. dnsmasq replaces the live config before reload, with no rollback on reload failure
- **Category:** reliability
- **File:** `internal/adapters/dns/dnsmasq.go:190-199`
- **Evidence:**
  ```go
  if err := os.Rename(tmpName, path); err != nil { ... }
  committed = true
  if err := b.executor().Run(ctx, b.reloadCommand); err != nil {
      return fmt.Errorf("reload dnsmasq after syncing zone %q: %w", zone.Name, err)
  }
  ```
- **Why:** On reload failure the on-disk config is the new version while the running daemon retains the old state; no rollback/retry.
- **Fix:** Preserve the prior file, validate before replacement, reload, and restore/reload the previous version if activation fails.

### P3-4. SBOM parser accepts structurally invalid documents as successful empty SBOMs
- **Category:** reliability
- **File:** `internal/adapters/sbom/parser.go:129-148,177-218,255-301`
- **Evidence:** Format detection keys only on `spdxVersion != ""` or `bomFormat == "CycloneDX"`; `{"spdxVersion":"garbage"}` / `{"bomFormat":"CycloneDX"}` parse to zero packages with no error.
- **Why:** Required version/namespace/identity/component fields and supported schema versions are not enforced, so malformed inputs succeed silently.
- **Fix:** Validate against supported SPDX/CycloneDX schemas or enforce required fields/versions before returning success.

### P3-5. SBOM storage trusts the Blossom upload result and can panic on a nil logger/descriptor
- **Category:** reliability / security
- **File:** `internal/adapters/sbom/storage.go:44-58,195-216,221-242`
- **Evidence:**
  ```go
  desc, err := r.blossom.Upload(ctx, data, mediaType)
  ... Hash: desc.SHA256,    // no local digest comparison; nil desc ⇒ panic
  ...
  if len(hashPart) != 64 { return "", fmt.Errorf("invalid hash length: %d", len(hashPart)) }  // length only, not hex
  ```
- **Why:** The returned descriptor's hash/URL are not checked against the uploaded bytes; the constructor accepts a nil logger that later paths dereference; and the "SHA-256" check validates length but not hex content.
- **Fix:** Compute and compare the digest locally, reject nil/malformed descriptors, validate hex via `hex.DecodeString`, and default a nil logger.

### P3-6. CLI accepts raw Nostr private keys as command-line flags
- **Category:** security
- **File:** `cmd/cli/main.go:58-59`
- **Evidence:**
  ```go
  rootCmd.PersistentFlags().StringVar(&nostrNsec, "nsec", "", "...")
  rootCmd.PersistentFlags().StringVar(&nostrPrivateKey, "privkey", "", "...")
  ```
- **Why:** Command-line secrets leak via shell history, `ps`/process listings, and CI logs.
- **Fix:** Prefer key files, stdin, OS keychains, secret managers, or a remote signer; gate raw-key flags behind an explicit unsafe opt-in.

### P3-7. Relay/server defaults embed third-party relays and plaintext dev endpoints
- **Category:** unsafe-hardcoding
- **File:** `internal/config/config.go:738-744` (ContextVM relays), `:755-760` (relay sidecar `ws://`/`0.0.0.0:3334`); consumed by `cmd/relay/main.go:20`, `cmd/server/main.go`
- **Evidence:**
  ```go
  ContextVMRelays: []string{ "wss://relay.contextvm.org", "wss://relay2.contextvm.org", "wss://cvm.otherstuff.ai" },
  Sidecar: RelaySidecarConfig{ ListenAddr: "0.0.0.0:3334", PublicURL: "ws://localhost:3334", BackendURL: "ws://localhost:3334" },
  ```
- **Why:** Starting without explicit config connects the production service to externally-operated relays (availability/privacy/trust dependency) and, when the sidecar is enabled without overriding endpoints, listens on all interfaces over plaintext `ws://`.
- **Fix:** Require explicit relay and listen/public/backend addresses in production; default to loopback and require `wss://` for externally reachable deployments.

### P3-8. mcpclient ignores `Config.Timeout` when an HTTP client is injected
- **Category:** reliability
- **File:** `internal/adapters/mcpclient/client.go:109-124`
- **Evidence:**
  ```go
  if config.Timeout == 0 { config.Timeout = defaultTimeout }
  ...
  httpClient := config.HTTPClient
  if httpClient == nil { httpClient = &http.Client{Timeout: config.Timeout} }  // injected client's timeout not enforced
  ```
- **Why:** A commonly-injected `&http.Client{}` has no overall timeout; negative configured timeouts are not normalized.
- **Fix:** Require injected clients to carry a positive timeout, or wrap each request in `context.WithTimeout`.

### P3-9. Signet: `ListAgents` returns only volatile cache; auth-header marshal error swallowed; core ops lack deadlines
- **Category:** fake-completion / reliability / security
- **File:** `internal/adapters/signet/client.go:502-511`, `:697-700`, `:137-147,287-290,338-348`
- **Evidence:**
  ```go
  func (c *Client) ListAgents(ctx context.Context) ([]*AgentIdentity, error) {
      ... return agents, nil   // ctx unused, no Signet query; empty after restart
  }
  ...
  eventJSON, _ := json.Marshal(event)   // error ignored ⇒ possibly invalid auth header
  ...
  bunker, err := nip46.ConnectBunker(ctx, ...)   // no client-enforced connect/sign deadline
  ```
- **Why:** `ListAgents` presents cache-only state as an authoritative list (empty on a fresh process); a marshal failure can yield a nominally-successful but invalid `Nostr <base64>` auth header; and connect/sign can wait indefinitely with `context.Background()`.
- **Fix:** Implement a real management query (or rename to `ListCachedAgents`); check the marshal error; add configurable connect/sign timeouts.

---

## Confirmed Real (notable non-findings)

These were specifically checked and found to make genuine external calls with reasonable handling; no fake-completion:

- **Backups:** `kopia_backend.go`/`velero_backend.go` exec the real CLIs via `exec.CommandContext`, capture stdout/stderr, and propagate non-zero exits.
- **DNS:** CoreDNS applies via a real atomic etcd transaction; PowerDNS makes real GET/PATCH API calls with status validation (timeout gap noted in P2-15).
- **SBOM:** Syft uses Anchore's in-process library (real generation); cdxgen runs the real binary and checks exit status/combined output.
- **Security:** `security/osv.go` makes real `/querybatch` and `/vulns/{id}` calls with a 30s timeout, result-count validation, non-200 rejection, and retry handling.
- **Registry:** DockerHub/GHCR/OCI perform real authentication and manifest/digest retrieval (issues are the 401 mapping in P2-7 and auth-client timeouts in P2-16).
- **Runtime:** `runtime/factory.go` and `resolver.go` return only concrete Docker/Compose/Kubernetes/Podman runtimes — **no mock/fake/stub runtime is reachable in production**; the `desired_state_capability.go` "Stub implementations for adapters not yet migrated" comment is **stale** (the implementations are real and report support), and unsupported capability resolution returns `ErrDesiredStateNotSupported` rather than a no-op success.
- **Backends:** `filesystem_mock` does real filesystem storage/indexing (the concern in P2-1 is its production *exposure*, not fabricated data).
- **Loom/HiveCI:** HiveCI subscriber consumes real merged relay channels (kinds 5401/5402) and dispatches to real handlers; Loom builds/signs real Nostr events and performs real NIP-44 secret encryption (`NIP44ConversationKey` + `nip44.Encrypt`).
- **cmd:** `bahia-test-relay`'s hardcoded secret hex constants and canned contextVM responses are **isolated to the test binary** — repo-wide search found no production references to `bahia-test-relay`, `serviceSecretHex`, or `contextVMResultForRequest`. OpenClaw sidecar/control perform real signing/CLI delegation, not stubs.

---

## Cross-Cutting Themes & Recommendations

1. **"Success" must mean "confirmed effect."** The most common defect class (P1-1, P2-2/5/6/8/10/12, P3-1/2) is returning success after an unconfirmed or failed external effect. Establish a convention: mutations verify the observed result (read-back or acknowledgement) before reporting success, and `n == 0`/empty-body cases are errors, not `Synced`.
2. **Security verifiers must fail closed.** Cosign (P1-1), Nostr `VerifySignatures` (P2-3), and SBOM attestation (P2-4) either don't verify or swallow errors. Verification unavailability must be a distinct, non-acceptable status.
3. **HTTP hygiene at every boundary.** Several clients use `http.DefaultClient` (no timeout) or ignore `Config.Timeout` (P2-15, P2-16, P3-8); none implement retry/backoff for transient 429/5xx. Standardize a bounded, retry-aware client factory.
4. **SSRF & secret handling.** Provider/user-controlled URLs are fetched without allowlisting or size limits (P2-11, P2-17), and secrets appear on the command line (P3-6) and in logs (P2-18). Add SSRF guards, response caps, and secret redaction.
5. **Test/dev components are exposed in production surfaces.** `filesystem_mock` (P2-1) and the filesystem DNS backend (P3-2) are dev-oriented yet selectable in production. Gate them behind build tags or explicit dev-mode.
