# RoomPlay

A multi-room music player built with Go. The browser controls independent room players through a coordinator and a Core NATS broker. A separate HTTP library serves the music.

## Requirements

- [Go 1.26 or newer](https://go.dev/doc/install).
- Git, a browser, and speakers or headphones for audio.
- Internet for the initial clone/build. Subsequent use needs Internet only if dependencies are missing or changed.

## Quick start — Mac and Windows

Clone the repository and enter its folder in Terminal (Mac) or PowerShell (Windows):

```sh
git clone https://github.com/pkz074/RoomPlay.git
cd RoomPlay
```

Then run **one command**, the same on both systems:

```sh
go run ./cmd/demo
```

The launcher builds the services and module-pinned NATS server, starts everything in the background, waits for three online rooms and opens **[http://127.0.0.1:8080](http://127.0.0.1:8080)**. No separate NATS install, Node.js, Docker or additional terminal windows are needed.

- **Bedroom** uses real audio through the laptop's default output.
- **Kitchen** and **Living Room** run silently in simulation mode.
- Service logs are saved under `.demo-logs/run-*/`; compiled services go in `.demo-bin/`.
- Keep this single terminal open. **Ctrl+C stops all six background services.**

The first run takes longer while dependencies download and binaries build. Later runs reuse Go's build cache. If you already have a checkout, update it from `main` and run the same launcher command:

```sh
git switch main
git pull --ff-only
go run ./cmd/demo
```

## Play the song

1. Choose your presentation audio output and set its volume before starting the launcher.
2. In the browser, select **Bedroom** and click **Play now** on *Lost in a Dream*.
3. Confirm you hear the song. The sound comes from the Go player process, not from the browser.
4. Use Pause/Resume, Add to queue and the queue's remove buttons. The catalog contains one song, which can be queued multiple times as separate occurrences.
5. Select Kitchen or Living Room to control their independent state; they are intentionally silent in this setup.

Under **More actions**, **Stop playback** keeps the selected track, history and queue, and resets position to zero. The main Play button restarts that retained track without adding history. **Clear queue** removes only upcoming items.

**Presentation rehearsal:** preparing the full MP3 can exceed the coordinator's 1.5-second request deadline. The UI may report an unknown outcome before playback starts. Wait a few seconds and refresh state; do not repeatedly click Play. Before presenting, load the song, confirm sound and Pause. Begin the live demo with **Resume**, which uses the prepared audio.

## Launcher options

Run without an audio device, with all rooms simulated:

```sh
go run ./cmd/demo -simulation
```

Print the URL without automatically opening a browser:

```sh
go run ./cmd/demo -no-browser
```

If your usual ports are occupied, choose another set:

```sh
go run ./cmd/demo -web-port 8081 -library-port 8091 -broker-port 4223
```

The launcher stops its own services if startup fails or a child service exits unexpectedly. It never stops an unrelated process occupying a port. The six-window method remains available in the [Mac runbook](docs/DEMO_MAC.txt) and [Windows runbook](docs/DEMO_WINDOWS.txt) for demonstrations where you deliberately want individual service logs and restarts.

## Troubleshooting

- **No sound:** check output selection, master/per-app volume and `.demo-logs/run-*/bedroom.log`. Restart the launcher after changing audio output if necessary. Rehearse on the actual presentation laptop; Mac builds are checked for Apple Silicon and Intel, but audible output depends on that machine.
- **Port already in use:** stop your previous demo, or use the alternate-port options above. Closing the browser alone does not stop the services.
- **Room fails to start:** the launcher prints the failing service and log path. Check its log; use `-simulation` if the laptop has no available audio device.
- **Go is not found:** install Go and open a new terminal.
- **Old behavior after updating:** stop the previous launcher with Ctrl+C, update `main` and rerun. The launcher rebuilds the service binaries.

## Configuration and components

The launcher uses loopback addresses on one laptop. It sets the child processes' environment itself; `.env` files are not loaded automatically. For manual or multi-machine launches, see [`.env.example`](.env.example) and the [developer guide](docs/DEVELOPMENT.md).

| Component | Default address | Purpose |
| --- | --- | --- |
| Coordinator | `http://127.0.0.1:8080` | Web controller, REST API and SSE |
| Library | `http://127.0.0.1:8090` | Catalog and MP3 delivery |
| Core NATS | `nats://127.0.0.1:4222` | Commands, state events and heartbeats |
| Players | Outbound connections | Own each room's playback, queue and history |

Core NATS carries control messages. Audio travels directly from the library to an audio-enabled player. This single-laptop setup does not demonstrate different physical speakers or different songs.

## Tests

Node.js is needed only for frontend tests. Race tests require a supported C toolchain. The process tests start and clean up their own services; a manually running demo is not required.

```sh
go test ./...
go vet ./...
go build ./cmd/...
go test -race ./...
go test -race -tags=broker ./tests/integration -run '^TestBroker' -count=1 -timeout=2m
go test -tags=process ./cmd/demo ./tests/integration -run '^TestProcess' -count=1 -timeout=5m
node --test web/app.test.js tests/integration/frontend_http.test.js
```

See the [test plan](docs/TEST_PLAN.md) for child-process race instrumentation, evidence collection and optional audio checks, and [P5 results](docs/P5_RESULTS.txt) for verified behavior and limits.

## Documentation and credits

- [Architecture](docs/ARCHITECTURE.md), [API](docs/API.md), [development](docs/DEVELOPMENT.md)
- [Contributor instructions](AGENTS.md) and [Git workflow](docs/GIT_WORKFLOW.md)
- [Music source, license and attribution](assets/music/ATTRIBUTION.md)

Demo music: *Lost in a Dream* by Eternity Arcade, supplied as a Wikimedia Commons MP3 derivative listed under CC BY 4.0. Retain attribution and source/license metadata when copying the audio. See the attribution document for verification limits.
