# RoomPlay

A distributed multi-room music player built with Go, featuring independent playback nodes, centralized coordination, and fault-tolerant communication.

> **Status:** Library, player, and coordinator REST/SSE implementations are available. Real-broker tests exercise the HTTP-to-player path and state streams. The full independent-process failure demonstration remains P5.

## Architecture

- **Coordinator:** Go HTTP REST + SSE frontend gateway, NATS routing and room registry (Najin)
- **Players:** independent Go processes with authoritative state, queue/history, simulation, and optional audio output (Kyle)
- **Music library:** Go HTTP track catalog and reusable MP3 delivery (Colton)
- **Frontend:** HTML/CSS/JavaScript controller using REST + SSE (Kaira)
- **Integration and protocols:** common wire models, CI, integration and failure tests (João)

Core NATS carries command request/reply and state/heartbeat events; it never transports audio bytes.

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [API and NATS contracts](docs/API.md)
- [Development workflow](docs/DEVELOPMENT.md)
- [Test plan](docs/TEST_PLAN.md)
- [AI/contributor guide](AGENTS.md) and [role-specific guides](docs/agents/)

## Development requirements

Go 1.26+ (see `go.mod`), Git, and a local NATS server for integration once services are implemented. Go package tests currently need no NATS broker. The library uses `os.Root` for traversal-resistant audio access.

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/...
```

Environment variable examples: [`.env.example`](.env.example). Copy its values into your terminal environment when running services; **no automatic .env loader is provided**.

Start Core NATS, then `go run ./cmd/library` and `go run ./cmd/coordinator` from the repository root. Open [the local controller](http://localhost:8080). The coordinator serves `web/`, the `/api/v1` REST API and SSE; configure `COORDINATOR_ADDR`, `LIBRARY_URL`, `NATS_URL` and optional `WEB_ROOT` as needed. No automatic `.env` loading is provided.

Set `ROOM_ID` (and optionally `ROOM_NAME`) separately for each room, then run `go run ./cmd/player`. `PLAYER_MODE=simulation` needs no speakers; `PLAYER_MODE=audio` uses the default audio device and downloads tracks from the library. Players keep local playback running through coordinator/broker outages and renew registration after recovery. Upgrade players and coordinator together for the P4 instance-targeted messaging contract. See [API.md](docs/API.md).

Simulation mode needs no audio hardware and remains the recommended mode for tests and multi-room demonstrations.

### Music library

Run `go run ./cmd/library` from the repository root. It serves `GET /tracks` and
`GET`/`HEAD /audio/{filename}` using `assets/music/catalog.json` and `assets/music/`.
Override `LIBRARY_ADDR`, `LIBRARY_CATALOG`, and `LIBRARY_AUDIO_ROOT` as needed.
Set `LIBRARY_URL` to an HTTP(S) origin reachable by players, for example
`http://192.0.2.10:8090`. Credentials, query strings, fragments and path prefixes
are rejected. The loopback default is only for a same-machine demonstration.

Catalogs must contain one JSON object with a `tracks` array (`[]` is valid) and
be at most 1 MiB. Filenames must be portable, single-component MP3 names;
URLs escape spaces, Unicode and URL punctuation. Only catalog-listed files
are served, and symlinks may resolve only within the opened audio directory.
Keep that directory under trusted administrative control: directory confinement
does not establish music licensing, validate MP3 encoding, or prevent privileged
mount changes/hard links. See [audio attribution](assets/music/ATTRIBUTION.md)
for the demo track's evidence and remaining verification limits.

## Collaboration

Every developer works on a short-lived feature branch and submits a PR against `main`; `pkz074` reviews contract changes. Before coding, read `AGENTS.md` and your guide under `docs/agents/`. Do not add music unless its license permits our demonstration and redistribution; document provenance in `assets/music/ATTRIBUTION.md`.
