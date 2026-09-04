ALTER TABLE dns_zones
  DROP CONSTRAINT IF EXISTS dns_zones_visibility_check;

ALTER TABLE dns_zones
  ADD CONSTRAINT dns_zones_visibility_check
  CHECK (visibility IN ('internal', 'external', 'edge'));
