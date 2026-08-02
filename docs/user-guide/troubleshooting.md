# Troubleshooting

Common issues and solutions when using Bahia.

## Connection Issues

### Cannot Connect to API Server

**Symptoms:**
- "Connection refused" errors
- Timeouts when accessing `http://localhost:8080`

**Solutions:**
1. Verify the server is running:
   ```bash
   docker compose ps
   # or
   ps aux | grep bahia
   ```

2. Check the server logs:
   ```bash
   docker compose logs bahia
   ```

3. Verify the port is correct:
   ```bash
   curl http://localhost:8080/health
   ```

4. Check firewall rules if accessing remotely.

### Cannot Connect to Relays

**Symptoms:**
- "WebSocket connection failed"
- Events not appearing
- Subscriptions timing out

**Solutions:**
1. Verify relay URLs in configuration:
   ```bash
   curl http://localhost:8080/.well-known/nostr.json
   ```

2. Test relay connectivity directly:
   ```bash
   websocat wss://relay.example.com
   ```

3. Check relay health in the discovery endpoint.

4. Ensure TLS certificates are valid for wss:// URLs.

## Authentication Issues

### "Unauthorized" Errors

**Symptoms:**
- 401 responses from API
- "Unauthorized pubkey" errors

**Solutions:**
1. Verify authentication is configured correctly:
   ```yaml
   auth:
     enabled: true
     bootstrap_owner_pubkeys:
       - "your-pubkey-hex"
   ```

2. Ensure the selected browser signer is connected and still returns the pubkey stored in the session. For CLI remote signing, confirm the bunker URI, relay pool, and persistent client key file are configured.

3. Check the pubkey is in the appropriate allowlist.

4. Verify NIP-98 token is being sent correctly.

### NIP-07 Not Working

**Symptoms:**
- "No Nostr extension found"
- Signing requests hang

**Solutions:**
1. Verify browser extension is installed (nos2x, Alby, etc.)
2. Check extension permissions for the site
3. Ensure `window.nostr` is available:
   ```javascript
   console.log(window.nostr)
   ```

### NIP-46 Connection Failed

**Symptoms:**
- "Bunker connection failed"
- Timeout connecting to remote signer

**Solutions:**
1. Verify bunker URI is correct
2. Check relay connectivity for the bunker
3. Ensure the bunker is online and accepting connections

## Deployment Issues

### Deployment Stuck in Pending

**Symptoms:**
- Intent stays in "pending" status
- No progress after creation

**Solutions:**
1. Check whether approval is required in the Deployments UI or with `bahia_get_intent` through MCP.

2. Verify policies by publishing a signed `PolicyEvaluate` event for the artifact and environment, or use a UI flow backed by the Nostr control plane.

3. Check authorized approvers are available.

4. Review environment settings for approval requirements.

### Deployment Failed

**Symptoms:**
- Run shows "failed" status
- Error in deployment logs

**Solutions:**
1. Check the run in the Deployments UI or call `bahia_get_run_logs` through MCP.

2. Verify artifact exists and is pullable:
   ```bash
   docker pull registry.example.com/image:tag
   ```

3. Check worker connectivity:
   ```bash
   bahia workers list
   ```

4. Verify runtime target is accessible.

### Drift Detected

**Symptoms:**
- State shows "drifted"
- Observed artifact differs from desired

**Solutions:**
1. Check what's actually running:
   ```bash
   bahia state list --service svc-123
   ```

2. Deploy to correct the drift by publishing a ContextVM `service/deploy` intent or using a UI flow backed by the Nostr control plane. Legacy `DeployRequest` custom kinds are startup migration inputs only.

3. Investigate why drift occurred (manual changes, crashes, etc.)

4. Consider enabling auto-remediation.

## Worker Issues

### Worker Offline

**Symptoms:**
- Worker shows "offline" status
- Deployments queue but don't execute

**Solutions:**
1. Check worker process is running
2. Verify relay connectivity from worker
3. Check worker logs for errors
4. Restart worker if necessary

### Worker Capability Missing

**Symptoms:**
- "No worker with capability X"
- Deployment waiting for worker

**Solutions:**
1. List workers and capabilities:
   ```bash
   bahia workers list
   ```

2. Ensure a worker has the required capability.

3. Add capability to worker configuration and restart.

## Database Issues

### Migration Failed

**Symptoms:**
- Server won't start
- "Migration error" in logs

**Solutions:**
1. Check database connectivity:
   ```bash
   psql $DATABASE_URL -c "SELECT 1"
   ```

2. Review migration logs for specific errors.

3. Ensure database user has necessary permissions.

4. Try running migrations manually:
   ```bash
   make migrate
   ```

### Data Not Persisting

**Symptoms:**
- State disappears after restart
- Events not found

**Solutions:**
1. Verify DATABASE_URL is set correctly.

2. Check PostgreSQL is persisting data:
   ```bash
   docker compose exec postgres psql -U postgres -c "SELECT count(*) FROM services"
   ```

3. Ensure volume mounts are correct in Docker Compose.

## Nostr Issues

### Events Not Publishing

**Symptoms:**
- Publish returns error
- Events don't appear on relay

**Solutions:**
1. Check relay OK response:
   - `accepted: true` — published successfully
   - `accepted: false` — check message for reason

2. Verify event signature is valid.

3. Check rate limits on relay.

4. Ensure relay accepts the event kind.

### Subscriptions Not Receiving Events

**Symptoms:**
- No events received
- EOSE never arrives

**Solutions:**
1. Verify the filter is scoped to canonical observables:
   ```json
   {"kinds": [30900, 30315, 4903], "authors": ["<bahia-service-pubkey>"], "#service": ["svc-123"]}
   ```

2. Check relay has the events (try different relay).

3. Ensure subscription is to correct relay.

4. Verify authors filter matches Bahia service pubkey.

### ContextVM / NIP-44 Operations Failing

**Symptoms:**
- "Encryption failed" errors
- ContextVM requests do not receive result events

**Solutions:**
1. Verify signer supports NIP-44:
   ```javascript
   console.log(window.nostr.nip44)
   ```

2. Check Bahia discovery advertises standard browser/bootstrap or ContextVM relays.

3. Verify correct service pubkey is used.

### Migration App Fails at Startup

**Symptoms:**
- Startup logs mention legacy-kind migration failure
- Canonical observables are missing after restart
- Relay backfill does not complete

**Solutions:**
1. Verify the Bahia service private key and Nostr publisher are configured for non-dry-run migration.
2. Check relay connectivity and require `EOSE` for legacy backfill before treating migration as complete.
3. Rerun startup after fixing configuration. The migration app is idempotent: it skips canonical outputs already tagged with `migrated-from=<legacy_event_id>` and preserves `legacy-kind` metadata.
4. Keep relay sidecar allowlists in place. The sidecar should route canonical outputs and migration publishes to configured allowlisted relays; do not re-enable legacy live subscribers as a workaround.

## Web UI Issues

### UI Not Loading

**Symptoms:**
- Blank page
- JavaScript errors

**Solutions:**
1. Check browser console for errors.

2. Verify static files are being served.

3. Check API connectivity from browser.

4. Clear browser cache and reload.

### State Not Updating

**Symptoms:**
- UI shows stale data
- Changes don't appear

**Solutions:**
1. Check WebSocket connection to relay.

2. Verify subscriptions are active.

3. Check browser console for errors.

4. Try refreshing the page.

## Performance Issues

### Slow API Responses

**Symptoms:**
- High latency on requests
- Timeouts on queries

**Solutions:**
1. Check database query performance.

2. Review PostgreSQL connection pool settings.

3. Enable query logging to identify slow queries.

4. Consider adding indexes for common queries.

### High Memory Usage

**Symptoms:**
- OOM errors
- Server crashes under load

**Solutions:**
1. Review connection pool sizes.

2. Check for memory leaks in logs.

3. Increase container memory limits.

4. Consider scaling horizontally.

## Security OSV Scanning Issues

### Scan Not Triggering After SBOM Import

**Symptoms:**
- SBOM imports successfully but no Security scan starts
- No Security `30315` status events appear

**Solutions:**
1. Verify Security is enabled in the relevant policy:
   ```json
   { "type": "security_osv_scan", "params": { "enabled": true } }
   ```

2. Confirm SBOM import published `30078` and `30004` events with relay OK acceptance.

3. Check Bahia service logs for Security SBOM subscription status and EOSE processing.

4. Verify relay connectivity and that the Security service can subscribe to `#domain=sbom` filters.

### Security Scan Failed

**Symptoms:**
- Scan status shows `failed` in `30315` events
- `4903` audit fact includes error details

**Solutions:**
1. Check the scan error field for the root cause:
   - **Payload hash mismatch** — SBOM payload in Blossom storage doesn't match the `30078` reference hash. Re-import or regenerate the SBOM.
   - **OSV unreachable** — Network issues reaching `api.osv.dev`; retries exhausted. Check outbound HTTP connectivity.
   - **Invalid request (400)** — Malformed PURL or invalid version combination rejected by OSV. Check SBOM package data quality.

2. For transient failures, request a rescan:
   ```json
   { "method": "security/rescan", "params": { "target_key_hash": "<hash>", "force": true } }
   ```

3. Review `4903` audit facts with `#domain=security` and `type=security-scan` filters.

### Breach Notifications Not Received

**Symptoms:**
- Policy breach expected but no notification dispatched
- Notification logs show no `security.policy_breached` events

**Solutions:**
1. Verify notification channel includes `security.policy_breached` in its event filter.

2. Confirm the breach is new or materially changed — unchanged recurring breaches are intentionally suppressed to avoid alert fatigue.

3. Check organization-scoped delivery logs in the Notifications UI or call `bahia_list_notifications` through MCP.

4. Verify breach was detected by checking `4903` audit facts with `type=security-policy-breach`.

### Policy Blocking Deployments Due to Missing Security Scan

**Symptoms:**
- Deployment blocked with policy violation
- No Security scan exists for the artifact

**Solutions:**
1. Check the `no_scan` policy parameter — if set to `"block"`, deployments will be blocked when no scan exists.

2. During initial rollout, consider using `"warn"` instead of `"block"`:
   ```json
   { "type": "security_osv_scan", "params": { "no_scan": "warn" } }
   ```

3. Trigger an explicit scan via `security/scan` for the artifact's SBOM target.

4. Check `security/schedules-list` for scan freshness state.

## Getting Help

### Logs

Collect logs for debugging:

```bash
# Docker Compose
docker compose logs bahia > bahia.log
docker compose logs postgres > postgres.log

# Systemd
journalctl -u bahia > bahia.log
```

### Debug Mode

Enable verbose logging:

```yaml
logging:
  level: debug
```

Or:
```bash
BAHIA_LOG_LEVEL=debug bahia-server
```

### Health Checks

```bash
# API health
curl http://localhost:8080/health

# Readiness
curl http://localhost:8080/ready

# Discovery
curl http://localhost:8080/.well-known/nostr.json
```

### Community

- GitHub Issues: Report bugs and feature requests
- Nostr: Connect with the community

## Related

- [Getting Started](getting-started.md) — Setup guide
- [Core Concepts](core-concepts.md) — Understanding Bahia
- [Nostr Integration](nostr-integration.md) — Event model
