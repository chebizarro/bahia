-- Secret values live only in the secret store (docs/architecture/secret-store.md);
-- Bahia seals no value, so the wrapped service-secret data keys are retired.
-- Rows still marked aes256gcm-v2 are legacy values every consumer refuses.
DROP TABLE IF EXISTS service_secret_data_keys;
