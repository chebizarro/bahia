DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM service_secrets WHERE encryption_method = 'aes256gcm-v2') OR
       EXISTS (SELECT 1 FROM secret_versions WHERE encryption_method = 'aes256gcm-v2') THEN
        RAISE EXCEPTION 'cannot remove wrapped service-secret data keys while v2 ciphertext remains';
    END IF;
END $$;
DROP TABLE service_secret_data_keys;
