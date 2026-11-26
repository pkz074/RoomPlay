# RoomPlay — System Architecture
Status: Proposed implementation baseline • Owner: João (pkz074)

## Purpose and scope
RoomPlay is a distributed multi-room music player built in Go. Different room players run as independent processes, own their playback state, and can play different MP3 tracks. This design implements the submitted project proposal's distributed communication, concurrency, node registration, availability monitoring, and failure isolation. We have chosen a hybrid event-driven backend (NATS) instead of the proposal's original direct player-to-coordinator HTTP control; document this implementation change in the final report.

### Logical topology
```text
Browser (HTML/CSS/JS)
  | REST commands, REST snapshots, SSE
  v
Coordinator (Go HTTP + NATS)
  | NATS request/reply (commands, queries, registration)
  | NATS pub/sub (state updates, heartbeats)
  v
NATS Core broker <----> independent Go room players (bedroom, kitchen, livingroom)
                                 |
                                 | HTTP GET audio file / catalog
                                 v
                         Music Library (Go HTTP, MP3 assets)
```

**Control plane:** browser and coordinator. **Execution plane:** individual players. **Media delivery:** library to players, never proxied through NATS or coordinator.

## Ownership and team
- João: architecture, protocol contracts, integration/failure/concurrency tests, CI, reviews.
- Najin: coordinator, registry, NATS request routing, SSE.
- Kyle: player process, serialized command processor, playback, history and queue, registration client.
- Kaira: browser UI, REST/SSE consumption, room controls.
- Colton: music catalog, licensed MP3 delivery, library and component tests; collaborate with Kaira.
- All component authors write their own unit tests.

## Data ownership
- Coordinator: registry of room IDs, names, active instance IDs, last heartbeat, availability; read-only cached player snapshots.
- Player: current track occurrence, paused/playing/stopped state, position, history, upcoming queue, revision, deduplication cache.
- Library: catalog entries and audio bytes.
- Browser: selected room and presentation-only state.
No component besides a player mutates that room's playback state. Coordinator must not treat cached state as authoritative.

## Deployment and processes
Start one NATS server, one library server, one coordinator, and 2–3 player processes. Separate processes on one laptop are acceptable; separate machines are an optional deployment test. URLs, ports, broker address, room ID, and room name must be configurable (env or flags). Example local defaults: NATS 127.0.0.1:4222; coordinator :8080; library :8090. Players need no inbound HTTP port because NATS carries their control requests. Avoid hardcoded localhost library URLs for cross-machine use. The browser talks only to coordinator.

## Messaging patterns
- Core NATS **request/reply** for one-room commands and authoritative state reads, and for registration.
- Core NATS **pub/sub** for heartbeats and state-change notifications.
- HTTP REST for browser reads/commands, SSE for browser live notifications, HTTP for MP3 delivery.
- No JetStream or persisted command backlog in MVP. Interactive commands must not execute unexpectedly after a room returns online.
- **Do not use NATS queue groups to share a room's command subject**: there should be exactly one effective command handler per room ID.
- Standard NATS subjects and schemas: [API.md](API.md).

## Room startup and membership
1. Player connects to broker and installs command/query subscriptions; flushes subscriptions before registration.
2. Player sends NATS registration request with room_id, name, and a per-process random instance_id.
3. Coordinator rejects another instance using an online room_id. It may replace an explicitly expired/offline registration with a new instance.
4. Coordinator grants a registration token/lease identifier, recorded by the player and included in heartbeats/state publications. P4 also includes the target instance/token in every routed command/query and the responding instance/token in replies. NATS still delivers to every ordinary subscriber; cooperating players with mismatched identity remain silent and do not execute the command. Rejected duplicate players exit and unsubscribe. Reply checks reject a changed registration or unexpected responder. This is fencing within RoomPlay, not authentication against malicious publishers; upgrade the coordinator and players together.
5. Player sends heartbeat every 2 seconds; coordinator checks local monotonic elapsed time since last valid heartbeat and marks a room offline after 6 seconds (timeouts are suspicion, not proof of process crash).
6. Player periodically renews registration (e.g. every 4 seconds) even while healthy so a restarted coordinator can rebuild its in-memory registry. Registration renewal for the same instance is idempotent. If disconnected from NATS, reconnect, re-install subscriptions, then re-register and synchronize.
7. Coordinator re-queries a player for authoritative state on registration/recovery. Old instance IDs and nonincreasing revisions must not overwrite a newer cached snapshot.

## Playback state and navigation
Player owns a `PlaybackState`: status (stopped|playing|paused), `current` (nullable QueueItem), `position_ms`, history (oldest first), queue (next first), version (increases on logical mutations), and instance_id supplied with published snapshots. QueueItem has unique occurrence ID + track ID, including for directly selected tracks, so a song can occur multiple times.
- `playback.play(track_id)`: select new occurrence, push previous current to history, preserve upcoming, start at 0.
- `playback.pause`: freeze current position; repeat pause is no-op; stopped is INVALID_STATE.
- `playback.resume`: resume paused current; repeat while playing is no-op; stopped is INVALID_STATE.
- `playback.stop`: keep selected current/history and upcoming, reset position to 0, set stopped; stopped is no-op.
- `playback.next`: push current to history, consume first upcoming and start it from 0; if queue empty, move current to history, clear current and stop. If paused, navigation starts playing.
- `playback.previous`: if history exists, pop latest and prepend displaced current to upcoming; start history track from 0. If history empty and current exists, restart current at 0; if neither exists, no-op. If paused, navigation starts playing.
- `queue.add`: append occurrence; `queue.remove(item_id)`: remove only matching upcoming entry; `queue.clear`: only upcoming, never current or history.
- When a track ends naturally, advance with the same Next logic. Do not build playlists or room-session lifecycle.
- Empty-queue Next while stopped with retained current also moves it into history; test this explicitly. Stopped + current is valid.
- Stop and Clear are separate backend actions, placed in the UI secondary menu. Main UI shows Play/Pause/Resume + Next/Previous.

## Playback time
For simulation, player records a base elapsed position and a process-local monotonic start instant; query position = base + time.Since(start) while running. Pause snapshots elapsed position. Stop resets base to 0. Do not serialize/compare monotonic instants across processes. Real MP3 engine should report decoded/playback cursor position where available. Logical revision need not increment as time advances; snapshots may have newer `position_ms` with the same version.

## Command processing and deduplication
One serial mutation path per player (command loop or one lock around check+apply+cache) protects state and ensures concurrent same-ID requests cannot both execute. Every mutation includes a request ID. Retain a bounded in-memory cache (e.g. 1,000 entries with deterministic eviction); cache original success and application rejection along with canonical command type/payload. Duplicate identical ID+command returns original result; reuse with different payload -> DUPLICATE_REQUEST_CONFLICT. No exactly-once guarantee after eviction/restart. Coordinator must not blindly retry uncertain outcome with a new ID. Audio preparation failures should not partially mutate logical state.

## Failure model and consistency
- Single player failure: other players unaffected. Coordinator suspects offline after missing heartbeats and rejects commands for known offline rooms.
- Coordinator outage: players may continue already-started local playback; browser control unavailable.
- Broker outage: NATS control and heartbeats fail; local playback continues. Upon recovery players reconnect, resubscribe, re-register; coordinator resynchronizes.
- Lost NATS state event / SSE message: initial browser load and SSE reconnect use REST state snapshots. Coordinator caches are eventually consistent, and UI should indicate stale/offline information rather than invent current position.
- NATS timeout: outcome **unknown**, since mutation might have happened before the reply was lost. Never claim failure-to-execute. Distinguish no responders vs request timeout.
- This is not HA for coordinator/broker; acknowledge those single points of failure in report.

## Design invariants
- Only one active logical instance per room ID; no two live handlers for one command subject.
- Only player mutates queue/history/state; occurrences unique across current/history/queue.
- Success responses describe state immediately after command; duplicate responses may describe an older state.
- State version increases on logical state changes, not each tick.
- Error response contains no new authoritative state.
- All HTTP outbound requests use deadlines; subscription and SSE cleanup on cancellation.
- All external input (IDs, JSON, catalog paths) validated; music fetch is allowlisted by track ID.

## Deferred features
JetStream, durable playback state, replication/leader election, playlists, queue reordering, seeking, authentication for untrusted networks, multi-device audio synchronization and synchronized group playback are out of scope.
