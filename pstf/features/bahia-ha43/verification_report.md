# Verification report — bahia-ha43

## Summary
Docker-compatible Engine API mutation ownership is centralized in `dockerEngineControlClient`. Docker desired-state apply uses `DockerControlClient` for lookup, resource ensures, image pull, container stop/remove/create/start, and network attach operations. Podman continues to validate compatibility and delegate through the embedded Docker desired-state path, preserving Engine API execution mode reporting.

Legacy Docker `Deploy` now delegates pull/create/start mutations to the control client instead of constructing those Engine API requests directly in `docker.go`.

## Evidence
- Added deterministic delegation tests:
  - `TestApplyDesiredState_DelegatesMutationsToControlClient`
  - `TestDockerDeploy_DelegatesMutationsToControlClient`
- Required quality gate: `go test ./internal/adapters/runtime` passed in 7.641s.

## Documentation
No `docs/deployment.md` update was needed because this change does not introduce or alter user-facing runtime configuration. Existing execution-mode semantics remain `engine_api` for Docker/Podman and are now enforced by the control client ownership boundary.

## Remaining work
No remaining work identified for bahia-ha43.
