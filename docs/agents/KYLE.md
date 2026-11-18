# Kyle — Room Player and Playback Engine Owner

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Mission
Implement one independent Go player process, reusable for many rooms via configuration. Player is the **only authority** for its room's current track, history, upcoming queue, position and logical revision. Support simulation and real MP3 playback through a small backend abstraction.

## Expected areas
`cmd/player/`, `internal/player/`. Consume `internal/protocol/` models; propose protocol changes to João instead of editing contracts.

## NATS and lifecycle
- Set up subscriptions for `roomplay.rooms.{id}.command` and `roomplay.rooms.{id}.get_state` before registration; flush before reporting ready.
- Request register on `roomplay.rooms.register`; include fresh per-start instance ID; retain granted registration token.
- Publish heartbeats every 2s and state-change events with room ID, instance ID and token.
- Renew registration periodically and restore subscriptions/re-register after broker/coordinator recovery. Handle duplicate-room rejection without accepting commands for an unregistered duplicate.
- Use Core NATS request/reply; command request always `{request_id,type,payload}` and command response always `{request_id,success,state,error}`.

## Command processor
Implement *one serialized mutation path*. Decode and validate typed payload, check bounded in-memory command-ID cache (incl. payload fingerprint), execute once, cache success or application error and reply. Conflict if same ID is reused for different content. A cached original response can represent old state; never mistake it for a new live snapshot. Do not promise dedup after restart/eviction.

Nine mutations (exact semantics in API.md): `playback.play`, `playback.pause`, `playback.resume`, `playback.stop`, `playback.next`, `playback.previous`, `queue.add`, `queue.remove`, `queue.clear`.
Critical cases:
- Play specified track immediately from 0, push old current to history, preserve upcoming.
- Stop keeps current/history/upcoming, resets to 0. Clear only upcoming.
- Next consumes upcoming; if empty, push current to history, clear current and stop.
- Previous pops history to current and prepends displaced current to upcoming; if history empty, restart current or no-op if no current.
- Next/Previous while paused **start playing** the navigated track immediately.
- `QueueItem` is an occurrence, not just a track ID; identical tracks can appear more than once.
- Natural track completion uses Next behavior.
- No-op should not change state version; logical mutation increments once; position progression alone doesn't.

## Audio engine
- Simulation backend uses process-local monotonic elapsed time (base position + `time.Since(start)` while running) and deterministic test clock/fake backend.
- Real backend downloads legal MP3 from library HTTP, validates track, prepares decoder and starts output when possible. Prefer actual engine position rather than wall time if supported.
- Coordinate state commit with audio preparation; invalid/unavailable MP3 should return AUDIO_ERROR without damaging current logical session where feasible.
- Avoid busy ticking goroutines and time.Sleep(1ms) counters. Do not serialize monotonic clock instants.
- Running player continues local playback when broker/coordinator disconnects.

## Suggested PR slices
1. Pure state machine, queue/history and fake clock with table-driven tests (no NATS).
2. Deduplication + serialized command processor + concurrent duplicate test.
3. NATS subscriptions, registration/heartbeat/reconnect, state event publishing.
4. Simulation engine and natural completion.
5. Real MP3 backend integration (optional second pass if hardware challenges).

## Acceptance criteria
- Comprehensive table-driven tests for every transition including pause/resume/stop and duplicate track occurrences.
- Previous from C to B then Next returns to C.
- Same request ID cannot execute twice concurrently within cache window.
- Player state survives coordinator outage while process is alive.
- Safe errors for malformed commands and invalid tracks, without partial mutations.
- Race detector passes and only this player owns its state.
