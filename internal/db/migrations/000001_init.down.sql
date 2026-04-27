-- 000001_init.down.sql
-- Reverts the core schema.

DROP TABLE IF EXISTS nostr_events;
DROP TABLE IF EXISTS environment_service_state;
DROP TABLE IF EXISTS runtime_observations;
DROP TABLE IF EXISTS deployment_runs;
DROP TABLE IF EXISTS deployment_intents;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS builds;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS services;
