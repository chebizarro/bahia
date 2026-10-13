ALTER TABLE route_canary_state
  ADD COLUMN observation_sequence BIGINT NOT NULL DEFAULT 0
  CHECK (observation_sequence >= 0);

ALTER TABLE route_canary_events
  ADD COLUMN observation_sequence BIGINT NOT NULL DEFAULT 0
  CHECK (observation_sequence >= 0);

CREATE TABLE route_canary_sequence_floor (
  route_coordinate TEXT PRIMARY KEY,
  observation_sequence BIGINT NOT NULL CHECK (observation_sequence >= 0)
);
