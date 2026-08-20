# Dedicated OpenClaw Runtime Orchestration Verification

Task: `bahia-openclaw-dedicated-runtime-orchestration-20260819`

The adapter now reconciles one deterministic Docker Compose gateway per external soul. Runtime ownership is verified from labels before adoption, restart, or deletion. The generated specification pins the image digest and source commit, mounts separate persistent state/workspace/agent paths, enforces resource and process limits, waits for health, bounds logs, uses an isolated bridge network, and mounts secret files read-only after ownership/mode validation.

Behavioral coverage is mapped in `test_matrix.json`.

Verified on 2026-08-19:

- `go test -race ./internal/soulfactory/openclawcontrol` — pass
- `go vet ./internal/soulfactory/openclawcontrol ./cmd/openclaw-soulfactory-control` — pass
- `go build ./...` — pass
- `go test ./...` — pass
- `jq empty pstf/features/OPENCLAW_DEDICATED_RUNTIME_ORCHESTRATION/*.json` — pass
