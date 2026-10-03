ALTER TABLE ml_model_versions ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE ml_model_versions SET updated_at = created_at;
