# Verification Report: bahia-cxj1

## Observed behavior

- `internal/adapters/llm/avatar.go` now exposes an `AvatarProvider` interface and `AvatarProviderRegistry`.
- `flux-comfyui` is implemented by refactoring the existing Lemmy/ComfyUI HTTP flow.
- `fal` and `replicate` are represented as registered-but-unavailable provider infos until concrete clients/credentials are added.
- `GenerateWithSpec` accepts `domain.SoulAvatarGenerationSpec` and expands style presets.
- `GenerateAsync` streams queued/dispatch/provider/completion progress events without polling.
- Legacy `Generate` and `GenerateDefault` entrypoints remain available.

## Verification evidence

- `go test ./internal/adapters/llm` passed.
- `go test ./internal/...` passed.
- Oracle review completed; follow-up fixes added for dimension validation, provider availability metadata, terminal progress ownership, ordered progress assertions, and legacy entrypoint tests.

## Known boundaries

- Hosted `fal` and `replicate` clients are not implemented in this task; they are exposed as unavailable provider metadata rather than usable generation backends.
- Blossom upload/storage remains outside this task and is tracked by downstream avatar storage work.
