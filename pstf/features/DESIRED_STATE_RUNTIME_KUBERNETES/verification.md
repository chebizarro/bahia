# Verification Evidence

## Implementation Date
2026-06-08

## Files Changed
- `internal/domain/runtime_desired_state.go` — KubernetesExtension populated
- `internal/adapters/runtime/kubernetes_desired_state.go` — NEW: manifest generation + apply helpers
- `internal/adapters/runtime/desired_state_capability.go` — K8s SupportsDesiredState() flipped to true
- `internal/adapters/runtime/observation_normalizer.go` — NormalizeKubernetesPod added
- Tests: kubernetes_desired_state_test.go, kubernetes_drift_test.go, updated capability/resolver tests

## Verification Commands
```bash
go test ./internal/domain/... -run "TestDesired|TestKubernetes" -v -count=1
go test ./internal/adapters/runtime/... -run "TestK8s|TestKubernetes|TestBahiaK8s|TestMapDesiredSpec|TestDrift" -v -count=1
go vet ./internal/...
```

## Design Decisions
- Uses kubectl CLI (no client-go dependency) — consistent with existing KubernetesRuntime pattern
- Generates Deployment + optional Service manifests via kubectl apply -f -
- Observation normalization works with kubectl get pods JSON output
- Preserves legacy Deploy/Observe methods alongside new desired-state path
