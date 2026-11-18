# Najin — Coordinator Service Owner

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Mission
Implement the central Go coordinator: serve browser HTTP API + SSE, manage player registry and reachability, translate REST actions to NATS request/reply, and consume room state events. Do not own or modify player queue logic.

## Expected areas
`cmd/coordinator/`, `internal/coordinator/`. Read shared models in `internal/protocol/` when João adds them; request changes through João rather than redefining wire types.

## NATS responsibilities
- Connect to Core NATS, subscribe to `roomplay.rooms.register`, `roomplay.rooms.heartbeat`, and `roomplay.rooms.*.state`.
- Registration is request/reply; validate safe room IDs, unique online instance ID, re-registration of same instance; do not pretend registration token solves two simultaneous live command subscribers by itself.
- Registry fields: room ID, display name, instance ID, registration token, last-seen local receive time, online/offline flag and cached snapshot with stale indicator.
- Heartbeats every ~2s from players; mark offline after 6s missed; reject mismatched/old tokens and instance IDs. Use process-local monotonic time for timeout checks.
- Expose subject `roomplay.rooms.{id}.command` through NATS request/reply, preserving incoming request IDs; `get_state` for authoritative read.
- Use bounded timeouts; distinguish room not found, room offline, no responders, broker unavailable and command timeout (outcome unknown).
- On coordinator restart, allow periodic player registration renewal to repopulate registry; query fresh state on registration and resync.

## HTTP endpoints to implement
Implement exactly the public API in `docs/API.md`:
`GET /api/v1/rooms`, `GET /api/v1/rooms/{id}`,
`POST /api/v1/rooms/{id}/commands`,
`GET|POST|DELETE /api/v1/rooms/{id}/queue`,
`DELETE /api/v1/rooms/{id}/queue/{itemId}`,
`GET /api/v1/tracks`, `GET /api/v1/events`.
Map queue mutations to `queue.add`, `queue.remove`, `queue.clear` on the same NATS command subject; do **not** alter player state locally. Forward GET tracks from library or use a read-only catalog client; do not proxy audio bytes.

## SSE
One `/api/v1/events` stream per browser, named `room.state` and `room.availability`. Broadcast events from NATS and offline detector; keepalive comments, bounded client buffers, cleanup on disconnect. Reconnection uses REST snapshots, no guarantee of replay.

## Suggested PR slices
1. Coordinator server startup + in-memory concurrency-safe registry + unit tests.
2. NATS registration/heartbeat/state handlers + expiry tests with injectable clock.
3. NATS command request proxy + error mapping and timeout tests.
4. HTTP router + `httptest` tests for each route.
5. SSE broadcasting and recovery tests.

## Acceptance criteria
- Three different rooms register and appear in REST; duplicate *online* room ID rejected.
- Only matching instance/registration-token heartbeats/events update registry.
- Invalid routes/JSON return standardized error shape and HTTP status codes.
- Commands reach correct player once; no blind retries and no coordinator-owned queue mutation.
- One room timeout does not block independent room requests.
- SSE delivers room status changes and closes cleanly when client disconnects.
- `go test -race ./...` shows no coordinator registry/broadcast race.
