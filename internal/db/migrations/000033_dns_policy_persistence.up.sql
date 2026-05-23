CREATE TABLE dns_policies (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  zone_id UUID,
  environment_id UUID REFERENCES environments(id) ON DELETE CASCADE,
  rules JSONB NOT NULL DEFAULT '[]'::jsonb,
  enabled BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (jsonb_typeof(rules) = 'array'),
  CHECK (jsonb_array_length(rules) > 0),
  CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX idx_dns_policies_enabled ON dns_policies(enabled) WHERE enabled = true;
CREATE INDEX idx_dns_policies_zone_id ON dns_policies(zone_id) WHERE zone_id IS NOT NULL;
CREATE INDEX idx_dns_policies_environment_id ON dns_policies(environment_id) WHERE environment_id IS NOT NULL;

CREATE TABLE dns_zones (
  name TEXT PRIMARY KEY,
  visibility TEXT NOT NULL CHECK (visibility IN ('internal', 'external', 'edge')),
  backend_ref TEXT NOT NULL,
  ttl INTEGER NOT NULL CHECK (ttl > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_dns_zones_backend_ref ON dns_zones(backend_ref);
CREATE INDEX idx_dns_zones_visibility ON dns_zones(visibility);

CREATE TABLE dns_record_overrides (
  id UUID PRIMARY KEY,
  zone_name TEXT NOT NULL REFERENCES dns_zones(name) ON DELETE CASCADE,
  record_name TEXT NOT NULL,
  record_type TEXT NOT NULL CHECK (record_type IN ('A', 'AAAA', 'CNAME', 'SRV')),
  value TEXT NOT NULL,
  ttl INTEGER NOT NULL CHECK (ttl > 0),
  reason TEXT NOT NULL,
  operator_pubkey TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ
);

CREATE INDEX idx_dns_record_overrides_zone_name ON dns_record_overrides(zone_name);
CREATE INDEX idx_dns_record_overrides_expires_at ON dns_record_overrides(expires_at) WHERE expires_at IS NOT NULL;
