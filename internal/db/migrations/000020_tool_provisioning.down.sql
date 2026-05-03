DROP INDEX IF EXISTS idx_tool_approval_log_intent_created;
DROP INDEX IF EXISTS idx_tool_profile_state_environment;
DROP INDEX IF EXISTS idx_tool_provision_runs_status;
DROP INDEX IF EXISTS idx_tool_provision_runs_intent_id;
DROP INDEX IF EXISTS idx_tool_provision_intents_nostr_event_id;
DROP INDEX IF EXISTS idx_tool_provision_intents_pending_approval;
DROP INDEX IF EXISTS idx_tool_provision_intents_status;
DROP INDEX IF EXISTS idx_tool_provision_intents_service_env_created;

DROP TABLE IF EXISTS tool_approval_log;
DROP TABLE IF EXISTS tool_profile_state;
DROP TABLE IF EXISTS tool_provision_runs;
DROP TABLE IF EXISTS tool_provision_intents;
DROP TABLE IF EXISTS tool_denylist;
