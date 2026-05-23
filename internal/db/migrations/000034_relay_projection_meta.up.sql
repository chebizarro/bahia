CREATE TABLE IF NOT EXISTS relay_projection_meta (
  stream TEXT NOT NULL,
  entity_key TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  source_event_id TEXT NOT NULL,
  tombstoned BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (stream, entity_key)
);

CREATE INDEX idx_rpm_stream ON relay_projection_meta(stream);
