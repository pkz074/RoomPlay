# RoomPlay — Developer Guide
Status: Proposed team workflow

## Repository and implementation
GitHub: https://github.com/pkz074/RoomPlay
Go module: `github.com/pkz074/RoomPlay`.
Monorepo with independently runnable Go binaries and plain HTML/CSS/JavaScript frontend:
```text
cmd/coordinator/       Go coordinator main
cmd/player/            Go player main
cmd/library/           Go library main
internal/coordinator/  registry, NATS clients, REST/SSE
internal/player/       commands, state, audio and simulation engines
internal/library/      catalog, serving MP3
internal/protocol/     shared Go wire models and error codes
web/                   browser UI
assets/music/          licensed MP3 demo assets (and attribution)
docs/                  architecture, API, development, test plan
docs/agents/           role-specific AI/developer instructions (next phase)
tests/integration/     integration test harness
.github/workflows/     CI (next phase)
```
Directories are target structure, not all initialized yet. Never create a separate module per player.

## Team boundaries
- João: architecture / protocol / integration / CI / review.
- Najin: coordinator.
- Kyle: player.
- Kaira: frontend.
- Colton: library, demo tracks, assists frontend.
All developers own tests for their components. Only João approves changes to shared `internal/protocol/`, `docs/API.md`, and `docs/ARCHITECTURE.md`.

## Local prerequisites
Install Go (team should pin one supported version in go.mod), NATS Server and Git. Optional: NATS CLI; Go audio backend may need OS audio dependencies. Avoid frameworks, databases, JetStream and Docker unless agreed. Do not store copyrighted MP3s without reuse rights in repo.

## Local running
1. Start Core NATS broker locally on :4222.
2. Start music library on :8090.
3. Start coordinator on :8080.
4. Start three player processes with distinct `ROOM_ID` and `ROOM_NAME`; point them to same `NATS_URL` and reachable `LIBRARY_URL`.
5. Open the coordinator-served controller at `http://localhost:8080`. The default static directory is `web/`; override `WEB_ROOT` when launching from elsewhere.
Environment names:
```text
NATS_URL=nats://127.0.0.1:4222
COORDINATOR_ADDR=:8080
LIBRARY_ADDR=:8090
LIBRARY_URL=http://127.0.0.1:8090
ROOM_ID=bedroom
ROOM_NAME=Bedroom
PLAYER_MODE=simulation  # or audio
```
Prefer first end-to-end demo in simulation mode; real audio in second integration pass. Do not hardcode service addresses in Go packages.

## Branch and review rules
- `main` is shared baseline; no direct pushes/force pushes.
- Branch off updated main: `feat/coordinator`, `feat/player`, `feat/frontend`, `feat/library`, `test/integration`; docs work under `docs/*`.
- Small focused PRs; describe contract changes, tests, screenshots/demo where relevant, and any dependency or breaking change.
- Author should not merge own PR. Request review from pkz074; only merge after CI and review.
- PR touching protocol, API, or architecture must be approved before dependent implementation changes.
- Squash merge preferred; keep PR scope manageable and rebase/merge main frequently.
- Do not commit binaries, credentials, caches, large unlicensed audio, or local machine-specific paths.

## Coding expectations
- `context.Context` and bounded timeouts for NATS requests/HTTP fetches.
- NATS subscriptions registered before player registration; cleanup on shutdown.
- Validate user input, use structured JSON errors, no silent fallbacks.
- Serialize each player's state mutations; registry synchronization on coordinator.
- Implement real/simulation playback through a narrow interface, so deterministic tests do not require speakers.
- Avoid swallowed errors and data races. Document unknown-outcome behavior on command timeouts.
- Use conventional gofmt formatting; no unnecessary external framework.

## Commands / checks
```sh
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/...
```
Integration tests additionally start an actual broker and services. CI should run unit/vet/build/race and an explicit integration job if broker is available.

## AI-assisted implementation protocol
1. Read `docs/ARCHITECTURE.md`, `docs/API.md`, `docs/DEVELOPMENT.md`, and your role guide under `docs/agents/` (to be added).
2. State your assigned component and exact file boundaries.
3. Follow shared wire schema verbatim; do not invent command names/subjects.
4. Propose any contract change in a PR/comment before implementing it.
5. Write tests for all success/error paths.
6. Run local tests and include results in PR description.
7. Avoid changing another member's component without coordinated agreement.
