# RoomPlay — Test Plan
Owner: João (system integration and distributed-failure testing); component developers own unit tests.

## Strategy
1. Unit: pure queue/history/state transitions, registry and validation, catalog parsing; fake clock/audio engine; no live NATS required.
2. Component: NATS transport handlers with local broker or test harness; HTTP handlers via httptest.
3. Integration: real broker + coordinator + library + multiple simulated players (separate processes if possible).
4. Distributed failure: process crashes, coordinator/broker disconnects, reconnect, stale messages and unknown outcomes.
5. Concurrency/race: parallel browser calls and NATS requests; run `go test -race ./...`.

## Invariants to assert
- Different rooms never share authoritative playback state.
- Queue item occurrence IDs unique across current/history/upcoming per player; removing one duplicate track occurrence never removes another.
- State transitions match API.md; no partial state mutation on application rejection.
- State versions increase on logical changes, not on monotonic position ticks.
- A duplicate same-ID request executes at most once while cache retained; conflicting payload yields DUPLICATE_REQUEST_CONFLICT.
- Stale state events never replace current instance/revision; snapshots reconcile missed events.
- Timeouts are labeled unknown outcome, not proof of execution failure.

## Unit test matrix
| ID | Setup/action | Expected |
| --- | --- | --- |
| U01 | `playback.play(A)` from stopped | Playing A at 0; upcoming preserved |
| U02 | Pause at 45s, then resume after delay | Position frozen while paused, then advances |
| U03 | Stop at 45s | Stopped, current retained, position 0, history/queue retained |
| U04 | A->B->C; previous; next | C->B->C, original upcoming retained |
| U05 | Previous with empty history and current | Restarts current from 0 |
| U06 | Previous with no current/history | Successful no-op |
| U07 | Next with empty upcoming | Stopped; prior current moved to history; current cleared |
| U08 | Next/previous while paused | Navigated track immediately playing |
| U09 | Clear upcoming | Current/history unaffected |
| U10 | Same track queued twice, remove one item ID | Exactly one occurrence removed |
| U11 | Unknown track ID | TRACK_NOT_FOUND, no state change |
| U12 | Unknown type/malformed payload | INVALID_COMMAND or INVALID_PAYLOAD |
| U13 | Pause/resume from stopped | INVALID_STATE |
| U14 | Repeat pause, resume, stop in matching state | No-op, no version increment |
| U15 | Natural track completion | Same as Next advancement |
| U16 | Two identical requests with same ID | One mutation, cached response reused |
| U17 | Reuse ID with different payload | DUPLICATE_REQUEST_CONFLICT |
| U18 | Two concurrent identical IDs | Exactly one mutation |
| U19 | Reuse ID after cache eviction/restart | No exactly-once guarantee; test documented behavior |
| U20 | Simulation clock, pause/resume and stop | Monotonic elapsed position; no tick-driven version increment |
| U21 | Stop then Play same selected track, repeatedly | Restart at 0; same occurrence, history and upcoming unchanged; one version increment per restart |

## Integration test matrix
| ID | Setup/action | Expected |
| --- | --- | --- |
| I01 | Start NATS, coordinator, 3 players | Three rooms register, unique room IDs |
| I02 | Send Play to bedroom via HTTP | Only bedroom state changes via NATS |
| I03 | Queue add/remove/clear via HTTP | Exact mapping to NATS unified commands |
| I04 | Room state change | NATS state event observed; SSE event reaches browser client |
| I05 | Disconnect and reconnect SSE | Client refreshes authoritative REST snapshot |
| I06 | Kill kitchen | Kitchen marked offline after threshold; others unaffected |
| I07 | Coordinator terminates | Already playing room continues; recovery re-registers |
| I08 | Broker disconnects | Playback continues; requests unavailable, players reconnect/re-register |
| I09 | Registration with duplicate active room ID | Reject second instance; no ambiguous command delivery |
| I10 | Old instance heartbeat/event after replacement | Ignored |
| I11 | Artificial delay of command response | HTTP 504, uncertain outcome; no transparent retry with new ID |
| I12 | Missing subscriber | NO_RESPONDERS/503 rather than false success |
| I13 | Concurrent commands to different rooms | Progress independently; no global serialization |
| I14 | Concurrent mutations same room | Serialized consistent state; race detector clean |
| I15 | Unlicensed/missing/traversal audio path | Rejected; no arbitrary filesystem reads |
| I16 | Load catalog/audio by track ID | MP3 available over HTTP, duration and source metadata readable |

## Demonstration script
1. Start NATS, library, coordinator and 3 simulated players; browser displays them online.
2. Play different songs in two rooms; control their queues independently.
3. Show pause/resume, stop/clear separation, and previous/next return behavior.
4. Kill one room; remaining room keeps playing and can receive commands.
5. Restart missing room; observe re-registration and state recovery (its in-memory queue is not recovered across its own crash).
6. Kill/restart coordinator; players maintain playback and re-register.
7. Trigger a command timeout; UI reports uncertain outcome; refresh state.
8. Repeat identical request ID and show deduplication in logs.
9. If feasible, run audio output on distinct machines or outputs.

## Evidence and acceptance
Save logs (request IDs, room IDs, instance IDs, versions, failure/recovery timestamps), screenshots, and the commands used to reproduce failures. Record test environment and distinguish simulation from audible playback. Target passing unit, integration and race tests before submission; report limitations honestly.

## Continuous integration
PR gate: `go test ./...`, `go vet ./...`, `go build ./cmd/...`, `go test -race ./...`. Optional broker-backed integration job should explicitly launch/stop NATS in CI. Tests should not depend on wall-clock sleeps where injectable clocks/events make deterministic assertions possible.

## Separate-process failure suite (P5)

Run `go test -tags=process ./cmd/demo ./tests/integration -run '^TestProcess' -count=1 -timeout=5m`.
The harness builds the repository's coordinator, library and player binaries and the
module-pinned NATS server. It launches them on loopback ports, runs three independent
simulated rooms, and kills/restarts only the child processes it owns. It needs permission
to open local TCP listeners. No separately installed NATS executable is required.

The suite covers I01–I14 and I16 through real HTTP, SSE and Core NATS: room isolation,
queue occurrence removal, playback navigation, reconnect, player/coordinator/broker
crashes, duplicate registration, retired-instance publications, uncertain timeouts,
missing responders, concurrent identical and distinct request IDs, and no command
replay after reconnect. I15 additionally exercises traversal/allowlist HTTP rejection
and a library process refusing an unlicensed catalog; detailed missing-file and
symlink checks remain in the library component suite.

Set `ROOMPLAY_PROCESS_RACE=1` and use `go test -race` to instrument both the harness
and all child binaries. The `process` CI job does this and uploads logs and JSON
state/timing evidence even on failure. `ROOMPLAY_EVIDENCE_DIR` chooses a persistent
evidence directory; otherwise evidence uses a temporary directory. Use a fresh
directory for each run. `ROOMPLAY_PROCESS_BIN_DIR` optionally chooses the build directory.

The two routing fixture track IDs are labelled copies of the **same** licensed MP3;
their source/license/attribution is retained. Simulation proves independent track
selection and control, not different audible songs or synchronized speakers.

For manual browser verification set `ROOMPLAY_BROWSER_READY_FILE` to an absolute file
path and use a longer test timeout, e.g. `-timeout=15m`. Once automatic checks finish,
the harness writes the live controller URL there and waits up to ten minutes. Open
that URL, check room switching, playback, queue controls and refresh; create the file
`<ready-file>.continue` to let the suite finish and clean up. Remove any old continuation
file before another interactive run. This pause is disabled in CI.

Optional `ROOMPLAY_AUDIO_SMOKE=1` uses the real audio backend for a short local output
check. It requires an audio device and is disabled in CI. Backend progress alone does
not establish human audibility or playback on multiple machines/outputs.

The launcher lifecycle test in `cmd/demo` exercises the same entry point as
`go run ./cmd/demo`: real service builds, six child processes, three fresh rooms,
cancellation cleanup and closed listener ports. It runs silently in CI. Optional
`ROOMPLAY_DEMO_AUDIO_TEST=1` enables a local Bedroom audio check for that test;
it is disabled in CI. Unit tests also cover child-exit reporting during readiness
and idempotent cleanup of every owned child process.
