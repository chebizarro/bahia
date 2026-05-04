# PSTF Runtime Artifacts

This directory contains Product Specification Testing Framework artifacts for this repository.

Generated files should be inspectable, versionable, and understandable by both humans and agents.

## Suggested sequence

1. Create or update `product_map.md`.
2. Create or update `planning_docs_index.md`.
3. Create or update `spec_gap_report.md`.
4. Select one feature slice.
5. Create `/features/<FEATURE_ID>/feature_spec.json`.
6. Create acceptance criteria.
7. Create a test matrix.
8. Verify, diagnose, patch, and review.

## Feature folder convention

```text
features/<FEATURE_ID>/
  feature_spec.json
  acceptance_criteria.json
  test_matrix.json
  defects.json
  verification_report.md
  hitl_decisions.md
```
