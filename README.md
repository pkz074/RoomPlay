# RoomPlay

A distributed multi-room music player built with Go, featuring independent playback nodes, centralized coordination, and fault-tolerant communication.

> **Status:** Initial project scaffold. The binaries currently exit with a not-implemented message; NATS connections, REST endpoints, SSE, music playback and real library serving are assigned to feature owners and are **not yet functional**.

## Architecture

- **Coordinator:** Go HTTP REST + SSE frontend gateway, NATS routing and room registry (Najin)
- **Players:** independent Go processes, authoritative playback state, queue/history, simulation/MP3 engine (Kyle)
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

Go 1.23+ (see `go.mod`), Git, and a local NATS server for integration once services are implemented. Go package tests currently need no NATS broker.

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/...
```

Environment variable examples: [`.env.example`](.env.example). Copy its values into your terminal environment when running services; **no automatic .env loader is provided**.

Once implemented, run a NATS server, start the library and coordinator, then start multiple player processes with distinct `ROOM_ID` values. See [DEVELOPMENT.md](docs/DEVELOPMENT.md) for the planned environment and startup sequence.

## Collaboration

Every developer works on a short-lived feature branch and submits a PR against `main`; `pkz074` reviews contract changes. Before coding, read `AGENTS.md` and your guide under `docs/agents/`. Do not add music unless its license permits our demonstration and redistribution; document provenance in `assets/music/ATTRIBUTION.md`.
