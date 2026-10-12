DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM secret_access_audit WHERE secret_version_id IS NULL) THEN
        RAISE EXCEPTION 'cannot restore secret_access_audit.secret_version_id NOT NULL while store-backed access audit rows remain';
    END IF;
END $$;
ALTER TABLE secret_access_audit ALTER COLUMN secret_version_id SET NOT NULL;
