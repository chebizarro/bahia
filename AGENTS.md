# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

⸻

## 🧭 Nostr Protocol Guardrails for Agents

### Purpose

This repository is Nostr-native. All inter-service communication must follow event-driven pub/sub semantics, not polling or request/response patterns.

Agents MUST follow these rules when implementing, modifying, or reviewing code.

Violations are protocol bugs that break event-driven semantics.

---

### Core Mental Model

* Nostr is an event stream, not a request/response API.
* You subscribe (REQ) and react to events.
* Do not use timers to wait for events - use subscriptions.
* The relay tells you what's happening via:
    * EVENT
    * OK
    * EOSE
    * CLOSED
    * AUTH (if required)

👉 If your code is "waiting and checking" instead of "subscribing and reacting", it is wrong.

---

### 🌳 Quick Decision Tree

**I need to know when something happens:**
→ Open subscription with filter + on_event callback

**I need to process historical data:**
→ Filter with since/until, wait for EOSE, then continue with realtime

**I need to confirm my event was accepted:**
→ Publish, check OK response (both accepted flag AND message), handle rejection

**I need to retry after failure:**
→ Use exponential backoff on reconnect, NOT polling loops

**I need to route work between agents:**
→ Use kinds + tags (#agent, #t, #p), subscribe with scoped filters

---

### 🚫 Forbidden Patterns (Code Smells)

#### 1. Polling for Events

Do NOT:

* use sleep, setTimeout, setInterval, retry loops to check for messages
* repeatedly open short-lived subscriptions to "peek"
* simulate inbox polling

Bad (Python):

```python
while not message_received:
    await asyncio.sleep(1)
```

Bad (JavaScript):

```javascript
setInterval(() => {
  checkForNewMessages();
}, 1000);
```

Correct:

* open a subscription
* handle events via callback

```python
await subscribe_filter(
    filters=[{"kinds": [1234], "#p": [agent_pubkey]}],
    on_event=handle_message,
)
```

---

#### 2. Timeout-Based Completion

Do NOT:

* assume "no events after X ms = done"
* close subscriptions after arbitrary delays
* wait N seconds "for relay response"

Bad (JavaScript):

```javascript
await new Promise(resolve => setTimeout(resolve, 5000));
// assume we got everything
```

Correct:

* use EOSE to mark completion of historical/stored events (relay has sent all matching events it had at subscription time)
* use application-level completion signals (e.g., kind 7001 "task complete" event)
* keep subscriptions open for realtime flows

```python
def on_eose(sub_id):
    print(f"Historical events complete for {sub_id}")
    # Now in realtime mode
```

---

#### 3. Ignoring Relay Responses

Do NOT ignore:

* OK (especially OK false)
* CLOSED (with reason)
* AUTH challenges

Bad (Python):

```python
await relay.send(event)  # assumes success
```

Correct:

* verify OK response structure: `["OK", <event_id>, <accepted:bool>, <message>]`
* check BOTH the accepted flag AND the message
* handle rejection reasons (rate limit, auth required, invalid event)
* respond to AUTH challenges (NIP-42)

```python
ok_msg = await relay.publish_and_wait_ok(event)
if not ok_msg[2]:  # accepted = false
    print(f"Rejected: {ok_msg[3]}")
    # Handle rejection (backoff, fix event, request auth, etc.)
```

Reference: [NIP-01 OK message](https://github.com/nostr-protocol/nips/blob/master/01.md), [NIP-42 AUTH](https://github.com/nostr-protocol/nips/blob/master/42.md)

---

#### 4. Sleep-Based Backfill

Do NOT:

* "wait for history" using delays
* assume first batch = complete

Bad (Python):

```python
# Subscribe
await asyncio.sleep(2)  # "wait for history"
# Process events
```

Correct:

* use since, until, limit in filters
* wait for EOSE to mark catch-up complete
* transition to realtime mode after EOSE

```python
backfill_complete = False

def on_eose(sub_id):
    global backfill_complete
    backfill_complete = True
    print("Backfill complete, now processing realtime")
```

---

#### 5. Weak or Missing Filters

Do NOT:

* subscribe broadly and filter locally
* omit domain tags
* download entire relay history

Bad (all events of a kind):

```python
{"kinds": [1234]}  # downloads everything
```

Correct:

* scope filters using:
    * kinds
    * #p (pubkey tags - target agent/user)
    * #agent (custom agent routing tag)
    * #t (task/topic id)
    * #d (for parameterized replaceable events, NIP-33)
    * since (don't fetch ancient history unless needed)

Good (scoped filter):

```python
{
    "kinds": [1234],
    "#p": ["<agent-pubkey>"],
    "#t": ["<task-id>"],
    "since": int(time.time()) - 3600  # last hour
}
```

Reference: [NIP-01 filters](https://github.com/nostr-protocol/nips/blob/master/01.md)

---

#### 6. No Deduplication / Idempotency

Do NOT:

* process the same event multiple times
* assume single delivery
* ignore replaceable event semantics

Correct:

* dedupe by event.id (maintain seen set)
* use correlation keys like #t for task tracking
* design handlers to be idempotent
* for replaceable events (kind 0, 3, 10000-19999): keep only latest by pubkey
* for parameterized replaceable (kind 30000-39999): keep only latest by pubkey + d-tag

```python
seen_events = set()

def on_event(event):
    if event['id'] in seen_events:
        return
    seen_events.add(event['id'])
    process_event(event)
```

Reference: [NIP-01 replaceable events](https://github.com/nostr-protocol/nips/blob/master/01.md), [NIP-33 parameterized replaceable](https://github.com/nostr-protocol/nips/blob/master/33.md)

---

#### 7. Recreating Queues or RPC

Do NOT:

* build Redis-style queues or inbox systems
* wrap Nostr in request/response abstractions
* treat relays like HTTP endpoints
* create "RPC-over-Nostr" layers

Bad (queue abstraction):

```python
class NostrQueue:
    def push(self, message): ...
    def pop(self): ...  # polls!
```

Correct:

* model workflows using:
    * event kinds (kind 1234 = task request, kind 1235 = task result)
    * tags (#t for correlation, #p for routing)
    * subscriptions (subscribers react to kind 1234, publish kind 1235)

```python
# Agent subscribes to requests
await subscribe_filter(
    filters=[{"kinds": [1234], "#agent": [my_agent_id]}],
    on_event=handle_task_request,
)

# Agent publishes results
result_event = {
    "kind": 1235,
    "tags": [["t", original_task_id], ["p", requestor_pubkey]],
    "content": json.dumps(result),
}
```

---

#### 8. Blind Relay Assumptions

Do NOT:

* assume all relays behave the same
* ignore relay capabilities
* assume all relays support all NIPs
* skip relay metadata checks

Correct:

* query NIP-11 relay information document (GET https://relay.url/ with Accept: application/nostr+json)
* support NIP-42 AUTH if required
* implement reconnect + exponential backoff
* handle relay-specific errors gracefully
* track per-relay connection health

```python
# Check relay capabilities
async def get_relay_info(relay_url):
    response = await http_get(relay_url, headers={"Accept": "application/nostr+json"})
    info = json.loads(response)
    return info.get("supported_nips", [])

# Reconnect with backoff
backoff_seconds = 1
while not connected:
    try:
        await relay.connect()
    except:
        await asyncio.sleep(backoff_seconds)
        backoff_seconds = min(backoff_seconds * 2, 60)
```

Reference: [NIP-11 relay info](https://github.com/nostr-protocol/nips/blob/master/11.md), [NIP-42 AUTH](https://github.com/nostr-protocol/nips/blob/master/42.md)

---

#### 9. Misusing Timers

Timers are ONLY valid for:

* reconnect backoff
* health checks / heartbeats
* autoscaling logic
* rate limiting outbound publishes (if needed)

Timers are NOT valid for:

* message delivery
* event completion detection
* waiting for relay responses

---

#### 10. Sleep-Based Tests

Do NOT:

* use sleeps to "wait for events" in tests

Bad (test):

```python
await relay.subscribe(filters)
await asyncio.sleep(0.5)  # "wait for events"
assert len(received_events) > 0
```

Correct:

* mock EVENT, EOSE, OK, CLOSED messages
* trigger deterministic callbacks
* verify handlers called correctly
* no sleeps waiting for async behavior

```python
# Mock relay sends EVENT
mock_relay.inject_event(test_event)
# Verify handler called
assert handler.call_count == 1
```

---

## ✅ Required Patterns

### Event-Driven Subscription

```python
close = await subscribe_filter(
    sub_id="example",
    filters=[{"kinds": [1234], "#p": [pubkey]}],
    on_event=handle_event,
    on_eose=handle_eose,
    on_closed=handle_closed,
)
```

---

### EOSE-Aware Flow

```python
# Phase 1: backfill historical events
# Relay sends stored events...
# Relay sends EOSE

def on_eose(sub_id):
    print("Backfill complete")
    # Phase 2: realtime mode
    # Continue processing new events as they arrive
```

---

### Publish with Verification

```python
event_id = await publish_event(event)
ok_response = await wait_for_ok(event_id, timeout=5)

# Check OK: ["OK", <event_id>, <accepted>, <message>]
if not ok_response[2]:
    handle_rejection(ok_response[3])
```

---

### Reconnect Strategy

* reconnect on disconnect
* re-issue REQ subscriptions
* use exponential backoff
* dedupe events by ID (may receive duplicates after reconnect)

```python
async def maintain_connection():
    backoff = 1
    while True:
        try:
            await relay.connect()
            await reissue_subscriptions()
            backoff = 1  # reset on success
        except ConnectionError:
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 60)
```

---

### Subscription Management

* Close subscriptions when no longer needed (use CLOSE message)
* Many relays limit concurrent subscriptions per connection
* On shutdown, cleanly close all subscriptions before disconnecting
* Track active subscription IDs to avoid duplicates

```python
active_subs = {}

async def subscribe(sub_id, filters):
    if sub_id in active_subs:
        await close_subscription(sub_id)
    active_subs[sub_id] = await relay.subscribe(sub_id, filters)

async def cleanup():
    for sub_id in list(active_subs.keys()):
        await relay.close(sub_id)
```

---

### Batch Publishing

When publishing multiple events:

* Publish in parallel when possible
* Collect ALL OK responses
* Handle partial failures (some accepted, some rejected)
* Do NOT assume batch atomicity

```python
events = [event1, event2, event3]
results = await asyncio.gather(
    *[publish_and_wait_ok(e) for e in events],
    return_exceptions=True
)

for i, result in enumerate(results):
    if isinstance(result, Exception) or not result[2]:
        handle_failed_publish(events[i], result)
```

---

## 🔐 Event Validation

Agents MUST verify inbound events:

* Event ID matches SHA256 hash of serialized event (NIP-01 format)
* Signature is cryptographically valid (schnorr signature over event ID)
* Pubkey in signature matches event.pubkey
* Timestamp is reasonable (not wildly future/past - reject if >10min future or >1yr past, adjust as needed)
* Required tags are present for the kind
* Content is well-formed (valid JSON if expected)

Never trust relay-provided events without validation.

```python
def validate_event(event):
    # 1. Verify ID
    serialized = serialize_event_for_id(event)
    computed_id = hashlib.sha256(serialized.encode()).hexdigest()
    if computed_id != event['id']:
        raise InvalidEvent("ID mismatch")
    
    # 2. Verify signature
    if not verify_schnorr(event['pubkey'], event['id'], event['sig']):
        raise InvalidEvent("Invalid signature")
    
    # 3. Check timestamp
    now = int(time.time())
    if event['created_at'] > now + 600:  # 10min future
        raise InvalidEvent("Timestamp too far in future")
    
    # 4. Validate kind-specific requirements
    validate_kind_requirements(event)
```

Reference: [NIP-01 event validation](https://github.com/nostr-protocol/nips/blob/master/01.md)

---

## 🔌 Relay Selection & Management

* Use NIP-65 (relay list metadata, kind 10002) when available for discovering user's preferred relays
* Support multiple relays for redundancy (publish to N, subscribe from M)
* Handle relay-specific AUTH requirements (NIP-42)
* Implement per-relay connection health tracking
* Don't assume all relays support all NIPs (check NIP-11 support list)
* When routing work, include relay hints in tags where appropriate

```python
# Read user's relay list (NIP-65)
user_relays = await fetch_kind_10002(user_pubkey)

# Connect to multiple relays
for relay_url in user_relays['read']:
    await connect_relay(relay_url)

# Publish to write relays
for relay_url in user_relays['write']:
    await publish_to_relay(relay_url, event)
```

Reference: [NIP-65 relay list](https://github.com/nostr-protocol/nips/blob/master/65.md)

---

## 🔑 AUTH Flow (NIP-42)

When a relay requires authentication:

1. Relay sends: `["AUTH", <challenge>]`
2. Agent creates kind 22242 event with challenge in `challenge` tag
3. Agent signs and publishes auth event
4. Relay verifies and grants access

Do NOT:

* Ignore AUTH challenges
* Assume all relays require AUTH
* Cache AUTH events across relays (challenge is relay-specific)

```python
def on_auth_challenge(challenge):
    auth_event = {
        "kind": 22242,
        "tags": [
            ["relay", relay_url],
            ["challenge", challenge]
        ],
        "content": "",
        "created_at": int(time.time()),
    }
    signed_event = sign_event(auth_event)
    await relay.send(["AUTH", signed_event])
```

Reference: [NIP-42 authentication](https://github.com/nostr-protocol/nips/blob/master/42.md)

---

## 🔍 PR / Code Review Checklist

Agents MUST verify:

* ✅ No polling loops for message delivery
* ✅ No timeout-based completion logic
* ✅ EOSE is used correctly for backfill/realtime transition
* ✅ OK responses are handled (both accepted flag AND message)
* ✅ CLOSED reasons are logged and handled
* ✅ AUTH flow supported if relay requires it (NIP-42)
* ✅ Filters are properly scoped (kinds, tags, since/until)
* ✅ Deduplication is implemented (event ID tracking)
* ✅ Replaceable event semantics respected (NIP-01, NIP-33)
* ✅ Event validation performed (ID, signature, timestamp)
* ✅ No queue/RPC abstractions replacing Nostr semantics
* ✅ Tests are event-driven (mock EVENT/EOSE/OK, no sleeps)
* ✅ Subscription cleanup on shutdown
* ✅ Reconnect logic uses exponential backoff
* ✅ Relay capabilities checked (NIP-11) before assuming features

---

## 🧠 Heuristic Rule

If you wrote:

* a sleep
* a timeout
* a retry loop waiting for data

Ask yourself:

**"Could this be replaced with a subscription + event handler?"**

If yes → you are violating the architecture.

The answer is almost always yes.

---

## 🧩 Architecture Reminder

* **Communication** = Nostr events over relays
* **Routing** = kinds + tags (#p, #agent, #t, #d)
* **State** = derived from event streams
* **Reliability** = EOSE + idempotency + backoff + event validation

There are:

* ❌ no queues
* ❌ no polling APIs
* ❌ no request/response workflows
* ❌ no "wait and check" patterns

Only:

* ✅ subscriptions (REQ)
* ✅ event handlers (on_event, on_eose, on_closed)
* ✅ publishing (EVENT)
* ✅ verification (OK)

---

## ⚠️ Enforcement

Agents must:

* Flag violations during implementation
* Correct violations during refactoring
* Block PRs that introduce protocol smells
* Treat these rules as architectural constraints, not suggestions

These rules are non-optional.

Violating them breaks the event-driven contract and creates systems that will fail under load, reconnect scenarios, and multi-relay deployments.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->
