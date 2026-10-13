DROP TABLE route_canary_sequence_floor;
ALTER TABLE route_canary_events DROP COLUMN observation_sequence;
ALTER TABLE route_canary_state DROP COLUMN observation_sequence;
