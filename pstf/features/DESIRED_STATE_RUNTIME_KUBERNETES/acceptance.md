# DESIRED_STATE_RUNTIME_KUBERNETES — Acceptance Criteria

Feature: bahia-amqy — Kubernetes desired-state renderer and adapter

## Acceptance Criteria

1. Uses existing DesiredServiceSpec/DesiredEnvironmentPlan domain contract
2. KubernetesExtension populated with K8s-specific fields (namespace, replicas, service type, resources, probes, etc.)
3. Implements DesiredStateApplier for Kubernetes via kubectl apply
4. Runtime resolver exposes K8s support when adapter is implemented
5. Apply flow: find existing → hash compare → no-op or generate manifests → kubectl apply
6. Observation normalization for K8s pods produces NormalizedObservation
7. Drift detection works through existing CompareDrift path
8. Tests cover: apply, no-op hash match, rejection paths, observation normalization, drift states
9. No fallback to legacy desired-state pretending
10. Legacy Deploy/Observe methods preserved alongside desired-state path

## Test Matrix

| Scenario | Test File | Status |
|----------|-----------|--------|
| K8s extension serialization | runtime_desired_state_test.go | ✅ |
| Manifest generation | kubernetes_desired_state_test.go | ✅ |
| Apply no-op (hash match) | kubernetes_desired_state_test.go | ✅ |
| Apply with mutations | kubernetes_desired_state_test.go | ✅ |
| Dry run | kubernetes_desired_state_test.go | ✅ |
| Observation normalization | observation_normalizer_test.go | ✅ |
| Drift in_sync | kubernetes_drift_test.go | ✅ |
| Drift drifted | kubernetes_drift_test.go | ✅ |
| Drift unknown | kubernetes_drift_test.go | ✅ |
| Resolver resolution | resolver_desired_state_test.go | ✅ |
| Capability probe | desired_state_capability_test.go | ✅ |
