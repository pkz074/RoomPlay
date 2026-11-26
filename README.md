# RoomPlay

A multi-room music player built with Go. The browser controls independent room players through a coordinator and a Core NATS broker. A separate HTTP library serves the music.

This guide runs everything on one laptop: **Bedroom plays real audio**, while **Kitchen and Living Room run silently in simulation mode**. The catalog contains one song, which can be queued multiple times.

## Requirements

- [Go 1.26 or newer](https://go.dev/doc/install), Git, and a browser.
- Speakers or headphones for the audible room.
- Internet for the initial clone/build. After building, the demo runs locally.

No Node.js, Docker, separate NATS installation or C compiler is required for the ordinary demo builds below. Choose your presentation audio output and set its volume **before starting Bedroom**.

## 1. Get the code and build

Use your operating system's commands. Stop if any command fails. If you already have a checkout, switch to `main`, run `git pull --ff-only` and rebuild there instead of cloning again.

### macOS — Terminal

```sh
git clone --branch main https://github.com/pkz074/RoomPlay.git "$HOME/Downloads/RoomPlay"
cd "$HOME/Downloads/RoomPlay"
go version
mkdir -p .demo-bin
CGO_ENABLED=0 go build -o .demo-bin/nats-server github.com/nats-io/nats-server/v2
CGO_ENABLED=0 go build -o .demo-bin/library ./cmd/library
CGO_ENABLED=0 go build -o .demo-bin/coordinator ./cmd/coordinator
CGO_ENABLED=0 go build -o .demo-bin/player ./cmd/player
```

### Windows — PowerShell

```powershell
git clone --branch main https://github.com/pkz074/RoomPlay.git "$env:USERPROFILE\Downloads\RoomPlay"
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
go version
New-Item -ItemType Directory -Path .demo-bin -Force | Out-Null
$env:CGO_ENABLED = '0'
go build -o .demo-bin\nats-server.exe github.com/nats-io/nats-server/v2
go build -o .demo-bin\library.exe ./cmd/library
go build -o .demo-bin\coordinator.exe ./cmd/coordinator
go build -o .demo-bin\player.exe ./cmd/player
```

## 2. Start the services

Use **six separate terminal tabs/windows**, in the order below. Leave each process running. Each block changes to the repository root; adjust that directory if you cloned elsewhere. Stop any previous RoomPlay demo first.

### macOS

**Terminal 1 — broker**

```sh
cd "$HOME/Downloads/RoomPlay"
./.demo-bin/nats-server -a 127.0.0.1 -p 4222
```

**Terminal 2 — music library**

```sh
cd "$HOME/Downloads/RoomPlay"
LIBRARY_ADDR=127.0.0.1:8090 LIBRARY_URL=http://127.0.0.1:8090 LIBRARY_CATALOG=assets/music/catalog.json LIBRARY_AUDIO_ROOT=assets/music ./.demo-bin/library
```

**Terminal 3 — coordinator and web controller**

```sh
cd "$HOME/Downloads/RoomPlay"
NATS_URL=nats://127.0.0.1:4222 LIBRARY_URL=http://127.0.0.1:8090 COORDINATOR_ADDR=127.0.0.1:8080 WEB_ROOT=web ./.demo-bin/coordinator
```

**Terminal 4 — Bedroom, with sound**

```sh
cd "$HOME/Downloads/RoomPlay"
ROOM_ID=bedroom ROOM_NAME=Bedroom PLAYER_MODE=audio NATS_URL=nats://127.0.0.1:4222 LIBRARY_URL=http://127.0.0.1:8090 ./.demo-bin/player
```

**Terminal 5 — Kitchen, simulated**

```sh
cd "$HOME/Downloads/RoomPlay"
ROOM_ID=kitchen ROOM_NAME=Kitchen PLAYER_MODE=simulation NATS_URL=nats://127.0.0.1:4222 LIBRARY_URL=http://127.0.0.1:8090 ./.demo-bin/player
```

**Terminal 6 — Living Room, simulated**

```sh
cd "$HOME/Downloads/RoomPlay"
ROOM_ID=livingroom ROOM_NAME=LivingRoom PLAYER_MODE=simulation NATS_URL=nats://127.0.0.1:4222 LIBRARY_URL=http://127.0.0.1:8090 ./.demo-bin/player
```

### Windows

**PowerShell 1 — broker**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
.\.demo-bin\nats-server.exe -a 127.0.0.1 -p 4222
```

**PowerShell 2 — music library**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
$env:LIBRARY_ADDR = '127.0.0.1:8090'
$env:LIBRARY_URL = 'http://127.0.0.1:8090'
$env:LIBRARY_CATALOG = 'assets/music/catalog.json'
$env:LIBRARY_AUDIO_ROOT = 'assets/music'
.\.demo-bin\library.exe
```

**PowerShell 3 — coordinator and web controller**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
$env:NATS_URL = 'nats://127.0.0.1:4222'
$env:LIBRARY_URL = 'http://127.0.0.1:8090'
$env:COORDINATOR_ADDR = '127.0.0.1:8080'
$env:WEB_ROOT = 'web'
.\.demo-bin\coordinator.exe
```

**PowerShell 4 — Bedroom, with sound**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
$env:ROOM_ID = 'bedroom'
$env:ROOM_NAME = 'Bedroom'
$env:PLAYER_MODE = 'audio'
$env:NATS_URL = 'nats://127.0.0.1:4222'
$env:LIBRARY_URL = 'http://127.0.0.1:8090'
.\.demo-bin\player.exe
```

**PowerShell 5 — Kitchen, simulated**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
$env:ROOM_ID = 'kitchen'
$env:ROOM_NAME = 'Kitchen'
$env:PLAYER_MODE = 'simulation'
$env:NATS_URL = 'nats://127.0.0.1:4222'
$env:LIBRARY_URL = 'http://127.0.0.1:8090'
.\.demo-bin\player.exe
```

**PowerShell 6 — Living Room, simulated**

```powershell
Set-Location "$env:USERPROFILE\Downloads\RoomPlay"
$env:ROOM_ID = 'livingroom'
$env:ROOM_NAME = 'LivingRoom'
$env:PLAYER_MODE = 'simulation'
$env:NATS_URL = 'nats://127.0.0.1:4222'
$env:LIBRARY_URL = 'http://127.0.0.1:8090'
.\.demo-bin\player.exe
```

## 3. Open the app and play the song

1. Open **[http://127.0.0.1:8080](http://127.0.0.1:8080)** in your browser.
2. Wait for all three rooms to show **Online**.
3. Select **Bedroom**, then **Play now** on *Lost in a Dream*.
4. Confirm you hear the song. Pause/Resume controls the selected room.
5. Use **Add to queue** to add occurrences; remove an occurrence with its remove button.
6. Select Kitchen or Living Room to control their separate state. They are silent because they use simulation mode.

Audio comes from the Bedroom process through the laptop's default output, rather than from the browser. To make the entire demo silent, start Bedroom with `PLAYER_MODE=simulation` instead of `audio`.

**Audio preparation can return a timeout warning.** Preparing the full MP3 may exceed the coordinator's 1.5-second request deadline even though playback subsequently starts. Wait a few seconds and refresh room state; do not repeatedly click Play. For a presentation, load the song, confirm sound, then Pause beforehand. Begin the live demo with **Resume**, which uses the prepared audio.

**Stop and Clear are separate:** under **More actions**, Stop retains the selected track/history/queue and resets position to zero. The main Play button then restarts that retained track without adding history. Clear queue removes only upcoming items.

## 4. Try recovery and shut down

- **Player recovery:** Ctrl+C in Kitchen's terminal stops Kitchen. It becomes Offline after about six seconds; Bedroom keeps playing. Press Up, Enter in Kitchen's terminal to repeat its launch command. It returns with fresh in-memory state.
- **Coordinator recovery:** Ctrl+C in Terminal 3, then Up, Enter. Prepared Bedroom audio continues while browser controls reconnect and room registration recovers.
- **Broker recovery:** repeat this in Terminal 1. Audio continues; commands are unavailable until the broker and registrations recover.
- **Shutdown:** Ctrl+C in each player terminal, then the coordinator, library and broker terminals. Closing the browser does not stop the services or sound.

Never start two players with the same room ID. If a port is occupied, stop your previous demo first. If there is no sound, check the system output, volume and Bedroom's terminal; restart Bedroom after changing the output if needed. Mac builds are checked for Apple Silicon and Intel; rehearse runtime audio on your Mac.

For a presentation sequence, see the [Mac runbook](docs/DEMO_MAC.txt) or [Windows runbook](docs/DEMO_WINDOWS.txt).

## Configuration

Variables are set in each service's terminal; `.env` files are **not loaded automatically**.

| Variable | Purpose | Default |
| --- | --- | --- |
| `NATS_URL` | Broker address for coordinator/players | `nats://127.0.0.1:4222` |
| `COORDINATOR_ADDR` | Coordinator HTTP listener | `:8080` |
| `WEB_ROOT` | Static frontend directory | `web` |
| `LIBRARY_ADDR` | Music library HTTP listener | `:8090` |
| `LIBRARY_URL` | Library origin advertised to players | `http://127.0.0.1:8090` |
| `LIBRARY_CATALOG` | Catalog manifest | `assets/music/catalog.json` |
| `LIBRARY_AUDIO_ROOT` | Audio directory | `assets/music` |
| `ROOM_ID` | Unique player ID | Required |
| `ROOM_NAME` | Player display name | Same as `ROOM_ID` |
| `PLAYER_MODE` | `simulation` or `audio` | `simulation` |

The commands above bind to loopback for a single-laptop demo. For separate machines, use broker/library addresses reachable by the players. Core NATS carries control messages; audio travels directly from the library to each audio-enabled player.

## Tests

Run from the repository root:

```sh
go test ./...
go vet ./...
go build ./cmd/...
go test -race ./...
go test -race -tags=broker ./tests/integration -run '^TestBroker' -count=1 -timeout=2m
go test -tags=process ./tests/integration -run '^TestProcess' -count=1 -timeout=5m
node --test web/app.test.js tests/integration/frontend_http.test.js
```

Node.js is needed only for frontend tests. Race builds need a supported C toolchain; do not use `CGO_ENABLED=0` for race tests. In the Windows build terminal, remove the override with `Remove-Item Env:CGO_ENABLED` before race testing. The process suite builds and owns its broker/services; no manually running demo is required.

See the [test plan](docs/TEST_PLAN.md) for child-process race instrumentation, evidence collection and optional audio checks, and [P5 results](docs/P5_RESULTS.txt) for verified behavior and limits.

## Documentation and credits

- [Architecture](docs/ARCHITECTURE.md), [API](docs/API.md), [development](docs/DEVELOPMENT.md)
- [Contributor instructions](AGENTS.md) and [Git workflow](docs/GIT_WORKFLOW.md)
- [Music source, license and attribution](assets/music/ATTRIBUTION.md)

Demo music: *Lost in a Dream* by Eternity Arcade, supplied as a Wikimedia Commons MP3 derivative listed under CC BY 4.0. Retain attribution and source/license metadata when copying the audio. See the attribution document for verification limits.
