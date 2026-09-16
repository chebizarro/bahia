# Verification report

Feature: `BAHIA_WEB_UPSTREAM_RESILIENCE`

## Results

- Repository `web/Dockerfile` build passed and produced
  `sha256:1b9e3954e2c373acf18c917b88ecad2528f0b5d5a44b5b0e2e9a1a5b0b9b0487`.
- An isolated container with required public bootstrap environment and no
  backend/relay containers reached `healthy`.
- `GET /` returned HTTP 200.
- `GET /api/health` returned HTTP 502, and nginx remained healthy.
- `nginx -t` passed for the resulting image.

## Production-path assessment

Both proxy locations now use Docker's embedded resolver with variable upstream
names. Service discovery therefore occurs when a request reaches the proxy
location rather than while nginx loads its configuration. Static content and
the container healthcheck no longer depend on control-plane backend presence.
