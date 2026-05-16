# Bucket 4 Fail-Closed Core Adapters Verification

## Scope

Beads: `bahia-00vf`, `bahia-ig2d`, `bahia-qat5`, `bahia-wg9r`.

## Acceptance coverage

| Bead | Acceptance criteria | Verification |
| --- | --- | --- |
| `bahia-00vf` | Secret mutation paths fail closed without an encryptor; no plaintext fallback. | `NewEncryptor` rejects blank keys; HTTP create/update return 503 when encryptor is nil; encrypted-route/MCP callsites updated for fallible constructor. |
| `bahia-ig2d` | Enabled Cashu live mode must not advertise unsupported success. | `cashu.enabled=true` now fails config validation and app startup with explicit unsupported mint-backed-flow message; payment handlers return 503 when service is nil. |
| `bahia-qat5` | Qdrant supports auth headers and fails closed for missing auth. | Qdrant config/client now accept API key/header name; every request uses the centralized authenticated request helper; URL without API key fails unless explicit local unauthenticated mode is set. |
| `bahia-wg9r` | No hardcoded private-network avatar backend; no placeholder provider advertising. | Avatar generator registers only explicitly configured providers; no private LAN default; generation fails before request handling when no provider or unconfigured provider is selected. |

## Test evidence

Command run:

```bash
go test ./internal/adapters/secrets ./internal/api/handlers ./internal/config ./internal/app ./internal/adapters/qdrant ./internal/adapters/llm ./internal/controlplane ./internal/mcp ./internal/service ./internal/soulfactory
```

Result: all listed packages passed.

## Notes

- `pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage/web/coverage-summary.json` was intentionally not touched.
- Bucket 3 tool provisioning recovery/serialization was intentionally not implemented.
