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

2. Ensure your signer is connected:
   ```bash
   bahia auth status
   ```

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
1. Check if approval is required:
   ```bash
   bahia deployments get intent-123
   ```

2. Verify policies by publishing a signed `PolicyEvaluate` event for the artifact and environment, or use a UI flow backed by the Nostr control plane.

3. Check authorized approvers are available.

4. Review environment settings for approval requirements.

### Deployment Failed

**Symptoms:**
- Run shows "failed" status
- Error in deployment logs

**Solutions:**
1. Check run logs:
   ```bash
   bahia deployments logs run-456
   ```

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

### Encrypted Operations Failing

**Symptoms:**
- "Encryption failed" errors
- Encrypted requests not working

**Solutions:**
1. Verify signer supports NIP-44:
   ```javascript
   console.log(window.nostr.nip44)
   ```

2. Check encrypted request relays are configured.

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
