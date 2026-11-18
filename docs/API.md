# RoomPlay — API & Messaging Contract
Status: Proposed implementation baseline • API version: v1

## Conventions
- UTF-8 JSON; NATS messages are JSON payloads (audio never goes through NATS).
- IDs: `room_id` must be a non-empty safe subject token such as `bedroom` (use lowercase letters, digits, hyphens, underscore; no dots, spaces, wildcard tokens). `instance_id` is new for each player start. `request_id` is unique per logical mutation and stable across retries.
- `position_ms` and `duration_ms` are nonnegative integers; all state arrays are emitted as `[]`, not `null`.
- `version` increments on logical state mutations; use `(instance_id,version)` to reject stale state events. No event replay guarantees with Core NATS.
- Distinct outer envelopes for NATS commands and HTTP resources, but one shared error object.

## NATS subject table
| Subject | Pattern | Publisher -> Consumer |
| --- | --- | --- |
| `roomplay.rooms.register` | request/reply | player -> coordinator |
| `roomplay.rooms.heartbeat` | pub/sub | player -> coordinator |
| `roomplay.rooms.{id}.command` | request/reply | coordinator -> one player |
| `roomplay.rooms.{id}.get_state` | request/reply | coordinator -> one player |
| `roomplay.rooms.{id}.state` | pub/sub | player -> coordinator |
Only live room IDs registered with coordinator may be used to route commands. Do not use queue groups for addressed room commands.

## Shared structures (wire format)
```json
{
  "track": {"id":"track-001","title":"Demo","artist":"Artist","duration_ms":210000,"url":"http://localhost:8090/audio/track-001.mp3"},
  "queue_item": {"id":"item-001","track_id":"track-001"},
  "playback_state": {
    "status":"playing",
    "current":{"id":"item-001","track_id":"track-001"},
    "position_ms":45000,
    "history":[],
    "queue":[{"id":"item-002","track_id":"track-002"}],
    "version":3
  }
}
```
`current` is JSON `null` if none; stopped may retain a current item. State events carry room_id and instance_id separately.

## Typed command request (NATS)
```json
{
  "request_id":"cmd-123",
  "type":"playback.play",
  "payload":{"track_id":"track-001"}
}
```
Always provide `payload` as an object (`{}` if empty). Reject missing or invalid type-specific fields; unknown fields may be rejected to prevent silent misuse. Coordinator forwards request ID unchanged, and browser uses same ID on intentional retry.

| Type | Required payload | Effect |
| --- | --- | --- |
| `playback.play` | `{"track_id":"..."}` | Select/play track from 0, preserve upcoming |
| `playback.pause` | `{}` | Freeze position |
| `playback.resume` | `{}` | Resume paused position |
| `playback.stop` | `{}` | Stop; keep current/history/queue; position 0 |
| `playback.next` | `{}` | Advance queue, or stop if empty |
| `playback.previous` | `{}` | Pop history to current, push displaced current to queue front |
| `queue.add` | `{"track_id":"..."}` | Append new queue occurrence |
| `queue.remove` | `{"item_id":"..."}` | Remove occurrence from upcoming |
| `queue.clear` | `{}` | Clear upcoming only |

When Next/Previous is issued while paused, the navigated track immediately plays. Previous with no history restarts current; when nothing selected it is a no-op. Next with no upcoming stops/clears current and retains history. Pause while paused, resume while playing, stop while stopped are no-ops. Pause/resume while stopped -> INVALID_STATE. Every changed-state command increments version once; no-op does not increment.

## Command reply (NATS)
Success:
```json
{"request_id":"cmd-123","success":true,"state":{"status":"playing","current":{"id":"item-009","track_id":"track-001"},"position_ms":0,"history":[],"queue":[],"version":4},"error":null}
```
Application failure:
```json
{"request_id":"cmd-123","success":false,"state":null,"error":{"code":"TRACK_NOT_FOUND","message":"Unknown track."}}
```
Cache original application responses keyed by request ID; identical duplicate returns cached response (even if now stale); reused ID with changed command -> conflict. No transparent retry of timed-out mutation with a fresh ID.

## Registration request/reply
`roomplay.rooms.register` request:
```json
{"room_id":"bedroom","name":"Bedroom","instance_id":"f7d4a0"}
```
Reply:
```json
{"success":true,"room_id":"bedroom","registration_token":"lease-456","heartbeat_interval_ms":2000,"error":null}
```
Active different instance for same room -> DUPLICATE_ROOM_ID; same-instance renewal returns/refreshes token. Offline/expired previous instance may be replaced. Token is ephemeral; no durable identity guarantee. Coordinator should not command duplicate live subscribers for the same subject.

Heartbeat publication on `roomplay.rooms.heartbeat`:
```json
{"room_id":"bedroom","instance_id":"f7d4a0","registration_token":"lease-456"}
```
Coordinator records its local receive instant; don't trust sender wall clock for timeout. Ignore heartbeats with mismatched registration token/instance. Heartbeat every 2s; suspect offline at 6s. Periodic re-registration (e.g. 4s) permits coordinator restart recovery.

## Read state and state change event
Request to `roomplay.rooms.{id}.get_state`: `{"request_id":"read-123"}`.
Reply: `{"request_id":"read-123","success":true,"state":<PlaybackState>,"error":null}` (same format as command reply).
Publication to `roomplay.rooms.{id}.state`:
```json
{"room_id":"bedroom","instance_id":"f7d4a0","registration_token":"lease-456","state":{"status":"playing","current":null,"position_ms":0,"history":[],"queue":[],"version":1}}
```
Coordinator accepts event only from current registration, and only if its version is newer than the last cached version for that instance. Explicit authoritative state query on recovery refreshes cache. A state event should be emitted on meaningful state changes, including natural track completion.

## HTTP public API
| Method | URL | Success | Translation |
| --- | --- | --- | --- |
| GET | `/api/v1/rooms` | 200 array of room summaries | Registry and cached state |
| GET | `/api/v1/rooms/{id}` | 200 room detail | NATS get_state for online room; include availability |
| POST | `/api/v1/rooms/{id}/commands` | 200 `{"request_id":"...","state":<PlaybackState>}` | NATS typed command (playback.* only) |
| GET | `/api/v1/rooms/{id}/queue` | 200 `{"room_id":"...","queue":[...]}` | NATS get_state |
| POST | `/api/v1/rooms/{id}/queue` | 200 `{"request_id":"...","state":<PlaybackState>}` | `queue.add` |
| DELETE | `/api/v1/rooms/{id}/queue/{itemId}` | 200 `{"request_id":"...","state":<PlaybackState>}` | `queue.remove` |
| DELETE | `/api/v1/rooms/{id}/queue` | 200 `{"request_id":"...","state":<PlaybackState>}` | `queue.clear` |
| GET | `/api/v1/tracks` | 200 `{"tracks":[<Track>,...]}` | Library catalog |
| GET | `/api/v1/events` | 200 text/event-stream | SSE state/availability events |
All POST/DELETE mutations require a `request_id` sent as `X-Request-ID` header; the unified commands endpoint may instead provide `request_id` in body. If both are sent they must match. For queue POST body `{"track_id":"track-001"}`; queue DELETE has no JSON body. Coordinator maps HTTP requests to typed envelope with same ID, never independently mutates player state. Avoid REST reads silently showing stale data as current; return availability/staleness explicitly.

Example:
```http
POST /api/v1/rooms/bedroom/commands
Content-Type: application/json

{"request_id":"cmd-123","type":"playback.play","payload":{"track_id":"track-001"}}
```

Room listing example:
```json
{"rooms":[{"id":"bedroom","name":"Bedroom","instance_id":"f7d4a0","availability":"online","last_seen":"2026-11-18T16:00:00Z","state":{"status":"playing","current":null,"position_ms":0,"history":[],"queue":[],"version":1},"state_stale":false}]}
```
The above is a shape example (actual playing state must have a non-null current); implementations/tests should enforce that invariant and populate valid snapshots.

## SSE
Single browser EventSource at `GET /api/v1/events`, `Content-Type: text/event-stream`. Named events:
```text
event: room.state
data: {"room_id":"bedroom","instance_id":"f7d4a0","state":{"status":"paused","current":{"id":"item-1","track_id":"track-1"},"position_ms":1000,"history":[],"queue":[],"version":6}}

event: room.availability
data: {"room_id":"bedroom","availability":"offline"}
```
Send keepalive comment periodically. Clean up disconnected clients. Events are notifications, not durable replay; browser loads REST snapshot on initial connection and after reconnect, and ignores stale (instance_id,version) updates.

## Library HTTP
`GET /tracks` -> `{"tracks":[<Track>,...]}`; `GET /audio/{safe-file-name}` -> binary MP3 with correct content-type. Catalog must include track IDs, human-readable metadata, duration, reachable audio URLs, and licensing/source attribution in metadata or accompanying manifest. Whitelist catalog entries: reject arbitrary filenames/traversal. Use licensed reusable demo tracks only.

## Error model
```json
{"code":"INVALID_STATE","message":"Cannot resume while stopped."}
```
Application errors (from player): INVALID_COMMAND (400), INVALID_PAYLOAD (400), TRACK_NOT_FOUND (404), QUEUE_ITEM_NOT_FOUND (404), INVALID_STATE (409), DUPLICATE_REQUEST_CONFLICT (409), AUDIO_ERROR (500), INTERNAL_ERROR (500).
Coordinator/infrastructure: ROOM_NOT_FOUND (404), ROOM_OFFLINE (503), NO_RESPONDERS (503), BROKER_UNAVAILABLE (503), COMMAND_TIMEOUT (504), SERVICE_UNAVAILABLE (503), DUPLICATE_ROOM_ID (409 for registration).
HTTP error envelope:
```json
{"request_id":"cmd-123","error":{"code":"ROOM_OFFLINE","message":"Bedroom is unavailable."}}
```
NATS application error remains in command reply with `success:false`, `state:null`. HTTP response uses status + HTTP error envelope. Timeout means **outcome unknown**, not proven failure. Browser must refresh authoritative state; do not auto-resubmit with a new request ID. Unexpected dependency errors should not leak internal paths or stack traces.

## Validation and safety
Bound JSON body sizes, reject unknown commands/fields, enforce room ID grammar, validate nonempty track IDs and request IDs, use finite timeouts. New player must establish subscriptions before announcing online. Never infer online solely from stale SSE information.
