-- Keyset observation scan and exact semantic package representative lookup.
CREATE INDEX idx_f74a_observations_coordinate_time
  ON runtime_observations(service_id, environment_id, observed_at, id);
CREATE INDEX idx_f74a_state_current_observation
  ON environment_service_state(current_observation_id)
  WHERE current_observation_id IS NOT NULL;
CREATE INDEX idx_f74a_packages_semantic_id
  ON sbom_packages(sbom_id, name, version,
    (COALESCE(ecosystem, '')), (COALESCE(license, '')),
    (COALESCE(purl, '')), (COALESCE(cpe, '')), id);
