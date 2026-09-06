ALTER TABLE dns_zones
    ADD COLUMN allow_empty_authoritative BOOLEAN NOT NULL DEFAULT FALSE;
