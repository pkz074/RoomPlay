# RoomPlay — AI and Contributor Instructions

This file is the entry point for any AI coding assistant or teammate working on **RoomPlay**, a distributed multi-room music player in Go.

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Identify your role
Ask the developer which teammate they are, unless already stated. Then read the appropriate guide:
- **João** — `docs/agents/JOAO.md`: architect, protocol owner, review, CI and integration testing
- **Najin** — `docs/agents/NAJIN.md`: coordinator, NATS routing/registry, REST and SSE
- **Kyle** — `docs/agents/KYLE.md`: room player, command state machine, audio and simulation
- **Kaira** — `docs/agents/KAIRA.md`: frontend UI, REST and SSE client
- **Colton** — `docs/agents/COLTON.md`: music library, catalog, audio HTTP delivery and tests

## Hard architecture boundaries
- The **browser** communicates through HTTP REST and SSE with the **coordinator**, never directly with NATS.
- The **coordinator** owns room registration/reachability; it routes commands via Core NATS and publishes SSE updates. It **does not** mutate room playback/queues.
- Each **player** is the sole owner of its own playback state, history, upcoming queue and request deduplication.
- The **library** serves a track catalog and licensed MP3 assets over HTTP; NATS never transports audio bytes.
- Commands use a single `roomplay.rooms.{id}.command` NATS subject with typed `type` and `payload`; read state through `get_state`; do not invent subjects.
- Standard errors and response shapes come from `docs/API.md`.
- **No** playlists, room-session lifecycle, JetStream, database, broker queue groups per room, distributed consensus, or synchronization across speakers in MVP.

## Development rules
1. Start by describing the goal, affected files, interfaces and a small implementation plan.
2. Work only within assigned component boundaries; request explicit approval before altering `internal/protocol/`, `docs/API.md`, or another teammate's files.
3. Prefer the Go standard library; check before adding third-party dependencies. A narrow audio or NATS library is expected.
4. Keep handlers thin; make core logic deterministic and independently unit-testable.
5. Use typed models, input validation, bounded NATS/HTTP timeouts, `context.Context`, clean shutdown and clear error handling.
6. Serialize player state mutation and deduplication together. Protect coordinator registry from data races.
7. Preserve NATS request IDs through coordinator; **do not retry timed-out mutations with a new ID** or report timeouts as proven non-execution.
8. For each change add meaningful tests; run `go fmt ./...`, `go test ./...`, `go vet ./...`, `go test -race ./...`, `go build ./cmd/...` when applicable. Report unrun checks honestly.
9. Use feature branches and focused pull requests. Do not push directly to `main`; do not merge your own PR. Request review from `pkz074`.
10. Do not commit secrets, generated binaries, machine-specific paths or unlicensed music files.

## Definition of done for a feature
- Meets its documented contract; no silent behavior changes.
- Contains tests for success, invalid input, failure and concurrency where applicable.
- No known race in shared state; returns structured error codes.
- PR says what changed, how to run/test it, and its dependencies.
- Architect reviewed any protocol or architectural adjustment.

## Suggested first AI prompt
"I'm [teammate name] working on RoomPlay. Read AGENTS.md, my role guide and all required docs. Summarize my scope, dependencies, acceptance criteria, then help me implement my first small testable PR without changing any shared API contract."
